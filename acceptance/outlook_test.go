//go:build acceptance

package acceptance

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/calendar"
	"github.com/ourostack/m365crawl/internal/hxstore"
	"github.com/ourostack/m365crawl/internal/outlookcal"
	"github.com/ourostack/m365crawl/internal/outlookdesktop"
	"github.com/ourostack/m365crawl/internal/syncer"
)

// The Outlook real-store checks (plan-calendar-outlook.md, O12). They read the store under
// M365CRAWL_OUTLOOK_ROOT (<root>/<profile>/HxStore.hxd) and skip without it. They log counts,
// field names, class and tag numbers, durations and sizes only: never a string from the store.
// Every Outlook check starts with outlookSyncs, so the syncs and their peak RSS come first. The peak
// RSS is the whole process's, so make acceptance-calendar runs TestRealOutlookCost in a process of
// its own: in a shared process the other checks' in-process store scans set the peak first.
//
// Fail: the container and guard counts, the twin rate, the unwalked-byte ids, the cost budgets, the
// safety rules and the append-only rule. Report: agreement counts, histograms, E15, the watch
// simulation, covered days, OccurrenceID and zone agreement.

// scanned is one private copy of the store read once with outlookcal.Collect, shared by the
// container, version and cost checks.
type scanned struct {
	info     outlookdesktop.Info
	copyWall time.Duration
	scanWall time.Duration
	res      outlookcal.Result
	err      string
}

var scanOnce struct {
	done bool
	s    scanned
}

func scanStore(t *testing.T, d outlookRunData) scanned {
	t.Helper()
	if scanOnce.done {
		return scanOnce.s
	}
	scanOnce.done = true
	s := &scanOnce.s
	start := time.Now()
	info, cleanup, err := outlookdesktop.Snapshot(context.Background(), d.profile.StorePath)
	calMu.Lock()
	calCleanups = append(calCleanups, cleanup)
	calMu.Unlock()
	if err != nil {
		s.err = "copy failed with code " + errCode(err)
		return *s
	}
	s.info, s.copyWall = info, time.Since(start)
	f, err := os.Open(info.Path)
	if err != nil {
		s.err = "open of the copy failed"
		return *s
	}
	defer func() { _ = f.Close() }()
	start = time.Now()
	hs, err := outlookcal.OpenStore(f, info.Size)
	if err == nil {
		s.res, err = outlookcal.Collect(context.Background(), hs, "acceptance", outlookcal.Options{})
	}
	s.scanWall = time.Since(start)
	if err != nil {
		var g *outlookcal.GuardError
		if errors.As(err, &g) {
			s.err = "the guard refused the store: " + g.Code
		} else {
			s.err = "scan failed"
		}
	}
	return *s
}

func requireScan(t *testing.T, d outlookRunData) scanned {
	t.Helper()
	s := scanStore(t, d)
	if s.err != "" {
		t.Fatal(s.err)
	}
	return s
}

