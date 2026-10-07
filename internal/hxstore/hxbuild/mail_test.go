package hxbuild

import (
	"bytes"
	"testing"
	"time"
)

var mailTime = time.Date(2031, 4, 2, 3, 4, 5, 0, time.UTC)

// mailStr reads a string word pair at wordOff with the given base: the text, the raw length word.
func mailStr(t *testing.T, o *Object, wordOff, base int) (string, uint32) {
	t.Helper()
	b := o.Encode()[4:]
	l := u32(b, wordOff+4)
	if l == 0 {
		return "", 0
	}
	return utf16z(t, b, base+int(u32(b, wordOff))), l
}

func TestNewMailHeader(t *testing.T) {
	o := NewMailHeader(MailHeaderSpec{
		Key: 7, Stamp: 99, DetailKey: 8, FolderKey: 9, Received: mailTime,
		Subject: "Fixture subject", SenderName: "Fixture Sender", SenderAddr: "sender@example.invalid", Preview: "Fixture preview",
		Unread: 1, FlagByte: 2, Importance: 2, ICalHex: "04000000820E", Lead: 3,
	})
	b := o.Encode()[4:]
	if int(u32(b, 4)) != len(b) || u16(b, 2) != TagMailHeader || u16(b, 10) != ClassMailHeader || len(b) < MailHeaderSize {
		t.Fatalf("envelope %x len %d", b[:12], len(b))
	}
	if u32(b, 20) != 7 || u32(b, 40) != 7 || u64(b, 112) != 99 || u32(b, 104) != 3 || u32(b, 292) != 8 {
		t.Fatal("key, stamp, lead or detail key")
	}
	if u32(b, 532) != 9 || u32(b, 552) != 9 || u32(b, 740) != 1 || u32(b, 752) != 2 || b[1033] != 2 {
		t.Fatal("folder, read, importance or flag")
	}
	if u64(b, 224) != Ticks(mailTime) || u64(b, 672) != Ticks(mailTime) {
		t.Fatal("received")
	}
	base := MailHeaderSize + 3
	for off, want := range map[int]string{900: "Fixture subject", 884: "Fixture Sender", 940: "sender@example.invalid", 920: "Fixture preview"} {
		got, l := mailStr(t, o, off, base)
		if got != want || l&lengthFlag == 0 || int(l&^lengthFlag) != len(UTF16Z(want)) {
			t.Errorf("+%d: %q length word %#x", off, got, l)
		}
	}
	ical := b[MailHeaderSize+int(u32(b, 772)):][:u32(b, 776)]
	if string(ical) != "04000000820E" {
		t.Fatalf("invite %q", ical)
	}
}

func TestNewMailHeaderAbsentAndTag(t *testing.T) {
	o := NewMailHeader(MailHeaderSpec{Tag: 0x431, Key: 1})
	b := o.Encode()[4:]
	if b[2] != 0x31 || b[3] != 0x04 || len(b) != MailHeaderSize {
		t.Fatalf("tag %x len %d", b[2:4], len(b))
	}
	for _, off := range []int{900, 884, 940, 920, 772} {
		if u32(b, off) != 0 || u32(b, off+4) != 0 {
			t.Errorf("+%d is not absent", off)
		}
	}
}

func TestNewMailDetail(t *testing.T) {
	o := NewMailDetail(MailDetailSpec{Key: 5, Stamp: 6, MessageID: "<a@example.invalid>", Class: "IPM.Note", InReplyTo: "<b@example.invalid>", Sent: mailTime, Received: mailTime, Lead: 1})
	b := o.Encode()[4:]
	if u16(b, 10) != ClassMailDetail || u32(b, 20) != 5 || u64(b, 112) != 6 || u64(b, 728) != Ticks(mailTime) || u64(b, 288) != Ticks(mailTime) {
		t.Fatal("header words")
	}
	base := MailDetailSize + 1
	for off, want := range map[int]string{1228: "<a@example.invalid>", 1236: "IPM.Note", 1212: "<b@example.invalid>"} {
		if got, _ := mailStr(t, o, off, base); got != want {
			t.Errorf("+%d: %q", off, got)
		}
	}
	empty := NewMailDetail(MailDetailSpec{Tag: 0x610}).Encode()[4:]
	if u32(empty, 1212+4) != 0 || empty[2] != 0x10 || empty[3] != 0x06 {
		t.Fatal("absent in-reply-to or tag")
	}
}

