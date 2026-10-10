package documenttext

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestPublicXMLTokenBudget(t *testing.T) {
	// Package relationships and content types each spend four tokens; the main root spends two.
	open := `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">`
	pairs := (8388608 - 10) / 2
	main := open + strings.Repeat("<x/>", pairs) + `</w:document>`
	for _, extra := range []string{"", "<!--extra-->"} {
		raw := wordFixture(t, strings.Replace(main, `</w:document>`, extra+`</w:document>`, 1), "", "")
		got, err := Extract(context.Background(), bytes.NewReader(raw), int64(len(raw)), "docx", DefaultLimits())
		if extra != "" {
			requireError(t, got, err, "too_large")
			continue
		}
		if err != nil || got.State != "text_observations" {
			t.Fatalf("public XML tokens: %#v,%v", got, err)
		}
	}
}

func TestPublicSelectedPartBudget(t *testing.T) {
	for _, headers := range []int{508, 509} {
		var main, rels strings.Builder
		main.WriteString(`<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><w:body><w:sectPr>`)
		rels.WriteString(`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">`)
		var parts []zipPart
		for i := range headers {
			fmt.Fprintf(&main, `<w:headerReference r:id="h%d"/>`, i)
			fmt.Fprintf(&rels, `<Relationship Id="h%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/header" Target="header%d.xml"/>`, i, i)
			parts = append(parts, zipPart{fmt.Sprintf("word/header%d.xml", i), `<w:hdr xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"/>`})
		}
		main.WriteString(`</w:sectPr></w:body></w:document>`)
		rels.WriteString(`</Relationships>`)
		parts = append(parts, zipPart{"word/_rels/document.xml.rels", rels.String()})
		raw := wordFixture(t, main.String(), "", "", parts...)
		got, err := Extract(context.Background(), bytes.NewReader(raw), int64(len(raw)), "docx", DefaultLimits())
		if headers == 509 {
			requireError(t, got, err, "too_large")
			continue
		}
		if err != nil || got.State != "text_observations" {
			t.Fatalf("public parts: %#v,%v", got, err)
		}
	}
}

func TestPublicMemberAndSelectedByteBudget(t *testing.T) {
	mainOpen := `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">`
	mainClose := `</w:document>`
	relsOpen := `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="r1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/>`
	relsClose := `</Relationships>`
	types := `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/></Types>`
	for _, extra := range []int{0, 1} {
		main := mainOpen + strings.Repeat(" ", (32<<20)-len(mainOpen)-len(mainClose)+extra) + mainClose
		relsSize := (32 << 20) - len(types)
		rels := relsOpen + strings.Repeat(" ", relsSize-len(relsOpen)-len(relsClose)) + relsClose
		raw := wordFixture(t, main, rels, types)
		got, err := Extract(context.Background(), bytes.NewReader(raw), int64(len(raw)), "docx", DefaultLimits())
		if extra == 1 {
			requireError(t, got, err, "too_large")
			continue
		}
		if err != nil || got.State != "text_observations" {
			t.Fatalf("public member/total: %#v,%v", got, err)
		}
		// Hold each member under its cap while exceeding only the aggregate by one byte.
		rels = relsOpen + strings.Repeat(" ", relsSize-len(relsOpen)-len(relsClose)+1) + relsClose
		raw = wordFixture(t, main, rels, types)
		got, err = Extract(context.Background(), bytes.NewReader(raw), int64(len(raw)), "docx", DefaultLimits())
		requireError(t, got, err, "too_large")
	}
}