// (1) The container and guard counts.
func TestRealOutlookContainer(t *testing.T) {
	d := outlookSyncs(t)
	s := requireScan(t, d)
	c, st := calendarThresholds, s.res.Stats
	invalid := ratio(st.BlocksRejected(), st.BlocksFound)
	t.Logf("blocks found=%d valid=%d rejected=%d (%s) invalid ratio %.3f%%", st.BlocksFound, st.BlocksValid, st.BlocksRejected(), countsLine(st.Rejected), 100*invalid)
	if invalid > c.InvalidBlockRatioMax {
		t.Errorf("invalid block ratio %.3f%% is above %.3f%%", 100*invalid, 100*c.InvalidBlockRatioMax)
	}

	perClass := map[uint16]int{}
	for p, n := range st.Pairs {
		perClass[p.Class] += n
	}
	type cn struct {
		class uint16
		n     int
	}
	var ranked []cn
	for k, n := range perClass {
		ranked = append(ranked, cn{k, n})
	}
	sort.Slice(ranked, func(i, j int) bool {
		return ranked[i].n > ranked[j].n || ranked[i].n == ranked[j].n && ranked[i].class < ranked[j].class
	})
	t.Logf("objects=%d (spike %d); the five largest classes:", st.Objects, c.SpikeObjects)
	for i := 0; i < len(ranked) && i < 5; i++ {
		if want, ok := c.SpikeClassCounts[ranked[i].class]; ok {
			t.Logf("  class 0x%x: %d (spike %d, difference %+d)", ranked[i].class, ranked[i].n, want, ranked[i].n-want)
		} else {
			t.Logf("  class 0x%x: %d (no spike count recorded in the plan)", ranked[i].class, ranked[i].n)
		}
	}
	for class, want := range c.SpikeClassCounts {
		if _, ranged := c.ClassCountRanges[class]; ranged {
			continue
		}
		if got := perClass[class]; !within(got, want, c.ClassCountTolerance) {
			t.Errorf("class 0x%x has %d objects, not within %.2f%% of the spike's %d", class, got, 100*c.ClassCountTolerance, want)
		}
	}

	for class, r := range c.ClassCountRanges {
		if got := perClass[class]; got < r[0] || got > r[1] {
			t.Errorf("class 0x%x has %d objects, outside the range %d to %d", class, got, r[0], r[1])
		}
	}
	if headers := perClass[0x4f]; headers > 0 {
		per := float64(perClass[0x55]) / float64(headers)
		t.Logf("class 0x55 per class 0x4f header: %.1f (want %.0f to %.0f)", per, c.Class55PerHeaderRange[0], c.Class55PerHeaderRange[1])
		if per < c.Class55PerHeaderRange[0] || per > c.Class55PerHeaderRange[1] {
			t.Errorf("class 0x55 holds %.1f objects per header, outside %.0f to %.0f", per, c.Class55PerHeaderRange[0], c.Class55PerHeaderRange[1])
		}
	}

	notObject := ratio(int(st.UnwalkedBytes+st.FramingBytes), int(st.PayloadBytes))
	t.Logf("payload bytes=%d unwalked=%d framing=%d: not covered by an object %.2f%% (want %.0f%% +/- %.0f points)",
		st.PayloadBytes, st.UnwalkedBytes, st.FramingBytes, 100*notObject, 100*c.UnwalkedFractionWant, 100*c.UnwalkedFractionBand)
	if diff := notObject - c.UnwalkedFractionWant; diff > c.UnwalkedFractionBand || -diff > c.UnwalkedFractionBand {
		t.Errorf("the unwalked share %.2f%% is outside %.0f%% +/- %.0f points", 100*notObject, 100*c.UnwalkedFractionWant, 100*c.UnwalkedFractionBand)
	}

	t.Logf("unknown layouts: %d; objects resynced %d; pairs overflow %d", len(s.res.UnknownLayouts), st.ObjectsResynced, st.PairsOverflow)
	for _, u := range s.res.UnknownLayouts {
		t.Logf("  unknown layout class 0x%x tag 0x%x: %d objects", u.Class, u.Tag, u.Count)
	}
	if len(s.res.UnknownLayouts) != 0 {
		t.Errorf("unknown_layouts holds %d (class, tag) pairs, want none", len(s.res.UnknownLayouts))
	}
	n := s.res.Notes
	t.Logf("events: objects=%d distinct=%d superseded copies=%d id-less=%d resynced skipped=%d; detail objects=%d missing=%d; losses %s",
		n.EventObjects, n.DistinctEvents, n.SupersededCopies, n.EventNoID, n.ResyncedSkipped, n.DetailObjects, n.DetailMissing, lossLine(s.res.Losses))
}

func lossLine(ls []outlookcal.Loss) string {
	m := map[string]int{}
	for _, l := range ls {
		m[l.Code] += l.Count
	}
	return countsLine(m)
}

// sidOf is the series an event id belongs to: its series id when the id embeds a date, else the
// series key, else the id itself.
func sidOf(key, seriesKey string) string {
	id, _, _ := strings.Cut(key, "|")
	if series, _, ok := calendar.SplitOccurrenceID(id); ok {
		return strings.ToLower(series)
	}
	if seriesKey != "" {
		return strings.ToLower(seriesKey)
	}
	return strings.ToLower(key)
}

// twinPair is a Teams event and the Outlook event under the same key.
type twinPair struct{ tm, om [7]string } // start, end, response, event type, zone, series key, last modified

type twinData struct {
	teamsLive, teamsSeries int
	twinEvents             int
	twinSeries             int
	// newerEvents and newerSeries count the Teams events (and series made only of such events)
	// with no twin that were last modified after the newest Outlook last-modified: snapshot skew.
	// They are left out of teamsLive and teamsSeries, the gated denominators.
	newerEvents, newerSeries int
	pairs                    []twinPair
	linked                   bool
}

