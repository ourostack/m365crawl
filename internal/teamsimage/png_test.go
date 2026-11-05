package teamsimage

import (
	"bytes"
	"compress/zlib"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"io"
	"testing"

	"github.com/ourostack/m365crawl/internal/errs"
)

func imagePNGFixture(t *testing.T, img image.Image) []byte {
	t.Helper()
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func imagePNGChunk(name string, data []byte) []byte {
	if uint64(len(data)) > uint64(^uint32(0)) {
		panic("PNG fixture chunk length exceeds uint32")
	}
	result := make([]byte, 12+len(data))
	binary.BigEndian.PutUint32(result[:4], uint32(len(data)))
	copy(result[4:8], name)
	copy(result[8:], data)
	binary.BigEndian.PutUint32(result[len(result)-4:], crc32.ChecksumIEEE(result[4:len(result)-4]))
	return result
}

func imagePNGHeader(data []byte, width, height uint32, depth byte) []byte {
	result := bytes.Clone(data)
	binary.BigEndian.PutUint32(result[16:20], width)
	binary.BigEndian.PutUint32(result[20:24], height)
	result[24] = depth
	binary.BigEndian.PutUint32(result[29:33], crc32.ChecksumIEEE(result[12:29]))
	return result
}

func imagePNGCode(t *testing.T, err error, code string) {
	t.Helper()
	var coded *errs.Coded
	if !errors.As(err, &coded) || coded.Code != code {
		t.Fatalf("want %s, got %v", code, err)
	}
}

func TestImagePNGIntegrity(t *testing.T) {
	ctx := context.Background()
	good := imagePNGFixture(t, image.NewNRGBA(image.Rect(0, 0, 2, 1)))
	info, err := ValidatePNG(ctx, good, "image/png")
	sum := sha256.Sum256(good)
	if err != nil || info.Width != 2 || info.Height != 1 || info.Size != len(good) ||
		info.SHA256 != hex.EncodeToString(sum[:]) || info.ContentType != "image/png" {
		t.Fatalf("complete PNG metadata not verified: %+v %v", info, err)
	}
	badSignature := bytes.Clone(good)
	badSignature[0] = 0
	badCRC := bytes.Clone(good)
	badCRC[29] ^= 1
	badLength := bytes.Clone(good)
	binary.BigEndian.PutUint32(badLength[8:12], ^uint32(0))
	badRaster := append(bytes.Clone(good[:33]), imagePNGChunk("IDAT", []byte("not-zlib"))...)
	badRaster = append(badRaster, imagePNGChunk("IEND", nil)...)
	if string(good[37:41]) != "IDAT" {
		t.Fatal("generated fixture no longer has expected first IDAT placement")
	}
	idatLength := int(binary.BigEndian.Uint32(good[33:37]))
	idat := good[41 : 41+idatLength]
	trailingIDAT := append(append(bytes.Clone(good[:len(good)-12]), imagePNGChunk("IDAT", []byte("garbage"))...), good[len(good)-12:]...)
	nonconsecutive := append(bytes.Clone(good[:33]), imagePNGChunk("IDAT", idat[:len(idat)/2])...)
	nonconsecutive = append(nonconsecutive, imagePNGChunk("tEXt", nil)...)
	nonconsecutive = append(nonconsecutive, imagePNGChunk("IDAT", idat[len(idat)/2:])...)
	nonconsecutive = append(nonconsecutive, imagePNGChunk("IEND", nil)...)
	for name, data := range map[string][]byte{
		"signature":        badSignature,
		"crc":              badCRC,
		"chunk-length":     badLength,
		"missing-iend":     good[:len(good)-12],
		"trailing-byte":    append(bytes.Clone(good), 0),
		"second-iend":      append(bytes.Clone(good), imagePNGChunk("IEND", nil)...),
		"second-header":    append(append(bytes.Clone(good[:33]), good[8:33]...), good[33:]...),
		"missing-idat":     append(bytes.Clone(good[:33]), imagePNGChunk("IEND", nil)...),
		"bad-raster":       badRaster,
		"trailing-idat":    trailingIDAT,
		"idat-sequence":    nonconsecutive,
		"unknown-critical": append(append(bytes.Clone(good[:33]), imagePNGChunk("ABCD", nil)...), good[33:]...),
		"animation":        append(append(bytes.Clone(good[:33]), imagePNGChunk("acTL", make([]byte, 8))...), good[33:]...),
		"animation-frame":  append(append(bytes.Clone(good[:33]), imagePNGChunk("fcTL", make([]byte, 26))...), good[33:]...),
		"animation-data":   append(append(bytes.Clone(good[:33]), imagePNGChunk("fdAT", []byte{0, 0, 0, 0})...), good[33:]...),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ValidatePNG(ctx, data, "image/png")
			imagePNGCode(t, err, "image_bytes_invalid")
		})
	}
	split := append(bytes.Clone(good[:33]), imagePNGChunk("IDAT", idat[:len(idat)/2])...)
	split = append(split, imagePNGChunk("IDAT", idat[len(idat)/2:])...)
	split = append(split, imagePNGChunk("IEND", nil)...)
	if _, err := ValidatePNG(ctx, split, "image/png"); err != nil {
		t.Fatal("valid consecutive split IDAT refused", err)
	}
	if _, err := ValidatePNG(ctx, good, "image/jpeg"); err == nil {
		t.Fatal("declared MIME mismatch admitted")
	}
}

