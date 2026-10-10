package documenttext

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sort"
)

func Extract(ctx context.Context, r io.ReaderAt, size int64, kind string, limits Limits) (result Result, err error) {
	if ctx == nil {
		return Result{}, &ReadError{Code: "unsupported"}
	}
	defer func() {
		if cancelled := ctx.Err(); cancelled != nil {
			result, err = Result{}, cancelled
		} else if err != nil {
			result = Result{}
		}
	}()
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if r == nil || size <= 0 || (kind != "docx" && kind != "xlsx" && kind != "pptx") || !validLimits(limits) {
		return Result{}, &ReadError{Code: "unsupported"}
	}
	if size > limits.SourceBytes {
		return Result{}, &ReadError{Code: "too_large"}
	}
	header := make([]byte, min(size, 8))
	n, err := r.ReadAt(header, 0)
	if n != len(header) || (err != nil && !errors.Is(err, io.EOF)) {
		code := "read_failed"
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			code = "malformed"
		}
		return Result{}, &ReadError{Code: code}
	}
	if bytes.Equal(header, []byte{0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1}) {
		return Result{Kind: kind, State: "compound_unreadable"}, nil
	}
	if len(header) < 4 || !bytes.Equal(header[:4], []byte{0x50, 0x4b, 0x03, 0x04}) && !bytes.Equal(header[:4], []byte{0x50, 0x4b, 0x01, 0x02}) {
		return Result{}, &ReadError{Code: "unsupported"}
	}
	if size < 22 {
		return Result{}, &ReadError{Code: "malformed"}
	}
	x, err := openPackage(ctx, r, size, kind, limits)
	if err != nil {
		return Result{}, err
	}
	switch kind {
	case "docx":
		err = x.readWord()
	case "xlsx":
		err = x.readExcel()
	case "pptx":
		err = x.readPowerPoint()
	}
	if err != nil {
		return Result{}, err
	}
	for code, count := range x.losses {
		x.result.Losses = append(x.result.Losses, Loss{Code: code, Count: count})
	}
	sort.Slice(x.result.Losses, func(i, j int) bool { return x.result.Losses[i].Code < x.result.Losses[j].Code })
	x.result.Partial = len(x.result.Losses) > 0
	return x.result, nil
}

func validLimits(l Limits) bool {
	maximum := DefaultLimits()
	pairs := [][2]int64{
		{l.SourceBytes, maximum.SourceBytes}, {l.MemberBytes, maximum.MemberBytes},
		{l.SelectedBytes, maximum.SelectedBytes}, {l.TextBytes, maximum.TextBytes},
		{l.SharedStringBytes, maximum.SharedStringBytes},
		{int64(l.Entries), int64(maximum.Entries)}, {int64(l.XMLTokens), int64(maximum.XMLTokens)},
		{int64(l.Depth), int64(maximum.Depth)}, {int64(l.Paragraphs), int64(maximum.Paragraphs)},
		{int64(l.Cells), int64(maximum.Cells)}, {int64(l.Parts), int64(maximum.Parts)},
	}
	for _, p := range pairs {
		if p[0] <= 0 || p[0] > p[1] {
			return false
		}
	}
	return true
}