func loadTwins(t *testing.T, d outlookRunData) twinData {
	t.Helper()
	st := openArchive(t, calendarArchive(t).db)
	var out twinData
	acct := strings.ReplaceAll(d.account, "'", "''")
	out.linked = count(t, st, `select count(*) from calendar_account_links where source='outlook' and unlinked_at is null and principal_id='`+acct+`'`) > 0
	oKeys, oSeries := map[string]bool{}, map[string]bool{}
	var newestOutlook time.Time
	for _, r := range rowsOf(t, st, `select event_key, series_key, coalesce(last_modified,'') from calendar_source_events where source='outlook' and removed_at is null`) {
		oKeys[asString(r[0])] = true
		oSeries[sidOf(asString(r[0]), asString(r[1]))] = true
		if lm, ok := parseTime(asString(r[2])); ok && lm.After(newestOutlook) {
			newestOutlook = lm
		}
	}
	tSeries := map[string]bool{}
	seriesEvents, seriesNewer := map[string]int{}, map[string]int{}
	const cols = `start_at, end_at, response, event_type, time_zone_iana, series_key, coalesce(last_modified,'')`
	for _, r := range rowsOf(t, st, `select event_key, `+cols+` from calendar_source_events where source='teams' and account_id='`+acct+`' and removed_at is null and event_type<>'master'`) {
		key, sid := asString(r[0]), sidOf(asString(r[0]), asString(r[6]))
		lm, _ := parseTime(asString(r[7]))
		seriesEvents[sid]++
		switch {
		case oKeys[key]:
			out.twinEvents++
			out.teamsLive++
		case newerThanCopy(lm, newestOutlook):
			out.newerEvents++
			seriesNewer[sid]++
		default:
			out.teamsLive++
		}
		tSeries[sid] = true
	}
	for sid := range tSeries {
		switch {
		case oSeries[sid]:
			out.twinSeries++
			out.teamsSeries++
		case seriesNewer[sid] == seriesEvents[sid]:
			out.newerSeries++
		default:
			out.teamsSeries++
		}
	}
	// The pairs, for the agreement counts: one Outlook row per Teams event (the first by account).
	for _, r := range rowsOf(t, st, `select t.start_at, t.end_at, t.response, t.event_type, t.time_zone_iana, t.series_key, coalesce(t.last_modified,''),
	    o.start_at, o.end_at, o.response, o.event_type, o.time_zone_iana, o.series_key, coalesce(o.last_modified,'')
	  from calendar_source_events t join calendar_source_events o on o.source='outlook' and o.removed_at is null and o.event_key=t.event_key
	  where t.source='teams' and t.account_id='`+acct+`' and t.removed_at is null and t.event_type<>'master' group by t.event_key`) {
		var p twinPair
		for i := range 7 {
			p.tm[i], p.om[i] = asString(r[i]), asString(r[7+i])
		}
		out.pairs = append(out.pairs, p)
	}
	return out
}

func sameInstant(a, b string) (equal, comparable bool) {
	ta, ok1 := parseTime(a)
	tb, ok2 := parseTime(b)
	return ta.Equal(tb), ok1 && ok2
}

