package documenttext

import (
	"bytes"
	"context"
	"reflect"
	"testing"
)

func TestPackageMainAdmission(t *testing.T) {
	raw := wordFixture(t, "", "", "")
	p, err := openPackage(context.Background(), bytes.NewReader(raw), int64(len(raw)), "docx", DefaultLimits())
	if err != nil || p.main != "word/document.xml" {
		t.Fatalf("package = %#v, %v", p, err)
	}

	if err := p.readXML(p.main, xmlNoop); err != nil {
		t.Fatalf("main XML = %v", err)
	}
	rels, err := p.relationships("")
	want := []relationship{{ID: "r1", Type: "http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument", Target: "word/document.xml"}}
	if err != nil || !reflect.DeepEqual(rels, want) {
		t.Fatalf("relationships = %#v, %v", rels, err)
	}
	rels, err = p.relationships("word/document.xml")
	if err != nil || len(rels) != 0 {
		t.Fatalf("missing optional relationships = %#v, %v", rels, err)
	}
	for _, kind := range []string{"xlsx", "pptx"} {
		_, err := openPackage(context.Background(), bytes.NewReader(raw), int64(len(raw)), kind, DefaultLimits())
		requireZIPError(t, err, "unsupported")
	}
}

func TestUTF8BOMPackageParts(t *testing.T) {
	raw := wordFixture(t,
		"\ufeff"+`<?xml version="1.0" encoding="UTF-8"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body/></w:document>`,
		"\ufeff"+`<?xml version="1.0" encoding="UTF-8"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="r1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/></Relationships>`,
		"\ufeff"+`<?xml version="1.0" encoding="UTF-8"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/></Types>`,
	)
	got, err := Extract(context.Background(), bytes.NewReader(raw), int64(len(raw)), "docx", DefaultLimits())
	if err != nil || got.State != "text_observations" || len(got.Paragraphs) != 0 {
		t.Fatalf("UTF8 BOM parts = %#v, %v", got, err)
	}
}

func TestPackageRelationshipAmbiguity(t *testing.T) {
	prefix := `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">`
	office := `<Relationship Id="r1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/>`
	for _, tc := range []struct {
		name, relationships string
	}{
		{"no-main", prefix + `</Relationships>`},
		{"duplicate-id", prefix + office + `<Relationship Id="r1" Type="other" Target="unselected.bin"/></Relationships>`},
		{"duplicate-main", prefix + office + `<Relationship Id="r2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/></Relationships>`},
		{"external-main", prefix + `<Relationship Id="r1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="https://example.invalid/file" TargetMode="External"/></Relationships>`},
		{"traversal-main", prefix + `<Relationship Id="r1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="../word/document.xml"/></Relationships>`},
		{"duplicate-target-attribute", prefix + `<Relationship Id="r1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="unselected.xml" Target="word/document.xml"/></Relationships>`},
		{"wrong-namespace", `<Relationships xmlns="urn:unqualified">` + office + `</Relationships>`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := wordFixture(t, "", tc.relationships, "")
			_, err := openPackage(context.Background(), bytes.NewReader(raw), int64(len(raw)), "docx", DefaultLimits())
			requireZIPError(t, err, "unsupported")
		})
	}
}

func TestPackageContentTypeAdmission(t *testing.T) {
	for _, types := range []string{
		`<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"/>`,
		`<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Override PartName="/word/document.xml" ContentType="unqualified"/></Types>`,
		`<Types xmlns="urn:unqualified"><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/></Types>`,
		`<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/></Types>`,
	} {
		raw := wordFixture(t, "", "", types)
		_, err := openPackage(context.Background(), bytes.NewReader(raw), int64(len(raw)), "docx", DefaultLimits())
		requireZIPError(t, err, "unsupported")
	}
}

func TestSelectedMemberIntegrity(t *testing.T) {
	raw := wordFixture(t, "", "", "", zipPart{"word/document.xml", "ambiguous"})
	_, err := openPackage(context.Background(), bytes.NewReader(raw), int64(len(raw)), "docx", DefaultLimits())
	requireZIPError(t, err, "unsupported")

	raw = wordFixture(t, "", "", "", zipPart{"unselected.bin", "a"}, zipPart{"unselected.bin", "b"})
	p, err := openPackage(context.Background(), bytes.NewReader(raw), int64(len(raw)), "docx", DefaultLimits())
	if err != nil {
		t.Fatalf("unselected duplicates: %v", err)
	}
	if err := p.readXML(p.main, xmlNoop); err != nil {
		t.Fatal(err)
	}
	for _, main := range []string{
		`<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">`,
		`<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"/><extra/>`,
	} {
		raw = wordFixture(t, main, "", "")
		p, err := openPackage(context.Background(), bytes.NewReader(raw), int64(len(raw)), "docx", DefaultLimits())
		if err != nil {
			t.Fatal(err)
		}
		requireZIPError(t, p.readXML(p.main, xmlNoop), "malformed")
	}
	raw = wordFixture(t, `<document xmlns="urn:unqualified"/>`, "", "")
	p, err = openPackage(context.Background(), bytes.NewReader(raw), int64(len(raw)), "docx", DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	requireZIPError(t, p.readXML(p.main, xmlNoop), "unsupported")
	raw = wordFixture(t, `<!DOCTYPE document [<!ENTITY hidden "not-exported">]><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"/>`, "", "")
	p, err = openPackage(context.Background(), bytes.NewReader(raw), int64(len(raw)), "docx", DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	requireZIPError(t, p.readXML(p.main, xmlNoop), "unsupported")

	raw = wordFixture(t, "", "", "")
	central := bytes.Index(raw, []byte{0x50, 0x4b, 0x01, 0x02})
	if central < 0 {
		t.Fatal("fixture central directory absent")
	}
	raw[central+16] ^= 1
	p, err = openPackage(context.Background(), bytes.NewReader(raw), int64(len(raw)), "docx", DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	requireZIPError(t, p.readXML(p.main, xmlNoop), "malformed")
}
