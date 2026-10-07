package outlookmail

import (
	"errors"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/hxstore/hxbuild"
)

func headerSpec() hxbuild.MailHeaderSpec {
	return hxbuild.MailHeaderSpec{
		Key: 11, Stamp: 5, DetailKey: 21, FolderKey: 31, Received: at(2, 9),
		Subject: "Fixture subject", SenderName: "Fixture Sender", SenderAddr: "sender@example.invalid", Preview: "Fixture preview",
		Unread: 1, FlagByte: 0, Importance: 1, Lead: 59,
	}
}

func TestMapHeader(t *testing.T) {
	o := obj(hxbuild.NewMailHeader(headerSpec()))
	o.BlockOffset = 4096
	h, err := MapHeader(o)
	if err != nil {
		t.Fatal(err)
	}
	if h.Key != 11 || h.DetailKey != 21 || h.FolderKey != 31 || h.Stamp != 5 || h.Offset != 4096 {
		t.Fatalf("keys %+v", h)
	}
	if h.Subject != "Fixture subject" || h.SenderName != "Fixture Sender" || h.SenderAddress != "sender@example.invalid" || h.Preview != "Fixture preview" {
		t.Fatalf("strings %+v", h)
	}
	if !h.Received.Equal(at(2, 9)) || h.ICalUID != "" || h.Recipients != nil {
		t.Fatalf("received, invite or recipients %+v", h)
	}
	if h.Unread == nil || !*h.Unread || h.ReadState != "unread" || h.Flag != "none" || h.Importance != "normal" {
		t.Fatalf("states %+v", h)
	}
}

func TestMapHeaderStringsAbsentAndPresent(t *testing.T) {
	s := headerSpec()
	s.Subject, s.SenderName, s.SenderAddr, s.Preview = "", "", "", ""
	h, err := MapHeader(obj(hxbuild.NewMailHeader(s)))
	if err != nil || h.Subject != "" || h.SenderName != "" || h.SenderAddress != "" || h.Preview != "" {
		t.Fatalf("absent strings: %+v %v", h, err)
	}
	// A bit-31 length word is masked; a plain one reads the same.
	o := hxbuild.NewMailHeader(headerSpec())
	o.PutU32(904, 32) // the subject's 32 bytes, bit 31 clear
	if h, err := MapHeader(obj(o)); err != nil || h.Subject != "Fixture subject" {
		t.Fatalf("plain length: %+v %v", h, err)
	}
	// A missing terminator at the stated end (length one char short) fails the mapping.
	for _, word := range []int{904, 888, 944, 924} {
		o := hxbuild.NewMailHeader(headerSpec())
		o.PutU32(word, 4|1<<31)
		_, err := MapHeader(obj(o))
		var ue *UnmappedError
		if !errors.As(err, &ue) || !strings.Contains(err.Error(), "string") {
			t.Fatalf("+%d: %v", word, err)
		}
	}
}

func TestMapHeaderReadStateFlagImportance(t *testing.T) {
	for _, c := range []struct {
		raw       uint32
		state     string
		unreadSet bool
		unread    bool
	}{{0, "read", true, false}, {1, "unread", true, true}, {3, "unknown", false, false}, {7, "unknown", false, false}, {99, "unknown", false, false}} {
		s := headerSpec()
		s.Unread = c.raw
		h, err := MapHeader(obj(hxbuild.NewMailHeader(s)))
		if err != nil || h.ReadState != c.state || (h.Unread != nil) != c.unreadSet || (c.unreadSet && *h.Unread != c.unread) {
			t.Errorf("read %d: %+v %v", c.raw, h, err)
		}
	}
	for flag, want := range map[uint8]string{0: "none", 1: "complete", 2: "flagged", 7: "unknown"} {
		s := headerSpec()
		s.FlagByte = flag
		if h, _ := MapHeader(obj(hxbuild.NewMailHeader(s))); h.Flag != want {
			t.Errorf("flag %d: %q", flag, h.Flag)
		}
	}
	for imp, want := range map[uint32]string{0: "low", 1: "normal", 2: "high", 5: "unknown"} {
		s := headerSpec()
		s.Importance = imp
		if h, _ := MapHeader(obj(hxbuild.NewMailHeader(s))); h.Importance != want {
			t.Errorf("importance %d: %q", imp, h.Importance)
		}
	}
}

func TestMapHeaderTimeSentinelAndUnset(t *testing.T) {
	for _, ticks := range []uint64{0x2BCA2875F4373FFF, 0} {
		o := hxbuild.NewMailHeader(headerSpec())
		o.PutU64(224, ticks)
		if h, _ := MapHeader(obj(o)); !h.Received.IsZero() {
			t.Fatalf("%#x read as %v", ticks, h.Received)
		}
	}
}

