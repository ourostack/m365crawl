package documenttext

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"os"
	"reflect"
	"testing"
)

func TestSelectedPartPreflight(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*zip.File, *extractor)
		code string
	}{
		{"symlink", func(f *zip.File, _ *extractor) { f.SetMode(os.ModeSymlink | 0600) }, "unsupported"},
		{"directory", func(f *zip.File, _ *extractor) { f.SetMode(os.ModeDir | 0700) }, "unsupported"},
		{"encrypted", func(f *zip.File, _ *extractor) { f.Flags = 1 }, "unsupported"},
		{"compression", func(f *zip.File, _ *extractor) { f.Method = 99 }, "unsupported"},
		{"declared-member", func(f *zip.File, x *extractor) { f.UncompressedSize64 = 33; x.limits.MemberBytes = 32 }, "too_large"},
		{"declared-total", func(f *zip.File, x *extractor) { f.UncompressedSize64 = 33; x.limits.SelectedBytes = 32 }, "too_large"},
		{"parts", func(_ *zip.File, x *extractor) { x.limits.Parts = 1; x.selected["first"] = true }, "too_large"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file := &zip.File{FileHeader: zip.FileHeader{Name: "word/document.xml", Method: zip.Store}}
			x := &extractor{limits: DefaultLimits(), members: map[string][]*zip.File{"word/document.xml": {file}}, selected: map[string]bool{}}
			tc.edit(file, x)
			_, err := x.selectPart("word/document.xml")
			requireZIPError(t, err, tc.code)
		})
	}
}

func TestXMLWorkBudgets(t *testing.T) {
	raw := wordFixture(t, "", "", "")
	x, err := openPackage(context.Background(), bytes.NewReader(raw), int64(len(raw)), "docx", DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	before := x.tokens
	x.limits.XMLTokens = before + 3
	if err := x.readXML(x.main, xmlNoop); err == nil {
		t.Fatal("main start/body start/body end/main end exceeded three remaining tokens")
	} else {
		requireZIPError(t, err, "too_large")
	}

	x, err = openPackage(context.Background(), bytes.NewReader(raw), int64(len(raw)), "docx", DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	x.limits.Depth = 1
	requireZIPError(t, x.readXML(x.main, xmlNoop), "too_large")
	x, err = openPackage(context.Background(), bytes.NewReader(raw), int64(len(raw)), "docx", DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	x.limits.Depth = 2
	if err := x.readXML(x.main, xmlNoop); err != nil {
		t.Fatalf("inclusive native XML depth: %v", err)
	}
}

func TestXMLTerminalCancellationAndCallback(t *testing.T) {
	raw := wordFixture(t, "", "", "")
	ctx, cancel := context.WithCancel(context.Background())
	x, err := openPackage(ctx, bytes.NewReader(raw), int64(len(raw)), "docx", DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	err = x.readXML(x.main, func(token xml.Token) error {
		if _, ok := token.(xml.EndElement); ok {
			cancel()
			return &ReadError{Code: "unsupported"}
		}
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation precedence: %v", err)
	}
	got, err := Extract(ctx, bytes.NewReader(raw), int64(len(raw)), "docx", DefaultLimits())
	if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(got, Result{}) {
		t.Fatalf("fatal result: %#v, %v", got, err)
	}
	x, err = openPackage(context.Background(), bytes.NewReader(raw), int64(len(raw)), "docx", DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	requireZIPError(t, x.readXML(x.main, func(xml.Token) error { return errors.New("synthetic-private-value") }), "read_failed")
}
