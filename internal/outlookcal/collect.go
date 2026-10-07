package outlookcal

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/ourostack/teamscrawl/internal/calendar"
	"github.com/ourostack/teamscrawl/internal/hxstore"
)

// The codes a refused or lossy read carries. The first three, when returned in a
// GuardError, turn the source off for the sync: nothing is applied.
const (
	CodeStoreUnrecognized = "outlook_store_unrecognized"
	CodeStoreVersion      = "outlook_store_version"
	CodeLayoutUnsupported = "outlook_layout_unsupported"
	CodeBlocksDamaged     = "outlook_blocks_damaged"
	CodeEventUnmapped     = "outlook_event_unmapped"
	detailNoEventObjects  = "no_event_objects"
	detailWalkCoverage    = "walk_coverage"
	minCoveragePercent    = 80
	damagedBlocksPercent  = 2
)

// GuardError is a refusal: the store is not one this reader can read, or it changed layout.
// Code is one of the Code constants and Detail holds numbers and format names only, never
// text from the store.
type GuardError struct {
	Code   string
	Detail string
}

func (e *GuardError) Error() string {
	if e.Detail == "" {
		return e.Code
	}
	return e.Code + ": " + e.Detail
}

// OpenStore opens the store and maps the container's errors to coded refusals: a file that
// is not an HxStore, a version byte or a page size that is not the known one. Other errors
// (a read failure) are returned as they are.
func OpenStore(r io.ReaderAt, size int64) (*hxstore.Store, error) {
	s, err := hxstore.Open(r, size)
	var ev hxstore.ErrStoreVersion
	var ep hxstore.ErrPageSize
	switch {
	case err == nil:
		return s, nil
	case errors.Is(err, hxstore.ErrNotHxStore):
		return nil, &GuardError{Code: CodeStoreUnrecognized}
	case errors.As(err, &ev):
		return nil, &GuardError{Code: CodeStoreVersion, Detail: fmt.Sprintf("version byte 0x%02x, known %s", ev.Found, knownVersions())}
	case errors.As(err, &ep):
		return nil, &GuardError{Code: CodeStoreVersion, Detail: fmt.Sprintf("page size %d, known %d", ep.Found, hxstore.KnownPageSize)}
	}
	return nil, err
}

func knownVersions() string {
	var parts []string
	for _, v := range hxstore.KnownStoreVersions {
		parts = append(parts, fmt.Sprintf("0x%02x", v))
	}
	return strings.Join(parts, ",")
}

// Options tunes Collect.
type Options struct {
	// Rule chooses the current copy of an event; empty means CurrentVersionRule.
	Rule VersionRule
	// ExpectEvents is set when the last successful read of this store found events: a
	// store that now holds none is then refused, so a change to the envelope itself cannot
	// read as an empty calendar.
	ExpectEvents bool
}

// Loss is one counted loss code.
type Loss struct {
	Code  string
	Count int
}

// PairCount is a count of objects of one (class, tag) the reader does not know.
type PairCount struct {
	Class, Tag uint16
	Count      int
}

