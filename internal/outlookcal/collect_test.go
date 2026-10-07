package outlookcal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ourostack/teamscrawl/internal/calendar"
	"github.com/ourostack/teamscrawl/internal/hxstore"
	"github.com/ourostack/teamscrawl/internal/hxstore/hxbuild"
	"github.com/ourostack/teamscrawl/internal/hxstore/hxfixture"
)

var fixtureDir = filepath.Join("..", "..", filepath.FromSlash(hxfixture.Dir))

func fixtureBytes(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(fixtureDir, filepath.FromSlash(name))) //nolint:gosec // a fixed fixture path
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func fixtureStore(t *testing.T, name string) *hxstore.Store { return open(t, fixtureBytes(t, name)) }

// expected states, from the generator's spec and from tables written here and not
// taken from the mapper, what the mapper must produce for one event.
func expectAgainst(t *testing.T, e calendar.Event, c hxfixture.Case) {
	t.Helper()
	s := c.Spec
	id := strings.ToLower(hxbuild.HexID(s.ID))
	eq := func(name string, got, want any) {
		t.Helper()
		if got != want {
			t.Errorf("%s: %s = %v, want %v", id[len(id)-20:], name, got, want)
		}
	}
	eq("SourceID", e.SourceID, id)
	eq("GlobalID", e.GlobalID, id)
	eq("ICalUID", e.ICalUID, id)
	eq("Subject", e.Subject, s.Subject)
	eq("Location", e.Location, s.Location)
	eq("Organizer", e.Organizer, s.OrganizerName)
	eq("OrganizerAddress", e.OrganizerAddress, s.OrganizerAddr)
	eq("BodyPreview", e.BodyPreview, s.Preview)
	eq("Start", e.Start.Equal(s.Start), true)
	eq("End", e.End.Equal(s.End), true)
	eq("LastModified", e.LastModified.Equal(s.LastModified), true)
	eq("EventType", e.EventType, []string{"single", "occurrence", "exception", "master"}[s.EventType])
	eq("ShowAs", e.ShowAs, map[uint32]string{0: "free", 1: "tentative", 2: "busy"}[s.ShowAs])
	eq("Response", e.Response, map[uint32]string{0: "accepted", 1: "tentative", 4: "none"}[s.Response])
	eq("Cancelled", e.Cancelled, calendar.TriOf(s.Cancelled))
	eq("IsOnlineMeeting", e.IsOnlineMeeting, calendar.TriOf(s.Online))
	eq("TimeZone", e.TimeZone, s.ZoneName)
	wantIANA := "" // the fixture's zone names are invented, except the twins' Windows zone name
	if s.ZoneName == "Pacific Standard Time" {
		wantIANA = "America/Los_Angeles"
	}
	eq("TimeZoneIANA", e.TimeZoneIANA, wantIANA)
	if s.AllDay {
		eq("AllDay", e.AllDay, calendar.TriTrue)
		eq("StartDate", e.StartDate, s.Start.Format("2006-01-02"))
		eq("EndDate", e.EndDate, s.End.Format("2006-01-02"))
	} else {
		eq("AllDay", e.AllDay, calendar.TriFalse)
	}
	var want []map[string]string
	resp := map[uint32]string{0: "accepted", 1: "tentative", 2: "declined", 4: "none"}
	for _, a := range s.Attendees {
		typ := ""
		if a.A == 1 {
			typ = "optional"
		}
		want = append(want, map[string]string{"name": a.Name, "address": a.Address, "type": typ, "response": resp[a.B]})
	}
	var got []map[string]string
	if e.AttendeesJSON != "" {
		if err := json.Unmarshal([]byte(e.AttendeesJSON), &got); err != nil {
			t.Fatal(err)
		}
	}
	wj, _ := json.Marshal(want)
	gj, _ := json.Marshal(got)
	if len(want) == 0 {
		wj, gj = nil, nil
	}
	eq("Attendees", string(gj), string(wj))
	d := c.Detail
	eq("OnlineMeetingURL", e.OnlineMeetingURL, d.JoinLink)
	eq("DialInTollNumber", e.DialInTollNumber, d.DialIn)
	eq("BodyHTML", e.BodyHTML, d.BodyHTML)
	eq("UnknownDeclared", e.UnknownDeclared, true)
}