func TestImagePNGExpansionBounds(t *testing.T) {
	ctx := context.Background()
	small := imagePNGFixture(t, image.NewGray(image.Rect(0, 0, 2, 1)))
	for name, data := range map[string][]byte{
		"width":        imagePNGHeader(small, 16385, 1, 8),
		"height":       imagePNGHeader(small, 1, 16385, 8),
		"pixels":       imagePNGHeader(small, 4096, 4097, 8),
		"sixteen-bit":  imagePNGHeader(small, 4096, 4096, 16),
		"storage-byte": imagePNGHeader(small, 2049, 4096, 16),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ValidatePNG(ctx, data, "image/png")
			imagePNGCode(t, err, "image_too_large")
		})
	}
	for name, img := range map[string]image.Image{
		"width-limit":      image.NewGray(image.Rect(0, 0, 16384, 1)),
		"height-limit":     image.NewGray(image.Rect(0, 0, 1, 16384)),
		"pixel-limit":      image.NewGray(image.Rect(0, 0, 4096, 4096)),
		"storage-limit-16": image.NewGray16(image.Rect(0, 0, 2048, 4096)),
	} {
		t.Run(name, func(t *testing.T) {
			data := imagePNGFixture(t, img)
			info, err := ValidatePNG(ctx, data, "image/png")
			if err != nil || info.Width != img.Bounds().Dx() || info.Height != img.Bounds().Dy() {
				t.Fatal("exact expansion/dimension bound refused", err)
			}
		})
	}
	padding := bytes.Repeat([]byte{'x'}, 4194304-len(small)-12)
	copy(padding, "padding\x00")
	exact := append(append(bytes.Clone(small[:33]), imagePNGChunk("tEXt", padding)...), small[33:]...)
	info, err := ValidatePNG(ctx, exact, "image/png")
	if err != nil || info.Size != 4194304 {
		t.Fatal("exact raw byte limit refused", err)
	}
	_, err = ValidatePNG(ctx, append(exact, 0), "image/png")
	imagePNGCode(t, err, "image_too_large")
}

