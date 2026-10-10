package documenttext

import (
	"bytes"
	"context"
	"reflect"
	"testing"
)

func excelFixture(t *testing.T, sheet, shared string, extra ...zipPart) []byte {
	t.Helper()
	parts := []zipPart{
		{"xl/worksheets/sheet1.xml", sheet},
		{"xl/sharedStrings.xml", shared},
		{"xl/workbook.xml", `<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="Native sheet" sheetId="1" r:id="s1"/></sheets></workbook>`},
		{"xl/_rels/workbook.xml.rels", `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="s1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/><Relationship Id="ss" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/sharedStrings" Target="sharedStrings.xml"/></Relationships>`},
		{"_rels/.rels", `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="root" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/></Relationships>`},
		{"[Content_Types].xml", `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/></Types>`},
	}
	return zipFixture(t, append(parts, extra...)...)
}

func TestExcelSharedStringsBeforeWorksheets(t *testing.T) {
	sheet := `<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData><row><c r="A1" t="s"><v>1</v></c><c r="B1" t="s"><v>0</v></c><c r="C1" t="s"><v>1</v></c></row></sheetData></worksheet>`
	shared := `<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><si><t>A</t><r><t>B</t></r></si><si><t xml:space="preserve"> Later </t></si></sst>`
	raw := excelFixture(t, sheet, shared)
	got, err := Extract(context.Background(), bytes.NewReader(raw), int64(len(raw)), "xlsx", DefaultLimits())
	want := Result{Kind: "xlsx", State: "text_observations", Cells: []Cell{
		{Part: "xl/worksheets/sheet1.xml", Reference: "A1", Type: "s", Value: " Later "},
		{Part: "xl/worksheets/sheet1.xml", Reference: "B1", Type: "s", Value: "AB"},
		{Part: "xl/worksheets/sheet1.xml", Reference: "C1", Type: "s", Value: " Later "},
	}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("Excel = %#v, %v; want %#v", got, err, want)
	}
}

func TestExcelCellKinds(t *testing.T) {
	sheet := `<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData><row><c r="A1"><v>003.20</v></c><c r="B1" t="b"><v>1</v></c><c r="C1" t="e"><v>#VALUE!</v></c><c r="D1" t="str"><v>raw</v></c><c r="E1" t="d"><v>2026-10-09</v></c><c r="F1" t="inlineStr"><is><t>A</t><r><t>B</t></r></is></c><c r="G1"><f>SUM(A1:A9)</f><v>7</v></c><c r="H1"><f>A2+1</f></c><c r="I1"/></row></sheetData></worksheet>`
	raw := excelFixture(t, sheet, `<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"/>`)
	got, err := Extract(context.Background(), bytes.NewReader(raw), int64(len(raw)), "xlsx", DefaultLimits())
	want := Result{Kind: "xlsx", State: "text_observations", Partial: true, Cells: []Cell{
		{Part: "xl/worksheets/sheet1.xml", Reference: "A1", Type: "n", Value: "003.20"},
		{Part: "xl/worksheets/sheet1.xml", Reference: "B1", Type: "b", Value: "1"},
		{Part: "xl/worksheets/sheet1.xml", Reference: "C1", Type: "e", Value: "#VALUE!"},
		{Part: "xl/worksheets/sheet1.xml", Reference: "D1", Type: "str", Value: "raw"},
		{Part: "xl/worksheets/sheet1.xml", Reference: "E1", Type: "d", Value: "2026-10-09"},
		{Part: "xl/worksheets/sheet1.xml", Reference: "F1", Type: "inlineStr", Value: "AB"},
		{Part: "xl/worksheets/sheet1.xml", Reference: "G1", Type: "n", Value: "7", FormulaCached: true},
		{Part: "xl/worksheets/sheet1.xml", Reference: "H1", Type: "n"},
		{Part: "xl/worksheets/sheet1.xml", Reference: "I1", Type: "n"},
	}, Losses: []Loss{{Code: "formula_without_cache", Count: 1}}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("Excel kinds = %#v, %v; want %#v", got, err, want)
	}
}