// (2) The twin rate, with agreement counts and the last-modified histogram.
func TestRealOutlookTwinRate(t *testing.T) {
	d := outlookSyncs(t)
	c := calendarThresholds
	tw := loadTwins(t, d)
	t.Logf("link of the Outlook profile to the Teams account is active: %v", tw.linked)
	if !tw.linked {
		t.Error("the Outlook profile is not linked to the Teams account (PR 85: --outlook-account), so the twin rate is not what the merged agenda shows")
	}
	if tw.teamsLive == 0 {
		t.Fatal("the Teams account has no live non-master events")
	}
	t.Logf("Teams events newer than the Outlook copy (last_modified after its newest, no twin; left out of the denominators): events %d, series %d", tw.newerEvents, tw.newerSeries)
	evRate, serRate := ratio(tw.twinEvents, tw.teamsLive), ratio(tw.twinSeries, tw.teamsSeries)
	t.Logf("twin rate, events: %d of %d (%.1f%%; spike %d of %d)", tw.twinEvents, tw.teamsLive, 100*evRate, c.SpikeTwinEvents, c.SpikeTwinEvents)
	t.Logf("twin rate, series: %d of %d (%.1f%%; spike %d of %d)", tw.twinSeries, tw.teamsSeries, 100*serRate, c.SpikeTwinSeries, c.SpikeTwinSeries)
	if evRate < c.TwinRateMin {
		t.Errorf("event twin rate %.1f%% is below %.1f%%", 100*evRate, 100*c.TwinRateMin)
	}
	if serRate < c.TwinSeriesRateMin {
		t.Errorf("series twin rate %.1f%% is below %.1f%%", 100*serRate, 100*c.TwinSeriesRateMin)
	}

	hist := newHistogram(lastModifiedEdges, lastModifiedOver)
	agree := map[string][2]int{} // field -> agreeing, comparable
	scheduleInside := 0
	field := func(name string, equal, comparable bool) {
		a := agree[name]
		if comparable {
			a[1]++
			if equal {
				a[0]++
			}
		}
		agree[name] = a
	}
	for _, p := range tw.pairs {
		se, sc := sameInstant(p.tm[0], p.om[0])
		ee, ec := sameInstant(p.tm[1], p.om[1])
		field("start", se, sc)
		field("end", ee, ec)
		field("response", p.tm[2] == p.om[2], p.tm[2] != "" && p.om[2] != "")
		field("event_type", p.tm[3] == p.om[3], p.tm[3] != "" && p.om[3] != "")
		field("zone", p.tm[4] == p.om[4], p.tm[4] != "" && p.om[4] != "")
		field("series_key", strings.EqualFold(p.tm[5], p.om[5]), p.tm[5] != "" && p.om[5] != "")
		lt, ok1 := parseTime(p.tm[6])
		lo, ok2 := parseTime(p.om[6])
		if ok1 && ok2 {
			diff := absDuration(lt.Sub(lo))
			hist.add(bucketOf(diff, lastModifiedEdges, lastModifiedOver))
			if ((sc && !se) || (ec && !ee)) && diff <= c.ScheduleWindow {
				scheduleInside++
			}
		}
	}
	names := make([]string, 0, len(agree))
	for k := range agree {
		names = append(names, k)
	}
	slices.Sort(names)
	for _, k := range names {
		t.Logf("agreement %-10s %d of %d twin pairs comparable", k, agree[k][0], agree[k][1])
	}
	t.Logf("last-modified difference of twin pairs (%d): %s", hist.total(), hist)
	t.Logf("schedule disagreements with last-modified within %v: %d", c.ScheduleWindow, scheduleInside)
}

// copyInfo is one stored copy of an event object, for the version-rule checks.
type copyInfo struct {
	lastMod, stamp uint64
	block          int64
	pos            int
}

// The event layout offsets the version checks read; they mirror outlookcal's pinned layout
// (docs/outlook-store.md) and change with it.
const (
	evClass, evTag = 0x6b, 0x455
	evFixed        = 1109
	offStamp       = 112
	offLastMod     = 288
	offID          = 820
)

func hexText(b []byte) bool {
	for _, c := range b {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return len(b) > 0
}

// idTexts finds, in raw bytes, every UTF-16LE hex string that starts with the Exchange class id
// and is at least 40 characters long, lower-cased. It reads text only to compare it: the strings
// stay in memory and are never logged.
func idTexts(raw []byte, prefix []byte, into map[string]bool) {
	for from := 0; from < len(raw); {
		i := bytes.Index(raw[from:], prefix)
		if i < 0 {
			return
		}
		at := from + i
		var id []byte
		for j := at; j+1 < len(raw) && raw[j+1] == 0; j += 2 {
			if !hexText(raw[j : j+1]) {
				break
			}
			id = append(id, raw[j])
		}
		if len(id) >= 40 {
			into[strings.ToLower(string(id))] = true
		}
		from = at + len(prefix)
	}
}

func utf16Of(s string) []byte {
	out := make([]byte, 0, 2*len(s))
	for i := range len(s) {
		out = append(out, s[i], 0)
	}
	return out
}

// payloadsOf calls fn with the inflated payload of every valid block of the store file, found by
// scanning for the block magic with a sliding window: the same blocks Walk reads, but all of their
// bytes, including the ones no object covers.
func payloadsOf(path string, fn func(payload []byte)) (blocks int, err error) {
	f, err := os.Open(path) //nolint:gosec // path is the private copy this run made
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return 0, err
	}
	magic := []byte{0x05, 0x6a, 0x70, 0x3b, 0x64, 0x45, 0x02, 0x5d}
	const magicAt, window = 8, 4 << 20
	buf := make([]byte, window)
	var inflated, block []byte
	for from := int64(0); from < fi.Size(); {
		n, rerr := f.ReadAt(buf, from)
		if rerr != nil && !errors.Is(rerr, io.EOF) {
			return blocks, rerr
		}
		chunk := buf[:n]
		i := bytes.Index(chunk, magic)
		if i < 0 {
			if n < len(buf) {
				return blocks, nil
			}
			from += int64(n - len(magic) + 1)
			continue
		}
		start := from + int64(i) - magicAt
		from += int64(i) + int64(len(magic))
		if start < 0 || start+hxstore.HeaderSize > fi.Size() {
			continue
		}
		var hdr [hxstore.HeaderSize]byte
		if _, err := f.ReadAt(hdr[:], start); err != nil {
			continue
		}
		payloadLen := int64(binary.LittleEndian.Uint32(hdr[0x14:]))
		if payloadLen <= 0 || start+hxstore.HeaderSize+payloadLen > fi.Size() || payloadLen > hxstore.MaxInflated {
			continue
		}
		if cap(block) < int(hxstore.HeaderSize+payloadLen) {
			block = make([]byte, hxstore.HeaderSize+payloadLen)
		}
		block = block[:hxstore.HeaderSize+payloadLen]
		if _, err := f.ReadAt(block, start); err != nil {
			continue
		}
		_, out, verr := hxstore.VerifyBlock(block, inflated, hxstore.MaxInflated)
		if verr != nil {
			continue
		}
		if cap(out) > cap(inflated) {
			inflated = out[:cap(out)]
		}
		blocks++
		fn(out)
		from = start + int64(len(block))
	}
	return blocks, nil
}

