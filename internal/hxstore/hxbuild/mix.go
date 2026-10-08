package hxbuild

import (
	"fmt"
	"strings"
	"time"
)

// ClassFiller is the class of the objects Mix writes that no reader maps: stand-ins for the large
// classes of a real store that the calendar and mail readers walk past (a real store holds about
// 120,000 of one such class).
const (
	ClassFiller = 0x71
	TagFiller   = 1100
)

// MixOptions sizes a synthetic store whose object mix has the shape of a real one: many objects
// the readers ignore, many recipients, fewer messages and fewer events, 16 objects to a block.
type MixOptions struct {
	Events      int  // event objects, including older copies of the same event
	EventIDs    int  // distinct events among them; zero means Events
	Details     int  // detail objects, which the events link to in turn; zero means Events
	Messages    int  // mail messages: a header, a detail and an inline body each
	Recipients  int  // recipient objects, spread over the messages
	Filler      int  // objects of ClassFiller
	FillerNoise int  // incompressible bytes in each filler object
	FileBodies  bool // every other message keeps its body in a file, named by MixBodyPath
	DetailBytes int  // about this many bytes of body text per event detail; zero means BodyBytes
	BodyBytes   int  // about this many bytes of body text per message (and per event detail, unless DetailBytes says otherwise)
	Codec       Codec
}

// MixBlockObjects is the number of objects Mix puts in one block.
const MixBlockObjects = 16

// MixBodyPath is the store path of the body file of message i when MixOptions.FileBodies is set.
func MixBodyPath(i int) string { return fmt.Sprintf("~/Files/mix-%d.dat", i) }

// MixObjects is the number of objects a store built with o holds.
func (o MixOptions) MixObjects() int {
	return o.Events + o.details() + 3*o.Messages + o.Recipients + o.Filler + 1
}

func (o MixOptions) details() int {
	if o.Details > 0 {
		return o.Details
	}
	return o.Events
}

// Mix builds a store with the object mix o describes. Every key and id is distinct, so every
// object is the winner of its key and the readers keep all of them. The result is deterministic.
func Mix(o MixOptions) []byte {
	b := New(Options{})
	body := "<p>" + strings.Repeat("Fixture body text. ", max(o.BodyBytes, 19)/19) + "</p>"
	detailBody := body
	if o.DetailBytes > 0 {
		detailBody = "<p>" + strings.Repeat("Fixture body text. ", max(o.DetailBytes, 19)/19) + "</p>"
	}
	var objs []*Object
	flush := func() {
		if len(objs) > 0 {
			b.BlockCodec(FramedPayload(Head(15), objs...), o.Codec)
			objs = objs[:0]
		}
	}
	add := func(x *Object) {
		objs = append(objs, x)
		if len(objs) == MixBlockObjects {
			flush()
		}
	}
	day := time.Date(2031, 3, 3, 9, 0, 0, 0, time.UTC)
	add(NewMailFolder(MailFolderSpec{Key: 101, Parent: 1000, Name: "Fixture Inbox", Type: 0x61}))
	ids, details := o.EventIDs, o.details()
	if ids <= 0 {
		ids = o.Events
	}
	for i := 0; i < o.Events; i++ {
		n := i % max(ids, 1)
		add(NewEvent(EventSpec{
			ID: GlobalObjectID(0, 0, 0, fmt.Sprintf("FIXTURE-MIX-%07d", n)), SeriesKey: 0xaa00 + uint64(n), DetailKey: uint32(2_000_000 + n%max(details, 1)), //nolint:gosec // small test counts
			LastModified: day.Add(time.Duration(i/max(ids, 1)) * time.Minute), Start: day.Add(time.Duration(n) * time.Hour), End: day.Add(time.Duration(n+1) * time.Hour),
			ZoneName: "Pacific Standard Time", ShowAs: 2, Subject: "Fixture subject", SubjectBare: "Fixture subject", Location: "Fixture Room",
			OrganizerName: "Fixture Organizer", OrganizerAddr: "organizer@example.invalid", Preview: "Fixture preview",
			Attendees:   []Attendee{{Name: "Fixture One", Address: "one@example.invalid", A: 1, B: 0}, {Name: "Fixture Two", Address: "two@example.invalid", B: 4}},
			AreaOneSize: 812,
		}))
	}
	for i := 0; i < details; i++ {
		add(NewDetail(DetailSpec{Key: uint32(2_000_000 + i), JoinLink: "https://example.invalid/join", DialIn: "Fixture dial-in", BodyHTML: detailBody, Lead: 3})) //nolint:gosec // small test counts
	}
	for i := 0; i < o.Messages; i++ {
		k := uint32(10_000 + i) //nolint:gosec // small test counts
		add(NewMailHeader(MailHeaderSpec{Key: k, Stamp: 1, DetailKey: k + 5_000_000, FolderKey: 101, Received: day.Add(time.Duration(i) * time.Minute), Subject: fmt.Sprintf("Fixture message %d", i),
			SenderName: "Fixture Sender", SenderAddr: "sender@example.invalid", Unread: 1, Importance: 1, Preview: "Fixture preview"}))
		add(NewMailDetail(MailDetailSpec{Key: k + 5_000_000, Stamp: 1, MessageID: fmt.Sprintf("<%d@example.invalid>", i), Class: "IPM.Note", Sent: day}))
		if o.FileBodies && i%2 == 1 {
			add(NewMailBody(MailBodySpec{Key: k + 5_000_000, Stamp: 1, Path: MixBodyPath(i)}))
		} else {
			add(NewMailBody(MailBodySpec{Key: k + 5_000_000, Stamp: 1, HTML: []byte(body)}))
		}
	}
	for i := 0; i < o.Recipients; i++ {
		parent := uint32(5_000_000)
		if o.Messages > 0 {
			parent += uint32(10_000 + i%o.Messages) //nolint:gosec // small test counts
		}
		add(NewRecipient(RecipientSpec{Key: uint32(20_000_000 + i), Stamp: 1, Parent: parent, Name: "Fixture Recipient", Address: fmt.Sprintf("r%d@example.invalid", i)})) //nolint:gosec // small test counts
	}
	seed := uint64(0x9e3779b97f4a7c15)
	noise := min(o.FillerNoise, TagFiller-32)
	for i := 0; i < o.Filler; i++ {
		f := NewObject(ClassFiller, TagFiller, TagFiller)
		f.PutU32(20, uint32(i))                //nolint:gosec // small test counts
		for j := 24; j+8 <= 24+noise; j += 8 { // incompressible bytes, so a block shrinks about as a real one does
			seed ^= seed << 13
			seed ^= seed >> 7
			seed ^= seed << 17
			f.PutU64(j, seed)
		}
		add(f)
	}
	flush()
	return b.Bytes()
}
