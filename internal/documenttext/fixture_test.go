package documenttext

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"testing"
)

type zipPart struct{ name, body string }

func zipFixture(t *testing.T, parts ...zipPart) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for _, part := range parts {
		member, err := writer.Create(part.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := member.Write([]byte(part.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func wordFixture(t *testing.T, main, relationships, contentTypes string, extra ...zipPart) []byte {
	t.Helper()
	if relationships == "" {
		relationships = `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="r1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/></Relationships>`
	}
	if contentTypes == "" {
		contentTypes = `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/></Types>`
	}
	if main == "" {
		main = `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body/></w:document>`
	}
	parts := []zipPart{
		{"word/document.xml", main},
		{"[Content_Types].xml", contentTypes},
		{"_rels/.rels", relationships},
	}
	return zipFixture(t, append(parts, extra...)...)
}

func xmlNoop(xml.Token) error { return nil }
