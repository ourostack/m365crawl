package hxbuild

import "time"

// The mail object layouts, written from docs/outlook-store.md ("Mail") and nothing else. Every
// offset counts from the first byte of the object's envelope. A string field is a pair of words,
// an offset and a length. The offset is counted from the string area base, which is the class's
// fixed size plus the u32 at +104 (the lead). The length counts bytes, terminator included, and
// has bit 31 set. An absent string is a pair of zeros and appends nothing.
const (
	// ClassMailHeader is the message header (the object a mail list reads).
	ClassMailHeader = 0x4f
	TagMailHeader   = 0x430
	MailHeaderSize  = 1072
	// ClassMailDetail is the message detail: Message-ID, class, In-Reply-To, sent time.
	ClassMailDetail = 0xc9
	TagMailDetail   = 0x60f
	MailDetailSize  = 1551
	// ClassMailBody is the body record: inline HTML or a path to an EFMData file.
	ClassMailBody = 0xca
	TagMailBody   = 0x74e
	MailBodySize  = 1870
	// ClassAttachment is an attachment record.
	ClassAttachment = 0x16a
	TagAttachment   = 0x318
	AttachmentSize  = 792
	// ClassMailFolder is a folder.
	ClassMailFolder = 0x4d
	TagMailFolder   = 0x4d0
	MailFolderSize  = 1232
	// ClassRecipient is one recipient of a message.
	ClassRecipient = 0x55
	TagRecipient   = 0x15e
	RecipientSize  = 350

	mailKey, mailKey2, mailLead, mailStamp = 20, 40, 104, 112
	mailParent                             = 32

	// Header.
	mhDetailKey = 292
	mhFolder    = 532
	mhFolder2   = 552
	mhReceived  = 224
	mhReceived2 = 672
	mhRead      = 740
	mhImportant = 752
	mhICal      = 772 // word, base = the tag (no lead); the length word has no terminator
	mhSenderNm  = 884
	mhSubject   = 900
	mhPreview   = 920
	mhSenderAd  = 940
	mhFlag      = 1033

	// Detail.
	mdReceived  = 288
	mdSent      = 728
	mdInReplyTo = 1212
	mdMessageID = 1228
	mdClass     = 1236

	// Body.
	mbDefaultInline = 1668
	mbDefaultPath   = 1524

	// Attachment.
	maSize    = 568
	maName    = 608
	maState   = 624
	maPath    = 648
	maMessage = 380

	// Folder.
	mfName = 1088
	mfType = 1160

	// Recipient.
	mrName    = 292
	mrAddress = 300
	mrKind    = 316
)

// mailObject starts an object of the class with its key, change stamp and lead (the zero bytes
// between the fixed region and the string area), and returns it with the writer for its strings.
func mailObject(class, tag uint16, size int, key uint32, stamp uint64, lead int) (*Object, mailStrings) {
	o := NewObject(class, tag, size)
	o.PutU32(mailKey, key)
	o.PutU32(mailKey2, key)
	o.PutU64(mailStamp, stamp)
	o.PutU32(mailLead, clampU32(lead))
	o.Append(make([]byte, lead))
	return o, mailStrings{o: o, base: size + lead}
}

// mailStrings writes string words for one object.
type mailStrings struct {
	o    *Object
	base int
}

// put writes s as UTF-16LE with a terminator and sets the word pair at wordOff; an empty s is
// written as an absent string.
func (w mailStrings) put(wordOff int, s string) {
	if s == "" {
		w.o.PutU32(wordOff, 0)
		w.o.PutU32(wordOff+4, 0)
		return
	}
	w.o.PutStringWord(wordOff, w.base, s)
	w.o.PutU32(wordOff+4, clampU32(len(UTF16Z(s)))|lengthFlag)
}

// bytes appends raw bytes as a string-area entry and sets the word pair at wordOff, relative to
// the string area base. The length word has bit 31 set when flag is true.
func (w mailStrings) bytes(wordOff int, p []byte, flag bool) {
	at := w.o.Append(p)
	w.o.PutU32(wordOff, clampU32(at-w.base))
	n := clampU32(len(p))
	if flag {
		n |= lengthFlag
	}
	w.o.PutU32(wordOff+4, n)
}

// MailHeaderSpec describes one class 0x4f object. Zero values are written as zero; empty
// strings are written as absent.
type MailHeaderSpec struct {
	// Tag overrides the envelope tag (a test of an unknown layout); zero means TagMailHeader.
	Tag                                      uint16
	Key                                      uint32
	Stamp                                    uint64
	DetailKey, FolderKey                     uint32
	Received                                 time.Time
	Subject, SenderName, SenderAddr, Preview string
	Unread                                   uint32 // +740: 1 unread, 0 read, 2 to 7 unknown
	FlagByte                                 uint8  // +1033: 0 none, 1 complete, 2 flagged
	Importance                               uint32 // +752: 0 low, 1 normal, 2 high
	ICalHex                                  string // the invite id as ASCII hex text; empty is absent
	Lead                                     int
}

