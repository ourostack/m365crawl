package documenttext

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
)

func preflightZIP(ctx context.Context, r io.ReaderAt, size int64, maxEntries int) (err error) {
	defer func() {
		if cancelled := ctx.Err(); cancelled != nil {
			err = cancelled
		}
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	tailSize := min(size, int64(65557))
	tail, err := readAt(r, size-tailSize, tailSize)
	if err != nil {
		return err
	}
	signature := []byte{0x50, 0x4b, 0x05, 0x06}
	endIndex := -1
	for end := len(tail); end > 0; {
		i := bytes.LastIndex(tail[:end], signature)
		if i < 0 {
			break
		}
		if len(tail)-i >= 22 && int(binary.LittleEndian.Uint16(tail[i+20:])) == len(tail)-i-22 {
			endIndex = i
			break
		}
		end = i
	}
	if endIndex < 0 {
		return &ReadError{Code: "malformed"}
	}
	end := tail[endIndex:]
	u16 := func(offset int) uint16 { return binary.LittleEndian.Uint16(end[offset:]) }
	u32 := func(offset int) uint32 { return binary.LittleEndian.Uint32(end[offset:]) }
	if u16(4) != 0 || u16(6) != 0 || u16(8) == 65535 || u16(10) == 65535 || u32(12) == 0xffffffff || u32(16) == 0xffffffff {
		return &ReadError{Code: "unsupported"}
	}
	if u16(8) != u16(10) {
		return &ReadError{Code: "malformed"}
	}
	if int(u16(10)) > maxEntries {
		return &ReadError{Code: "too_large"}
	}
	offset := int64(u32(16))
	directoryEnd := offset + int64(u32(12))
	if directoryEnd != size-tailSize+int64(endIndex) {
		return &ReadError{Code: "malformed"}
	}
	count := 0
	for offset < directoryEnd {
		if err := ctx.Err(); err != nil {
			return err
		}
		if count == maxEntries {
			return &ReadError{Code: "too_large"}
		}
		if directoryEnd-offset < 46 {
			return &ReadError{Code: "malformed"}
		}
		header, err := readAt(r, offset, 46)
		if err != nil {
			return err
		}
		if binary.LittleEndian.Uint32(header) != 0x02014b50 {
			return &ReadError{Code: "malformed"}
		}
		if binary.LittleEndian.Uint16(header[34:]) != 0 {
			return &ReadError{Code: "unsupported"}
		}
		if binary.LittleEndian.Uint32(header[20:]) == 0xffffffff || binary.LittleEndian.Uint32(header[24:]) == 0xffffffff || binary.LittleEndian.Uint32(header[42:]) == 0xffffffff {
			return &ReadError{Code: "unsupported"}
		}
		length := int64(46)
		for _, field := range []int{28, 30, 32} {
			length += int64(binary.LittleEndian.Uint16(header[field:]))
		}
		if length > directoryEnd-offset {
			return &ReadError{Code: "malformed"}
		}
		offset += length
		count++
	}
	if count != int(u16(10)) {
		return &ReadError{Code: "malformed"}
	}
	return nil
}

func readAt(r io.ReaderAt, offset, size int64) ([]byte, error) {
	data := make([]byte, size)
	n, err := r.ReadAt(data, offset)
	if n != len(data) || (err != nil && !errors.Is(err, io.EOF)) {
		code := "read_failed"
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			code = "malformed"
		}
		return nil, &ReadError{Code: code}
	}
	return data, nil
}
