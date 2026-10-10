package documenttext

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"testing"
)

func TestPowerPointExternalAndSharedNotes(t *testing.T) {
	base := powerPointFixture(t, `<p:sld xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"><a:p/></p:sld>`)
	raw := mutateZIP(t, base, "ppt/_rels/presentation.xml.rels", `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="s9" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slide" Target="https://example.invalid/slide" TargetMode="External"/><Relationship Id="s1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slide" Target="slides/slide1.xml"/></Relationships>`)
	got, err := Extract(context.Background(), bytes.NewReader(raw), int64(len(raw)), "pptx", DefaultLimits())
	if err != nil || len(got.Paragraphs) != 1 || len(got.Losses) != 1 || got.Losses[0].Code != "external_part_unmapped" {
		t.Fatalf("external slide: %#v,%v", got, err)
	}
	raw = mutateZIP(t, base, "ppt/slides/_rels/slide9.xml.rels", `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="n" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/notesSlide" Target="../slides/slide9.xml"/></Relationships>`)
	got, err = Extract(context.Background(), bytes.NewReader(raw), int64(len(raw)), "pptx", DefaultLimits())
	requireError(t, got, err, "unsupported")
	raw = mutateZIP(t, base, "ppt/slides/_rels/slide1.xml.rels", `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="n" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/notesSlide" Target="../notesSlides/notesSlide9.xml"/></Relationships>`)
	got, err = Extract(context.Background(), bytes.NewReader(raw), int64(len(raw)), "pptx", DefaultLimits())
	if err != nil || len(got.Paragraphs) != 3 {
		t.Fatalf("shared notes: %#v,%v", got, err)
	}
	limits := DefaultLimits()
	limits.Paragraphs = 1
	got, err = Extract(context.Background(), bytes.NewReader(base), int64(len(base)), "pptx", limits)
	requireError(t, got, err, "too_large")
}

func TestEmptySelectedXML(t *testing.T) {
	raw := zipFixture(t, zipPart{"empty.xml", ""})
	reader, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	x := &extractor{ctx: context.Background(), limits: DefaultLimits(), members: map[string][]*zip.File{"empty.xml": {reader.File[0]}}, roots: map[string]xml.Name{"empty.xml": {Local: "root"}}, selected: map[string]bool{}}
	requireZIPError(t, x.readXML("empty.xml", xmlNoop), "malformed")
}
