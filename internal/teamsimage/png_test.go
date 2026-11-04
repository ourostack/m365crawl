package teamsimage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"hash/crc32"
	"image"
	"image/png"
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
	padding := make([]byte, 4194304-len(small)-12)
	exact := append(append(bytes.Clone(small[:33]), imagePNGChunk("tEXt", padding)...), small[33:]...)
	info, err := ValidatePNG(ctx, exact, "image/png")
	if err != nil || info.Size != 4194304 {
		t.Fatal("exact raw byte limit refused", err)
	}
	_, err = ValidatePNG(ctx, append(exact, 0), "image/png")
	imagePNGCode(t, err, "image_too_large")
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
