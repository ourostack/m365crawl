// Package outlookmail maps the mail objects of the new Outlook for Mac store (HxStore.hxd, read
// through internal/hxstore) to typed values: message headers (class 0x4f), details (0xc9), body
// records (0xca), attachments (0x16a), folders (0x4d) and recipients (0x55). It is pure: it
// reads objects handed to it, and the one file read it makes (a downloaded body, ReadDat) goes
// through outlookdesktop's read-only open.
//
// The layouts are the ones in docs/outlook-store.md ("Mail"). Offsets count from the first byte
// of the object's envelope. A field is mapped only when that document marks it established or
// likely; a field the store does not provide is left empty, never guessed.
package outlookmail

import (
	"encoding/hex"
	"strings"
	"time"

	"github.com/ourostack/m365crawl/internal/hxstore"
)

// MapperVersion is raised whenever a mapping change alters derived mail data, so the archive can
// re-read the store.
const MapperVersion = 1

// Class and tag of each mapped object. The tag is the size of the fixed region.
const (
	ClassHeader     = 0x4f
	TagHeader       = 0x430
	ClassDetail     = 0xc9
	TagDetail       = 0x60f
	ClassBody       = 0xca
	TagBody         = 0x74e
	ClassAttachment = 0x16a
	TagAttachment   = 0x318
	ClassFolder     = 0x4d
	TagFolder       = 0x4d0
	ClassRecipient  = 0x55
	TagRecipient    = 0x15e
)

// Offsets shared by every class.
const (
	offKey   = 20  // u32 object key
	offLead  = 104 // u32: the string area starts at the fixed size plus this
	offStamp = 112 // u64 change stamp
	offLink  = 32  // u32 parent key (folder, recipient)

	lengthMask = 1<<31 - 1
)

// Header fixed region (class 0x4f).
const (
	hdFolder    = 532
	hdReceived  = 224
	hdRead      = 740
	hdImportant = 752
	hdICal      = 772 // word, base = the tag; the length word has no terminator
	hdSenderNm  = 884
	hdDetailKey = 292
	hdSubject   = 900
	hdPreview   = 920
	hdSenderAd  = 940
	hdFlag      = 1033
)

// Detail fixed region (class 0xc9).
const (
	dtSent      = 728
	dtInReplyTo = 1212
	dtMessageID = 1228
	dtClass     = 1236
)

// Recipient fixed region (class 0x55).
const (
	rcName    = 292
	rcAddress = 300
	rcKind    = 316
)

// UnmappedError is an object that could not be mapped. Reason is a fixed phrase, never text from
// the store.
type UnmappedError struct{ Reason string }

func (e *UnmappedError) Error() string { return "outlookmail: unmapped object: " + e.Reason }

func unmapped(reason string) error { return &UnmappedError{Reason: reason} }

// Recipient is one recipient of a message. To versus Cc is not established, so only the raw kind
// word is kept.
type Recipient struct {
	Name    string
	Address string
	KindRaw uint32
}

// Header is a message header. Offset is the file offset of the block that held the copy.
type Header struct {
	Key, DetailKey, FolderKey uint32
	Stamp                     uint64
	Offset                    int64
	Subject, SenderName       string
	SenderAddress, Preview    string
	// ICalUID is the invite id (the event's iCal UID) in lower case, empty when the header
	// carries none.
	ICalUID  string
	Received time.Time
	// Unread is nil when the read state is unknown.
	Unread     *bool
	ReadState  string // read, unread or unknown
	Flag       string // none, complete, flagged or unknown
	Importance string // low, normal, high or unknown
	Recipients []Recipient
}

// Detail is a message detail object.
type Detail struct {
	Key                         uint32
	MessageID, InReplyTo, Class string
	Sent                        time.Time
}

// fields reads the string words of one object. The first out-of-range string is remembered.
type fields struct {
	o    hxstore.Object
	base int
	bad  bool
}

// newFields checks that the object is at least as long as its fixed region and sets the string
// area base: the fixed size plus the lead word at +104.
func newFields(o hxstore.Object, fixed int) (*fields, error) {
	if o.Len() < fixed {
		return nil, unmapped("the object is shorter than its fixed region")
	}
	lead, _ := o.U32(offLead) // inside the fixed region
	return &fields{o: o, base: fixed + int(lead)}, nil
}