func TestCollectFixtureReadsEveryFieldOfEveryEvent(t *testing.T) {
	res := collect(t, fixtureStore(t, "HxStore.hxd"), Options{ExpectEvents: true})
	cases := hxfixture.MainEvents()
	// The current copy of each id: the one with the latest last-modified.
	current := map[string]hxfixture.Case{}
	copies := 0
	for _, c := range cases {
		if c.Stub {
			continue
		}
		copies++
		id := strings.ToLower(hxbuild.HexID(c.Spec.ID))
		if old, ok := current[id]; !ok || c.Spec.LastModified.After(old.Spec.LastModified) {
			current[id] = c
		}
	}
	n := res.Notes
	if len(res.Events) != len(current) || n.DistinctEvents != len(current) || n.EventObjects != copies || n.SupersededCopies != copies-len(current) ||
		n.EventNoID != 2 || n.DetailObjects != 8 || n.DetailMissing != 0 || n.ResyncedSkipped != 0 || len(res.Losses) != 0 || n.DetailUnreadable != 0 {
		t.Fatalf("%+v %d events", n, len(res.Events))
	}
	for _, e := range res.Events {
		c, ok := current[e.SourceID]
		if !ok {
			t.Fatalf("unexpected event %s", e.SourceID)
		}
		expectAgainst(t, e, c)
		delete(current, e.SourceID)
		calendar.AssertEveryFieldClassified(t, e, calendar.FieldMeetingChatID, calendar.FieldDialIn, calendar.FieldJoinURL, calendar.FieldBody, calendar.FieldLocation, calendar.FieldBodyPreview) // an event may have no location or preview
	}
	if len(current) != 0 {
		t.Fatalf("events missing from the result: %d", len(current))
	}
	// Events are sorted by id. The store's other classes are not calendar objects: none is unknown.
	for i := 1; i < len(res.Events); i++ {
		if res.Events[i-1].SourceID >= res.Events[i].SourceID {
			t.Fatal("events not sorted")
		}
	}
	if len(res.UnknownLayouts) != 0 {
		t.Fatalf("%+v", res.UnknownLayouts)
	}
	if len(n.UnknownZones) != 3 || n.ResponseUnmapped != 1 || n.AttendeesAtCap != 1 || n.AllDayUnaligned != 0 {
		t.Fatalf("%+v", n)
	}
}

func TestCollectFixtureOddOffsets(t *testing.T) {
	// The online meeting's area one is even and its detail has a 3-byte lead: its strings
	// sit at odd offsets, and are read.
	res := collect(t, fixtureStore(t, "HxStore.hxd"), Options{})
	found := false
	for _, e := range res.Events {
		if e.Subject == "Fixture pacific sync" {
			found = e.OnlineMeetingURL == "https://example.invalid/fixture/join/0002" && e.TeamsThreadID == "" && e.DialInTollNumber == "Fixture dial-in 555-0100"
		}
	}
	if !found {
		t.Fatal("odd-offset strings not read")
	}
}

func TestMapEventIDEqualsTeamsKey(t *testing.T) {
	res := collect(t, fixtureStore(t, "HxStore.hxd"), Options{})
	var teams [][]byte
	err := filepath.WalkDir(filepath.Join("..", "..", "testdata", "teams-fixture"), func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p) //nolint:gosec // walking the committed Teams fixture
		teams = append(teams, b)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	inTeams := func(id string) bool {
		for _, b := range teams {
			if bytes.Contains(b, []byte(id)) {
				return true
			}
		}
		return false
	}
	twins := map[string]bool{}
	for _, l := range strings.Split(string(fixtureBytes(t, "twin-candidates.txt")), "\n") {
		if l != "" && !strings.HasPrefix(l, "#") {
			twins[l] = true
		}
	}
	byID := map[string]calendar.Event{}
	for _, e := range res.Events {
		byID[e.ICalUID] = e
	}
	for upper := range twins {
		// The fixture holds the id in upper case, as the real store does, and the Teams fixture holds
		// it in lower case, as a real Teams cache does: the twin is found only through the mapper's
		// case. A test whose two fixtures shared a case would pass without it.
		id := strings.ToLower(upper)
		if id == upper || inTeams(upper) {
			t.Fatalf("the twin id must differ in case between the stores: %s", upper[len(upper)-20:])
		}
		e, ok := byID[id]
		if !ok || !inTeams(e.ICalUID) || e.GlobalID != id {
			t.Errorf("twin %s: event %v, in the Teams fixture %v", id[len(id)-20:], ok, ok && inTeams(e.ICalUID))
		}
		// The key is the global id and the (absent) original start, as for the Teams twin.
		if calendar.Key(e) != id+"|" {
			t.Errorf("key %q", calendar.Key(e))
		}
	}
	// An occurrence and an exception name the master's id as their series, which is what the
	// Teams record's cleanGlobalObjectId holds.
	var master calendar.Event
	for _, e := range byID {
		if e.EventType == calendar.EventMaster && twins[strings.ToUpper(e.ICalUID)] {
			master = e
		}
	}
	n := 0
	for _, e := range byID {
		if (e.EventType == calendar.EventOccurrence || e.EventType == calendar.EventException) && twins[strings.ToUpper(e.ICalUID)] {
			n++
			if e.SeriesKey != master.ICalUID || e.SeriesKey == e.ICalUID {
				t.Errorf("series key of %s", e.EventType)
			}
		}
	}
	if n != 2 || master.SeriesKey != master.ICalUID {
		t.Fatalf("%d occurrences", n)
	}
}