func TestExcelCellLosses(t *testing.T) {
	sheet := `<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData><row><c r="A1" t="s"><v>1</v></c><c r="B1" t="s"><v>01</v></c><c r="C1" t="unknown"><v>v</v></c><c r="a1"/><c/><c r="D1"><v>good</v></c><c r="D1"><v>duplicate</v></c><c r="E1" t="inlineStr"><v>wrong</v></c></row></sheetData></worksheet>`
	raw := excelFixture(t, sheet, `<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><si><t>A</t><rPh><t>phonetic</t></rPh><t>B</t></si></sst>`)
	got, err := Extract(context.Background(), bytes.NewReader(raw), int64(len(raw)), "xlsx", DefaultLimits())
	want := Result{Kind: "xlsx", State: "text_observations", Partial: true,
		Cells:  []Cell{{Part: "xl/worksheets/sheet1.xml", Reference: "D1", Type: "n", Value: "good"}},
		Losses: []Loss{{Code: "cell_unmapped", Count: 7}, {Code: "phonetic_text_unmapped", Count: 1}},
	}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("Excel losses = %#v, %v; want %#v", got, err, want)
	}
}

func TestExcelIndependentStringBudgets(t *testing.T) {
	raw := excelFixture(t, `<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData><row><c r="A1" t="s"><v>0</v></c><c r="B1" t="s"><v>0</v></c></row></sheetData></worksheet>`, `<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><si><t>AB</t></si></sst>`)
	limits := DefaultLimits()
	limits.SharedStringBytes = 2
	got, err := Extract(context.Background(), bytes.NewReader(raw), int64(len(raw)), "xlsx", limits)
	if err != nil || len(got.Cells) != 2 {
		t.Fatalf("inclusive shared table = %#v, %v", got, err)
	}
	limits.SharedStringBytes = 1
	got, err = Extract(context.Background(), bytes.NewReader(raw), int64(len(raw)), "xlsx", limits)
	requireError(t, got, err, "too_large")
	limits = DefaultLimits()
	limits.TextBytes = 58 // each cell: 24-byte part + 2-byte ref + 1-byte type + 2-byte value
	got, err = Extract(context.Background(), bytes.NewReader(raw), int64(len(raw)), "xlsx", limits)
	if err != nil || len(got.Cells) != 2 {
		t.Fatalf("inclusive repeated output = %#v, %v", got, err)
	}
	limits.TextBytes--
	got, err = Extract(context.Background(), bytes.NewReader(raw), int64(len(raw)), "xlsx", limits)
	requireError(t, got, err, "too_large")
}

func TestExcelAliasedWorksheet(t *testing.T) {
	raw := zipFixture(t,
		zipPart{"xl/workbook.xml", `<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet r:id="s1"/><sheet r:id="s2"/></sheets></workbook>`},
		zipPart{"xl/_rels/workbook.xml.rels", `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="s1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/><Relationship Id="s2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>`},
		zipPart{"xl/worksheets/sheet1.xml", `<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData><row><c r="A1" t="inlineStr"><is><t>Native</t></is></c></row></sheetData></worksheet>`},
		zipPart{"_rels/.rels", `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="root" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/></Relationships>`},
		zipPart{"[Content_Types].xml", `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/></Types>`},
	)
	got, err := Extract(context.Background(), bytes.NewReader(raw), int64(len(raw)), "xlsx", DefaultLimits())
	want := Result{Kind: "xlsx", State: "text_observations", Cells: []Cell{{Part: "xl/worksheets/sheet1.xml", Reference: "A1", Type: "inlineStr", Value: "Native"}}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("worksheet aliases = %#v, %v; want %#v", got, err, want)
	}

}

func TestExcelNonElementMainTokens(t *testing.T) {
	raw := excelFixture(t, `<worksheet xmlns="`+excelNS+`"><c r="A1"/></worksheet>`, `<sst xmlns="`+excelNS+`"/>`)
	raw = mutateZIP(t, raw, "xl/workbook.xml", `<workbook xmlns="`+excelNS+`" xmlns:r="`+relationNS+`"><!--unselected--><sheets><sheet r:id="s1"/></sheets></workbook>`)
	got, err := Extract(context.Background(), bytes.NewReader(raw), int64(len(raw)), "xlsx", DefaultLimits())
	if err != nil || len(got.Cells) != 1 {
		t.Fatalf("main non-element tokens = %#v,%v", got, err)
	}
}