// (3) The version rule: E15 (reported) and E17 (event ids found only in unwalked bytes, expect 0).
func TestRealOutlookVersionRule(t *testing.T) {
	d := outlookSyncs(t)
	s := requireScan(t, d)
	c := calendarThresholds
	hs, err := hxstore.OpenFile(s.info.Path)
	if err != nil {
		t.Fatal("open of the copy failed")
	}
	defer func() { _ = hs.Close() }()

	prefix := utf16Of(c.EventClassPrefix)
	groups := map[string][]copyInfo{}
	walkedText := map[string]bool{}
	_, err = hs.Walk(context.Background(), hxstore.WalkOptions{}, func(o hxstore.Object) error {
		idTexts(o.Raw, prefix, walkedText)
		if o.Class != evClass || o.Tag != evTag || o.Resynced || !outlookcal.HasID(o) {
			return nil
		}
		off, _ := o.U32(offID)
		n, _ := o.U32(offID + 4)
		raw, ok := o.Bytes(evFixed+int(off), int(n))
		if !ok {
			return nil
		}
		lm, _ := o.U64(offLastMod)
		stamp, _ := o.U64(offStamp)
		key := string(bytes.ToLower(raw))
		groups[key] = append(groups[key], copyInfo{lm, stamp, o.BlockOffset, o.PayloadPos})
		return nil
	})
	if err != nil {
		t.Fatal("walk of the copy failed")
	}

	// E15: for every event with two or more copies, which copy each ordering would pick.
	pick := func(cs []copyInfo, less func(a, b copyInfo) bool) int {
		best := 0
		for i := 1; i < len(cs); i++ {
			if less(cs[best], cs[i]) {
				best = i
			}
		}
		return best
	}
	byLM := func(a, b copyInfo) bool {
		if a.lastMod != b.lastMod {
			return a.lastMod < b.lastMod
		}
		return a.stamp < b.stamp || a.stamp == b.stamp && (a.block < b.block || a.block == b.block && a.pos < b.pos)
	}
	byStamp := func(a, b copyInfo) bool {
		if a.stamp != b.stamp {
			return a.stamp < b.stamp
		}
		return byLM(a, b)
	}
	byFile := func(a, b copyInfo) bool { return a.block < b.block || a.block == b.block && a.pos < b.pos }
	multi, lmStamp, lmFile, stampFile := 0, 0, 0, 0
	for _, cs := range groups {
		if len(cs) < 2 {
			continue
		}
		multi++
		a, b, f := pick(cs, byLM), pick(cs, byStamp), pick(cs, byFile)
		if a != b {
			lmStamp++
		}
		if a != f {
			lmFile++
		}
		if b != f {
			stampFile++
		}
	}
	t.Logf("E15 (reported): %d event ids, %d with two or more copies; the copy picked by last-modified differs from the stamp's in %d, from the highest file position's in %d; stamp vs file position %d",
		len(groups), multi, lmStamp, lmFile, stampFile)
	t.Logf("version rule in use: %s", outlookcal.CurrentVersionRule)

	// How much the rule matters: collect again under the other rule and count events whose schedule differs.
	f, err := os.Open(s.info.Path)
	if err != nil {
		t.Fatal("open of the copy failed")
	}
	defer func() { _ = f.Close() }()
	other, err := outlookcal.OpenStore(f, s.info.Size)
	if err == nil {
		var alt outlookcal.Result
		if alt, err = outlookcal.Collect(context.Background(), other, "acceptance", outlookcal.Options{Rule: outlookcal.RuleStamp}); err == nil {
			cur := map[string]calendar.Event{}
			for _, e := range s.res.Events {
				cur[e.SourceID] = e
			}
			differ := 0
			for _, e := range alt.Events {
				if c0, ok := cur[e.SourceID]; ok && (!c0.Start.Equal(e.Start) || !c0.End.Equal(e.End)) {
					differ++
				}
			}
			t.Logf("events whose start or end differs between the last-modified rule and the stamp rule: %d of %d", differ, len(cur))
		}
	}

	// E17: event ids that appear only in bytes the walk does not cover.
	if len(walkedText) == 0 {
		t.Fatalf("no walked object holds an id that starts with the class prefix: EventClassPrefix is wrong for this store")
	}
	allText := map[string]bool{}
	blocks, err := payloadsOf(s.info.Path, func(p []byte) { idTexts(p, prefix, allText) })
	if err != nil {
		t.Fatal("scan of the copy failed")
	}
	onlyUnwalked := 0
	for id := range allText {
		if !walkedText[id] {
			onlyUnwalked++
		}
	}
	t.Logf("E17: %d blocks scanned; ids in all payload bytes %d, in walked objects %d, only in unwalked bytes %d (want at most %d)",
		blocks, len(allText), len(walkedText), onlyUnwalked, c.UnwalkedOnlyIDsMax)
	if onlyUnwalked > c.UnwalkedOnlyIDsMax {
		t.Errorf("%d event ids exist only in unwalked bytes: newer copies may be missed, so the source must not be enabled by default", onlyUnwalked)
	}
}

