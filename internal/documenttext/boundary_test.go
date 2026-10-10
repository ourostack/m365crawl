package documenttext

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
)

type paddedCompoundReader struct{ size int64 }

func (r paddedCompoundReader) ReadAt(p []byte, off int64) (int, error) {
	clear(p)
	if off < 8 {
		copy(p, compound[off:])
	}
	return len(p), nil
}

func TestExtractSourceByteBudget(t *testing.T) {
	got, err := Extract(context.Background(), paddedCompoundReader{64 << 20}, 64<<20, "docx", DefaultLimits())
	if err != nil || got.State != "compound_unreadable" {
		t.Fatalf("public source inclusive = %#v, %v", got, err)
	}
	got, err = Extract(context.Background(), paddedCompoundReader{64<<20 + 1}, 64<<20+1, "docx", DefaultLimits())
	requireError(t, got, err, "too_large")
	raw := wordFixture(t, "", "", "")
	limits := DefaultLimits()
	limits.SourceBytes = int64(len(raw))
	got, err = Extract(context.Background(), bytes.NewReader(raw), int64(len(raw)), "docx", limits)
	if err != nil || got.State != "text_observations" {
		t.Fatalf("reduced source = %#v, %v", got, err)
	}
	limits.SourceBytes--
	got, err = Extract(context.Background(), bytes.NewReader(raw), int64(len(raw)), "docx", limits)
	requireError(t, got, err, "too_large")
}

func TestPublicParagraphAndCellBounds(t *testing.T) {
	wordOpen := `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>`
	for _, n := range []int{65536, 65537} {
		raw := wordFixture(t, wordOpen+strings.Repeat("<w:p/>", n)+`</w:body></w:document>`, "", "")
		got, err := Extract(context.Background(), bytes.NewReader(raw), int64(len(raw)), "docx", DefaultLimits())
		if n == 65537 {
			requireError(t, got, err, "too_large")
			continue
		}
		if err != nil || len(got.Paragraphs) != 65536 {
			t.Fatalf("public paragraph boundary = %d, %v", len(got.Paragraphs), err)
		}
	}
	sheetOpen := `<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>`
	for _, n := range []int{131072, 131073} {
		raw := excelFixture(t, sheetOpen+strings.Repeat("<c/>", n)+`</sheetData></worksheet>`, `<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"/>`)
		got, err := Extract(context.Background(), bytes.NewReader(raw), int64(len(raw)), "xlsx", DefaultLimits())
		if n == 131073 {
			requireError(t, got, err, "too_large")
			continue
		}
		if err != nil || len(got.Cells) != 0 || !reflect.DeepEqual(got.Losses, []Loss{{Code: "cell_unmapped", Count: 131072}}) {
			t.Fatalf("public refused cell boundary = %#v, %v", got.Losses, err)
		}
	}
}

func TestPublicOutputStringBudget(t *testing.T) {
	open := `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>`
	close := `</w:t></w:r></w:p></w:body></w:document>`
	for _, length := range []int{4194287, 4194288} {
		raw := wordFixture(t, open+strings.Repeat("a", length)+close, "", "")
		got, err := Extract(context.Background(), bytes.NewReader(raw), int64(len(raw)), "docx", DefaultLimits())
		if length == 4194288 {
			requireError(t, got, err, "too_large")
			continue
		}
		if err != nil || len(got.Paragraphs) != 1 || len(got.Paragraphs[0].Text) != 4194287 {
			t.Fatalf("public output boundary = %d, %v", len(got.Paragraphs), err)
		}
	}
}

func TestPublicXMLDepthBudget(t *testing.T) {
	for _, depth := range []int{128, 129} {
		body := `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">` + strings.Repeat("<x>", depth-1) + strings.Repeat("</x>", depth-1) + `</w:document>`
		raw := wordFixture(t, body, "", "")
		got, err := Extract(context.Background(), bytes.NewReader(raw), int64(len(raw)), "docx", DefaultLimits())
		if depth == 129 {
			requireError(t, got, err, "too_large")
			continue
		}
		if err != nil || got.State != "text_observations" {
			t.Fatalf("public depth = %#v, %v", got, err)
		}
	}
}

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
		{"impossible-size", func(f *zip.File, _ *extractor) { f.UncompressedSize64 = ^uint64(0) }, "too_large"},
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