func TestMapHeaderInviteID(t *testing.T) {
	hexID := hxbuild.HexID(hxbuild.GlobalObjectID(0, 0, 0, "FIXTURE-MAIL-001")) // 112 upper-case hex characters
	if len(hexID) != 112 {
		t.Fatal(len(hexID))
	}
	s := headerSpec()
	s.ICalHex = hexID
	h, err := MapHeader(obj(hxbuild.NewMailHeader(s)))
	if err != nil || h.ICalUID != strings.ToLower(hexID) {
		t.Fatalf("invite %q %v", h.ICalUID, err)
	}
	// Anything that is not whole hex text is no invite id; so is a length outside the object.
	for _, bad := range []string{"0400zz", "040"} {
		s.ICalHex = bad
		if h, err := MapHeader(obj(hxbuild.NewMailHeader(s))); err != nil || h.ICalUID != "" {
			t.Errorf("%q: %q %v", bad, h.ICalUID, err)
		}
	}
	o := hxbuild.NewMailHeader(s)
	o.PutU32(776, 1<<20)
	if h, err := MapHeader(obj(o)); err != nil || h.ICalUID != "" {
		t.Errorf("long: %q %v", h.ICalUID, err)
	}
}

func TestMapHeaderShortObject(t *testing.T) {
	o := obj(hxbuild.NewMailHeader(headerSpec()))
	o.Raw = o.Raw[:1000]
	if _, err := MapHeader(o); err == nil || !strings.Contains(err.Error(), "fixed region") {
		t.Fatal(err)
	}
	o = obj(hxbuild.NewMailHeader(headerSpec()))
	o.Raw = o.Raw[:hxbuild.MailHeaderSize]
	o.Raw[104], o.Raw[105], o.Raw[106], o.Raw[107] = 0xff, 0xff, 0xff, 0x7f // lead past the object
	if _, err := MapHeader(o); err == nil {
		t.Fatal("a string area past the object must not map")
	}
}

func TestMapDetail(t *testing.T) {
	o := obj(hxbuild.NewMailDetail(hxbuild.MailDetailSpec{Key: 21, MessageID: "<a@example.invalid>", Class: "IPM.Note", InReplyTo: "<b@example.invalid>", Sent: at(2, 8), Lead: 79}))
	d, err := MapDetail(o)
	if err != nil || d.Key != 21 || d.MessageID != "<a@example.invalid>" || d.Class != "IPM.Note" || d.InReplyTo != "<b@example.invalid>" || !d.Sent.Equal(at(2, 8)) {
		t.Fatalf("%+v %v", d, err)
	}
	d, err = MapDetail(obj(hxbuild.NewMailDetail(hxbuild.MailDetailSpec{Key: 22, MessageID: "<c@example.invalid>"})))
	if err != nil || d.InReplyTo != "" || d.Class != "" || !d.Sent.IsZero() {
		t.Fatalf("absent in-reply-to: %+v %v", d, err)
	}
	for _, word := range []int{1216, 1232, 1240} {
		o := hxbuild.NewMailDetail(hxbuild.MailDetailSpec{Key: 1, MessageID: "<c@example.invalid>", Class: "IPM.Note", InReplyTo: "<d@example.invalid>"})
		o.PutU32(word, 4|1<<31)
		if _, err := MapDetail(obj(o)); err == nil {
			t.Errorf("+%d: bad string accepted", word)
		}
	}
	short := obj(hxbuild.NewMailDetail(hxbuild.MailDetailSpec{Key: 1}))
	short.Raw = short.Raw[:100]
	if _, err := MapDetail(short); err == nil {
		t.Fatal("short object")
	}
}

func TestMapRecipient(t *testing.T) {
	p, r, err := MapRecipient(obj(hxbuild.NewRecipient(hxbuild.RecipientSpec{Key: 1, Parent: 21, Name: "Fixture Person", Address: "person@example.invalid", Kind: 3, Lead: 5})))
	if err != nil || p != 21 || r.Name != "Fixture Person" || r.Address != "person@example.invalid" || r.KindRaw != 3 {
		t.Fatalf("%d %+v %v", p, r, err)
	}
	_, r, err = MapRecipient(obj(hxbuild.NewRecipient(hxbuild.RecipientSpec{Key: 2, Parent: 21, Name: "Fixture Person"})))
	if err != nil || r.Address != "" {
		t.Fatalf("absent address: %+v %v", r, err)
	}
	for _, word := range []int{296, 304} {
		o := hxbuild.NewRecipient(hxbuild.RecipientSpec{Key: 2, Parent: 21, Name: "Fixture Person", Address: "person@example.invalid"})
		o.PutU32(word, 4|1<<31)
		if _, _, err := MapRecipient(obj(o)); err == nil {
			t.Errorf("+%d: bad string accepted", word)
		}
	}
	short := obj(hxbuild.NewRecipient(hxbuild.RecipientSpec{Key: 2}))
	short.Raw = short.Raw[:100]
	if _, _, err := MapRecipient(short); err == nil {
		t.Fatal("short object")
	}
}