// watchReads simulates a watch that re-reads at most once per interval and only when the store
// changed since its last read, sampling the original's size and modification time (a stat; nothing
// is opened).
func watchReads(path string, window, sample, interval time.Duration) (changes, reads int, err error) {
	stamp := func() (int64, int64, error) {
		fi, err := os.Stat(path)
		if err != nil {
			return 0, 0, err
		}
		return fi.Size(), fi.ModTime().UnixNano(), nil
	}
	size, mod, err := stamp()
	if err != nil {
		return 0, 0, err
	}
	lastSeenSize, lastSeenMod := size, mod
	readSize, readMod := size, mod
	lastRead := time.Now()
	start := lastRead
	for time.Since(start) < window {
		time.Sleep(sample)
		s, m, err := stamp()
		if err != nil {
			return changes, reads, err
		}
		if s != lastSeenSize || m != lastSeenMod {
			changes++
			lastSeenSize, lastSeenMod = s, m
		}
		if (s != readSize || m != readMod) && time.Since(lastRead) >= interval {
			reads++
			readSize, readMod, lastRead = s, m, time.Now()
		}
	}
	return changes, reads, nil
}

// (4) Cost: budgets fail, the rest is reported.
func TestRealOutlookCost(t *testing.T) {
	d := outlookSyncs(t)
	s := requireScan(t, d)
	c := calendarThresholds
	mb := func(n int64) string { return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20)) }

	t.Logf("store size %s (budget %s); bytes copied per read %s; extra disk while syncing about %s", mb(s.info.Size), mb(c.StoreSizeMax), mb(s.info.Size), mb(s.info.Size))
	if s.info.Size > c.StoreSizeMax {
		t.Errorf("store is %s, above the %s budget", mb(s.info.Size), mb(c.StoreSizeMax))
	}
	wall := s.copyWall + s.scanWall
	t.Logf("copy %v + scan %v = %v (budget %v); copy attempts %d", s.copyWall.Round(time.Millisecond), s.scanWall.Round(time.Millisecond), wall.Round(time.Millisecond), c.SnapshotWallMax, s.info.Attempts)
	if wall > c.SnapshotWallMax {
		t.Errorf("copy plus scan took %v, above the %v budget", wall.Round(time.Millisecond), c.SnapshotWallMax)
	}

	for _, m := range []struct {
		name string
		m    syncMeasure
	}{{"Teams-only sync", d.teams}, {"first Outlook sync (Teams unchanged)", d.first}, {"unchanged sync", d.again}, {"forced Outlook-only re-read", d.forced}} {
		share := 0.0
		if m.m.wall > 0 {
			share = 100 * float64(m.m.cpu) / float64(m.m.wall)
		}
		t.Logf("%-38s wall %v, cpu %v (%.0f%% of one core), peak RSS %s -> %s, outlook sources: %s",
			m.name, m.m.wall.Round(time.Millisecond), m.m.cpu.Round(time.Millisecond), share, mb(m.m.peakBefore), mb(m.m.peakAfter), countsLine(m.m.outlookStatuses()))
	}
	if d.forcedErr != "" {
		t.Logf("the forced Outlook-only re-read failed with code %s", d.forcedErr)
	}

	for _, m := range []struct {
		name string
		m    syncMeasure
	}{{"first Outlook sync", d.first}, {"unchanged sync", d.again}} {
		var parts []string
		for _, src := range m.m.rep.Sources {
			parts = append(parts, src.Source[:min(len(src.Source), 7)]+"="+src.Status)
		}
		t.Logf("%s: source statuses (a Teams source that is not unchanged was read again in this sync): %s", m.name, strings.Join(parts, ", "))
	}
	if d.first.ok {
		over := max(d.first.peakAfter-d.teams.peakAfter, 0)
		t.Logf("combined run, for the record and not checked: whole-run peak RSS %s over the Teams-only peak %s: +%s. It includes whatever the Teams pass of that sync did, and on macOS the pages Go frees stay resident", mb(d.first.peakAfter), mb(d.teams.peakAfter), mb(over))
	}
	switch {
	case d.own.failure != "":
		t.Errorf("the Outlook read's own cost could not be measured: %s", d.own.failure)
	case !d.own.ok:
		t.Log("peak RSS is not available on this platform; the memory budget is not checked")
	default:
		t.Logf("the Outlook read alone, each in a fresh process on a copy of the archive: peak RSS %s when it reads the store, %s for a fresh process that only opens the archive (Outlook off, no Teams root): +%s (budget +%s)", mb(d.own.first), mb(d.own.baseline), mb(d.own.over()), mb(c.RSSOverTeamsMax))
		if d.own.over() > c.RSSOverTeamsMax {
			t.Errorf("the Outlook read adds %s of peak RSS, above the %s budget", mb(d.own.over()), mb(c.RSSOverTeamsMax))
		}
	}

	t.Logf("OutlookMinReadInterval is %v", syncer.OutlookMinReadInterval)
	if os.Getenv(watchEnv) != "1" {
		t.Logf("watch simulation not run: set %s=1 to sample the original's size and mtime for %v and count the re-reads a watch would do", watchEnv, c.WatchWindow)
		return
	}
	changes, reads, err := watchReads(d.profile.StorePath, c.WatchWindow, c.WatchSample, syncer.OutlookMinReadInterval)
	if err != nil {
		t.Fatal("watch simulation: stat of the original failed")
	}
	t.Logf("watch simulation over %v: the store changed %d times between %v samples; a watch with a %v minimum interval would re-read %d times", c.WatchWindow, changes, c.WatchSample, syncer.OutlookMinReadInterval, reads)
}

