package documenttext

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"testing"
)

func directoryFixture(count int, declared uint16) []byte {
	if count < 0 || count > 65537 {
		panic("invalid synthetic directory fixture size")
	}
	raw := make([]byte, count*46+22)
	for i := range count {
		h := raw[i*46:]
		binary.LittleEndian.PutUint32(h, 0x02014b50)
	}
	end := raw[count*46:]
	binary.LittleEndian.PutUint32(end, 0x06054b50)
	binary.LittleEndian.PutUint16(end[8:], declared)
	binary.LittleEndian.PutUint16(end[10:], declared)
	binary.LittleEndian.PutUint32(end[12:], uint32(count*46))
	return raw
}

func TestZIPDirectoryBudget(t *testing.T) {
	raw := directoryFixture(8192, 8192)
	if err := preflightZIP(context.Background(), bytes.NewReader(raw), int64(len(raw)), 8192); err != nil {
		t.Fatalf("inclusive entry bound: %v", err)
	}
	raw = directoryFixture(8193, 8193)
	requireZIPError(t, preflightZIP(context.Background(), bytes.NewReader(raw), int64(len(raw)), 8192), "too_large")

	// A small declared count must not conceal a much larger actual directory.
	raw = directoryFixture(65537, 1)
	requireZIPError(t, preflightZIP(context.Background(), bytes.NewReader(raw), int64(len(raw)), 8192), "too_large")
}

func TestZIPDirectoryFraming(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func([]byte)
		want string
	}{
		{"count-mismatch", func(b []byte) { binary.LittleEndian.PutUint16(b[len(b)-12:], 1) }, "malformed"},
		{"disk", func(b []byte) { binary.LittleEndian.PutUint16(b[len(b)-18:], 1) }, "unsupported"},
		{"zip64", func(b []byte) { binary.LittleEndian.PutUint16(b[len(b)-12:], 65535) }, "unsupported"},
		{"directory-offset", func(b []byte) { binary.LittleEndian.PutUint32(b[len(b)-6:], 1) }, "malformed"},
		{"directory-size", func(b []byte) { binary.LittleEndian.PutUint32(b[len(b)-10:], 1) }, "malformed"},
		{"header", func(b []byte) { b[0] = 0 }, "malformed"},
		{"name-outside-directory", func(b []byte) { binary.LittleEndian.PutUint16(b[28:], 65535) }, "malformed"},
		{"member-disk", func(b []byte) { binary.LittleEndian.PutUint16(b[34:], 1) }, "unsupported"},
		{"member-zip64-size", func(b []byte) { binary.LittleEndian.PutUint32(b[24:], 0xffffffff) }, "unsupported"},
		{"member-zip64-offset", func(b []byte) { binary.LittleEndian.PutUint32(b[42:], 0xffffffff) }, "unsupported"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := directoryFixture(2, 2)
			tc.edit(raw)
			requireZIPError(t, preflightZIP(context.Background(), bytes.NewReader(raw), int64(len(raw)), 8192), tc.want)
		})
	}
	raw := directoryFixture(1, 1)
	raw = append(raw, []byte("not-an-end-record")...)
	requireZIPError(t, preflightZIP(context.Background(), bytes.NewReader(raw), int64(len(raw)), 8192), "malformed")
	requireZIPError(t, preflightZIP(context.Background(), bytes.NewReader(nil), 8, 8192), "malformed")
	requireZIPError(t, preflightZIP(context.Background(), failingReaderAt{}, 100, 8192), "read_failed")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := preflightZIP(ctx, bytes.NewReader(raw), int64(len(raw)), 8192); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled directory: %v", err)
	}
}

func requireZIPError(t *testing.T, err error, code string) {
	t.Helper()
	var readErr *ReadError
	if !errors.As(err, &readErr) || readErr.Code != code || err.Error() != code {
		t.Fatalf("directory error = %v, want %s", err, code)
	}
}