// versionStore builds a store holding three copies of one event, one per block, in the
// given file order.
func versionStore(t *testing.T, copies ...hxbuild.EventSpec) *hxstore.Store {
	t.Helper()
	var payloads [][]byte
	for _, c := range copies {
		payloads = append(payloads, framed(hxbuild.NewEvent(c), hxbuild.NewDetail(baseDetail(1))))
	}
	return storeOf(t, payloads...)
}

func copySpec(subject string, lm int, stamp uint64) hxbuild.EventSpec {
	s := baseSpec(1)
	s.Subject = subject
	s.LastModified = at(lm, 8)
	s.Stamp = stamp
	return s
}

func subjectOf(t *testing.T, r Result) string {
	t.Helper()
	if len(r.Events) != 1 {
		t.Fatalf("%d events", len(r.Events))
	}
	return r.Events[0].Subject
}

func TestCollectLastModifiedWinsOverFileOrder(t *testing.T) {
	// The newest copy is first in the file, an older one last.
	s := versionStore(t, copySpec("newest", 3, 0), copySpec("middle", 2, 0), copySpec("oldest", 1, 0))
	r := collect(t, s, Options{})
	if subjectOf(t, r) != "newest" || r.Notes.SupersededCopies != 2 || r.Notes.EventObjects != 3 {
		t.Fatalf("%+v", r.Notes)
	}
}

func TestCollectStampBreaksALastModifiedTie(t *testing.T) {
	s := versionStore(t, copySpec("stamp 9", 2, 9), copySpec("stamp 4", 2, 4))
	if subjectOf(t, collect(t, s, Options{})) != "stamp 9" {
		t.Fatal("the higher stamp must win a last-modified tie")
	}
}

func TestCollectFileOrderIsTheLastTieBreak(t *testing.T) {
	s := versionStore(t, copySpec("earlier", 2, 5), copySpec("later", 2, 5))
	if subjectOf(t, collect(t, s, Options{})) != "later" {
		t.Fatal("equal copies: the later in the file wins")
	}
	// Within one block the later object wins too.
	one := storeOf(t, framed(hxbuild.NewEvent(copySpec("first", 2, 5)), hxbuild.NewEvent(copySpec("second", 2, 5))))
	if subjectOf(t, collect(t, one, Options{})) != "second" {
		t.Fatal("same block: the later position wins")
	}
}

func TestCollectStampRule(t *testing.T) {
	s := versionStore(t, copySpec("newer time, low stamp", 3, 1), copySpec("older time, high stamp", 1, 7))
	if subjectOf(t, collect(t, s, Options{Rule: RuleStamp})) != "older time, high stamp" {
		t.Fatal("RuleStamp compares the stamp first")
	}
	if subjectOf(t, collect(t, s, Options{})) != "newer time, low stamp" {
		t.Fatal("the default rule compares last modified first")
	}
	// Equal stamps fall through to last modified.
	s = versionStore(t, copySpec("a", 3, 1), copySpec("b", 1, 1))
	if subjectOf(t, collect(t, s, Options{Rule: RuleStamp})) != "a" {
		t.Fatal("RuleStamp falls back to last modified")
	}
}