// Notes counts what the read did, numbers only. Nothing in it is text from the store
// except the names of unmapped zones, which are public Windows ids.
type Notes struct {
	EventObjects      int // class 0x6b objects with the known tag and an id
	DistinctEvents    int // ids those objects carry
	SupersededCopies  int // EventObjects - DistinctEvents: older copies the version rule dropped
	EventNoID         int // id-less stubs, skipped
	ResyncedSkipped   int // event and detail objects reached after unknown bytes, skipped
	DetailObjects     int
	DetailMissing     int // events whose detail object was not found
	DetailUnreadable  int
	AttendeesUnparsed int
	AttendeesAtCap    int
	AllDayUnaligned   int // all-day flag set but not whole days: the flag is left unknown
	// Cancelled-flag events (+1082 bit 4, numbering unverified) by whether the bare
	// subject (+876) and the subject (+1024) differ or match.
	CancelledSubjectsDiffer, CancelledSubjectsMatch int
	OccurrenceNoDate                                int // occurrence or exception events whose id embeds no date
	SeriesKeySplits                                 int // series keys (+20) whose events split into more than one series by id
	SeriesGroupSplits                               int // series by id whose events carry more than one series key (+20)
	AccountObjects                                  int // class 0x49 objects that carry an address
	AccountAddresses                                int // distinct addresses they carry
	DetailCopiesDiffer                              int // detail keys with more than one differing copy
	BodyNULTrimmed                                  int // detail objects (by detail key) whose body ended in a NUL, removed
	UnknownZonesRejected                            int // events whose unresolved zone name is not printable ASCII of at most MaxZoneNameLen
	// Unparsed attendee lists by cause; they sum to AttendeesUnparsed.
	AttendeesEndMismatch, AttendeesCountZero, AttendeesOtherUnparsed int
	// AttendeeFailures counts every unparsed list under one fixed cause name: end_mismatch,
	// count_zero, bare_string_end, count_outside, count_too_big, length_missing,
	// length_odd, text_outside, words_cut. The values sum to AttendeesUnparsed.
	AttendeeFailures map[string]int
	EventTypeUnknown int
	ShowAsUnmapped   int
	ResponseUnmapped int
	Redacted         int
	UnmappedReasons  map[string]int
	UnknownZones     []string // sorted, distinct, at most MaxUnknownZones
	UnknownZonesOver int      // distinct names past MaxUnknownZones, dropped
}

// Result is what Collect read.
type Result struct {
	Events []calendar.Event // the current copy of each event, sorted by SourceID
	// AccountAddresses are the addresses of the accounts signed in to the profile, lower case, distinct
	// and sorted: the addresses its account objects carry. They link the profile to the Teams account
	// that has one of them.
	AccountAddresses []string
	Notes            Notes
	Losses           []Loss
	UnknownLayouts   []PairCount // pairs of a mapped class (event, detail) whose tag KnownLayouts does not list
	Stats            hxstore.Stats
}

type winner struct {
	key copyKey
	obj hxstore.Object
}

// Collect reads every event in the store, keeps the current copy of each under the version
// rule, links each to its detail object and maps it. It refuses (a *GuardError, and no
// events) when the store's layout has moved: any class 0x6b object with a tag other than
// the pinned one, no event objects in a store that had some, or an object walk that covers
// under 80% of the payload bytes. A torn or invalid block is a counted loss, not a refusal.
func Collect(ctx context.Context, s *hxstore.Store, account string, opt Options) (Result, error) {
	rule := opt.Rule
	if rule == "" {
		rule = CurrentVersionRule
	}
	var res Result
	winners := map[string]winner{}
	details := map[uint32]hxstore.Object{}
	differing := map[uint32]bool{}
	badTags := map[uint16]int{}
	addresses := map[string]bool{}
	stats, err := s.Walk(ctx, hxstore.WalkOptions{}, func(o hxstore.Object) error {
		switch {
		case o.Class == classEvent && o.Tag != tagEvent:
			badTags[o.Tag]++
		case o.Class == classEvent:
			switch {
			case o.Resynced:
				res.Notes.ResyncedSkipped++
			case !HasID(o):
				res.Notes.EventNoID++
			default:
				keepEvent(winners, o, rule, &res.Notes)
			}
		case o.Class == classAccount && o.Tag == tagAccount:
			// A resynced object is taken too: the account objects of a real store all are, and the
			// two matching copies of a well-formed address are what vouch for the record.
			if a, ok := AccountAddress(o); ok {
				res.Notes.AccountObjects++
				addresses[a] = true
			}
		case o.Class == classDetail && o.Tag == tagDetail:
			if o.Resynced {
				res.Notes.ResyncedSkipped++
			} else if k, ok := DetailKey(o); ok {
				res.Notes.DetailObjects++
				if old, held := details[k]; held && !bytes.Equal(old.Raw, o.Raw) {
					differing[k] = true
				}
				details[k] = o.Clone() // the last copy in file order
			}
		}
		return nil
	})
	res.Stats = stats
	if err != nil {
		return res, err
	}
	progress(0)
	res.UnknownLayouts = unknownLayouts(stats) // before the guard, so a refused read still says which layout it met
	if g := guard(stats, badTags, len(winners)+res.Notes.EventNoID, opt); g != nil {
		return res, g
	}
	res.Notes.AccountAddresses = len(addresses)
	for a := range addresses {
		res.AccountAddresses = append(res.AccountAddresses, a)
	}
	sort.Strings(res.AccountAddresses)
	res.Notes.DetailCopiesDiffer = len(differing)
	res.Notes.DistinctEvents = len(winners)
	res.Notes.SupersededCopies = res.Notes.EventObjects - len(winners)
	if rej := stats.BlocksRejected(); rej*100 > stats.BlocksFound*damagedBlocksPercent {
		res.Losses = append(res.Losses, Loss{CodeBlocksDamaged, rej})
	}
	mapAll(&res, winners, details, account)
	return res, nil
}