func TestImagePNGStructuralAndRasterRefusals(t *testing.T) {
	good := imagePNGFixture(t, image.NewGray(image.Rect(0, 0, 1, 1)))
	withChunks := func(chunks ...[]byte) []byte {
		result := bytes.Clone(good[:33])
		for _, chunk := range chunks {
			result = append(result, chunk...)
		}
		return append(result, imagePNGChunk("IEND", nil)...)
	}
	compressed := func(raster []byte) []byte {
		var out bytes.Buffer
		writer := zlib.NewWriter(&out)
		if _, err := writer.Write(raster); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		return out.Bytes()
	}
	idat := bytes.Clone(good[33 : len(good)-12])
	for name, data := range map[string][]byte{
		"truncated-chunk": append(bytes.Clone(good[:8]), 0, 0, 0),
		"nonletter-kind":  withChunks(imagePNGChunk("t1Xt", nil), idat),
		"header-not-first": append(append(bytes.Clone(good[:8]),
			imagePNGChunk("tEXt", nil)...), good[8:]...),
		"zero-width":         imagePNGHeader(good, 0, 1, 8),
		"palette-after-data": withChunks(idat, imagePNGChunk("PLTE", []byte{0, 0, 0})),
		"invalid-filter":     withChunks(imagePNGChunk("IDAT", compressed([]byte{5, 0}))),
		"expanded-garbage":   withChunks(imagePNGChunk("IDAT", compressed(bytes.Repeat([]byte{0}, 32)))),
	} {
		t.Run(name, func(t *testing.T) {
			info, err := ValidatePNG(context.Background(), data, "image/png")
			imagePNGCode(t, err, "image_bytes_invalid")
			if info != (PNGInfo{}) {
				t.Fatal("invalid raster returned successful image metadata", info)
			}
		})
	}
	palette := image.NewPaletted(image.Rect(0, 0, 1, 1), color.Palette{color.Black, color.White})
	if _, err := ValidatePNG(context.Background(), imagePNGFixture(t, palette), "image/png"); err != nil {
		t.Fatal("valid indexed PNG with a palette before IDAT refused", err)
	}
}

func TestImagePNGCancellationAtReadBoundaries(t *testing.T) {
	good := imagePNGFixture(t, image.NewGray(image.Rect(0, 0, 1, 1)))
	cancelled := 0
	for boundary := int32(1); boundary <= 128; boundary++ {
		base, cancel := context.WithCancel(context.Background())
		ctx := &imageCancelAfterChecks{Context: base, cancel: cancel}
		ctx.left.Store(boundary)
		info, err := ValidatePNG(ctx, good, "image/png")
		if base.Err() != nil {
			cancelled++
			if !errors.Is(err, context.Canceled) || info != (PNGInfo{}) {
				t.Fatalf("cancellation at read boundary %d lost: %+v %v", boundary, info, err)
			}
		} else if err != nil || info.Width != 1 || info.Height != 1 {
			t.Fatalf("uncancelled valid PNG refused at boundary %d: %+v %v", boundary, info, err)
		}
		cancel()
	}
	if cancelled < 2 {
		t.Fatal("PNG validation no longer checks cancellation during processing")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if data, err := pngChunks(ctx, good); !errors.Is(err, context.Canceled) || data != nil {
		t.Fatal("cancelled chunk inspection returned compressed content", err)
	}
	reader := &pngReader{ctx: ctx, Reader: bytes.NewReader(good)}
	if n, err := reader.Read(make([]byte, 8)); n != 0 || !errors.Is(err, context.Canceled) || reader.Len() != len(good) {
		t.Fatal("cancelled raster read consumed bytes", n, err)
	}
	if _, err := reader.ReadByte(); !errors.Is(err, context.Canceled) || reader.Len() != len(good) {
		t.Fatal("cancelled raster byte read consumed bytes", err)
	}
	active := &pngReader{ctx: context.Background(), Reader: bytes.NewReader([]byte{17})}
	if value, err := active.ReadByte(); value != 17 || err != nil {
		t.Fatal("active byte read changed raster content", value, err)
	}
	if _, err := active.ReadByte(); !errors.Is(err, io.EOF) {
		t.Fatal("byte reader lost exact EOF", err)
	}
}
func TestImagePNGCancellationAndEmpty(t *testing.T) {
	good := imagePNGFixture(t, image.NewGray(image.Rect(0, 0, 1, 1)))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ValidatePNG(ctx, good, "image/png"); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation replaced by successful bytes or a parse error", err)
	}
	for _, data := range [][]byte{nil, []byte("<html>"), []byte("<svg/>"), []byte("\xff\xd8\xff"), []byte("GIF89a"), []byte("RIFFtestWEBP")} {
		_, err := ValidatePNG(context.Background(), data, "image/png")
		imagePNGCode(t, err, "image_bytes_invalid")
	}
}