func TestCollectOrderIndependent(t *testing.T) {
	a, b, c := copySpec("one", 1, 1), copySpec("two", 2, 2), copySpec("three", 3, 3)
	orders := [][]hxbuild.EventSpec{{a, b, c}, {c, b, a}, {b, c, a}, {b, a, c}}
	for _, o := range orders {
		if got := subjectOf(t, collect(t, versionStore(t, o...), Options{})); got != "three" {
			t.Fatalf("order %v: %q", o[0].Subject, got)
		}
	}
}

func TestVersionRuleMatchesTheLayoutDocument(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join("..", "..", "docs", "outlook-store.md"))
	if err != nil {
		t.Fatal(err)
	}
	// The document states the rule as: highest last-modified, then +112, then file order.
	if CurrentVersionRule != RuleLastModified || !strings.Contains(string(doc), "highest last-modified (+288), then the highest +112") {
		t.Fatal("the version rule in code and in docs/outlook-store.md disagree")
	}
}

func TestCollectLinksDetailAndCountsMissing(t *testing.T) {
	linked, orphan := baseSpec(1), baseSpec(2)
	orphan.DetailKey = 999 // no detail object has this key
	oldD, newD := baseDetail(1), baseDetail(1)
	oldD.BodyHTML, newD.BodyHTML = "<p>old copy</p>", "<p>new copy</p>"
	s := storeOf(t, framed(hxbuild.NewEvent(linked), hxbuild.NewEvent(orphan), hxbuild.NewDetail(oldD), hxbuild.NewDetail(newD)))
	r := collect(t, s, Options{})
	if len(r.Events) != 2 || r.Notes.DetailMissing != 1 {
		t.Fatalf("%+v", r.Notes)
	}
	for _, e := range r.Events {
		if e.SourceID == hxbuild.HexID(linked.ID) && !strings.Contains(e.BodyHTML, "new copy") {
			t.Fatalf("the last detail copy must be used: %q", e.BodyHTML)
		}
	}
}

func TestCollectSkipsResyncedObjects(t *testing.T) {
	// A stray byte before each object makes it resynced: it is counted and skipped.
	p := append([]byte{0xff}, hxbuild.NewEvent(baseSpec(1)).Encode()...)
	p = append(p, 0xff)
	p = append(p, hxbuild.NewDetail(baseDetail(1)).Encode()...)
	p = append(p, 0xff)
	p = append(p, hxbuild.NewEvent(baseSpec(2)).Encode()...)
	r := collect(t, storeOf(t, p), Options{})
	if len(r.Events) != 0 || r.Notes.ResyncedSkipped != 3 || r.Notes.DetailObjects != 0 {
		t.Fatalf("%+v", r.Notes)
	}
}

func TestCollectCountsUnmappedEvents(t *testing.T) {
	broken := hxbuild.NewEvent(baseSpec(1))
	broken.PutU32(820, 1<<30) // the id lies outside the object
	badTime := hxbuild.NewEvent(baseSpec(2))
	badTime.PutU64(584, 0)
	s := storeOf(t, framed(broken, badTime, hxbuild.NewEvent(baseSpec(3))))
	r := collect(t, s, Options{})
	if len(r.Events) != 1 || len(r.Losses) != 1 || r.Losses[0] != (Loss{CodeEventUnmapped, 2}) ||
		r.Notes.UnmappedReasons["id outside the object"] != 1 || r.Notes.UnmappedReasons["start or end is not a time"] != 1 {
		t.Fatalf("%+v %+v", r.Losses, r.Notes.UnmappedReasons)
	}
}

func TestCollectBrokenWinnerDoesNotFallBackToAnOlderCopy(t *testing.T) {
	// The newest copy cannot be mapped: the event is skipped, not shown with its older schedule.
	newest := hxbuild.NewEvent(copySpec("newest", 3, 0))
	newest.PutU64(592, 0)
	s := storeOf(t, framed(hxbuild.NewEvent(copySpec("older", 1, 0))), framed(newest))
	r := collect(t, s, Options{})
	if len(r.Events) != 0 || len(r.Losses) != 1 {
		t.Fatalf("%+v", r.Events)
	}
}

