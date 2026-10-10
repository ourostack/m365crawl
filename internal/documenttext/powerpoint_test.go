package documenttext

import (
	"bytes"
	"context"
	"reflect"
	"testing"
)

func powerPointFixture(t *testing.T, slide string) []byte {
	t.Helper()
	return zipFixture(t,
		zipPart{"ppt/slides/slide1.xml", `<p:sld xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"><a:p><a:r><a:t>Last</a:t></a:r></a:p></p:sld>`},
		zipPart{"ppt/slides/slide9.xml", slide},
		zipPart{"ppt/slides/_rels/slide9.xml.rels", `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="n" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/notesSlide" Target="../notesSlides/notesSlide9.xml"/></Relationships>`},
		zipPart{"ppt/notesSlides/notesSlide9.xml", `<p:notes xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"><a:p><a:r><a:t>Note</a:t></a:r></a:p></p:notes>`},
		zipPart{"ppt/presentation.xml", `<p:presentation xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><p:sldIdLst><p:sldId id="9" r:id="s9"/><p:sldId id="1" r:id="s1"/></p:sldIdLst></p:presentation>`},
		zipPart{"ppt/_rels/presentation.xml.rels", `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="s1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slide" Target="slides/slide1.xml"/><Relationship Id="s9" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slide" Target="slides/slide9.xml"/></Relationships>`},
		zipPart{"_rels/.rels", `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="root" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="ppt/presentation.xml"/></Relationships>`},
		zipPart{"[Content_Types].xml", `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Override PartName="/ppt/presentation.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.presentation.main+xml"/></Types>`},
	)
}

func TestPowerPointPresentationOrder(t *testing.T) {
	raw := powerPointFixture(t, `<p:sld xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"><a:p><a:r><a:t>A</a:t></a:r><a:r><a:t>B</a:t></a:r><a:br/><a:r><a:t>C</a:t></a:r></a:p><a:p/></p:sld>`)
	got, err := Extract(context.Background(), bytes.NewReader(raw), int64(len(raw)), "pptx", DefaultLimits())
	want := Result{Kind: "pptx", State: "text_observations", Paragraphs: []Paragraph{
		{Part: "ppt/slides/slide9.xml", Index: 0, Text: "AB\nC"},
		{Part: "ppt/slides/slide9.xml", Index: 1},
		{Part: "ppt/notesSlides/notesSlide9.xml", Index: 0, Text: "Note"},
		{Part: "ppt/slides/slide1.xml", Index: 0, Text: "Last"},
	}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("PowerPoint = %#v, %v; want %#v", got, err, want)
	}
}

func TestPowerPointContentLoss(t *testing.T) {
	raw := powerPointFixture(t, `<p:sld xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"><a:p><a:r><a:t>A</a:t></a:r><a:fld><a:t>dynamic</a:t></a:fld><a:r><a:t>B</a:t></a:r></a:p><p:pic><a:t>alt not visible</a:t></p:pic><a:t>unbound</a:t></p:sld>`)
	got, err := Extract(context.Background(), bytes.NewReader(raw), int64(len(raw)), "pptx", DefaultLimits())
	want := Result{Kind: "pptx", State: "text_observations", Partial: true, Paragraphs: []Paragraph{
		{Part: "ppt/slides/slide9.xml", Index: 0, Text: "AB"},
		{Part: "ppt/notesSlides/notesSlide9.xml", Index: 0, Text: "Note"},
		{Part: "ppt/slides/slide1.xml", Index: 0, Text: "Last"},
	}, Losses: []Loss{{Code: "embedded_content_unmapped", Count: 2}, {Code: "text_without_paragraph_unmapped", Count: 1}}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("PowerPoint loss = %#v, %v; want %#v", got, err, want)
	}

}

func TestPowerPointNonElementMainTokens(t *testing.T) {
	raw := powerPointFixture(t, `<p:sld xmlns:p="`+presentationNS+`"/>`)
	raw = mutateZIP(t, raw, "ppt/presentation.xml", `<p:presentation xmlns:p="`+presentationNS+`" xmlns:r="`+relationNS+`"><!--unselected--><p:sldIdLst><p:sldId r:id="s9"/></p:sldIdLst></p:presentation>`)
	got, err := Extract(context.Background(), bytes.NewReader(raw), int64(len(raw)), "pptx", DefaultLimits())
	if err != nil || len(got.Paragraphs) != 1 {
		t.Fatalf("main non-element tokens = %#v,%v", got, err)
	}
}