// progress is called with 0 once the walk has finished and with the count of events handled
// after each event is mapped. It is a seam for a test that measures the live heap.
var progress = func(int) {}

// keepEvent records o if it is the first copy of its id or beats the copy held.
func keepEvent(winners map[string]winner, o hxstore.Object, rule VersionRule, n *Notes) {
	off, _ := o.U32(evID)
	size, _ := o.U32(evID + 4)
	id, ok := o.Bytes(evFixed+int(off), int(size))
	if !ok {
		id = nil // an id outside the object: the copy still competes, and fails to map
	}
	n.EventObjects++
	lm, _ := o.U64(evLastMod)
	stamp, _ := o.U64(112)
	k := copyKey{lastMod: lm, stamp: stamp, block: o.BlockOffset, pos: o.PayloadPos}
	name := string(id)
	if old, held := winners[name]; held && !rule.newer(k, old.key) {
		return
	}
	winners[name] = winner{key: k, obj: o.Clone()}
}

// guard decides whether the read must be refused. events counts the recognized event
// objects with and without ids.
func guard(st hxstore.Stats, badTags map[uint16]int, events int, opt Options) error {
	if len(badTags) > 0 {
		tags := make([]uint16, 0, len(badTags))
		for t := range badTags {
			tags = append(tags, t)
		}
		sort.Slice(tags, func(i, j int) bool { return tags[i] < tags[j] })
		var parts []string
		for _, t := range tags {
			parts = append(parts, fmt.Sprintf("class 0x%x tag 0x%x x%d, known 0x%x", classEvent, t, badTags[t], tagEvent))
		}
		return &GuardError{Code: CodeLayoutUnsupported, Detail: strings.Join(parts, "; ")}
	}
	if opt.ExpectEvents && events == 0 {
		return &GuardError{Code: CodeLayoutUnsupported, Detail: detailNoEventObjects}
	}
	if st.PayloadBytes > 0 && (st.PayloadBytes-st.UnwalkedBytes)*100 < st.PayloadBytes*minCoveragePercent {
		return &GuardError{Code: CodeLayoutUnsupported, Detail: detailWalkCoverage}
	}
	return nil
}

// unknownLayouts lists the (class, tag) pairs of the classes the reader maps (event and detail)
// whose tag it does not know: the sign that Outlook changed a layout. Objects of any other class
// are not calendar objects and are not reported.
func unknownLayouts(st hxstore.Stats) []PairCount {
	var out []PairCount
	for p, n := range st.Pairs {
		mapped, known := false, false
		for _, l := range KnownLayouts {
			mapped = mapped || l.Class == p.Class
			known = known || (l.Class == p.Class && l.Tag == p.Tag)
		}
		if mapped && !known {
			out = append(out, PairCount{p.Class, p.Tag, n})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Class != out[j].Class {
			return out[i].Class < out[j].Class
		}
		return out[i].Tag < out[j].Tag
	})
	return out
}

