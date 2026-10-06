// Package hxfixture builds the committed synthetic Outlook store fixture under
// testdata/outlook-fixture. Everything in it is invented: the names start with
// "Fixture", the addresses end in example.invalid, the ids are hex of the ASCII
// word FIXTURE plus a counter, and the dates are in 2031 (the five events shared with the Teams fixture use its November 2023 dates, and their organizer). Nothing here reads a
// real store, a clock or a random source, so the output is the same bytes every
// time. scripts/hxfixture writes it; the tests here check that the committed
// copy is what this code makes.
package hxfixture

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/ourostack/teamscrawl/internal/hxstore/hxbuild"
)

// GeneratorVersion changes whenever the fixture's content changes on purpose.
const GeneratorVersion = 2

// Dir is where the fixture lives, relative to the repository root.
const Dir = "testdata/outlook-fixture"

// File is one file of the fixture, named relative to Dir.
type File struct {
	Name string
	Data []byte
}

// Pair names an object class and tag.
type Pair struct{ Class, Tag uint16 }

// Build returns every file of the fixture, sorted by name, ending with
// PROVENANCE.json (which describes the others).
func Build() []File {
	g := &gen{}
	files := []File{
		{"HxStore.hxd", g.main()},
		{"profile-two/HxStore.hxd", g.second()},
		{"store-version-j.hxd", g.guardStore(hxbuild.Options{Version: 'j'}, g.eventBlock(tagPlain))},
		{"store-page-8192.hxd", g.guardStore(hxbuild.Options{PageSize: 8192}, g.eventBlock(tagPlain))},
		{"store-not-hxstore.hxd", append([]byte("Notstrom"), make([]byte, 0x80)...)},
		{"store-short.hxd", []byte("Nostromo-and")},
		{"store-empty.hxd", hxbuild.New(hxbuild.Options{}).Bytes()},
		{"store-new-tag-mixed.hxd", g.guardStore(hxbuild.Options{}, g.eventBlock(tagMixed))},
		{"store-unknown-classes-only.hxd", g.guardStore(hxbuild.Options{}, g.unknownBlock())},
		{"store-low-coverage.hxd", g.lowCoverage()},
		{"store-damaged-blocks.hxd", g.damaged()},
		{"twin-candidates.txt", []byte(g.twins())},
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	return append(files, provenance(files))
}

// MainCounts is the number of objects of each (class, tag) the main store
// holds, for a test that checks a reader's census against what was written.
func MainCounts() map[Pair]int {
	g := &gen{}
	g.main()
	return g.counts
}

// Case is one class 0x6b object of the main store, as the generator specified it.
type Case struct {
	// Spec and Detail are what the generator wrote; a Stub has neither.
	Spec   hxbuild.EventSpec
	Detail hxbuild.DetailSpec
	Stub   bool
}

// MainEvents lists every event object of the main store, every copy included, so a test
// can read the store back and compare each mapped field with what was written.
func MainEvents() []Case {
	g := &gen{}
	master, occ, exc := g.series()
	v := g.versions()
	var out []Case
	for _, e := range []event{g.plain(), g.online(), g.allDay(), g.cancelled(), master, occ, exc, v[0], v[1], v[2], g.nineAttendees(), g.highBit()} {
		out = append(out, Case{Spec: e.spec, Detail: e.detail})
	}
	return append(out, Case{Stub: true}, Case{Stub: true})
}

type provenanceDoc struct {
	Generator        string            `json:"generator"`
	GeneratorVersion int               `json:"generator_version"`
	Layout           string            `json:"layout"`
	Files            map[string]string `json:"files"`
	Hash             string            `json:"hash"`
}

func provenance(files []File) File {
	doc := provenanceDoc{
		Generator: "scripts/hxfixture", GeneratorVersion: GeneratorVersion,
		Layout: "docs/outlook-store.md", Files: map[string]string{},
	}
	all := sha256.New()
	for _, f := range files {
		sum := sha256.Sum256(f.Data)
		doc.Files[f.Name] = hex.EncodeToString(sum[:])
		_, _ = fmt.Fprintf(all, "%s\n%x\n", f.Name, sum)
	}
	doc.Hash = hex.EncodeToString(all.Sum(nil))
	data, _ := json.MarshalIndent(doc, "", "  ") // cannot fail: only strings and an int
	return File{"PROVENANCE.json", append(data, '\n')}
}

// gen holds the state of one build: counters for the ids and a census of the
// objects put into the main store.
type gen struct {
	counts map[Pair]int
}

// note counts an object for the census and returns it.
func (g *gen) note(o *hxbuild.Object) *hxbuild.Object {
	if g.counts == nil {
		g.counts = map[Pair]int{}
	}
	env := o.Encode()[4:]
	g.counts[Pair{uint16(env[10]) | uint16(env[11])<<8, uint16(env[2]) | uint16(env[3])<<8}]++
	return o
}

func (g *gen) noteAll(os ...*hxbuild.Object) []*hxbuild.Object {
	for _, o := range os {
		g.note(o)
	}
	return os
}

func at(day, hour int) time.Time { return time.Date(2031, 3, day, hour, 0, 0, 0, time.UTC) }

func fixtureID(n int) string { return fmt.Sprintf("FIXTURE-EVT-%04d", n) }

// event is a fixture event with the detail object that goes with it.
type event struct {
	spec   hxbuild.EventSpec
	detail hxbuild.DetailSpec
}

func base(n int) hxbuild.EventSpec {
	return hxbuild.EventSpec{
		ID:              hxbuild.GlobalObjectID(0, 0, 0, fixtureID(n)),
		SeriesKey:       0xf1c7_0000_0000_0000 | uint64(n), //nolint:gosec // n is a small positive counter
		DetailKey:       uint32(1000 + n),                  //nolint:gosec // n is a small positive counter
		LastModified:    at(1, 12),
		Start:           at(3+n%25, 9),
		End:             at(3+n%25, 10),
		ZoneID:          2,
		ZoneName:        "Fixture Standard Time",
		ShowAs:          2,
		Response:        0,
		Subject:         fmt.Sprintf("Fixture event %d", n),
		SubjectBare:     fmt.Sprintf("Fixture event %d", n),
		Location:        "Fixture Room 1",
		OrganizerName:   "Fixture Organizer",
		OrganizerAddr:   "fixture.organizer@example.invalid",
		Preview:         "Fixture preview text.",
		AreaOneSize:     813,
		ReminderMinutes: 0,
	}
}

func attendees(n int, optionalFrom int) []hxbuild.Attendee {
	var out []hxbuild.Attendee
	responses := []uint32{0, 1, 2, 4}
	for i := 1; i <= n; i++ {
		a := hxbuild.Attendee{
			Name:    fmt.Sprintf("Fixture Attendee %d", i),
			Address: fmt.Sprintf("fixture.attendee%d@example.invalid", i),
			B:       responses[i%len(responses)],
		}
		if optionalFrom > 0 && i >= optionalFrom {
			a.A = 1
		}
		out = append(out, a)
	}
	return out
}

func body(n int) string { return fmt.Sprintf("<html><body><p>Fixture body %d.</p></body></html>", n) }

// teamsID is the iCal UID the Teams fixture (scripts/fixture/page.html) gives an
// event: the prefix, a date (zero for none), then the ASCII text FIXTURE-label
// padded with zeros to 36 bytes. Sharing these ids makes an Outlook event and a
// Teams event the same meeting.
func teamsID(label string, y, m, d int) []byte {
	tail := make([]byte, 36)
	copy(tail, "FIXTURE-"+label)
	return hxbuild.GlobalObjectIDTail(y, m, d, tail)
}

// teamsAt is a time on the Teams fixture's calendar (November 2023, UTC).
func teamsAt(day, hour, min int) time.Time {
	return time.Date(2023, 11, day, hour, min, 0, 0, time.UTC)
}

// twinOf makes s agree with the Teams fixture where real twins agree: the same organizer, and a
// last-modified time on the Teams fixture's calendar, an hour after the Teams copy (Outlook is the
// newer copy in 146 of 148 real twins). Disagreements the tests need are set by the caller.
func twinOf(s *hxbuild.EventSpec) {
	s.OrganizerName, s.OrganizerAddr = "Pat Example", "pat@example.invalid"
	s.LastModified = time.Date(2023, 11, 14, 10, 0, 0, 0, time.UTC)
}

func (g *gen) plain() event {
	s := base(1)
	s.ID = teamsID("RICH-MEETING", 0, 0, 0)
	twinOf(&s)
	// Deliberate disagreements, which tests rely on: Outlook names one of the Teams copy's two
	// rooms, and does not flag the event as an online meeting.
	s.Location = "Fixture Room Alpha"
	s.Start, s.End = teamsAt(20, 17, 0), teamsAt(20, 18, 0)
	s.Subject, s.SubjectBare = "Fixture planning review", "Fixture planning review"
	// Three of the six attendees the Teams copy of this meeting lists (scripts/fixture/page.html),
	// as in real data, where the Outlook list is the shorter one. Sam Example answered since the
	// Teams copy was cached, so the two copies disagree on that response.
	s.Attendees = []hxbuild.Attendee{
		{Name: "Sam Fixture", Address: "sam@example.invalid", B: 0},
		{Name: "Jordan Fixture", Address: "jordan@example.invalid", B: 1},
		{Name: "Casey Fixture", Address: "casey@example.invalid", B: 2},
	}
	return event{s, hxbuild.DetailSpec{Key: s.DetailKey, SeriesKey: s.SeriesKey, BodyHTML: body(1)}}
}

func (g *gen) online() event {
	s := base(2)
	s.ID = teamsID("PACIFIC-1", 0, 0, 0)
	twinOf(&s)
	s.Location = "Fixture Room Gamma"
	s.Start, s.End = teamsAt(21, 18, 0), teamsAt(21, 19, 0)
	s.Subject, s.SubjectBare = "Fixture pacific sync", "Fixture pacific sync"
	s.Online, s.ShowAs, s.Response, s.ReminderMinutes, s.ZoneID = true, 1, 1, 15, 3
	s.ZoneName = "Fixture Mountain Time"
	s.AreaOneSize = 812 // an even area one puts the strings at odd offsets
	s.Attendees = attendees(4, 3)
	return event{s, hxbuild.DetailSpec{
		Key: s.DetailKey, SeriesKey: s.SeriesKey, Lead: 3,
		JoinLink: "https://example.invalid/fixture/join/0002", DialIn: "Fixture dial-in 555-0100", BodyHTML: body(2),
	}}
}

func (g *gen) allDay() event {
	s := base(3)
	s.Subject, s.SubjectBare = "Fixture all-day event", "Fixture all-day event"
	s.Start, s.End = at(5, 0), at(6, 0)
	s.AllDay, s.ShowAs, s.Response, s.ZoneID = true, 0, 4, 4
	s.ZoneName = "Fixture Eastern Time"
	return event{s, hxbuild.DetailSpec{Key: s.DetailKey, SeriesKey: s.SeriesKey}}
}

func (g *gen) cancelled() event {
	s := base(4)
	s.Subject, s.SubjectBare = "Canceled: Fixture cancelled event", "Fixture cancelled event"
	s.Cancelled, s.Response, s.ZoneID = true, 3, 99 // an unmapped response and an unknown zone code
	s.ZoneName = "Fixture Unknown Time"
	return event{s, hxbuild.DetailSpec{Key: s.DetailKey, SeriesKey: s.SeriesKey, BodyHTML: body(4)}}
}

// series returns a master, an occurrence and an exception of one recurring
// event. They share the series key and the detail key.
func (g *gen) series() (master, occurrence, exception event) {
	mk := func(typ uint32, y, m, d int, subject string) event {
		s := base(5)
		s.ID = teamsID("STANDUP-1", y, m, d)
		twinOf(&s)
		// Deliberate disagreement: Outlook's own join link is not Teams' (see join/0005 below).
		s.EventType, s.Subject, s.SubjectBare = typ, subject, subject
		day := d
		if d == 0 {
			day = 22 // the master starts on the first day of the series
		}
		s.Start, s.End = teamsAt(day, 18, 0), teamsAt(day, 18, 30)
		return event{s, hxbuild.DetailSpec{Key: s.DetailKey, SeriesKey: s.SeriesKey, JoinLink: "https://example.invalid/fixture/join/0005", BodyHTML: body(5)}}
	}
	master = mk(3, 0, 0, 0, "Fixture standup")
	master.spec.Online = true
	occurrence = mk(1, 2023, 11, 22, "Fixture standup")
	occurrence.spec.Online = true
	exception = mk(2, 2023, 11, 24, "Fixture standup")
	exception.spec.Online, exception.spec.Start, exception.spec.End = true, teamsAt(24, 19, 0), teamsAt(24, 19, 30)
	return
}

// versions returns three copies of one event, oldest first.
func (g *gen) versions() [3]event {
	var out [3]event
	for i := range out {
		s := base(6)
		s.Subject = fmt.Sprintf("Fixture versioned event v%d", i+1)
		s.SubjectBare = s.Subject
		s.LastModified = at(1+i, 8)
		s.Stamp = uint64(2*i + 1)
		out[i] = event{s, hxbuild.DetailSpec{Key: s.DetailKey, SeriesKey: s.SeriesKey, BodyHTML: body(6)}}
	}
	return out
}

func (g *gen) nineAttendees() event {
	s := base(7)
	s.Subject, s.SubjectBare = "Fixture large meeting", "Fixture large meeting"
	s.Attendees = attendees(9, 7)
	return event{s, hxbuild.DetailSpec{Key: s.DetailKey, SeriesKey: s.SeriesKey, BodyHTML: body(7)}}
}

func (g *gen) highBit() event {
	s := base(8)
	s.Subject, s.SubjectBare = "Fixture high-bit lengths", "Fixture high-bit lengths"
	s.HighBitLengths = true
	return event{s, hxbuild.DetailSpec{Key: s.DetailKey, SeriesKey: s.SeriesKey}}
}

type tagMode int

const (
	tagPlain tagMode = iota // every event carries the known tag
	tagMixed                // some events carry an unknown tag
)

func (g *gen) unknownObjects() []*hxbuild.Object {
	withText := hxbuild.NewObject(0xd7, 40, 40)
	withText.AppendString("Fixture unknown-class text")
	return []*hxbuild.Object{
		hxbuild.NewObject(0x71, 20, 20), hxbuild.NewObject(0x71, 20, 20), hxbuild.NewObject(0x71, 20, 20),
		hxbuild.NewObject(0x55, 32, 32), hxbuild.NewObject(0x55, 32, 32), withText,
	}
}

func objs(es ...event) []*hxbuild.Object {
	out := make([]*hxbuild.Object, len(es))
	for i, e := range es {
		out[i] = hxbuild.NewEvent(e.spec)
	}
	return out
}

func details(es ...event) []*hxbuild.Object {
	out := make([]*hxbuild.Object, len(es))
	for i, e := range es {
		out[i] = hxbuild.NewDetail(e.detail)
	}
	return out
}

// main builds HxStore.hxd: four valid blocks and nothing damaged. The oldest
// copy of the versioned event is in the last block, after the newer ones.
func (g *gen) main() []byte {
	master, occ, exc := g.series()
	v := g.versions()
	all := []event{g.plain(), g.online(), g.allDay(), g.cancelled(), master, occ, exc, v[0], g.nineAttendees(), g.highBit()}
	b := hxbuild.New(hxbuild.Options{})
	b.BlockCodec(hxbuild.FramedPayload(hxbuild.Head(15),
		g.noteAll(append(objs(all[0], all[1], all[2], all[3]), objs(v[1])...)...)...), hxbuild.CodecLiteral)
	b.BlockCodec(hxbuild.FramedPayload(hxbuild.Head(31),
		g.noteAll(append(objs(master, occ, exc, v[2], all[8], all[9]), hxbuild.NewEventStub(0xf1c7_0000_0000_0099), hxbuild.NewEventStub(0xf1c7_0000_0000_0099))...)...), hxbuild.CodecMatches)
	b.BlockCodec(hxbuild.FramedPayload(hxbuild.Head(15),
		g.noteAll(details(all[0], all[1], all[2], all[3], master, v[2], all[8], all[9])...)...), hxbuild.CodecMatches)
	b.BlockCodec(hxbuild.FramedPayload(hxbuild.Head(35),
		g.noteAll(append(g.unknownObjects(), objs(v[0])...)...)...), hxbuild.CodecLiteral)
	return b.Bytes()
}

// second builds the store of a second profile: two events and their details.
func (g *gen) second() []byte {
	e := base(21)
	e.Subject, e.SubjectBare = "Fixture second profile event", "Fixture second profile event"
	e.Online = true
	d := hxbuild.DetailSpec{Key: e.DetailKey, SeriesKey: e.SeriesKey, JoinLink: "https://example.invalid/fixture/join/0021"}
	f := base(22)
	f.Subject, f.SubjectBare = "Fixture second profile event 2", "Fixture second profile event 2"
	b := hxbuild.New(hxbuild.Options{})
	b.BlockCodec(hxbuild.FramedPayload(hxbuild.Head(15),
		hxbuild.NewEvent(e), hxbuild.NewEvent(f), hxbuild.NewDetail(d), hxbuild.NewDetail(hxbuild.DetailSpec{Key: f.DetailKey, SeriesKey: f.SeriesKey})),
		hxbuild.CodecLiteral)
	return b.Bytes()
}

// eventBlock returns the payload of a block of three events. In tagMixed mode
// the second event carries a tag no known layout has.
func (g *gen) eventBlock(m tagMode) []byte {
	a, b, c := base(31), base(32), base(33)
	if m == tagMixed {
		b.Tag = 0x456
	}
	return hxbuild.FramedPayload(hxbuild.Head(15), objs(event{spec: a}, event{spec: b}, event{spec: c})...)
}

func (g *gen) unknownBlock() []byte {
	return hxbuild.FramedPayload(hxbuild.Head(15), g.unknownObjects()...)
}

// guardStore returns a store with the given header and one valid block.
func (g *gen) guardStore(o hxbuild.Options, payload []byte) []byte {
	b := hxbuild.New(o)
	b.BlockCodec(payload, hxbuild.CodecLiteral)
	return b.Bytes()
}

// lowCoverage returns a store whose one valid block is mostly bytes that belong
// to no object.
func (g *gen) lowCoverage() []byte {
	junk := make([]byte, 20000)
	for i := range junk {
		junk[i] = byte(i*7+i/251) | 0x80 // the high bit keeps the envelope marker out
	}
	payload := append(hxbuild.FramedPayload(hxbuild.Head(15), hxbuild.NewEvent(base(41))), junk...)
	return g.guardStore(hxbuild.Options{}, payload)
}

// damaged returns a store with one block of each kind a reader must reject, one
// valid block at the start and one hidden inside a torn block, and a block cut
// off by the end of the file.
func (g *gen) damaged() []byte {
	valid := func(n int) []byte {
		return hxbuild.EncodeBlock(hxbuild.BlockTypeData, hxbuild.FramedPayload(hxbuild.Head(15), hxbuild.NewEvent(base(n))))
	}
	b := hxbuild.New(hxbuild.Options{})
	b.Raw(valid(51))
	// A torn block whose payload holds a whole valid block: the scan must go on
	// past the torn magic and find the one inside.
	b.Raw(hxbuild.BadHeaderCRC(hxbuild.EncodeBlock(hxbuild.BlockTypeData, valid(52))))
	b.Raw(hxbuild.BadPayloadCRC(valid(53)))
	b.Raw(hxbuild.OversizeBlock(33 << 20))
	b.Raw(hxbuild.EncodeBlock(16, []byte("Fixture block of another type")))
	b.Raw(hxbuild.EncodeRaw(hxbuild.BlockTypeData, hxbuild.LiteralLZ4([]byte("Fixture")), 7, 5)) // header constant
	b.Raw(hxbuild.EncodeRaw(hxbuild.BlockTypeData, hxbuild.LiteralLZ4([]byte("Fixture")), 9, 4)) // inflates short
	// A valid block with no object in it: a payload in a record form the walk
	// does not decode.
	b.Raw(hxbuild.EncodeBlock(hxbuild.BlockTypeData, []byte("Fixture undecoded record form, no object here.")))
	b.Raw(hxbuild.Truncated(valid(54), hxbuild.BlockHeaderSize+10))
	return b.Bytes()
}

// twins lists, in hex, the ids of the fixture events that are the same meetings
// as events in the Teams fixture's calendar.
func (g *gen) twins() string {
	master, occ, exc := g.series()
	ids := []event{g.plain(), g.online(), master, occ, exc}
	var sb strings.Builder
	sb.WriteString("# iCal UIDs (hex) of fixture events that are the same meetings as events in the Teams fixture.\n")
	sb.WriteString("# They equal the iCalUID values in testdata/teams-fixture (account seed 1) once lower-cased: the store holds ids in upper case, as the real one does, and Teams holds them in lower case.\n")
	for _, e := range ids {
		sb.WriteString(hxbuild.HexID(e.spec.ID) + "\n")
	}
	return sb.String()
}
