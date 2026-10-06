package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/teamscrawl/internal/calendar"
	"github.com/ourostack/teamscrawl/internal/errs"
	"github.com/ourostack/teamscrawl/internal/teamsdesktop"
)

// sharedUID is the id three occurrences carry when Teams reports the series id on each of them.
const sharedUID = "uid-shared"

// sSeriesArchive holds three occurrences (Nov 3, 10 and 17 at 10:00 UTC) that share one iCalUID,
// the account's own name, and no recap yet.
func sSeriesArchive(t *testing.T) *Store {
	t.Helper()
	s := newStore(t)
	ctx := context.Background()
	if err := s.ApplyAccount(ctx, acctA); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyPeople(ctx, []teamsdesktop.Person{{TenantID: acctA.TenantID, ID: selfMRI(acctA), DisplayName: "Ada Fixture", SeenAt: base}}); err != nil {
		t.Fatal(err)
	}
	same := func(e *calendar.Event) { e.ICalUID = sharedUID }
	s.qApply(t, calendar.SourceTeams, qTeams, qEvent("s1", 3, same), qEvent("s2", 10, same), qEvent("s3", 17, same),
		qEvent("bare", 24, func(e *calendar.Event) { e.ICalUID = "" }))
	// A recap with no iCalUID belongs to no event, whatever its start.
	s.sRecap(t, "no-uid", "", "2026-11-24T10:00:00.000Z", "Ada Fixture")
	return s
}

// sRecap stores a recap linked to uid whose meeting started at start ("" for none), with one action
// item owned by owner.
func (s *Store) sRecap(t *testing.T, call, uid, start, owner string) {
	t.Helper()
	var ms any
	if start != "" {
		ms = start
	}
	s.qExec(t, `insert into calendar_recaps(account_id,call_id,ical_uid,recap_id,link_method,has_catchup,has_recap,headline,short_summary,outline,summary_sections_json,speakers_json,topics_json,recording_url,meeting_start_at,meeting_end_at,expires_at,first_seen_at,updated_at)
	  values(?,?,?,'','start_time',0,1,'H '||?,'','','','','','',?,null,'2026-12-31T00:00:00.000Z','2026-11-04T00:00:00.000Z','2026-11-04T00:00:00.000Z')`, qTeams, call, uid, call, ms)
	s.qExec(t, `insert into calendar_recap_items(account_id,call_id,item_key,kind,origin,title,text,owner_name,speaker_name,at,mentioned_by,highlights_json,ordinal,first_seen_at,updated_at,superseded_at)
	  values(?,?,'a1','action_item','recap','Item of '||?,'Do it','`+owner+`','Bo',null,'','',1,'2026-11-04T00:00:00.000Z','2026-11-04T00:00:00.000Z',null)`, qTeams, call, call)
}

