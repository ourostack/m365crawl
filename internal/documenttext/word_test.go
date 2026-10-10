package documenttext

import (
	"bytes"
	"context"
	"reflect"
	"testing"
)

func extractWord(t *testing.T, main, rels string, extras ...zipPart) (Result, error) {
	t.Helper()
	raw := wordFixture(t, main, "", "", append([]zipPart{{"word/_rels/document.xml.rels", rels}}, extras...)...)
	return Extract(context.Background(), bytes.NewReader(raw), int64(len(raw)), "docx", DefaultLimits())
}

func TestWordTextAndParts(t *testing.T) {
	main := `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t xml:space="preserve"> A </w:t><w:tab/><w:t>B</w:t><w:br/><w:t>C</w:t></w:r></w:p><w:p/></w:body></w:document>`
	rels := `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="h" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/header" Target="header1.xml"/><Relationship Id="n" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/footnotes" Target="footnotes.xml"/></Relationships>`
	main = `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><w:body><w:p><w:r><w:t xml:space="preserve"> A </w:t><w:tab/><w:t>B</w:t><w:br/><w:t>C</w:t></w:r></w:p><w:p/><w:sectPr><w:headerReference r:id="h"/></w:sectPr></w:body></w:document>`
	got, err := extractWord(t, main, rels,
		zipPart{"word/header1.xml", `<w:hdr xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:p><w:r><w:t>Header</w:t></w:r></w:p></w:hdr>`},
		zipPart{"word/footnotes.xml", `<w:footnotes xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:footnote><w:p><w:r><w:t>Note</w:t></w:r></w:p></w:footnote></w:footnotes>`},
		zipPart{"word/header9.xml", `<w:hdr xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:p><w:r><w:t>Not referenced</w:t></w:r></w:p></w:hdr>`},
	)
	want := Result{Kind: "docx", State: "text_observations", Paragraphs: []Paragraph{
		{Part: "word/document.xml", Index: 0, Text: " A \tB\nC"},
		{Part: "word/document.xml", Index: 1, Text: ""},
		{Part: "word/header1.xml", Index: 0, Text: "Header"},
		{Part: "word/footnotes.xml", Index: 0, Text: "Note"},
	}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("Word = %#v, %v; want %#v", got, err, want)
	}
}

func TestWordNestedParagraphsAndSharedHeader(t *testing.T) {
	main := `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><w:body><w:p><w:r><w:t>A</w:t></w:r><w:txbxContent><w:p><w:r><w:t>B</w:t></w:r></w:p></w:txbxContent><w:r><w:t>C</w:t></w:r></w:p><w:sectPr><w:headerReference r:id="h"/><w:headerReference r:id="h2"/></w:sectPr></w:body></w:document>`
	rels := `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="h" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/header" Target="header1.xml"/><Relationship Id="h2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/header" Target="header1.xml"/></Relationships>`
	got, err := extractWord(t, main, rels, zipPart{"word/header1.xml", `<w:hdr xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:p><w:r><w:t>H</w:t></w:r></w:p></w:hdr>`})
	want := Result{Kind: "docx", State: "text_observations", Paragraphs: []Paragraph{
		{Part: "word/document.xml", Index: 0, Text: "AC"},
		{Part: "word/document.xml", Index: 1, Text: "B"},
		{Part: "word/header1.xml", Index: 0, Text: "H"},
	}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("nested Word = %#v, %v; want %#v", got, err, want)
	}
}

func TestWordRevisionAndUnknownText(t *testing.T) {
	main := `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>A</w:t><w:instrText>instruction</w:instrText></w:r><w:del><w:r><w:delText>deleted</w:delText></w:r></w:del><w:moveFrom><w:r><w:t>old</w:t></w:r></w:moveFrom><w:ins><w:r><w:t>B</w:t></w:r></w:ins><w:moveTo><w:r><w:t>C</w:t></w:r></w:moveTo></w:p><w:t>unbound</w:t></w:body></w:document>`
	raw := wordFixture(t, main, "", "")
	got, err := Extract(context.Background(), bytes.NewReader(raw), int64(len(raw)), "docx", DefaultLimits())
	want := Result{Kind: "docx", State: "text_observations", Partial: true,
		Paragraphs: []Paragraph{{Part: "word/document.xml", Index: 0, Text: "ABC"}},
		Losses:     []Loss{{Code: "revision_or_instruction_unmapped", Count: 3}, {Code: "text_without_paragraph_unmapped", Count: 1}},
	}

	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("revision Word = %#v, %v; want %#v", got, err, want)
	}
}

func TestWordAlternateAndExternalParts(t *testing.T) {
	main := `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main" xmlns:mc="http://schemas.openxmlformats.org/markup-compatibility/2006" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><w:body><w:p><w:r><w:t>A</w:t></w:r><mc:AlternateContent><mc:Choice><w:r><w:t>choice</w:t></w:r></mc:Choice><mc:Fallback><w:r><w:t>fallback</w:t></w:r></mc:Fallback></mc:AlternateContent><w:r><w:t>B</w:t></w:r></w:p><w:sectPr><w:headerReference r:id="h"/></w:sectPr></w:body></w:document>`
	rels := `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="h" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/header" Target="https://example.invalid/header" TargetMode="External"/></Relationships>`
	got, err := extractWord(t, main, rels)
	want := Result{Kind: "docx", State: "text_observations", Partial: true,
		Paragraphs: []Paragraph{{Part: "word/document.xml", Index: 0, Text: "AB"}},
		Losses:     []Loss{{Code: "embedded_content_unmapped", Count: 1}, {Code: "external_part_unmapped", Count: 1}},
	}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("alternate Word = %#v, %v; want %#v", got, err, want)
	}
}

func TestWordOutputAndParagraphBounds(t *testing.T) {
	main := `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>AB</w:t></w:r></w:p><w:p/></w:body></w:document>`
	raw := wordFixture(t, main, "", "")
	limits := DefaultLimits()
	limits.TextBytes = 36 // two 17-byte part occurrences and two text bytes
	got, err := Extract(context.Background(), bytes.NewReader(raw), int64(len(raw)), "docx", limits)
	if err != nil || len(got.Paragraphs) != 2 {
		t.Fatalf("inclusive output = %#v, %v", got, err)
	}
	limits.TextBytes--
	got, err = Extract(context.Background(), bytes.NewReader(raw), int64(len(raw)), "docx", limits)
	requireError(t, got, err, "too_large")
	limits = DefaultLimits()
	limits.Paragraphs = 1
	got, err = Extract(context.Background(), bytes.NewReader(raw), int64(len(raw)), "docx", limits)
	requireError(t, got, err, "too_large")
}
