package documenttext

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestReviewIgnoredCandidateBudgets(t *testing.T) {
	for _, n := range []int{2, 65537} {
		raw := wordFixture(t, `<w:document xmlns:w="`+wordNS+`"><w:body><w:p><w:r><w:t>OK</w:t></w:r></w:p><w:del>`+strings.Repeat(`<w:p/>`, n)+`</w:del></w:body></w:document>`, "", "")
		limits := DefaultLimits()
		if n == 2 {
			limits.Paragraphs = 2
		}
		got, err := Extract(context.Background(), bytes.NewReader(raw), int64(len(raw)), "docx", limits)
		requireError(t, got, err, "too_large")
	}
	for _, n := range []int{2, 131073} {
		raw := excelFixture(t, `<worksheet xmlns="`+excelNS+`" xmlns:e="urn:unselected"><c r="A1"><v>OK</v></c><e:hidden>`+strings.Repeat(`<c/>`, n)+`</e:hidden></worksheet>`, `<sst xmlns="`+excelNS+`"/>`)
		limits := DefaultLimits()
		if n == 2 {
			limits.Cells = 2
		}
		got, err := Extract(context.Background(), bytes.NewReader(raw), int64(len(raw)), "xlsx", limits)
		requireError(t, got, err, "too_large")
	}
	raw := powerPointFixture(t, `<p:sld xmlns:p="`+presentationNS+`" xmlns:a="`+drawingNS+`"><p:extLst><a:p/><a:p/></p:extLst></p:sld>`)
	limits := DefaultLimits()
	limits.Paragraphs = 1
	got, err := Extract(context.Background(), bytes.NewReader(raw), int64(len(raw)), "pptx", limits)
	requireError(t, got, err, "too_large")
}

func TestReviewRejectedCellOutputPreflight(t *testing.T) {
	for _, size := range []int{100, 4194305} {
		raw := excelFixture(t, `<worksheet xmlns="`+excelNS+`"><c r="A1"><v>OK</v></c><c r="A1"><v>`+strings.Repeat("R", size)+`</v></c></worksheet>`, `<sst xmlns="`+excelNS+`"/>`)
		limits := DefaultLimits()
		if size == 100 {
			limits.TextBytes = 30
		}
		got, err := Extract(context.Background(), bytes.NewReader(raw), int64(len(raw)), "xlsx", limits)
		requireError(t, got, err, "too_large")
	}
}

func TestReviewExcelExtensionInsideValue(t *testing.T) {
	for _, cell := range []string{`<c r="A1"><v>A<e:hidden>HIDDEN</e:hidden>B</v></c>`, `<c r="A1" t="inlineStr"><is><t>A<e:hidden>HIDDEN</e:hidden>B</t></is></c>`} {
		raw := excelFixture(t, `<worksheet xmlns="`+excelNS+`" xmlns:e="urn:unselected">`+cell+`</worksheet>`, `<sst xmlns="`+excelNS+`"/>`)
		got, err := Extract(context.Background(), bytes.NewReader(raw), int64(len(raw)), "xlsx", DefaultLimits())
		if err != nil || len(got.Cells) != 1 || got.Cells[0].Value != "AB" {
			t.Fatalf("hidden value = %#v,%v", got, err)
		}
	}
}

func TestReviewHiddenWorkbookAndPresentationIDs(t *testing.T) {
	raw := excelFixture(t, `<worksheet xmlns="`+excelNS+`"><c r="A1"><v>VISIBLE</v></c></worksheet>`, `<sst xmlns="`+excelNS+`"/>`)
	raw = mutateZIP(t, raw, "xl/workbook.xml", `<workbook xmlns="`+excelNS+`" xmlns:r="`+relationNS+`" xmlns:e="urn:unselected"><e:hidden><sheet r:id="s1"/></e:hidden></workbook>`)
	got, err := Extract(context.Background(), bytes.NewReader(raw), int64(len(raw)), "xlsx", DefaultLimits())
	if err != nil || len(got.Cells) != 0 {
		t.Fatalf("hidden sheet = %#v,%v", got, err)
	}
	raw = powerPointFixture(t, `<p:sld xmlns:p="`+presentationNS+`" xmlns:a="`+drawingNS+`"><a:p><a:r><a:t>VISIBLE</a:t></a:r></a:p></p:sld>`)
	raw = mutateZIP(t, raw, "ppt/presentation.xml", `<p:presentation xmlns:p="`+presentationNS+`" xmlns:r="`+relationNS+`" xmlns:e="urn:unselected"><e:hidden><p:sldId r:id="s9"/></e:hidden></p:presentation>`)
	got, err = Extract(context.Background(), bytes.NewReader(raw), int64(len(raw)), "pptx", DefaultLimits())
	if err != nil || len(got.Paragraphs) != 0 {
		t.Fatalf("hidden slide = %#v,%v", got, err)
	}
}