// mapAll maps the winners in id order and folds the per-event notes into the result.
func mapAll(res *Result, winners map[string]winner, details map[uint32]hxstore.Object, account string) {
	ids := make([]string, 0, len(winners))
	for id := range winners {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	n := &res.Notes
	// Count the winners that link each detail object, and drop the detail objects no winner
	// links, so each detail's bytes can be freed with the last event that reads it.
	refs := map[uint32]int{}
	for _, w := range winners {
		if link, ok := detailLink(w.obj); ok {
			refs[link]++
		}
	}
	for k := range details {
		if refs[k] == 0 {
			delete(details, k)
		}
	}
	zones := map[string]bool{}
	nulKeys := map[uint32]bool{}
	keySeries := map[uint64]map[string]bool{}
	seriesKeys := map[string]map[uint64]bool{}
	unmapped := 0
	for i, id := range ids {
		ev := winners[id].obj
		delete(winners, id) // ev holds the bytes now; the map must not keep them past this event
		var d *hxstore.Object
		link, hasLink := detailLink(ev)
		if hasLink {
			if obj, found := details[link]; found {
				d = &obj
			}
		}
		e, mn, err := MapEvent(account, ev, d)
		if hasLink {
			if refs[link]--; refs[link] == 0 {
				delete(details, link) // the last event that reads this detail object
			}
		}
		progress(i + 1)
		if err != nil {
			unmapped++
			if n.UnmappedReasons == nil {
				n.UnmappedReasons = map[string]int{}
			}
			var ue *UnmappedError
			_ = errors.As(err, &ue) // MapEvent returns only *UnmappedError
			n.UnmappedReasons[ue.Reason]++
			continue
		}
		res.Events = append(res.Events, e)
		n.add(mn, zones)
		if mn.BodyNULTrimmed && !nulKeys[link] {
			nulKeys[link] = true // events sharing one detail object count once
			n.BodyNULTrimmed++
		}
		link2(keySeries, mn.SeriesWord, mn.SeriesID)
		link2(seriesKeys, mn.SeriesID, mn.SeriesWord)
	}
	n.SeriesKeySplits, n.SeriesGroupSplits = multi(keySeries), multi(seriesKeys)
	if unmapped > 0 {
		res.Losses = append(res.Losses, Loss{CodeEventUnmapped, unmapped})
	}
	for z := range zones {
		n.UnknownZones = append(n.UnknownZones, z)
	}
	sort.Strings(n.UnknownZones)
	if len(n.UnknownZones) > MaxUnknownZones {
		n.UnknownZonesOver = len(n.UnknownZones) - MaxUnknownZones
		n.UnknownZones = n.UnknownZones[:MaxUnknownZones]
	}
}

// MaxUnknownZones caps the distinct unresolved zone names Notes keeps.
const MaxUnknownZones = 32

// link2 records that a belongs with b.
func link2[A, B comparable](m map[A]map[B]bool, a A, b B) {
	if m[a] == nil {
		m[a] = map[B]bool{}
	}
	m[a][b] = true
}

// multi counts the entries that hold more than one value.
func multi[A, B comparable](m map[A]map[B]bool) int {
	n := 0
	for _, v := range m {
		if len(v) > 1 {
			n++
		}
	}
	return n
}

func (n *Notes) add(m MapNotes, zones map[string]bool) {
	count := func(c *int, on bool) {
		if on {
			*c++
		}
	}
	count(&n.DetailMissing, m.DetailMissing)
	count(&n.DetailUnreadable, m.DetailUnreadable)
	count(&n.AttendeesUnparsed, m.AttendeesUnparsed)
	count(&n.AttendeesAtCap, m.AttendeesAtCap)
	count(&n.AllDayUnaligned, m.AllDayUnaligned)
	count(&n.CancelledSubjectsDiffer, m.CancelledSubjectsDiffer)
	count(&n.CancelledSubjectsMatch, m.CancelledSubjectsMatch)
	count(&n.OccurrenceNoDate, m.OccurrenceNoDate)
	count(&n.UnknownZonesRejected, m.UnknownZoneRejected)
	count(&n.AttendeesEndMismatch, m.AttendeesEndMismatch)
	count(&n.AttendeesCountZero, m.AttendeesCountZero)
	count(&n.AttendeesOtherUnparsed, m.AttendeesOtherUnparsed)
	if m.AttendeeFailure != "" {
		if n.AttendeeFailures == nil {
			n.AttendeeFailures = map[string]int{}
		}
		n.AttendeeFailures[m.AttendeeFailure]++
	}
	count(&n.EventTypeUnknown, m.EventTypeUnknown)
	count(&n.ShowAsUnmapped, m.ShowAsUnmapped)
	count(&n.ResponseUnmapped, m.ResponseUnmapped)
	n.Redacted += m.Redacted
	if m.UnknownZone != "" {
		zones[m.UnknownZone] = true
	}
}
