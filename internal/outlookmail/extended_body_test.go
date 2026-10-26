package outlookmail

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/ourostack/m365crawl/internal/hxstore/hxbuild"
)

func extendedBody(s hxbuild.MailBodySpec) *hxbuild.Object {
	old := obj(hxbuild.NewMailBody(s)).Raw
	raw := append([]byte(nil), old[:0x74e]...)
	raw = append(raw, 0x12, 0x34, 0x56, 0x78)
	raw = append(raw, old[0x74e:]...)
	binary.LittleEndian.PutUint16(raw[2:], 0x752)
	binary.LittleEndian.PutUint32(raw[4:], uint32(len(raw)))
	o := hxbuild.NewObject(0xca, 0x752, len(raw))
	for i, b := range raw {
		o.PutU8(i, b)
	}
	return o
}

func TestExtendedBodyUsesItsOwnStringBase(t *testing.T) {
	html := []byte("<p>Fixture extended body</p>")
	o := extendedBody(hxbuild.MailBodySpec{Key: 21, HTML: html, Lead: 7})
	b := MapBody(obj(o))
	if b.State != BodyInline || !bytes.Equal(b.HTML, html) {
		t.Fatalf("extended inline body: %+v", b)
	}
	const path = "~/Files/S0/2/EFMData/12345.dat"
	b = MapBody(obj(extendedBody(hxbuild.MailBodySpec{Key: 21, Path: path, Lead: 3})))
	if b.State != BodyFile || b.Path != path {
		t.Fatalf("extended file body: %+v", b)
	}
}

func TestCollectMixedBodyLayoutsSelectsLatestCopy(t *testing.T) {
	objs := append(folderObjs(), hdr(11, 21, fInbox, 1, "Fixture message", 2), det(21, "<a@example.invalid>"),
		hxbuild.NewMailBody(hxbuild.MailBodySpec{Key: 21, Stamp: 1, HTML: []byte("<p>Fixture old</p>")}),
		extendedBody(hxbuild.MailBodySpec{Key: 21, Stamp: 2, HTML: []byte("<p>Fixture latest</p>")}))
	r := collect(t, storeOf(t, framed(objs...)), Options{})
	if len(r.Messages) != 1 || string(r.Messages[0].Body.HTML) != "<p>Fixture latest</p>" || len(r.Losses) != 0 {
		t.Fatalf("mixed body layout selection: %+v", r)
	}
}