func TestOneHundredByteIDAndFalseMagic(t *testing.T) {
	// A 100-byte id (the store has 16 of them) maps like a 56-byte one, and a second
	// block magic inside an object's tail does not hide anything.
	long := baseSpec(1)
	long.ID = hxbuild.GlobalObjectID(2031, 3, 5, strings.Repeat("F", 60))
	if len(long.ID) != 100 {
		t.Fatal(len(long.ID))
	}
	junk := hxbuild.NewObject(0x71, 20, 20)
	junk.Append(hxbuild.BlockMagic())
	junk.Append(make([]byte, 40))
	r := collect(t, storeOf(t, framed(junk, hxbuild.NewEvent(long), hxbuild.NewEvent(baseSpec(2)))), Options{})
	if len(r.Events) != 2 || r.Stats.BlocksFound != 1 || r.Stats.BlocksValid != 1 {
		t.Fatalf("%d events, %+v", len(r.Events), r.Stats)
	}
	var e calendar.Event
	for _, x := range r.Events {
		if len(x.SourceID) == 200 {
			e = x
		}
	}
	// 100 bytes: the series id has the date bytes zeroed, and the occurrence is its own id.
	series, date, ok := calendar.SplitOccurrenceID(e.ICalUID)
	if e.ICalUID != strings.ToLower(hxbuild.HexID(long.ID)) || !ok || date != "2031-03-05" || e.SeriesKey != strings.ToLower(series) {
		t.Fatalf("%q %v", e.SeriesKey, ok)
	}
}

