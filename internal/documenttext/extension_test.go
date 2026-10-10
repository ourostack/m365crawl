package documenttext

import (
	"bytes"
	"context"
	"testing"
)

func TestUnselectedExtensionDoesNotExportNativeLookingText(t *testing.T) {
	raw := wordFixture(t, `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main" xmlns:e="urn:unselected"><w:body><e:hidden><w:p><w:r><w:t>hidden</w:t></w:r></w:p></e:hidden><w:p><w:r><w:t>visible</w:t></w:r></w:p></w:body></w:document>`, "", "")
	got, err := Extract(context.Background(), bytes.NewReader(raw), int64(len(raw)), "docx", DefaultLimits())
	if err != nil || len(got.Paragraphs) != 1 || got.Paragraphs[0].Text != "visible" {
		t.Fatalf("Word extension = %#v,%v", got, err)
	}
	raw = excelFixture(t, `<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:e="urn:unselected"><e:hidden><c r="A1"><v>hidden</v></c></e:hidden><c r="B1" t="s"><v>0</v></c></worksheet>`, `<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:e="urn:unselected"><e:hidden><si><t>hidden</t></si></e:hidden><si><t>visible</t></si></sst>`)
	got, err = Extract(context.Background(), bytes.NewReader(raw), int64(len(raw)), "xlsx", DefaultLimits())
	if err != nil || len(got.Cells) != 1 || got.Cells[0].Value != "visible" {
		t.Fatalf("Excel extension = %#v,%v", got, err)
	}
	raw = powerPointFixture(t, `<p:sld xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:e="urn:unselected"><e:hidden><a:p><a:r><a:t>hidden</a:t></a:r></a:p></e:hidden><a:p><a:r><a:t>visible</a:t></a:r></a:p></p:sld>`)
	got, err = Extract(context.Background(), bytes.NewReader(raw), int64(len(raw)), "pptx", DefaultLimits())
	if err != nil || len(got.Paragraphs) != 3 || got.Paragraphs[0].Text != "visible" {
		t.Fatalf("PowerPoint extension = %#v,%v", got, err)
	}
}
