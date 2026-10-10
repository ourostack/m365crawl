package documenttext

import (
	"bytes"
	"context"
	"encoding/xml"
	"io"
	"strings"
	"testing"
)

func mutateZIP(t *testing.T, raw []byte, name, replacement string) []byte {
	t.Helper()
	p, err := openPackage(context.Background(), bytes.NewReader(raw), int64(len(raw)), map[bool]string{true: "pptx", false: "xlsx"}[strings.HasPrefix(name, "ppt/")], DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	var parts []zipPart
	found := false
	for key, files := range p.members {
		if key == name {
			found = true
			parts = append(parts, zipPart{key, replacement})
			continue
		}
		for _, file := range files {
			reader, e := file.Open()
			if e != nil {
				t.Fatal(e)
			}
			body, e := io.ReadAll(reader)
			_ = reader.Close()
			if e != nil {
				t.Fatal(e)
			}
			parts = append(parts, zipPart{key, string(body)})
		}
	}
	if !found {
		parts = append(parts, zipPart{name, replacement})
	}
	return zipFixture(t, parts...)
}

func TestExcelRelationshipFailures(t *testing.T) {
	base := excelFixture(t, `<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData/></worksheet>`, `<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"/>`)
	for _, tc := range []struct{ name, part, body, code string }{
		{"missing-id", "xl/workbook.xml", `<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheets><sheet/></sheets></workbook>`, "unsupported"},
		{"bad-root", "xl/workbook.xml", `<workbook xmlns="urn:unknown"/>`, "unsupported"},
		{"missing-relationship", "xl/_rels/workbook.xml.rels", `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"/>`, "malformed"},
		{"wrong-relationship", "xl/_rels/workbook.xml.rels", `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="s1" Type="wrong" Target="worksheets/sheet1.xml"/></Relationships>`, "unsupported"},
		{"bad-relationship-root", "xl/_rels/workbook.xml.rels", `<Relationships xmlns="urn:unknown"/>`, "unsupported"},
		{"duplicate-shared-table", "xl/_rels/workbook.xml.rels", `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="ss1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/sharedStrings" Target="sharedStrings.xml"/><Relationship Id="ss2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/sharedStrings" Target="sharedStrings.xml"/></Relationships>`, "unsupported"},
		{"nested-string", "xl/sharedStrings.xml", `<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><si><si/></si></sst>`, "malformed"},
		{"nested-cell", "xl/worksheets/sheet1.xml", `<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><c r="A1"><c r="B1"/></c></worksheet>`, "malformed"},
		{"duplicate-cell-attribute", "xl/worksheets/sheet1.xml", `<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><c r="A1" r="B1"/></worksheet>`, "unsupported"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := mutateZIP(t, base, tc.part, tc.body)
			got, err := Extract(context.Background(), bytes.NewReader(raw), int64(len(raw)), "xlsx", DefaultLimits())
			requireError(t, got, err, tc.code)
		})
	}
}

func TestExcelExternalAndMalformedCells(t *testing.T) {
	base := excelFixture(t, `<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><c r="A1"/></worksheet>`, `<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"/>`)
	raw := mutateZIP(t, base, "xl/_rels/workbook.xml.rels", `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="s1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="https://example.invalid/sheet" TargetMode="External"/></Relationships>`)
	got, err := Extract(context.Background(), bytes.NewReader(raw), int64(len(raw)), "xlsx", DefaultLimits())
	if err != nil || len(got.Cells) != 0 || len(got.Losses) != 1 || got.Losses[0].Code != "external_part_unmapped" {
		t.Fatalf("external worksheet: %#v,%v", got, err)
	}
	raw = excelFixture(t, `<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><extension xmlns="urn:unused">unselected</extension><c r="A1" s="1"><v>a</v><v>b</v></c><c r="A2" t="inlineStr"><is><t>A</t><rPh><t>ignore</t></rPh><t>B</t></is></c><c r="A3" t="inlineStr"><is/><is/></c><c r="A0"/><c r="A1x"/></worksheet>`, `<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><extension xmlns="urn:unused"/></sst>`)
	got, err = Extract(context.Background(), bytes.NewReader(raw), int64(len(raw)), "xlsx", DefaultLimits())
	if err != nil || len(got.Cells) != 1 || got.Cells[0].Value != "AB" || len(got.Losses) != 3 {
		t.Fatalf("cell losses: %#v,%v", got, err)
	}
	x, e := openPackage(context.Background(), bytes.NewReader(base), int64(len(base)), "xlsx", DefaultLimits())
	if e != nil {
		t.Fatal(e)
	}
	x.limits.Cells = 1
	// Shared strings use an independent finite table and candidate-entry guard.
	stringRaw := excelFixture(t, `<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"/>`, `<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><si/><si/></sst>`)
	x, e = openPackage(context.Background(), bytes.NewReader(stringRaw), int64(len(stringRaw)), "xlsx", DefaultLimits())
	if e != nil {
		t.Fatal(e)
	}
	x.limits.Cells = 1
	requireZIPError(t, x.readExcel(), "too_large")
}

