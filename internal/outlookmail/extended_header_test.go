package outlookmail

import (
	"context"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/ourostack/m365crawl/internal/hxstore"
	"github.com/ourostack/m365crawl/internal/hxstore/hxbuild"
)

// extendedHeader widens a 0x430 header by one byte after its fixed region, the measured 0x431
// layout: every mapped word keeps its offset and only the string base moves.
func extendedHeader(s hxbuild.MailHeaderSpec) *hxbuild.Object {
	return grown(hxbuild.NewMailHeader(s), ClassHeader, TagHeader, TagHeaderExtended)
}

func TestExtendedHeaderUsesItsOwnStringBase(t *testing.T) {
	s := headerSpec()
	s.ICalHex = "04000000820E"
	s.FlagByte = 2
	want, err := MapHeader(obj(hxbuild.NewMailHeader(s)))
	if err != nil {
		t.Fatal(err)
	}
	got, err := MapHeader(obj(extendedHeader(s)))
	if err != nil {
		t.Fatal(err)
	}
	if got.Subject != want.Subject || got.SenderAddress != want.SenderAddress || got.ICalUID != want.ICalUID ||
		got.ICalUID == "" || got.Importance != want.Importance || got.Received != want.Received {
		t.Fatalf("extended header %+v, want %+v", got, want)
	}
	if want.Flag != "flagged" || got.Flag != "unknown" {
		t.Fatalf("flag %q then %q: the 0x431 flag byte is not established", want.Flag, got.Flag)
	}
}

func TestMapHeaderRefusesUnknownLayout(t *testing.T) {
	s := headerSpec()
	s.Tag = 0x432
	if _, err := MapHeader(obj(hxbuild.NewMailHeader(s))); err == nil {
		t.Fatal("an unmeasured header layout was mapped")
	}
}

func TestCollectMixedHeaderAndBodyLayouts(t *testing.T) {
	objs := append(folderObjs(), hdr(11, 21, fInbox, 1, "Fixture old", 2),
		extendedHeader(hxbuild.MailHeaderSpec{Key: 11, Stamp: 2, DetailKey: 21, FolderKey: fInbox, Received: at(2, 9),
			Subject: "Fixture latest", SenderName: "Fixture Sender", SenderAddr: "sender@example.invalid", Unread: 1, Importance: 1, Lead: 18}),
		det(21, "<a@example.invalid>"), wideBody(hxbuild.MailBodySpec{Key: 21, Stamp: 2, HTML: []byte("<p>Fixture wide</p>"), Lead: 18}))
	r := collect(t, storeOf(t, framed(objs...)), Options{})
	if len(r.Messages) != 1 || r.Messages[0].Subject != "Fixture latest" || string(r.Messages[0].Body.HTML) != "<p>Fixture wide</p>" || len(r.Losses) != 0 {
		t.Fatalf("mixed layouts: %+v", r)
	}
	if r.Notes.HeadersSeen != 2 {
		t.Fatalf("headers seen %d", r.Notes.HeadersSeen)
	}
}

func TestGuardStillRefusesOtherHeaderAndBodyTags(t *testing.T) {
	objs := append(folderObjs(), hdr(10, 20, fInbox, 1, "Fixture message", 2), det(20, "<a@example.invalid>"),
		grown(hxbuild.NewMailHeader(hxbuild.MailHeaderSpec{Key: 11}), ClassHeader, TagHeader, 0x432),
		grown(hxbuild.NewMailBody(hxbuild.MailBodySpec{Key: 21}), ClassBody, TagBody, 0x753))
	_, err := Collect(context.Background(), storeOf(t, framed(objs...)), t.TempDir(), "outlook/Test", Options{})
	var g *hxstore.GuardError
	if !errors.As(err, &g) || g.Code != CodeMailLayoutUnsupported ||
		g.Detail != "class 0x4f tag 0x432 x1, known 0x430; class 0xca tag 0x753 x1, known 0x74e" {
		t.Fatalf("unknown tags were not refused: %v", err)
	}
}

func TestWideBodyUsesItsOwnStringBase(t *testing.T) {
	html := []byte("<p>Fixture wide body</p>")
	b := MapBody(obj(wideBody(hxbuild.MailBodySpec{Key: 21, HTML: html, Lead: 18})))
	if b.State != BodyInline || string(b.HTML) != string(html) {
		t.Fatalf("wide inline body: %+v", b)
	}
	const path = "~/Files/S0/2/EFMData/12345.dat"
	b = MapBody(obj(wideBody(hxbuild.MailBodySpec{Key: 21, Path: path, Lead: 18})))
	if b.State != BodyFile || b.Path != path {
		t.Fatalf("wide file body: %+v", b)
	}
}

// grown widens an object to a larger tag by inserting zero bytes after its fixed region.
func grown(src *hxbuild.Object, class, from, to uint16) *hxbuild.Object {
	old := obj(src).Raw
	raw := append([]byte(nil), old[:from]...)
	raw = append(raw, make([]byte, to-from)...)
	raw = append(raw, old[from:]...)
	binary.LittleEndian.PutUint16(raw[2:], to)
	o := hxbuild.NewObject(class, to, len(raw))
	for i, b := range raw {
		if i < 4 || i >= 8 {
			o.PutU8(i, b)
		}
	}
	return o
}

// wideBody widens a 0x74e body by 0x14 bytes, the measured 0x762 layout.
func wideBody(s hxbuild.MailBodySpec) *hxbuild.Object {
	return grown(hxbuild.NewMailBody(s), ClassBody, TagBody, TagBodyWide)
}