func TestOpenStoreRefusals(t *testing.T) {
	cases := map[string]struct{ file, code, detail string }{
		"version":     {"store-version-j.hxd", CodeStoreVersion, "version byte 0x6a, known 0x69"},
		"page size":   {"store-page-8192.hxd", CodeStoreVersion, "page size 8192, known 4096"},
		"not a store": {"store-not-hxstore.hxd", CodeStoreUnrecognized, ""},
		"short":       {"store-short.hxd", CodeStoreUnrecognized, ""},
	}
	for name, c := range cases {
		data := fixtureBytes(t, c.file)
		_, err := OpenStore(bytes.NewReader(data), int64(len(data)))
		var g *GuardError
		if !errors.As(err, &g) || g.Code != c.code || g.Detail != c.detail || !strings.HasPrefix(err.Error(), c.code) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A read failure is not a refusal.
	_, err := OpenStore(failingReader{}, 1000)
	var g *GuardError
	if err == nil || errors.As(err, &g) {
		t.Fatalf("%v", err)
	}
}

type failingReader struct{}

func (failingReader) ReadAt([]byte, int64) (int, error) { return 0, errors.New("disk failure") }

func guardOf(t *testing.T, file string, o Options) (Result, *GuardError) {
	t.Helper()
	r, err := Collect(context.Background(), fixtureStore(t, file), "outlook/Test", o)
	var g *GuardError
	if err != nil && !errors.As(err, &g) {
		t.Fatal(err)
	}
	return r, g
}

func TestGuardNewTagMixedAppliesNothing(t *testing.T) {
	r, g := guardOf(t, "store-new-tag-mixed.hxd", Options{})
	if g == nil || g.Code != CodeLayoutUnsupported || !strings.Contains(g.Detail, "tag 0x456 x1") || len(r.Events) != 0 {
		t.Fatalf("%v %d events", g, len(r.Events))
	}
	// Both directions: the event of the new tag is the one unknown layout, and the store's several
	// other classes are not reported.
	if len(r.UnknownLayouts) != 1 || r.UnknownLayouts[0] != (PairCount{0x6b, 0x456, 1}) {
		t.Fatalf("%+v", r.UnknownLayouts)
	}
}

func TestGuardNoEventObjects(t *testing.T) {
	if _, g := guardOf(t, "store-unknown-classes-only.hxd", Options{ExpectEvents: true}); g == nil || g.Code != CodeLayoutUnsupported || g.Detail != "no_event_objects" {
		t.Fatalf("%v", g)
	}
	// A store that never had events is merely empty.
	r, g := guardOf(t, "store-unknown-classes-only.hxd", Options{})
	if g != nil || len(r.Events) != 0 || len(r.UnknownLayouts) != 0 {
		t.Fatalf("%v %+v", g, r.UnknownLayouts)
	}
	if _, g := guardOf(t, "store-empty.hxd", Options{ExpectEvents: true}); g == nil {
		t.Fatal("an empty store after events is refused")
	}
}

func TestGuardLowWalkCoverage(t *testing.T) {
	if _, g := guardOf(t, "store-low-coverage.hxd", Options{}); g == nil || g.Code != CodeLayoutUnsupported || g.Detail != "walk_coverage" {
		t.Fatalf("%v", g)
	}
}

func TestGuardDamagedBlocksAreALossNotARefusal(t *testing.T) {
	r, g := guardOf(t, "store-damaged-blocks.hxd", Options{})
	if g != nil || len(r.Events) != 2 {
		t.Fatalf("%v %d events", g, len(r.Events))
	}
	if len(r.Losses) != 1 || r.Losses[0] != (Loss{CodeBlocksDamaged, 7}) {
		t.Fatalf("%+v", r.Losses)
	}
	// One bad block in many is under the 2% line.
	b := hxbuild.New(hxbuild.Options{})
	for i := 0; i < 60; i++ {
		b.BlockCodec(framed(hxbuild.NewEvent(baseSpec(byte(i%9)))), hxbuild.CodecLiteral)
	}
	b.Raw(hxbuild.BadHeaderCRC(hxbuild.EncodeBlock(8, []byte("x"))))
	if r := collect(t, open(t, b.Bytes()), Options{}); len(r.Losses) != 0 {
		t.Fatalf("%+v", r.Losses)
	}
}

func TestCollectReportsWalkErrors(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Collect(ctx, fixtureStore(t, "HxStore.hxd"), "a", Options{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("%v", err)
	}
}

func TestSecondProfileStore(t *testing.T) {
	r := collect(t, fixtureStore(t, "profile-two/HxStore.hxd"), Options{})
	if len(r.Events) != 2 || r.Events[0].AccountID != "outlook/Test" {
		t.Fatalf("%d events", len(r.Events))
	}
}

func TestGuardErrorText(t *testing.T) {
	if (&GuardError{Code: "c"}).Error() != "c" || (&GuardError{Code: "c", Detail: "d"}).Error() != "c: d" {
		t.Fatal("error text")
	}
}

func TestKnownLayouts(t *testing.T) {
	if len(KnownLayouts) != 3 || AccountLayout.Class != 0x49 || AccountLayout.Tag != 0x19d0 || EventLayout.Class != 0x6b || EventLayout.Tag != 0x455 || DetailLayout.Class != 0x6c || DetailLayout.Tag != 0x348 {
		t.Fatal("pinned layouts")
	}
}

func TestUnknownLayoutsSorted(t *testing.T) {
	r := collect(t, storeOf(t, framed(hxbuild.NewObject(0x6c, 21, 21), hxbuild.NewObject(0x6c, 20, 20), hxbuild.NewObject(0x55, 32, 32), hxbuild.NewEvent(baseSpec(1)))), Options{})
	want := []PairCount{{0x6c, 20, 1}, {0x6c, 21, 1}}
	if len(r.UnknownLayouts) != 2 || r.UnknownLayouts[0] != want[0] || r.UnknownLayouts[1] != want[1] {
		t.Fatalf("%+v", r.UnknownLayouts)
	}
}

func TestGuardNamesEveryUnknownTagInOrder(t *testing.T) {
	a, b := baseSpec(1), baseSpec(2)
	a.Tag, b.Tag = 0x457, 0x456
	_, err := Collect(context.Background(), storeOf(t, framed(hxbuild.NewEvent(a), hxbuild.NewEvent(b))), "a", Options{})
	var g *GuardError
	if !errors.As(err, &g) || !strings.Contains(g.Detail, "tag 0x456 x1, known 0x455; class 0x6b tag 0x457 x1") {
		t.Fatalf("%v", err)
	}
}

func TestUnknownLayoutsOfMappedClassesOnly(t *testing.T) {
	st := hxstore.Stats{Pairs: map[hxstore.Pair]int{
		{Class: 0x6c, Tag: 7}: 2, {Class: 0x6b, Tag: 0x456}: 1, {Class: 0x6b, Tag: 0x455}: 9, {Class: 0x6c, Tag: 0x348}: 4, {Class: 0x43, Tag: 5}: 80, {Class: 0, Tag: 0}: 17,
	}}
	got := unknownLayouts(st)
	if len(got) != 2 || got[0] != (PairCount{0x6b, 0x456, 1}) || got[1] != (PairCount{0x6c, 7, 2}) {
		t.Fatalf("%+v", got)
	}
}