func TestReviewNativeExtensionParagraphs(t *testing.T) {
	raw := powerPointFixture(t, `<p:sld xmlns:p="`+presentationNS+`" xmlns:a="`+drawingNS+`"><p:extLst><p:ext uri="urn:unselected"><a:p><a:r><a:t>HIDDEN</a:t></a:r></a:p></p:ext></p:extLst><a:p><a:r><a:t>VISIBLE</a:t></a:r></a:p></p:sld>`)
	got, err := Extract(context.Background(), bytes.NewReader(raw), int64(len(raw)), "pptx", DefaultLimits())
	if err != nil || len(got.Paragraphs) != 3 || got.Paragraphs[0].Text != "VISIBLE" {
		t.Fatalf("native extension = %#v,%v", got, err)
	}
}

func TestReviewWordNonNativeRelationshipType(t *testing.T) {
	main := `<w:document xmlns:w="` + wordNS + `" xmlns:r="` + relationNS + `"><w:body><w:sectPr><w:headerReference r:id="h"/></w:sectPr></w:body></w:document>`
	rels := `<Relationships xmlns="` + packageNS + `"><Relationship Id="h" Type="header" Target="header1.xml"/></Relationships>`
	got, err := extractWord(t, main, rels, zipPart{"word/header1.xml", `<w:hdr xmlns:w="` + wordNS + `"><w:p><w:r><w:t>NOT NATIVE</w:t></w:r></w:p></w:hdr>`})
	requireError(t, got, err, "unsupported")
}

func TestReviewNestedFormulaCache(t *testing.T) {
	raw := excelFixture(t, `<worksheet xmlns="`+excelNS+`"><c r="A1"><f><v>NOT-A-CACHE</v></f></c></worksheet>`, `<sst xmlns="`+excelNS+`"/>`)
	got, err := Extract(context.Background(), bytes.NewReader(raw), int64(len(raw)), "xlsx", DefaultLimits())
	if err != nil || len(got.Cells) != 0 || len(got.Losses) != 1 || got.Losses[0].Code != "cell_unmapped" {
		t.Fatalf("nested cache = %#v,%v", got, err)
	}
}

func TestReviewExternalValueLoss(t *testing.T) {
	raw := excelFixture(t, `<worksheet xmlns="`+excelNS+`"><c r="A1"><f>'[1]Sheet1'!A1</f><v>42</v></c></worksheet>`, `<sst xmlns="`+excelNS+`"/>`)
	raw = mutateZIP(t, raw, "xl/_rels/workbook.xml.rels", `<Relationships xmlns="`+packageNS+`"><Relationship Id="s1" Type="`+relationNS+`/worksheet" Target="worksheets/sheet1.xml"/><Relationship Id="el" Type="`+relationNS+`/externalLink" Target="externalLinks/externalLink1.xml"/></Relationships>`)
	got, err := Extract(context.Background(), bytes.NewReader(raw), int64(len(raw)), "xlsx", DefaultLimits())
	if err != nil || !got.Partial || len(got.Cells) != 1 || got.Cells[0].Value != "42" || len(got.Losses) != 1 || got.Losses[0].Code != "formatting_or_external_values_unmapped" {
		t.Fatalf("external cache = %#v,%v", got, err)
	}

}

func TestReviewInlinePreflightAndNestedCellFields(t *testing.T) {
	for _, cell := range []string{
		`<c r="A1" t="inlineStr"><f><is><t>not native</t></is></f></c>`,
		`<c r="A1"><is><f>not native</f></is></c>`,
	} {
		raw := excelFixture(t, `<worksheet xmlns="`+excelNS+`">`+cell+`</worksheet>`, `<sst xmlns="`+excelNS+`"/>`)
		got, err := Extract(context.Background(), bytes.NewReader(raw), int64(len(raw)), "xlsx", DefaultLimits())
		if err != nil || len(got.Cells) != 0 || len(got.Losses) != 1 {
			t.Fatalf("nested fields = %#v,%v", got, err)
		}
	}
	raw := excelFixture(t, `<worksheet xmlns="`+excelNS+`"><c r="A1" t="inlineStr"><is><t>`+strings.Repeat("x", 31)+`</t></is></c></worksheet>`, `<sst xmlns="`+excelNS+`"/>`)
	limits := DefaultLimits()
	limits.TextBytes = 30
	got, err := Extract(context.Background(), bytes.NewReader(raw), int64(len(raw)), "xlsx", limits)
	requireError(t, got, err, "too_large")
}
