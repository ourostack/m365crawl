package documenttext

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"testing"
)

var compound = []byte{0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1}

func TestExtractInputAndCompound(t *testing.T) {
	got, err := Extract(context.Background(), bytes.NewReader(compound), 8, "docx", DefaultLimits())
	if err != nil || !reflect.DeepEqual(got, Result{Kind: "docx", State: "compound_unreadable"}) {
		t.Fatalf("compound result = %#v, %v", got, err)
	}
	for _, kind := range []string{"docx", "xlsx", "pptx"} {
		t.Run(kind, func(t *testing.T) {
			got, err := Extract(context.Background(), bytes.NewReader(compound), 8, kind, DefaultLimits())
			if err != nil || !reflect.DeepEqual(got, Result{Kind: kind, State: "compound_unreadable"}) {
				t.Fatalf("compound result = %#v, %v", got, err)
			}
		})
	}

	for _, tc := range []struct {
		name string
		ctx  context.Context
		r    io.ReaderAt
		size int64
		kind string
	}{
		{"nil-context", nil, bytes.NewReader(compound), 8, "docx"},
		{"nil-reader", context.Background(), nil, 8, "docx"},
		{"zero-size", context.Background(), bytes.NewReader(compound), 0, "docx"},
		{"negative-size", context.Background(), bytes.NewReader(compound), -1, "docx"},
		{"unknown-kind", context.Background(), bytes.NewReader(compound), 8, "DOCX"},
		{"empty-kind", context.Background(), bytes.NewReader(compound), 8, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Extract(tc.ctx, tc.r, tc.size, tc.kind, DefaultLimits())
			requireError(t, got, err, "unsupported")
		})
	}
}

func TestExtractLimitParameterAdmission(t *testing.T) {
	for _, tc := range []struct {
		name string
		max  int64
		set  func(*Limits, int64)
	}{
		{"source", 64 << 20, func(l *Limits, n int64) { l.SourceBytes = n }},
		{"member", 32 << 20, func(l *Limits, n int64) { l.MemberBytes = n }},
		{"selected", 64 << 20, func(l *Limits, n int64) { l.SelectedBytes = n }},
		{"output", 4 << 20, func(l *Limits, n int64) { l.TextBytes = n }},
		{"shared-strings", 4 << 20, func(l *Limits, n int64) { l.SharedStringBytes = n }},
		{"entries", 8192, func(l *Limits, n int64) { l.Entries = int(n) }},
		{"tokens", 8388608, func(l *Limits, n int64) { l.XMLTokens = int(n) }},
		{"depth", 128, func(l *Limits, n int64) { l.Depth = int(n) }},
		{"paragraphs", 65536, func(l *Limits, n int64) { l.Paragraphs = int(n) }},
		{"cells", 131072, func(l *Limits, n int64) { l.Cells = int(n) }},
		{"parts", 512, func(l *Limits, n int64) { l.Parts = int(n) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, n := range []int64{1, tc.max} {
				limits := DefaultLimits()
				tc.set(&limits, n)
				size := int64(8)
				if tc.name == "source" && n == 1 {
					// A reduced source bound is valid, but the actual source must still fit.
					got, err := Extract(context.Background(), bytes.NewReader(compound), size, "docx", limits)
					requireError(t, got, err, "too_large")
					continue
				}
				got, err := Extract(context.Background(), bytes.NewReader(compound), size, "docx", limits)
				if err != nil || got.State != "compound_unreadable" {
					t.Fatalf("limit %d result = %#v, %v", n, got, err)
				}
			}
			for _, n := range []int64{-1, 0, tc.max + 1} {
				limits := DefaultLimits()
				tc.set(&limits, n)
				got, err := Extract(context.Background(), bytes.NewReader(compound), 8, "docx", limits)
				requireError(t, got, err, "unsupported")
			}
		})
	}
}

func TestExtractEntryCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err := Extract(ctx, bytes.NewReader(compound), 8, "docx", DefaultLimits())
	if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(got, Result{}) {
		t.Fatalf("cancelled result = %#v, %v", got, err)
	}
	got, err = Extract(ctx, nil, 0, "", Limits{})
	if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(got, Result{}) {
		t.Fatalf("cancelled invalid result = %#v, %v", got, err)
	}
}

type failingReaderAt struct{}

func (failingReaderAt) ReadAt(p []byte, off int64) (int, error) {
	return 0, errors.New("synthetic-private-value")
}

type cancelReaderAt struct{ cancel context.CancelFunc }

func (r cancelReaderAt) ReadAt(p []byte, off int64) (int, error) {
	r.cancel()
	return copy(p, compound), nil
}

func TestExtractHeaderFailure(t *testing.T) {
	got, err := Extract(context.Background(), failingReaderAt{}, 8, "docx", DefaultLimits())
	requireError(t, got, err, "read_failed")
	got, err = Extract(context.Background(), bytes.NewReader(nil), 8, "docx", DefaultLimits())
	requireError(t, got, err, "malformed")
	ctx, cancel := context.WithCancel(context.Background())
	got, err = Extract(ctx, cancelReaderAt{cancel}, 8, "docx", DefaultLimits())
	if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(got, Result{}) {
		t.Fatalf("terminal cancellation = %#v, %v", got, err)
	}
}

func TestExtractZIPBoundary(t *testing.T) {
	raw := directoryFixture(65537, 1)
	got, err := Extract(context.Background(), bytes.NewReader(raw), int64(len(raw)), "docx", DefaultLimits())
	requireError(t, got, err, "too_large")
	raw = directoryFixture(1, 1)
	raw[0] = 0
	got, err = Extract(context.Background(), bytes.NewReader(raw), int64(len(raw)), "docx", DefaultLimits())
	requireError(t, got, err, "unsupported")
	raw[0] = 'P'
	got, err = Extract(context.Background(), bytes.NewReader(raw[:8]), 8, "docx", DefaultLimits())
	requireError(t, got, err, "malformed")
}

func requireError(t *testing.T, got Result, err error, code string) {
	t.Helper()
	var readErr *ReadError
	if !reflect.DeepEqual(got, Result{}) || !errors.As(err, &readErr) || readErr.Code != code || err.Error() != code {
		t.Fatalf("result = %#v, error = %v; want empty/%s", got, err, code)
	}
}