func TestNewMailBody(t *testing.T) {
	html := []byte("<html>fixture</html>")
	o := NewMailBody(MailBodySpec{Key: 4, HTML: html, Lead: 2})
	b := o.Encode()[4:]
	base := MailBodySize + 2
	l := u32(b, 1668+4)
	if u16(b, 10) != ClassMailBody || int(l&^lengthFlag) != len(html) || l&lengthFlag == 0 || !bytes.Equal(b[base+int(u32(b, 1668)):][:len(html)], html) {
		t.Fatal("inline body")
	}
	p := NewMailBody(MailBodySpec{Key: 4, Path: "~/Files/S0/2/EFMData/1.dat"}).Encode()[4:]
	if got := utf16z(t, p, MailBodySize+int(u32(p, 1524))); got != "~/Files/S0/2/EFMData/1.dat" || u32(p, 1668+4) != 0 {
		t.Fatalf("path body %q", got)
	}
	at := NewMailBody(MailBodySpec{Path: "~/Files/x", WordOff: 1784}).Encode()[4:]
	if u32(at, 1784+4) == 0 || u32(at, 1524+4) != 0 {
		t.Fatal("explicit word")
	}
	none := NewMailBody(MailBodySpec{Tag: 0x74f}).Encode()[4:]
	if len(none) != MailBodySize || none[2] != 0x4f || none[3] != 0x07 {
		t.Fatal("empty body")
	}
}

func TestNewAttachment(t *testing.T) {
	o := NewAttachment(AttachmentSpec{Key: 3, MessageKey: 8, Name: "fixture.txt", Path: "~/Files/S0/2/Attachments/0/fixture[1].txt", Size: 72, State: 2})
	b := o.Encode()[4:]
	if u16(b, 10) != ClassAttachment || u32(b, 380) != 8 || u32(b, 568) != 72 || u32(b, 624) != 2 {
		t.Fatal("words")
	}
	if got, _ := mailStr(t, o, 608, AttachmentSize); got != "fixture.txt" {
		t.Fatal(got)
	}
	if got, _ := mailStr(t, o, 648, AttachmentSize); got != "~/Files/S0/2/Attachments/0/fixture[1].txt" {
		t.Fatal(got)
	}
	if NewAttachment(AttachmentSpec{Tag: 1}).Len() != AttachmentSize {
		t.Fatal("size")
	}
}

func TestNewMailFolder(t *testing.T) {
	o := NewMailFolder(MailFolderSpec{Key: 2, Parent: 1, Name: "Fixture Inbox", Type: 0x61})
	b := o.Encode()[4:]
	if u16(b, 10) != ClassMailFolder || u32(b, 32) != 1 || u32(b, 1160) != 0x61 {
		t.Fatal("words")
	}
	if got, _ := mailStr(t, o, 1088, MailFolderSize); got != "Fixture Inbox" {
		t.Fatal(got)
	}
	if NewMailFolder(MailFolderSpec{Tag: 1}).Len() != MailFolderSize {
		t.Fatal("size")
	}
}

func TestNewRecipient(t *testing.T) {
	o := NewRecipient(recipientSpecForTest())
	b := o.Encode()[4:]
	if u16(b, 10) != ClassRecipient || u32(b, 32) != 8 || u32(b, 316) != 3 {
		t.Fatal("words")
	}
	if got, _ := mailStr(t, o, 292, RecipientSize); got != "Fixture Person" {
		t.Fatal(got)
	}
	if got, _ := mailStr(t, o, 300, RecipientSize); got != "person@example.invalid" {
		t.Fatal(got)
	}
	if NewRecipient(RecipientSpec{Tag: 1}).Len() != RecipientSize {
		t.Fatal("size")
	}
}

func recipientSpecForTest() RecipientSpec {
	return RecipientSpec{Key: 1, Parent: 8, Name: "Fixture Person", Address: "person@example.invalid", Kind: 3}
}
