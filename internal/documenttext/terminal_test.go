package documenttext

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
)

type checkpointContext struct {
	context.Context
	remaining int
	cause     error
}

func (c *checkpointContext) Err() error {
	c.remaining--
	if c.remaining <= 0 {
		return c.cause
	}
	return nil
}

func TestExtractEveryContextCheckpoint(t *testing.T) {
	fixtures := []struct {
		kind string
		raw  []byte
	}{
		{"docx", wordFixture(t, `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>A</w:t><w:tab/></w:r></w:p></w:body></w:document>`, "", "")},
		{"xlsx", excelFixture(t, `<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><c r="A1" t="s"><v>0</v></c></worksheet>`, `<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><si><t>A</t></si></sst>`)},
		{"pptx", powerPointFixture(t, `<p:sld xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"><a:p><a:r><a:t>A</a:t></a:r><a:br/></a:p></p:sld>`)},
	}
	for _, fixture := range fixtures {
		for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
			complete := false
			for checkpoint := 1; checkpoint < 1000; checkpoint++ {
				ctx := &checkpointContext{Context: context.Background(), remaining: checkpoint, cause: cause}
				got, err := Extract(ctx, bytes.NewReader(fixture.raw), int64(len(fixture.raw)), fixture.kind, DefaultLimits())
				if err == nil {
					complete = true
					break
				}
				if !errors.Is(err, cause) || !reflect.DeepEqual(got, Result{}) {
					t.Fatalf("%s checkpoint%d = %#v,%v", fixture.kind, checkpoint, got, err)
				}
			}
			if !complete {
				t.Fatalf("%s did not reach a finite successful checkpoint", fixture.kind)
			}
		}
	}
}

type readFault struct {
	io.ReaderAt
	remaining int
}

func (r *readFault) ReadAt(p []byte, off int64) (int, error) {
	r.remaining--
	if r.remaining == 0 {
		return 0, errors.New("synthetic-private-value")
	}
	return r.ReaderAt.ReadAt(p, off)
}

func TestExtractEveryReadFailure(t *testing.T) {
	raw := wordFixture(t, "", "", "")
	for index := 1; index <= 25; index++ {
		reader := &readFault{ReaderAt: bytes.NewReader(raw), remaining: index}
		got, err := Extract(context.Background(), reader, int64(len(raw)), "docx", DefaultLimits())
		if err != nil {
			requireError(t, got, err, "read_failed")
		}
	}
}

type closeFaultReader struct{ io.Reader }

func (closeFaultReader) Close() error { return errors.New("synthetic-private-close") }

func TestSelectedMemberRuntimeIntegrity(t *testing.T) {
	raw := zipFixture(t, zipPart{"main.xml", `<root/>`})
	reader, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	makeExtractor := func() *extractor {
		return &extractor{ctx: context.Background(), limits: DefaultLimits(), members: map[string][]*zip.File{"main.xml": {reader.File[0]}}, roots: map[string]xml.Name{"main.xml": {Local: "root"}}, selected: map[string]bool{}}
	}
	reader.RegisterDecompressor(zip.Deflate, func(io.Reader) io.ReadCloser { return closeFaultReader{Reader: strings.NewReader("<root/>")} })
	requireZIPError(t, makeExtractor().readXML("main.xml", xmlNoop), "read_failed")
	reader.RegisterDecompressor(zip.Deflate, func(io.Reader) io.ReadCloser { return io.NopCloser(strings.NewReader(strings.Repeat(" ", 33))) })
	x := makeExtractor()
	x.limits.MemberBytes = 32
	x.selected["main.xml"] = true
	reader.File[0].UncompressedSize64 = 33
	requireZIPError(t, x.readXML("main.xml", xmlNoop), "too_large")
	reader.RegisterDecompressor(zip.Deflate, func(io.Reader) io.ReadCloser { return io.NopCloser(strings.NewReader("")) })
	reader.File[0].UncompressedSize64 = 0
	reader.File[0].CRC32 = 0
	requireZIPError(t, makeExtractor().readXML("main.xml", xmlNoop), "malformed")
	_, err = makeExtractor().selectPart("missing.xml")
	requireZIPError(t, err, "malformed")
	x = makeExtractor()
	delete(x.roots, "main.xml")
	requireZIPError(t, x.readXML("main.xml", xmlNoop), "unsupported")
	reader.File[0].Method = 99
	x = makeExtractor()
	x.selected["main.xml"] = true
	// A selected unsupported member is refused before attempting decompression.
	requireZIPError(t, x.readXML("main.xml", xmlNoop), "unsupported")
}

func TestXMLOutsideRootAndInstructions(t *testing.T) {
	for _, main := range []string{
		`not-whitespace<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"/>`,
		`<?arbitrary instruction?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"/>`,
		`<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"/><?xml version="1.0"?>`,
	} {
		raw := wordFixture(t, main, "", "")
		got, err := Extract(context.Background(), bytes.NewReader(raw), int64(len(raw)), "docx", DefaultLimits())
		code := "unsupported"
		if strings.HasPrefix(main, "not-") {
			code = "malformed"
		}
		requireError(t, got, err, code)
	}
}
