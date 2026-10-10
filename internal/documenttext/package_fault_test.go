package documenttext

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/xml"
	"reflect"
	"strings"
	"testing"
)

func TestPackageMissingRootAndContentTypeFields(t *testing.T) {
	raw := zipFixture(t, zipPart{"word/document.xml", "ignored"})
	_, err := openPackage(context.Background(), bytes.NewReader(raw), int64(len(raw)), "docx", DefaultLimits())
	requireZIPError(t, err, "unsupported")
	for _, types := range []string{
		`<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Override PartName="/word/document.xml" PartName="/word/document.xml" ContentType="x"/></Types>`,
		`<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Override PartName="word/document.xml" ContentType="x"/></Types>`,
		`<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Override PartName="/word/document.xml"/></Types>`,
		`<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Override PartName="/../escape" ContentType="x"/></Types>`,
		`<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="xml" Extension="rels" ContentType="x"/></Types>`,
		`<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default ContentType="x"/></Types>`,
		`<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="xml"/></Types>`,
		`<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="xml" ContentType="x"/><Default Extension="xml" ContentType="x"/></Types>`,
	} {
		raw = wordFixture(t, "", "", types)
		_, err = openPackage(context.Background(), bytes.NewReader(raw), int64(len(raw)), "docx", DefaultLimits())
		requireZIPError(t, err, "unsupported")
	}
	raw = wordFixture(t, "", "", `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="XML" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/></Types>`)
	_, err = openPackage(context.Background(), bytes.NewReader(raw), int64(len(raw)), "docx", DefaultLimits())
	if err != nil {
		t.Fatalf("default content type: %v", err)
	}
	rels := `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="o" Type="other" Target="unselected"/><Relationship Id="r1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/></Relationships>`
	raw = wordFixture(t, "", rels, "")
	_, err = openPackage(context.Background(), bytes.NewReader(raw), int64(len(raw)), "docx", DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	raw = wordFixture(t, "", strings.Replace(rels, `Target="word/document.xml"`, `Target="word/document.xml" TargetMode="unknown"`, 1), "")
	_, err = openPackage(context.Background(), bytes.NewReader(raw), int64(len(raw)), "docx", DefaultLimits())
	requireZIPError(t, err, "unsupported")
	_, err = selectedAttributes(xml.StartElement{Attr: []xml.Attr{{Name: xml.Name{Space: "urn:unknown", Local: "Id"}, Value: "not-native"}}}, "Id")
	if err != nil {
		t.Fatal(err)
	}
	_, err = internalTarget("a.xml", ".")
	requireZIPError(t, err, "unsupported")
}

func TestZIPCountMismatchAndTruncatedHeader(t *testing.T) {
	raw := directoryFixture(2, 1)
	requireZIPError(t, preflightZIP(context.Background(), bytes.NewReader(raw), int64(len(raw)), 8192), "malformed")
	raw = directoryFixture(2, 2)
	binary.LittleEndian.PutUint16(raw[28:], 1)
	requireZIPError(t, preflightZIP(context.Background(), bytes.NewReader(raw), int64(len(raw)), 8192), "malformed")
	// One complete header followed by a short leftover within the declared directory.
	raw = directoryFixture(1, 1)
	end := append([]byte(nil), raw[46:]...)
	raw = append(raw[:46], 0)
	binary.LittleEndian.PutUint32(end[12:], 47)
	raw = append(raw, end...)
	requireZIPError(t, preflightZIP(context.Background(), bytes.NewReader(raw), int64(len(raw)), 8192), "malformed")
}

func TestWordReferenceFailuresAndEmbeddedContent(t *testing.T) {
	open := `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><w:body>`
	close := `</w:body></w:document>`
	for _, tc := range []struct{ name, body, rels, code string }{
		{"missing-id", `<w:headerReference/>`, `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"/>`, "unsupported"},
		{"duplicate-id", `<w:headerReference r:id="h" r:id="h2"/>`, `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"/>`, "unsupported"},
		{"conflicting-role", `<w:headerReference r:id="h"/><w:footerReference r:id="h"/>`, `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"/>`, "unsupported"},
		{"missing-rel", `<w:headerReference r:id="h"/>`, `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"/>`, "malformed"},
		{"wrong-rel", `<w:headerReference r:id="h"/>`, `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="h" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/footer" Target="footer1.xml"/></Relationships>`, "unsupported"},
		{"missing-part", `<w:headerReference r:id="h"/>`, `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="h" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/header" Target="header1.xml"/></Relationships>`, "malformed"},
		{"bad-rels", `<w:p/>`, `<Relationships xmlns="urn:unknown"/>`, "unsupported"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := extractWord(t, open+tc.body+close, tc.rels)
			requireError(t, got, err, tc.code)
		})
	}
	rels := `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="ignore" Type="other" Target="unselected"/><Relationship Id="h" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/header" Target="header1.xml"/><Relationship Id="f" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/footer" Target="header1.xml"/></Relationships>`
	got, err := extractWord(t, open+`<w:headerReference r:id="h"/><w:footerReference r:id="f"/>`+close, rels, zipPart{"word/header1.xml", `<w:hdr xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"/>`})
	requireError(t, got, err, "unsupported")
	got, err = extractWord(t, open+`<w:p><w:drawing><w:t>ignore</w:t></w:drawing></w:p>`+close, `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="n" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/footnotes" Target="footnotes.xml"/></Relationships>`, zipPart{"word/footnotes.xml", `<w:footnotes xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:headerReference/></w:footnotes>`})
	if err != nil || !reflect.DeepEqual(got.Losses, []Loss{{Code: "embedded_content_unmapped", Count: 1}}) {
		t.Fatalf("Word embedded/optional references = %#v,%v", got, err)
	}
}