// NewMailHeader builds a header object.
func NewMailHeader(s MailHeaderSpec) *Object {
	tag := tagOr(s.Tag, TagMailHeader)
	o, w := mailObject(ClassMailHeader, tag, MailHeaderSize, s.Key, s.Stamp, s.Lead)
	o.PutU32(mhDetailKey, s.DetailKey)
	o.PutU32(mhFolder, s.FolderKey)
	o.PutU32(mhFolder2, s.FolderKey)
	o.PutU64(mhReceived, Ticks(s.Received))
	o.PutU64(mhReceived2, Ticks(s.Received))
	o.PutU32(mhRead, s.Unread)
	o.PutU32(mhImportant, s.Importance)
	o.PutU8(mhFlag, s.FlagByte)
	w.put(mhSubject, s.Subject)
	w.put(mhSenderNm, s.SenderName)
	w.put(mhSenderAd, s.SenderAddr)
	w.put(mhPreview, s.Preview)
	if s.ICalHex == "" {
		o.PutU32(mhICal, 0)
		o.PutU32(mhICal+4, 0)
	} else {
		at := o.Append([]byte(s.ICalHex))
		o.PutU32(mhICal, clampU32(at-MailHeaderSize)) // base is the fixed size, without the lead
		o.PutU32(mhICal+4, clampU32(len(s.ICalHex)))
	}
	return o
}

// MailDetailSpec describes one class 0xc9 object.
type MailDetailSpec struct {
	Tag                         uint16
	Key                         uint32
	Stamp                       uint64
	MessageID, Class, InReplyTo string
	Sent, Received              time.Time
	Lead                        int
}

// NewMailDetail builds a detail object.
func NewMailDetail(s MailDetailSpec) *Object {
	tag := tagOr(s.Tag, TagMailDetail)
	o, w := mailObject(ClassMailDetail, tag, MailDetailSize, s.Key, s.Stamp, s.Lead)
	o.PutU64(mdSent, Ticks(s.Sent))
	o.PutU64(mdReceived, Ticks(s.Received))
	w.put(mdMessageID, s.MessageID)
	w.put(mdClass, s.Class)
	w.put(mdInReplyTo, s.InReplyTo)
	return o
}

// MailBodySpec describes one class 0xca object. The body is inline HTML bytes, or a path
// (UTF-16LE, with a terminator), or neither.
type MailBodySpec struct {
	Tag   uint16
	Key   uint32
	Stamp uint64
	// HTML is the inline body, written as it is (UTF-8 in a real store; a test may pass
	// bytes that are not).
	HTML []byte
	// Path is the EFMData path of a body kept in a file.
	Path string
	// WordOff is where the word pair sits (a multiple of four inside the fixed region); zero
	// means 1668 for an inline body and 1524 for a path.
	WordOff int
	Lead    int
}

// NewMailBody builds a body object.
func NewMailBody(s MailBodySpec) *Object {
	tag := tagOr(s.Tag, TagMailBody)
	o, w := mailObject(ClassMailBody, tag, MailBodySize, s.Key, s.Stamp, s.Lead)
	switch {
	case len(s.HTML) > 0:
		w.bytes(wordOrDefault(s.WordOff, mbDefaultInline), s.HTML, true)
	case s.Path != "":
		w.bytes(wordOrDefault(s.WordOff, mbDefaultPath), UTF16Z(s.Path), true)
	}
	return o
}

func wordOrDefault(off, def int) int {
	if off == 0 {
		return def
	}
	return off
}

// AttachmentSpec describes one class 0x16a object.
type AttachmentSpec struct {
	Tag        uint16
	Key        uint32
	Stamp      uint64
	MessageKey uint32 // the detail key of the message
	Name, Path string
	Size       uint32
	State      uint32 // +624: 2 downloaded
	Lead       int
}

// NewAttachment builds an attachment object.
func NewAttachment(s AttachmentSpec) *Object {
	tag := tagOr(s.Tag, TagAttachment)
	o, w := mailObject(ClassAttachment, tag, AttachmentSize, s.Key, s.Stamp, s.Lead)
	o.PutU32(maMessage, s.MessageKey)
	o.PutU32(maSize, s.Size)
	o.PutU32(maState, s.State)
	w.put(maName, s.Name)
	w.put(maPath, s.Path)
	return o
}

// MailFolderSpec describes one class 0x4d object.
type MailFolderSpec struct {
	Tag    uint16
	Key    uint32
	Stamp  uint64
	Parent uint32
	Name   string
	Type   uint32 // +1160: 0x61 inbox, 0x63 archive, 0x64 drafts, 0x65 sent, 0x67 deleted, 0x7a generic (user folders, To Me, Junk Email and other system folders)
	Lead   int
}

// NewMailFolder builds a folder object.
func NewMailFolder(s MailFolderSpec) *Object {
	tag := tagOr(s.Tag, TagMailFolder)
	o, w := mailObject(ClassMailFolder, tag, MailFolderSize, s.Key, s.Stamp, s.Lead)
	o.PutU32(mailParent, s.Parent)
	o.PutU32(mfType, s.Type)
	w.put(mfName, s.Name)
	return o
}

// RecipientSpec describes one class 0x55 object.
type RecipientSpec struct {
	Tag     uint16
	Key     uint32
	Stamp   uint64
	Parent  uint32 // the detail key of the message
	Name    string
	Address string
	Kind    uint32
	Lead    int
}

// NewRecipient builds a recipient object.
func NewRecipient(s RecipientSpec) *Object {
	tag := tagOr(s.Tag, TagRecipient)
	o, w := mailObject(ClassRecipient, tag, RecipientSize, s.Key, s.Stamp, s.Lead)
	o.PutU32(mailParent, s.Parent)
	o.PutU32(mrKind, s.Kind)
	w.put(mrName, s.Name)
	w.put(mrAddress, s.Address)
	return o
}

func tagOr(tag, def uint16) uint16 {
	if tag == 0 {
		return def
	}
	return tag
}