// text reads the string whose word pair is at wordOff. An absent string is empty. A present one
// that is out of range, unterminated or not valid text marks the object bad.
func (f *fields) text(wordOff int) string {
	s, present, ok := f.o.PresentString(wordOff, f.base)
	if present && !ok {
		f.bad = true
	}
	return s
}

// err is the mapping error, or nil when every string read fine.
func (f *fields) err() error {
	if f.bad {
		return unmapped("a string is out of range")
	}
	return nil
}

// word reads a u32 inside the fixed region, which newFields checked.
func word(o hxstore.Object, off int) uint32 {
	v, _ := o.U32(off)
	return v
}

// MapHeader maps a class 0x4f object. The sender is the address at +940 (+912 is the list
// column's address and is ignored). A time that is unset, the max-ticks sentinel or out of
// range is the zero time.
func MapHeader(o hxstore.Object) (Header, error) {
	f, err := newFields(o, TagHeader)
	if err != nil {
		return Header{}, err
	}
	stamp, _ := o.U64(offStamp)
	h := Header{
		Key: word(o, offKey), DetailKey: word(o, hdDetailKey), FolderKey: word(o, hdFolder),
		Stamp: stamp, Offset: o.BlockOffset,
		Subject: f.text(hdSubject), SenderName: f.text(hdSenderNm), SenderAddress: f.text(hdSenderAd), Preview: f.text(hdPreview),
		ICalUID: inviteID(o),
	}
	if err := f.err(); err != nil {
		return Header{}, err
	}
	h.Received, _ = o.Ticks(hdReceived)
	switch word(o, hdRead) {
	case 0:
		unread := false
		h.Unread, h.ReadState = &unread, "read"
	case 1:
		unread := true
		h.Unread, h.ReadState = &unread, "unread"
	default:
		h.ReadState = "unknown" // 2 to 7 were seen and mean nothing known
	}
	flag, _ := o.U8(hdFlag)
	h.Flag = pick(uint32(flag), "none", "complete", "flagged")
	h.Importance = pick(word(o, hdImportant), "low", "normal", "high")
	return h, nil
}

// pick returns names[v], or "unknown" past the end.
func pick(v uint32, names ...string) string {
	if int64(v) < int64(len(names)) {
		return names[v]
	}
	return "unknown"
}

// inviteID reads the meeting invite id: ASCII hex text whose offset word is counted from the
// tag (no lead word) and whose length word has no terminator. It is lower case, the form the
// calendar ids have. Anything that is not whole hex text is no id.
func inviteID(o hxstore.Object) string {
	w, l := int64(word(o, hdICal)), int64(word(o, hdICal+4)&lengthMask)
	raw, ok := o.Bytes(int(w)+TagHeader, int(l))
	if l == 0 || !ok || l%2 != 0 {
		return ""
	}
	s := strings.ToLower(string(raw))
	if _, err := hex.DecodeString(s); err != nil {
		return ""
	}
	return s
}

// MapDetail maps a class 0xc9 object.
func MapDetail(o hxstore.Object) (Detail, error) {
	f, err := newFields(o, TagDetail)
	if err != nil {
		return Detail{}, err
	}
	d := Detail{
		Key:       word(o, offKey),
		MessageID: f.text(dtMessageID), Class: f.text(dtClass), InReplyTo: f.text(dtInReplyTo),
	}
	if err := f.err(); err != nil {
		return Detail{}, err
	}
	d.Sent, _ = o.Ticks(dtSent)
	return d, nil
}

// MapRecipient maps a class 0x55 object and returns the detail key of the message it belongs
// to.
func MapRecipient(o hxstore.Object) (parent uint32, r Recipient, err error) {
	f, err := newFields(o, TagRecipient)
	if err != nil {
		return 0, Recipient{}, err
	}
	r = Recipient{Name: f.text(rcName), Address: f.text(rcAddress), KindRaw: word(o, rcKind)}
	if err := f.err(); err != nil {
		return 0, Recipient{}, err
	}
	return word(o, offLink), r, nil
}