// eventOn is the detail of the occurrence that starts on a day of November 2026.
func eventOn(t *testing.T, s *Store, id string, day int) CalendarDetail {
	t.Helper()
	d, err := s.CalendarEvent(context.Background(), nil, EventID(qTeams, calendar.Key(qEvent(id, day, func(e *calendar.Event) { e.ICalUID = sharedUID }))))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// A recap linked by start time to the id a whole series shares belongs to the one occurrence its
// meeting start matches, and to no other.
func TestSeriesRecapAttachesToTheOneMatchingOccurrence(t *testing.T) {
	s := sSeriesArchive(t)
	s.sRecap(t, "call-2", sharedUID, "2026-11-10T10:03:00.000Z", "Ada Fixture")
	for _, c := range []struct {
		id   string
		day  int
		want int
	}{{"s1", 3, 0}, {"s2", 10, 1}, {"s3", 17, 0}} {
		d := eventOn(t, s, c.id, c.day)
		if len(d.Recaps) != c.want || d.HasRecap != (c.want == 1) || d.ActionItems != c.want {
			t.Fatalf("%s: %d recaps, has %v, %d actions", c.id, len(d.Recaps), d.HasRecap, d.ActionItems)
		}
		if c.want == 1 && (d.Recaps[0].SeriesLevel || d.Recaps[0].CallID != "call-2") {
			t.Fatalf("%s: %+v", c.id, d.Recaps[0])
		}
	}
	// The agenda rows agree with the detail.
	a, err := s.CalendarAgenda(context.Background(), CalendarFilter{From: qt("2026-11-01T00:00:00Z"), To: qt("2026-11-30T00:00:00Z")})
	if err != nil || len(a.Rows) != 4 || a.Rows[0].HasRecap || !a.Rows[1].HasRecap || a.Rows[2].HasRecap || a.Rows[3].HasRecap {
		t.Fatalf("%+v %v", a.Rows, err)
	}
}

// The tolerance is the start-time link's: five minutes either way, not more.
func TestSeriesRecapUsesTheStartLinkTolerance(t *testing.T) {
	s := sSeriesArchive(t)
	s.sRecap(t, "early", sharedUID, "2026-11-03T09:55:00.000Z", "Ada Fixture") // exactly five minutes before
	s.sRecap(t, "late", sharedUID, "2026-11-10T10:05:01.000Z", "Ada Fixture")  // one second too far
	first := eventOn(t, s, "s1", 3)
	if len(first.Recaps) != 2 || first.Recaps[0].CallID != "early" && first.Recaps[1].CallID != "early" {
		t.Fatalf("first: %+v", first.Recaps)
	}
	var early, late CalendarRecap
	for _, r := range first.Recaps {
		if r.CallID == "early" {
			early = r
		} else {
			late = r
		}
	}
	if early.SeriesLevel || !late.SeriesLevel {
		t.Fatalf("early series-level %v, late %v", early.SeriesLevel, late.SeriesLevel)
	}
	if got := eventOn(t, s, "s2", 10).Recaps; len(got) != 1 || got[0].CallID != "late" {
		t.Fatalf("second: %+v", got)
	}
}

// A recap that no occurrence matches, or that has no meeting start, stays on every occurrence and
// says it is a series-level link.
func TestSeriesRecapWithNoMatchingOccurrenceStaysSeriesLevel(t *testing.T) {
	s := sSeriesArchive(t)
	s.sRecap(t, "off-day", sharedUID, "2026-11-12T10:00:00.000Z", "Ada Fixture")
	s.sRecap(t, "no-start", sharedUID, "", "Ada Fixture")
	for _, c := range []struct {
		id  string
		day int
	}{{"s1", 3}, {"s2", 10}, {"s3", 17}} {
		d := eventOn(t, s, c.id, c.day)
		if len(d.Recaps) != 2 || !d.Recaps[0].SeriesLevel || !d.Recaps[1].SeriesLevel || d.ActionItems != 2 {
			t.Fatalf("%s: %+v", c.id, d.Recaps)
		}
	}
}

// A uid one occurrence holds attaches all its recaps whatever their start, as a recap linked by id
// always did.
func TestRecapOfAUidOneOccurrenceHoldsAttachesToIt(t *testing.T) {
	s := newStore(t)
	s.qApply(t, calendar.SourceTeams, qTeams, qEvent("only", 3, func(e *calendar.Event) { e.ICalUID = sharedUID }))
	s.sRecap(t, "c1", sharedUID, "2026-11-20T10:00:00.000Z", "Ada Fixture")
	d, err := s.CalendarEvent(context.Background(), nil, EventID(qTeams, calendar.Key(qEvent("only", 3, func(e *calendar.Event) { e.ICalUID = sharedUID }))))
	if err != nil || len(d.Recaps) != 1 || d.Recaps[0].SeriesLevel {
		t.Fatalf("%+v %v", d.Recaps, err)
	}
}

// Placing a recap does not depend on which occurrences are shown: a removed, cancelled or declined
// occurrence still owns the recap that starts at it, and the live ones do not take it over.
func TestSeriesRecapPlacementIgnoresVisibility(t *testing.T) {
	for _, c := range []struct{ name, set string }{
		{"removed", `removed_at='2026-11-05T00:00:00.000Z'`},
		{"cancelled", `cancelled=1`},
		{"declined", `response='declined'`},
	} {
		t.Run(c.name+" two", func(t *testing.T) {
			s := sSeriesArchive(t)
			s.sRecap(t, "call-1", sharedUID, "2026-11-03T10:00:00.000Z", "Ada Fixture")
			s.qExec(t, `update calendar_source_events set `+c.set+` where source_id in ('s1','s3')`)
			if got := eventOn(t, s, "s2", 10); len(got.Recaps) != 0 || got.HasRecap {
				t.Fatalf("the live occurrence took the recap: %+v", got.Recaps)
			}
		})
	}
	// The recap of the occurrence is found on it when it is cancelled or declined, and by the
	// actions list when the agenda is asked for it.
	for _, c := range []struct{ name, set string }{{"cancelled", `cancelled=1`}, {"declined", `response='declined'`}} {
		s := sSeriesArchive(t)
		s.sRecap(t, "call-2", sharedUID, "2026-11-10T10:00:00.000Z", "Ada Fixture")
		s.qExec(t, `update calendar_source_events set `+c.set+` where source_id='s2'`)
		a, err := s.CalendarAgenda(context.Background(), CalendarAgendaFilterAll())
		if err != nil {
			t.Fatal(err)
		}
		held := 0
		for _, r := range a.Rows {
			if r.HasRecap {
				held++
				if r.Key != calendar.Key(qEvent("s2", 10, func(e *calendar.Event) { e.ICalUID = sharedUID })) || r.ActionItems != 1 {
					t.Fatalf("%s: wrong owner %+v", c.name, r)
				}
			}
		}
		if held != 1 {
			t.Fatalf("%s: %d occurrences hold the recap", c.name, held)
		}
	}
}

// CalendarAgendaFilterAll is the filter of November 2026 that shows cancelled and declined events.
func CalendarAgendaFilterAll() CalendarFilter {
	return CalendarFilter{From: qt("2026-11-01T00:00:00Z"), To: qt("2026-11-30T00:00:00Z"), IncludeCancelled: true, IncludeDeclined: true}
}

// Two occurrences four minutes apart: the nearer one owns the recap, and on a tie the lower key.
func TestSeriesRecapNearestOccurrenceWins(t *testing.T) {
	same := func(e *calendar.Event) { e.ICalUID = sharedUID }
	later := func(e *calendar.Event) {
		e.ICalUID = sharedUID
		e.Start, e.End = e.Start.Add(4*time.Minute), e.End.Add(4*time.Minute)
	}
	for _, c := range []struct {
		start string
		owner string // source id of the occurrence that holds it
	}{{"2026-11-10T10:03:00.000Z", "b"}, {"2026-11-10T10:01:00.000Z", "a"}, {"2026-11-10T10:02:00.000Z", "a"}} {
		s := newStore(t)
		s.qApply(t, calendar.SourceTeams, qTeams, qEvent("a", 10, same), qEvent("b", 10, later), qEvent("c", 17, same))
		s.sRecap(t, "call", sharedUID, c.start, "Ada Fixture")
		a, err := s.CalendarAgenda(context.Background(), CalendarAgendaFilterAll())
		if err != nil {
			t.Fatal(err)
		}
		var holders []string
		for _, r := range a.Rows {
			if r.HasRecap {
				holders = append(holders, strings.TrimPrefix(r.Subject, "Q sync "))
			}
		}
		if len(holders) != 1 || holders[0] != c.owner {
			t.Fatalf("%s: held by %v, want %s", c.start, holders, c.owner)
		}
	}
}

func TestOwnerOccurrence(t *testing.T) {
	occs := []occurrenceStart{{"a", qt("2026-11-03T10:00:00Z")}, {"b", qt("2026-11-03T10:04:00Z")}}
	if k, ok := ownerOccurrence(occs, qt("2026-11-03T10:03:00Z")); !ok || k != "b" {
		t.Fatalf("nearest: %q %v", k, ok)
	}
	if k, ok := ownerOccurrence(occs, qt("2026-11-03T10:02:00Z")); !ok || k != "a" {
		t.Fatalf("tie goes to the first key: %q %v", k, ok)
	}
	if _, ok := ownerOccurrence(occs, time.Time{}); ok {
		t.Fatal("a recap with no start matched")
	}
	if _, ok := ownerOccurrence(occs, qt("2026-11-03T11:00:00Z")); ok {
		t.Fatal("a far recap matched")
	}
}

func actionTitles(r CalendarActions) string {
	var out []string
	for _, a := range r.Items {
		out = append(out, a.Title)
	}
	return strings.Join(out, ",")
}

func TestCalendarActionsFilters(t *testing.T) {
	ctx := context.Background()
	s := qArchive(t)
	if err := s.ApplyAccount(ctx, acctA); err != nil {
		t.Fatal(err)
	}
	f := CalendarActionFilter{From: qt("2026-11-01T00:00:00Z"), To: qt("2026-11-30T00:00:00Z")}
	// Superseded items are hidden and the recap's own origin wins over the catch-up's.
	got, err := s.CalendarActions(ctx, f)
	if err != nil || actionTitles(got) != "Send notes" {
		t.Fatalf("%s %v", actionTitles(got), err)
	}
	a := got.Items[0]
	if a.EventID != EventID(qTeams, a.EventKey) || a.Subject != "Q sync o1" || !a.EventStart.Equal(qt("2026-11-03T10:00:00Z")) || a.CallID != "call-1" ||
		a.Owner != "Ada" || a.Origin != "recap" || *a.Mine || !a.ExpiresAt.Equal(qt("2026-12-03T00:00:00Z")) || a.SeriesLevel {
		t.Fatalf("%+v", a)
	}
	// --mine needs a known name.
	f.Mine = true
	_, err = s.CalendarActions(ctx, f)
	var c *errs.Coded
	if !errors.As(err, &c) || c.Code != errs.CodeUsage || !strings.Contains(c.Fix, "--owner") {
		t.Fatalf("unknown own name: %v", err)
	}
	if _, err := s.ApplyPeople(ctx, []teamsdesktop.Person{{TenantID: acctA.TenantID, ID: selfMRI(acctA), DisplayName: "ada", SeenAt: base}}); err != nil {
		t.Fatal(err)
	}
	if got, err = s.CalendarActions(ctx, f); err != nil || len(got.Items) != 1 || !*got.Items[0].Mine || got.Items[0].MineBasis != MineFullName {
		t.Fatalf("mine: %+v %v", got.Items, err)
	}
	// Another account's own name makes none of this account's items mine.
	f.Account = &acctB
	if _, err = s.CalendarActions(ctx, f); err == nil {
		t.Fatal("an account with no name accepted --mine")
	}
	f.Account, f.Mine = nil, false
	// --owner is a case-insensitive substring.
	f.Owner = "AD"
	if got, err = s.CalendarActions(ctx, f); err != nil || len(got.Items) != 1 {
		t.Fatalf("owner: %+v %v", got.Items, err)
	}
	f.Owner = "zed"
	if got, err = s.CalendarActions(ctx, f); err != nil || len(got.Items) != 0 || got.Total != 0 {
		t.Fatalf("owner none: %+v %v", got.Items, err)
	}
	// The range is the event's start: another day holds none.
	f.Owner, f.From, f.To = "", qt("2026-11-10T00:00:00Z"), qt("2026-11-11T00:00:00Z")
	if got, err = s.CalendarActions(ctx, f); err != nil || len(got.Items) != 0 {
		t.Fatalf("other day: %+v %v", got.Items, err)
	}
}

func TestCalendarActionsWithoutCalendarTables(t *testing.T) {
	s := qArchive(t)
	s.qExec(t, `drop table calendar_recaps`)
	got, err := s.CalendarActions(context.Background(), CalendarActionFilter{From: qt("2026-11-01T00:00:00Z"), To: qt("2026-11-30T00:00:00Z")})
	if err != nil || !got.NoTables || len(got.Items) != 0 {
		t.Fatalf("%+v %v", got, err)
	}
}

// A recap that cannot be placed on one occurrence is listed once, under the first occurrence of
// the range, and says so; one that matches an occurrence is listed under it.
func TestCalendarActionsListASeriesLevelRecapOnce(t *testing.T) {
	ctx := context.Background()
	s := sSeriesArchive(t)
	s.sRecap(t, "placed", sharedUID, "2026-11-10T10:00:00.000Z", "Ada Fixture")
	s.sRecap(t, "floating", sharedUID, "2026-11-12T10:00:00.000Z", "Bo")
	f := CalendarActionFilter{From: qt("2026-11-01T00:00:00Z"), To: qt("2026-11-30T00:00:00Z")}
	got, err := s.CalendarActions(ctx, f)
	if err != nil || len(got.Items) != 2 {
		t.Fatalf("%+v %v", got.Items, err)
	}
	// Sorted by event start: the floating recap sits on the first occurrence, the placed one on the second.
	if got.Items[0].CallID != "floating" || !got.Items[0].SeriesLevel || !got.Items[0].EventStart.Equal(qt("2026-11-03T10:00:00Z")) ||
		got.Items[1].CallID != "placed" || got.Items[1].SeriesLevel || !got.Items[1].EventStart.Equal(qt("2026-11-10T10:00:00Z")) || !*got.Items[1].Mine {
		t.Fatalf("%+v", got.Items)
	}
	// A range that holds only a later occurrence lists the floating recap under it.
	f.From = qt("2026-11-16T00:00:00Z")
	if got, err = s.CalendarActions(ctx, f); err != nil || len(got.Items) != 1 || got.Items[0].CallID != "floating" || !got.Items[0].EventStart.Equal(qt("2026-11-17T10:00:00Z")) {
		t.Fatalf("%+v %v", got.Items, err)
	}
	// The limit cuts and says how many there were.
	f.From, f.Limit = qt("2026-11-01T00:00:00Z"), 1
	if got, err = s.CalendarActions(ctx, f); err != nil || len(got.Items) != 1 || !got.Truncated || got.Total != 2 {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestLinkCalendarOnMeetingConversations(t *testing.T) {
	ctx := context.Background()
	s := qArchive(t)
	conv := func(kind string) []ConversationRow {
		return []ConversationRow{{TenantID: acctA.TenantID, UserID: acctA.UserID, ID: qChat, Kind: kind}}
	}
	rows := conv("Meeting")
	if err := s.linkCalendar(ctx, rows); err != nil || rows[0].CalendarSeriesKey != qSeries || rows[0].CalendarEventCount != 3 {
		t.Fatalf("%+v %v", rows, err)
	}
	// Only a Meeting is linked, a chat no event names stays as it was, and the master is no occurrence.
	if rows = conv("Chat"); s.linkCalendar(ctx, rows) != nil || rows[0].CalendarEventCount != 0 {
		t.Fatalf("a chat: %+v", rows)
	}
	other := []ConversationRow{{TenantID: acctA.TenantID, UserID: acctA.UserID, ID: "19:meeting_NONE@thread.v2", Kind: "Meeting"}}
	if err := s.linkCalendar(ctx, other); err != nil || other[0].CalendarEventCount != 0 || other[0].CalendarSeriesKey != "" {
		t.Fatalf("no event: %+v %v", other, err)
	}
	// A single event on the chat counts but does not choose the series; two series leave no key.
	occ := func(extra ...calendar.Event) {
		s.qApply(t, calendar.SourceTeams, qTeams, append([]calendar.Event{qEvent("o1", 3), qEvent("o2", 10), qEvent("o3", 17)}, extra...)...)
	}
	single := qEvent("one", 20, func(e *calendar.Event) { e.EventType, e.SeriesKey = calendar.EventSingle, "uid-one" })
	occ(single)
	rows = conv("Meeting")
	if err := s.linkCalendar(ctx, rows); err != nil || rows[0].CalendarSeriesKey != qSeries || rows[0].CalendarEventCount != 4 {
		t.Fatalf("single: %+v %v", rows, err)
	}
	occ(single, qEvent("other", 24, func(e *calendar.Event) { e.SeriesKey = "OTHER" }))
	rows = conv("Meeting")
	if err := s.linkCalendar(ctx, rows); err != nil || rows[0].CalendarSeriesKey != "" || rows[0].CalendarEventCount != 5 {
		t.Fatalf("two series: %+v %v", rows, err)
	}
	// An event another source holds for the same occurrence counts once, through the principal.
	s.qLink(t)
	s.qApply(t, calendar.SourceOutlook, qOutlook, qEvent("o1", 3, func(e *calendar.Event) { e.Source, e.AccountID = calendar.SourceOutlook, qOutlook }))
	rows = conv("Meeting")
	if err := s.linkCalendar(ctx, rows); err != nil || rows[0].CalendarEventCount != 5 {
		t.Fatalf("two sources: %+v %v", rows, err)
	}
	// Removed events do not count.
	s.qExec(t, `update calendar_source_events set removed_at='2026-11-05T00:00:00.000Z' where source_id in ('o2','o3','one','other')`)
	rows = conv("Meeting")
	if err := s.linkCalendar(ctx, rows); err != nil || rows[0].CalendarEventCount != 1 {
		t.Fatalf("removed: %+v %v", rows, err)
	}
}

func TestLinkCalendarOldArchive(t *testing.T) {
	s := newStore(t)
	s.qExec(t, `drop table calendar_recaps`)
	rows := []ConversationRow{{TenantID: acctA.TenantID, UserID: acctA.UserID, ID: qChat, Kind: "Meeting"}}
	if err := s.linkCalendar(context.Background(), rows); err != nil || rows[0].CalendarEventCount != 0 {
		t.Fatalf("%+v %v", rows, err)
	}
}

func TestCalendarActionsAndLinksReportEveryStorageFailure(t *testing.T) {
	ctx := context.Background()
	setup := func(t *testing.T, s *Store) {
		qSeed(t, s)
		if err := s.ApplyAccount(ctx, acctA); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ApplyPeople(ctx, []teamsdesktop.Person{{TenantID: acctA.TenantID, ID: selfMRI(acctA), DisplayName: "Ada", SeenAt: base}}); err != nil {
			t.Fatal(err)
		}
		s.qApply(t, calendar.SourceTeams, qTeams, qEvent("o1", 3), qEvent("o2", 10), qEvent("o3", 17), qEvent("s1", 4, func(e *calendar.Event) { e.ICalUID = "uid-o1" }))
		s.qLink(t)
	}
	ops := map[string]func(context.Context, *Store) error{
		"actions": func(ctx context.Context, s *Store) error {
			_, err := s.CalendarActions(ctx, CalendarActionFilter{From: qt("2026-11-01T00:00:00Z"), To: qt("2026-11-30T00:00:00Z"), Mine: true})
			return err
		},
		"link": func(ctx context.Context, s *Store) error {
			return s.linkCalendar(ctx, []ConversationRow{{TenantID: acctA.TenantID, UserID: acctA.UserID, ID: qChat, Kind: "Meeting"}})
		},
		"conversations": func(ctx context.Context, s *Store) error {
			_, _, err := s.Conversations(ctx, "Meeting", "", Filter{})
			return err
		},
	}
	nullSafe := map[string][]int{}
	for name, op := range ops {
		t.Run(name, func(t *testing.T) { sweepReadFaults(t, setup, op, nullSafe[name]...) })
	}
}

// An occurrence two linked sources hold is one occurrence, so a series recap still has one owner.
func TestSeriesRecapWithAnOccurrenceHeldByTwoSources(t *testing.T) {
	s := sSeriesArchive(t)
	s.qLink(t)
	twin := qEvent("s2", 10, func(e *calendar.Event) { e.ICalUID = sharedUID })
	twin.Source, twin.AccountID, twin.SourceID, twin.TeamsThreadID = calendar.SourceOutlook, qOutlook, "s2-outlook", ""
	s.qApply(t, calendar.SourceOutlook, qOutlook, twin)
	s.sRecap(t, "call-2", sharedUID, "2026-11-10T10:00:00.000Z", "Ada Fixture")
	if d := eventOn(t, s, "s2", 10); len(d.Recaps) != 1 || d.Recaps[0].SeriesLevel || len(d.Sources) != 2 {
		t.Fatalf("%+v", d.Recaps)
	}
	if d := eventOn(t, s, "s1", 3); len(d.Recaps) != 0 {
		t.Fatalf("%+v", d.Recaps)
	}
}

func TestOwnerIsMe(t *testing.T) {
	cases := []struct {
		own, owner string
		people     []string
		mine       string // "true", "false" or "nil"
		basis      string
	}{
		{"Ada Fixture", "ada fixture", nil, "true", MineFullName},
		{"Ada Fixture", " Ada ", []string{"Ada Fixture", "Bo Example"}, "true", MineFirstName},
		{"Ada Fixture", "Ada", []string{"Ada Fixture", "Ada Other"}, "nil", MineAmbiguous},
		{"Ada Fixture", "ada", []string{"ADA Third"}, "nil", MineAmbiguous},
		{"Ada Fixture", "Ada Other", nil, "false", ""},
		{"Ada Fixture", "Bo", nil, "false", ""},
		{"Ada Fixture", "", nil, "false", ""},
		{"", "Ada", nil, "false", ""},
	}
	for _, c := range cases {
		mine, basis := ownerIsMe(c.own, c.owner, func() []string { return c.people })
		got := "nil"
		if mine != nil {
			got = map[bool]string{true: "true", false: "false"}[*mine]
		}
		if got != c.mine || basis != c.basis {
			t.Errorf("%+v: %s %q", c, got, basis)
		}
	}
}

func TestMeetingNames(t *testing.T) {
	r := CalendarRecap{Speakers: rawJSON(`[{"name":"Sp One"},{"displayName":"Sp Two"},{"speakerName":"Sp Three"},{"id":"x"}]`),
		ActionItems: []CalendarRecapItem{{Speaker: "Item Sp"}}, Mentions: []CalendarRecapItem{{Speaker: "Men Sp"}}}
	if got := strings.Join(meetingNames(`[{"name":"Att One"}]`, r), ","); got != "Att One" {
		t.Fatalf("attendees win: %s", got)
	}
	if got := strings.Join(meetingNames("", r), ","); got != "Sp One,Sp Two,Sp Three,Item Sp,Men Sp" {
		t.Fatalf("speakers: %s", got)
	}
	if got := meetingNames("", CalendarRecap{Speakers: rawJSON(`{"not":"a list"}`)}); len(got) != 0 {
		t.Fatalf("%v", got)
	}
}

// A one-word owner is the user when nobody else in the meeting shares the first name; it is unknown
// (not guessed) when someone does; --mine keeps the first and counts the second.
func TestCalendarActionsMatchFirstNames(t *testing.T) {
	ctx := context.Background()
	s := sSeriesArchive(t)
	s.qExec(t, `delete from calendar_recaps where call_id='no-uid'`)
	s.sRecap(t, "full", sharedUID, "2026-11-03T10:00:00.000Z", "Ada Fixture")
	s.sRecap(t, "first", sharedUID, "2026-11-10T10:00:00.000Z", "ada")
	s.sRecap(t, "other", sharedUID, "2026-11-17T10:00:00.000Z", "Bo")
	s.qExec(t, `update calendar_source_events set attendees_json='[{"name":"Ada Fixture"},{"name":"Bo Example"}]' where source_id in ('s1','s2')`)
	s.qExec(t, `update calendar_source_events set attendees_json='[{"name":"Ada Fixture"},{"name":"Ada Other"}]' where source_id='s3'`)
	s.sRecap(t, "amb", "uid-s3-only", "2026-11-17T11:00:00.000Z", "Ada")
	s.qExec(t, `update calendar_source_events set ical_uid='uid-s3-only' where source_id='s3'`)
	s.qExec(t, `update calendar_recaps set ical_uid='uid-s3-only' where call_id='amb'`)
	f := CalendarActionFilter{From: qt("2026-11-01T00:00:00Z"), To: qt("2026-11-30T00:00:00Z")}
	got, err := s.CalendarActions(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	basis := map[string]string{}
	state := map[string]string{}
	for _, a := range got.Items {
		basis[a.CallID] = a.MineBasis
		state[a.CallID] = "nil"
		if a.Mine != nil {
			state[a.CallID] = map[bool]string{true: "true", false: "false"}[*a.Mine]
		}
	}
	if basis["full"] != MineFullName || basis["first"] != MineFirstName || basis["other"] != "" || basis["amb"] != MineAmbiguous ||
		state["full"] != "true" || state["first"] != "true" || state["other"] != "false" || state["amb"] != "nil" {
		t.Fatalf("%v %v", basis, state)
	}
	f.Mine = true
	got, err = s.CalendarActions(ctx, f)
	if err != nil || len(got.Items) != 2 || got.MineAmbiguous != 1 || got.Total != 2 {
		t.Fatalf("mine: %d items, %d ambiguous, %v", len(got.Items), got.MineAmbiguous, err)
	}
}

// A cancelled occurrence does not count toward the chat's events, a declined one does: the meeting
// happened.
func TestLinkCalendarCountsDeclinedButNotCancelled(t *testing.T) {
	s := qArchive(t)
	s.qExec(t, `update calendar_source_events set response='declined' where source_id='o2'`)
	s.qExec(t, `update calendar_source_events set cancelled=1 where source_id='o3'`)
	rows := []ConversationRow{{TenantID: acctA.TenantID, UserID: acctA.UserID, ID: qChat, Kind: "Meeting"}}
	if err := s.linkCalendar(context.Background(), rows); err != nil || rows[0].CalendarEventCount != 2 {
		t.Fatalf("%d %v", rows[0].CalendarEventCount, err)
	}
}

// --mine matches the accounts that have a name and lists the ones that do not.
func TestCalendarActionsMineListsUnnamedAccounts(t *testing.T) {
	ctx := context.Background()
	s := qArchive(t)
	for _, a := range []teamsdesktop.Account{acctA, acctB} {
		if err := s.ApplyAccount(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.ApplyPeople(ctx, []teamsdesktop.Person{{TenantID: acctA.TenantID, ID: selfMRI(acctA), DisplayName: "Ada", SeenAt: base}}); err != nil {
		t.Fatal(err)
	}
	f := CalendarActionFilter{From: qt("2026-11-01T00:00:00Z"), To: qt("2026-11-30T00:00:00Z"), Mine: true}
	got, err := s.CalendarActions(ctx, f)
	if err != nil || len(got.Items) != 1 || len(got.MineUnknownAccounts) != 1 || got.MineUnknownAccounts[0] != acctB.TenantID+"/"+acctB.UserID {
		t.Fatalf("%+v %v", got, err)
	}
	f.Mine = false
	if got, err = s.CalendarActions(ctx, f); err != nil || got.MineUnknownAccounts != nil {
		t.Fatalf("without --mine: %+v %v", got.MineUnknownAccounts, err)
	}
}