// (5) Safety: the original is only read; nothing of ours is left behind.
func TestRealOutlookSafety(t *testing.T) {
	d := outlookSyncs(t)
	added, removed := addedRemoved(d.profileBefore, d.profileAfter)
	rootAdded, rootRemoved := addedRemoved(d.rootBefore, d.rootAfter)
	t.Logf("profile directory entries: %d before, %d after; added %d, removed %d (Outlook's own writes); root entries added %d, removed %d",
		len(d.profileBefore), len(d.profileAfter), len(added), len(removed), len(rootAdded), len(rootRemoved))
	for _, n := range append(append([]string{}, added...), rootAdded...) {
		if strings.HasPrefix(n, "m365crawl") {
			t.Errorf("an entry with our prefix appeared next to the original")
		}
	}
	if d.lockBefore.exists != d.lockAfter.exists {
		t.Logf("HxStore.lock existence changed: %v -> %v (Outlook's own, reported)", d.lockBefore.exists, d.lockAfter.exists)
	} else if d.lockAfter.exists {
		t.Logf("HxStore.lock modified during the run: %v (we never open it)", !d.lockBefore.info.ModTime().Equal(d.lockAfter.info.ModTime()))
	}
	if d.storeBefore.exists && d.storeAfter.exists {
		t.Logf("original still the same file: %v; mode unchanged: %v; size %d -> %d bytes", os.SameFile(d.storeBefore.info, d.storeAfter.info), d.storeBefore.info.Mode() == d.storeAfter.info.Mode(), d.storeBefore.info.Size(), d.storeAfter.info.Size())
		if d.storeBefore.info.Mode() != d.storeAfter.info.Mode() {
			t.Errorf("the original's mode changed")
		}
	} else {
		t.Errorf("the original store is missing (before %v, after %v)", d.storeBefore.exists, d.storeAfter.exists)
	}
	t.Logf("%s* directories in the temp directory: %d before, %d after", snapshotDirPrefix, d.snapsBefore, d.snapsAfter)
	if d.snapsAfter > d.snapsBefore {
		t.Errorf("%d snapshot directories were left behind", d.snapsAfter-d.snapsBefore)
	}
	t.Log("the original is opened O_RDONLY only: pinned by TestSnapshotOpensReadOnly and TestSnapshotNeverTouchesLock in internal/outlookdesktop; a real run cannot observe flags from outside")
}