func TestPowerPointRelationshipFailures(t *testing.T) {
	base := powerPointFixture(t, `<p:sld xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"/>`)
	for _, tc := range []struct{ name, part, body, code string }{
		{"missing-id", "ppt/presentation.xml", `<p:presentation xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"><p:sldIdLst><p:sldId/></p:sldIdLst></p:presentation>`, "unsupported"},
		{"bad-root", "ppt/presentation.xml", `<presentation xmlns="urn:unknown"/>`, "unsupported"},
		{"missing-slide-rels", "ppt/_rels/presentation.xml.rels", `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"/>`, "malformed"},
		{"bad-slide-rels", "ppt/_rels/presentation.xml.rels", `<Relationships xmlns="urn:unknown"/>`, "unsupported"},
		{"wrong-slide-type", "ppt/_rels/presentation.xml.rels", `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="s9" Type="wrong" Target="slides/slide9.xml"/></Relationships>`, "unsupported"},
		{"duplicate-slide-target", "ppt/_rels/presentation.xml.rels", `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="s1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slide" Target="slides/slide9.xml"/><Relationship Id="s9" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slide" Target="slides/slide9.xml"/></Relationships>`, "unsupported"},
		{"bad-slide", "ppt/slides/slide9.xml", `<sld xmlns="urn:unknown"/>`, "unsupported"},
		{"bad-notes-rels", "ppt/slides/_rels/slide9.xml.rels", `<Relationships xmlns="urn:unknown"/>`, "unsupported"},
		{"bad-notes", "ppt/notesSlides/notesSlide9.xml", `<notes xmlns="urn:unknown"/>`, "unsupported"},
		{"duplicate-notes", "ppt/slides/_rels/slide9.xml.rels", `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="n" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/notesSlide" Target="../notesSlides/notesSlide9.xml"/><Relationship Id="n2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/notesSlide" Target="../notesSlides/notesSlide9.xml"/></Relationships>`, "unsupported"},
		{"nested-paragraph", "ppt/slides/slide9.xml", `<p:sld xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"><a:p><a:p/></a:p></p:sld>`, "malformed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := mutateZIP(t, base, tc.part, tc.body)
			got, err := Extract(context.Background(), bytes.NewReader(raw), int64(len(raw)), "pptx", DefaultLimits())
			requireError(t, got, err, tc.code)
		})
	}
	raw := mutateZIP(t, base, "ppt/slides/_rels/slide9.xml.rels", `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="other" Type="other" Target="unselected"/><Relationship Id="n" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/notesSlide" Target="https://example.invalid/notes" TargetMode="External"/></Relationships>`)
	got, err := Extract(context.Background(), bytes.NewReader(raw), int64(len(raw)), "pptx", DefaultLimits())
	if err != nil || len(got.Losses) != 1 {
		t.Fatalf("external notes: %#v,%v", got, err)
	}
}

func TestRelationIdentityAndNativeTarget(t *testing.T) {
	for _, attrs := range [][]xml.Attr{nil, {{Name: xml.Name{Space: relationNS, Local: "id"}, Value: ""}}, {{Name: xml.Name{Space: relationNS, Local: "id"}, Value: "a"}, {Name: xml.Name{Space: relationNS, Local: "id"}, Value: "b"}}} {
		_, err := relationID(xml.StartElement{Attr: attrs})
		requireZIPError(t, err, "unsupported")
	}
	for _, value := range []string{"", "A", "1A", "a1", "A0", "A1x", strings.Repeat("A", 33)} {
		if validCellReference(value) {
			t.Fatalf("invalid reference %q admitted", value)
		}
	}
	for _, tc := range []struct{ part, target, want string }{
		{"word/document.xml", "./header1.xml", "word/header1.xml"},
		{"word/document.xml", "/word/header1.xml", "word/header1.xml"},
		{"ppt/slides/slide1.xml", "../notesSlides/notesSlide1.xml", "ppt/notesSlides/notesSlide1.xml"},
	} {
		got, err := internalTarget(tc.part, tc.target)
		if err != nil || got != tc.want {
			t.Fatalf("relative target %q = %q,%v", tc.target, got, err)
		}
	}
	for _, target := range []string{"https://example.invalid/a", "a?query=1", "a#fragment", "a\\b", "a//b", "/", "../escape", "./", "%00", "%ZZ"} {
		_, err := internalTarget("", target)
		requireZIPError(t, err, "unsupported")
	}
}
