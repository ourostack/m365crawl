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
	"image/png"
	"io"

	"github.com/ourostack/m365crawl/internal/errs"
)

type PNGInfo struct {
	Width, Height int
	Size          int
	SHA256        string
	ContentType   string
}

func ValidatePNG(ctx context.Context, data []byte, contentType string) (PNGInfo, error) {
	if err := ctx.Err(); err != nil {
		return PNGInfo{}, err
	}
	if len(data) > 4194304 {
		return PNGInfo{}, pngError("image_too_large")
	}
	if contentType != "image/png" {
		return PNGInfo{}, pngError("image_format_unsupported")
	}
	compressed, err := pngChunks(ctx, data)
	if err != nil {
		return PNGInfo{}, err
	}
	config, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return PNGInfo{}, pngError("image_bytes_invalid")
	}
	width, height := int64(config.Width), int64(config.Height)
	storagePerPixel := int64(4)
	if data[24] == 16 {
		storagePerPixel = 8
	}
	if width > 16384 || height > 16384 || width*height > 16777216 ||
		width*height*storagePerPixel > 67108864 {
		return PNGInfo{}, pngError("image_too_large")
	}
	// ByteReader prevents zlib from buffering past its stream into trailing IDAT
	// garbage that image/png deliberately tolerates. Scanline padding and Adam7
	// filters add at most fourteen bytes per row across the seven passes.
	input := &pngReader{ctx: ctx, Reader: bytes.NewReader(compressed)}
	stream, err := zlib.NewReader(input)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return PNGInfo{}, ctxErr
		}
		return PNGInfo{}, pngError("image_bytes_invalid")
	}
	limit := width*height*storagePerPixel + 14*height
	_, readErr := io.CopyN(io.Discard, stream, limit+1)
	closeErr := stream.Close()
	if err := ctx.Err(); err != nil {
		return PNGInfo{}, err
	}
	if !errors.Is(readErr, io.EOF) || closeErr != nil || input.Len() != 0 {
		return PNGInfo{}, pngError("image_bytes_invalid")
	}
	img, err := png.Decode(&pngReader{ctx: ctx, Reader: bytes.NewReader(data)})
	if ctxErr := ctx.Err(); ctxErr != nil {
		return PNGInfo{}, ctxErr
	}
	if err != nil || img.Bounds().Dx() != config.Width || img.Bounds().Dy() != config.Height {
		return PNGInfo{}, pngError("image_bytes_invalid")
	}
	digest := sha256.Sum256(data)
	return PNGInfo{Width: config.Width, Height: config.Height, Size: len(data),
		SHA256: hex.EncodeToString(digest[:]), ContentType: "image/png"}, nil
}

func pngChunks(ctx context.Context, data []byte) ([]byte, error) {
	if len(data) < 8 || !bytes.Equal(data[:8], []byte("\x89PNG\r\n\x1a\n")) {
		return nil, pngError("image_bytes_invalid")
	}
	var compressed []byte
	seenIDAT, endedIDAT := false, false
	for pos := 8; pos < len(data); {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if len(data)-pos < 12 {
			return nil, pngError("image_bytes_invalid")
		}
		length := binary.BigEndian.Uint32(data[pos : pos+4])
		if int64(length) > int64(len(data)-pos-12) {
			return nil, pngError("image_bytes_invalid")
		}
		end := pos + 12 + int(length)
		name := data[pos+4 : pos+8]
		for _, b := range name {
			if b < 'A' || b > 'Z' && b < 'a' || b > 'z' {
				return nil, pngError("image_bytes_invalid")
			}
		}
		if name[2]&0x20 != 0 || crc32.ChecksumIEEE(data[pos+4:end-4]) != binary.BigEndian.Uint32(data[end-4:end]) {
			return nil, pngError("image_bytes_invalid")
		}
		kind := string(name)
		payload := data[pos+8 : end-4]
		if pos == 8 && kind != "IHDR" {
			return nil, pngError("image_bytes_invalid")
		}
		switch kind {
		case "IHDR":
			if pos != 8 || length != 13 {
				return nil, pngError("image_bytes_invalid")
			}
		case "IDAT":
			if endedIDAT {
				return nil, pngError("image_bytes_invalid")
			}
			seenIDAT = true
			compressed = append(compressed, payload...)
		case "IEND":
			if length != 0 || !seenIDAT || end != len(data) {
				return nil, pngError("image_bytes_invalid")
			}
			return compressed, nil
		case "PLTE":
			if seenIDAT {
				return nil, pngError("image_bytes_invalid")
			}
		case "acTL", "fcTL", "fdAT":
			return nil, pngError("image_bytes_invalid")
		default:
			if name[0]&0x20 == 0 {
				return nil, pngError("image_bytes_invalid")
			}
			endedIDAT = seenIDAT
		}
		pos = end
	}
	return nil, pngError("image_bytes_invalid")
}

type pngReader struct {
	ctx context.Context
	*bytes.Reader
}

func (r *pngReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.Reader.Read(p)
}

func (r *pngReader) ReadByte() (byte, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.Reader.ReadByte()
}

func pngError(code string) *errs.Coded {
	return &errs.Coded{Code: code, Exit: errs.ExitEnvironment,
		Message: "the selected image bytes are not a complete supported PNG (" + code + ")",
		Fix:     "Only a complete, bounded, nonanimated PNG can be exported; no partial raster or format fallback was used."}
}