// (6) Append-only: consecutive real reads leave the Outlook rows non-decreasing.
func TestRealOutlookAppendOnly(t *testing.T) {
	d := outlookSyncs(t)
	compare := func(name string, before, after rowState) {
		missing, changed := 0, 0
		for k, fs := range before {
			a, ok := after[k]
			if !ok {
				missing++
			} else if a != fs {
				changed++
			}
		}
		t.Logf("%s: outlook rows %d -> %d, vanished %d, first_seen_at changed %d", name, len(before), len(after), missing, changed)
		if len(after) < len(before) || missing != 0 || changed != 0 {
			t.Errorf("%s: Outlook rows are not append-only (%d -> %d, %d vanished, %d first_seen_at changed)", name, len(before), len(after), missing, changed)
		}
	}
	if len(d.rowsFirst) == 0 {
		t.Error("the first Outlook sync left no Outlook rows")
	}
	compare("unchanged sync", d.rowsFirst, d.rowsAgain)
	if d.forcedErr != "" {
		t.Errorf("the forced re-read failed with code %s, so there is no second real read to compare", d.forcedErr)
		return
	}
	compare("forced re-read", d.rowsAgain, d.rowsForced)
}

// Reporting only: covered-day span and histogram, OccurrenceID agreement for ids that embed a date,
// and zone agreement of the twin pairs.
func TestRealOutlookReportingOnly(t *testing.T) {
	d := outlookSyncs(t)
	st := openArchive(t, calendarArchive(t).db)

	now := time.Now().UTC()
	hist := newHistogram(ageEdges, ageOver)
	days, first, last := 0, time.Time{}, time.Time{}
	for _, r := range rowsOf(t, st, `select day from calendar_covered_days where source='outlook'`) {
		day, err := time.Parse("2006-01-02", asString(r[0]))
		if err != nil {
			continue
		}
		days++
		if first.IsZero() || day.Before(first) {
			first = day
		}
		if day.After(last) {
			last = day
		}
		hist.add(bucketOf(now.Sub(day), ageEdges, ageOver))
	}
	if days > 0 {
		t.Logf("covered days: %d, spanning %d days; by age: %s", days, int(last.Sub(first).Hours()/24)+1, hist)
	} else {
		t.Log("covered days: none")
	}

	var dated [][]any
	for _, r := range rowsOf(t, st, `select series_key, coalesce(original_start, start_at), time_zone_iana, substr(event_key,1,instr(event_key,'|')-1) from calendar_source_events
	  where source='outlook' and removed_at is null and series_key<>'' and event_type in ('`+calendar.EventOccurrence+`','`+calendar.EventException+`') and event_key not like 'composite|%'`) {
		if _, _, ok := calendar.SplitOccurrenceID(asString(r[3])); ok {
			dated = append(dated, r)
		}
	}
	occurrenceAgreement(dated).log(t, "Outlook ids that embed a date,")

	tw := loadTwins(t, d)
	same, both := 0, 0
	for _, p := range tw.pairs {
		if p.tm[4] != "" && p.om[4] != "" {
			both++
			if p.tm[4] == p.om[4] {
				same++
			}
		}
	}
	t.Logf("zone agreement of twin pairs: %d of %d with a resolved zone on both sides", same, both)
}
