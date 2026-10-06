package calendar

import (
	"database/sql"
	"reflect"
	"testing"
)

// twin is one source's row of the meeting "Fixture Weekly Sync" on 2026-11-02, all flags known.
func twin(t testing.TB, src Source, lm string, mods ...func(*Event)) Event {
	account := acctTeams
	if src == SourceOutlook {
		account = acctOutlook
	}
	e := Event{
		Source: src, AccountID: account, SourceID: string(src) + "-1", GlobalID: "uid-1", Subject: "Fixture Weekly Sync",
		Start: mustTime(t, "2026-11-02T13:00:00Z"), End: mustTime(t, "2026-11-02T14:00:00Z"), LastModified: tp(t, lm),
		AllDay: TriFalse, IsOrganizer: TriFalse, IsPrivate: TriFalse, Cancelled: TriFalse,
	}
	for _, m := range mods {
		m(&e)
	}
	return e
}

func acctOf(src Source) string {
	if src == SourceOutlook {
		return acctOutlook
	}
	return acctTeams
}

func day(t testing.TB, db *sql.DB, q AgendaQuery) AgendaResult {
	t.Helper()
	if q.From.IsZero() {
		q.From, q.To = mustTime(t, "2026-11-02T00:00:00Z"), mustTime(t, "2026-11-03T00:00:00Z")
	}
	res, err := Agenda(ctx, db, q)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestAgendaMergesTwinsAcrossSources(t *testing.T) {
	for _, outlookFirst := range []bool{false, true} {
		db := linked(t)
		teams := twin(t, SourceTeams, "2026-11-02T09:00:00Z", func(e *Event) { e.Location, e.AttendeesJSON = "Fixture Room Alpha", attendeesTwo })
		outlook := twin(t, SourceOutlook, "2026-11-02T09:00:02Z", unknownOf(FieldLocation, FieldAttendees))
		apply2 := func() {
			snap(t, db, SourceTeams, acctTeams, "2026-11-02T09:10:00Z", "2026-11-02T09:10:00Z", teams)
		}
		applyO := func() {
			snap(t, db, SourceOutlook, acctOutlook, "2026-11-02T09:30:00Z", "2026-11-02T09:30:00Z", outlook)
		}
		if outlookFirst {
			applyO()
			apply2()
		} else {
			apply2()
			applyO()
		}
		res := day(t, db, AgendaQuery{})
		if len(res.Items) != 1 {
			t.Fatalf("a twin is one event: %d", len(res.Items))
		}
		it := res.Items[0]
		if it.Principal != acctTeams || !reflect.DeepEqual(it.Accounts, []string{acctOutlook, acctTeams}) || !reflect.DeepEqual(it.Sources, []Source{SourceTeams, SourceOutlook}) {
			t.Fatalf("%+v", it)
		}
		if it.Source != SourceOutlook || it.Location != "Fixture Room Alpha" || it.AttendeesJSON != attendeesTwo || fillOf(it.Merged, FieldLocation) == nil {
			t.Fatalf("%+v", it.Merged)
		}
		if len(res.Unlinked) != 0 {
			t.Fatalf("unlinked %v", res.Unlinked)
		}
	}
}

func TestAgendaDifferentPrincipalsNeverMerge(t *testing.T) {
	db := openDB(t)
	snap(t, db, SourceTeams, acctTeams, "2026-11-02T09:00:00Z", "2026-11-02T09:00:00Z", twin(t, SourceTeams, "2026-11-02T09:00:00Z"))
	other := twin(t, SourceTeams, "2026-11-02T09:00:00Z", func(e *Event) { e.AccountID = "tenant-2/user-2"; e.Subject = "Other" })
	snap(t, db, SourceTeams, "tenant-2/user-2", "2026-11-02T09:00:00Z", "2026-11-02T09:00:00Z", other)
	// An unlinked Outlook account holding the same key is its own principal too.
	snap(t, db, SourceOutlook, acctOutlook, "2026-11-02T09:00:00Z", "2026-11-02T09:00:00Z", twin(t, SourceOutlook, "2026-11-02T09:00:00Z"))
	res := day(t, db, AgendaQuery{})
	if len(res.Items) != 3 {
		t.Fatalf("three principals, three events: %d", len(res.Items))
	}
	for _, it := range res.Items {
		if len(it.Sources) != 1 || len(it.Accounts) != 1 || it.Principal != it.Accounts[0] {
			t.Fatalf("%+v", it)
		}
	}
	if len(day(t, db, AgendaQuery{AccountID: "tenant-2/user-2"}).Items) != 1 {
		t.Fatal("the account filter selects one principal")
	}
}

func TestAgendaUnlinkedAccountListed(t *testing.T) {
	db := linked(t)
	snap(t, db, SourceOutlook, "outlook-9", "2026-11-02T09:00:00Z", "2026-11-02T09:00:00Z")
	snap(t, db, SourceOutlook, "outlook-8", "2026-11-02T09:00:00Z", "2026-11-02T09:00:00Z")
	if got := day(t, db, AgendaQuery{}).Unlinked; !reflect.DeepEqual(got, []string{"outlook-8", "outlook-9"}) {
		t.Fatalf("%v", got)
	}
	if got := day(t, db, AgendaQuery{AccountID: "outlook-9"}).Unlinked; !reflect.DeepEqual(got, []string{"outlook-9"}) {
		t.Fatalf("%v", got)
	}
	if got := day(t, db, AgendaQuery{AccountID: acctTeams}).Unlinked; got != nil {
		t.Fatalf("%v", got)
	}
}

func TestAgendaAccountFilterByPrincipal(t *testing.T) {
	db := linked(t)
	snap(t, db, SourceOutlook, acctOutlook, "2026-11-02T09:30:00Z", "2026-11-02T09:30:00Z",
		twin(t, SourceOutlook, "2026-11-02T09:00:00Z", func(e *Event) { e.GlobalID = ""; e.Subject = "Outlook only" }))
	for _, filter := range []string{acctTeams, acctOutlook} {
		res := day(t, db, AgendaQuery{AccountID: filter})
		if len(res.Items) != 1 || res.Items[0].Subject != "Outlook only" || res.Items[0].Principal != acctTeams {
			t.Fatalf("--account %s must return the linked Outlook rows: %+v", filter, res.Items)
		}
	}
}

func TestCoverageByPrincipal(t *testing.T) {
	from, to := mustTime(t, "2026-11-02T00:00:00Z"), mustTime(t, "2026-11-04T00:00:00Z")
	build := func(link bool) *sql.DB {
		db := openDB(t)
		w := func(src Source, a string) Window {
			return Window{Source: src, AccountID: a, Start: from, End: to, SyncedAt: from, CacheFreshAt: from}
		}
		// Teams verified only the first day; Outlook's window covers both.
		if _, err := ApplySnapshot(ctx, db, w(SourceTeams, acctTeams), nil, from); err != nil {
			t.Fatal(err)
		}
		if _, err := ApplySnapshot(ctx, db, w(SourceOutlook, acctOutlook), nil, from); err != nil {
			t.Fatal(err)
		}
		exec(t, db, `DELETE FROM calendar_covered_days`)
		exec(t, db, `INSERT INTO calendar_covered_days VALUES ('teams',?,'2026-11-02','2026-11-02T00:00:00.000Z','2026-11-02T00:00:00.000Z')`, acctTeams)
		exec(t, db, `UPDATE calendar_sources SET window_start='2026-11-03T00:00:00.000Z' WHERE source='outlook'`)
		if link {
			tx, _ := db.BeginTx(ctx, nil)
			if err := LinkAccount(ctx, tx, SourceOutlook, acctOutlook, acctTeams, "config", from); err != nil {
				t.Fatal(err)
			}
			_ = tx.Commit()
		}
		return db
	}
	// Teams days plus Outlook's window cover the range for a linked principal.
	if res := day(t, build(true), AgendaQuery{From: from, To: to}); res.Gap {
		t.Fatal("the principal's sources complement each other")
	}
	// Not for an unlinked one: each account is judged alone.
	if res := day(t, build(false), AgendaQuery{From: from, To: to}); !res.Gap {
		t.Fatal("an unlinked account's gap must show")
	}
}

func TestFreshByPrincipalAndSource(t *testing.T) {
	// Two principals with a tie between their sources; each principal's own cache freshness decides.
	db := openDB(t)
	a, b := "tenant-1/user-1", "tenant-2/user-2"
	snap(t, db, SourceTeams, a, "2026-11-02T10:00:00Z", "2026-11-02T10:00:00Z", twin(t, SourceTeams, "2026-11-02T09:00:00Z"))
	snap(t, db, SourceTeams, b, "2026-11-02T08:00:00Z", "2026-11-02T08:00:00Z", twin(t, SourceTeams, "2026-11-02T09:00:00Z", func(e *Event) { e.AccountID = b }))
	snap(t, db, SourceOutlook, "o-a", "2026-11-02T08:00:00Z", "2026-11-02T08:00:00Z", twin(t, SourceOutlook, "2026-11-02T09:00:00Z", func(e *Event) { e.AccountID = "o-a" }))
	snap(t, db, SourceOutlook, "o-b", "2026-11-02T10:00:00Z", "2026-11-02T10:00:00Z", twin(t, SourceOutlook, "2026-11-02T09:00:00Z", func(e *Event) { e.AccountID = "o-b" }))
	link(t, db, SourceOutlook, "o-a", a)
	link(t, db, SourceOutlook, "o-b", b)
	got := map[string]Source{}
	for _, it := range day(t, db, AgendaQuery{}).Items {
		got[it.Principal] = it.Source
	}
	if got[a] != SourceTeams || got[b] != SourceOutlook {
		t.Fatalf("each principal uses its own freshness: %v", got)
	}
}

func TestAgendaTwinRowsOnDifferentSidesOfRange(t *testing.T) {
	db := linked(t)
	teams := twin(t, SourceTeams, "2026-11-02T12:00:00Z", func(e *Event) {
		e.AllDay, e.StartDate, e.EndDate = TriTrue, "2026-11-03", "2026-11-04"
		e.Start, e.End = mustTime(t, "2026-11-03T08:00:00Z"), mustTime(t, "2026-11-04T08:00:00Z")
	})
	outlook := twin(t, SourceOutlook, "2026-11-02T12:00:30Z", func(e *Event) {
		e.AllDay = TriUnknown
		e.Start, e.End = mustTime(t, "2026-11-03T00:00:00Z"), mustTime(t, "2026-11-04T00:00:00Z")
	})
	snap(t, db, SourceTeams, acctTeams, "2026-11-02T12:10:00Z", "2026-11-02T12:10:00Z", teams)
	snap(t, db, SourceOutlook, acctOutlook, "2026-11-02T12:10:00Z", "2026-11-02T12:10:00Z", outlook)
	in := day(t, db, AgendaQuery{From: mustTime(t, "2026-11-03T00:00:00Z"), To: mustTime(t, "2026-11-04T00:00:00Z")})
	if len(in.Items) != 1 || !in.Items[0].AllDay.Is(true) {
		t.Fatalf("one all-day event on the 3rd: %+v", in.Items)
	}
	// On the 4th the Outlook row's instants still touch the range, but the merged event is the
	// all-day event of the 3rd, which ends before it.
	out := day(t, db, AgendaQuery{From: mustTime(t, "2026-11-04T01:00:00Z"), To: mustTime(t, "2026-11-04T05:00:00Z")})
	if len(out.Items) != 0 {
		t.Fatalf("the merged event is judged, not the row: %+v", out.Items)
	}
	// The group is still found from the all-day row's widened dates.
	groups, err := loadGroups(ctx, db, Principals{}, nil, mustTime(t, "2026-11-04T09:00:00Z"), mustTime(t, "2026-11-04T10:00:00Z"), "2026-11-04", "2026-11-04")
	if err != nil || len(groups) != 1 {
		t.Fatalf("widened candidates: %d %v", len(groups), err)
	}
}

func TestLoadEventsUnknownAllDayNotDropped(t *testing.T) {
	db := openDB(t)
	e := twin(t, SourceTeams, "2026-11-02T09:00:00Z", func(e *Event) { e.AllDay = TriUnknown })
	snap(t, db, SourceTeams, acctTeams, "2026-11-02T09:10:00Z", "2026-11-02T09:10:00Z", e)
	if n := count(t, db, `SELECT count(*) FROM calendar_source_events WHERE all_day IS NULL`); n != 1 {
		t.Fatalf("stored as NULL: %d", n)
	}
	res := day(t, db, AgendaQuery{})
	if len(res.Items) != 1 || res.Items[0].AllDay.Known() {
		t.Fatalf("an unknown all-day row must still be on the agenda, timed: %+v", res.Items)
	}
	// And the removal sweep sees it as timed too.
	snap(t, db, SourceTeams, acctTeams, "2026-11-02T10:00:00Z", "2026-11-02T10:00:00Z")
	if n := count(t, db, `SELECT count(*) FROM calendar_source_events WHERE removed_at IS NOT NULL`); n != 1 {
		t.Fatal("an unknown all-day row must be removable")
	}
}

// removalDB holds the twin of Examples F to H: Teams saw the event go at 15:00:00, Outlook still
// has it with the given last-modified time.
func removalDB(t testing.TB, outlookLM string, outlookFirst bool) *sql.DB {
	db := linked(t)
	teamsRow := twin(t, SourceTeams, "2026-11-02T09:00:00Z", func(e *Event) { e.Location = "Teams Room" })
	outlookRow := twin(t, SourceOutlook, outlookLM, unknownOf(FieldLocation))
	teamsSees := func() {
		snap(t, db, SourceTeams, acctTeams, "2026-11-02T09:10:00Z", "2026-11-02T09:10:00Z", teamsRow)
		snap(t, db, SourceTeams, acctTeams, "2026-11-02T15:00:00Z", "2026-11-02T15:00:00Z") // the day is loaded and the event is gone
	}
	outlookSees := func() {
		snap(t, db, SourceOutlook, acctOutlook, "2026-11-02T16:30:00Z", "2026-11-02T16:30:00Z", outlookRow)
	}
	if outlookFirst {
		outlookSees()
		teamsSees()
	} else {
		teamsSees()
		outlookSees()
	}
	return db
}

func TestRemovalTeamsGoneOutlookLiveOlder(t *testing.T) {
	for _, first := range []bool{false, true} {
		db := removalDB(t, "2026-11-02T09:05:00Z", first)
		if res := day(t, db, AgendaQuery{}); len(res.Items) != 0 {
			t.Fatalf("Example F: the event is removed and hidden: %+v", res.Items)
		}
	}
}

func TestRemovalOutlookEditedAfterRemovalIsLive(t *testing.T) {
	for _, first := range []bool{false, true} {
		db := removalDB(t, "2026-11-02T16:00:00Z", first)
		res := day(t, db, AgendaQuery{})
		if len(res.Items) != 1 || res.Items[0].Removed || !reflect.DeepEqual(res.Items[0].Sources, []Source{SourceOutlook}) {
			t.Fatalf("Example G: %+v", res.Items)
		}
	}
}

// TestRemovedRowDroppedFromMergeWhenLive: the removed Teams row's location must not reach a live event.
func TestRemovedRowDroppedFromMergeWhenLive(t *testing.T) {
	db := removalDB(t, "2026-11-02T16:00:00Z", false)
	it := day(t, db, AgendaQuery{}).Items[0]
	if it.Location == "Teams Room" || len(it.Filled) != 0 {
		t.Fatalf("stale data from a removed row: %+v", it.Merged)
	}
}

func TestRemovalBoundary(t *testing.T) {
	// The database keeps milliseconds: 15:00:05.000 is not later than 15:00:05, .001 is.
	for lm, removed := range map[string]bool{"2026-11-02T15:00:05Z": true, "2026-11-02T15:00:05.001Z": false} {
		db := removalDB(t, lm, false)
		if got := len(day(t, db, AgendaQuery{}).Items) == 0; got != removed {
			t.Errorf("outlook at %s: removed=%v, want %v", lm, got, removed)
		}
	}
	// To the nanosecond, on rows.
	at := tp(t, "2026-11-02T15:00:00Z")
	gone := Event{Source: SourceTeams, RemovedAt: at, LastModified: tp(t, "2026-11-02T09:00:00Z")}
	for lm, want := range map[string]bool{"2026-11-02T15:00:05Z": true, "2026-11-02T15:00:05.000000001Z": false} {
		m := Merge([]Event{gone, {Source: SourceOutlook, LastModified: tp(t, lm)}}, nil)
		if m.Removed != want {
			t.Errorf("Example H at %s: removed=%v", lm, m.Removed)
		}
	}
	// A live row with no last-modified cannot show an edit.
	if m := Merge([]Event{gone, {Source: SourceOutlook}}, nil); !m.Removed {
		t.Fatal("a live row without a time counts as not later")
	}
}

func TestRemovalAllRowsRemoved(t *testing.T) {
	gone := Event{Source: SourceTeams, RemovedAt: tp(t, "2026-11-02T15:00:00Z"), LastModified: tp(t, "2026-11-02T09:00:00Z"), Location: "x"}
	goneToo := Event{Source: SourceOutlook, Unknown: []Field{FieldLocation}, RemovedAt: tp(t, "2026-11-02T16:00:00Z"), LastModified: tp(t, "2026-11-02T09:00:00Z")}
	m := Merge([]Event{gone, goneToo}, nil)
	if !m.Removed || !reflect.DeepEqual(m.RemovedBy, []Source{SourceTeams, SourceOutlook}) || !m.RemovedAt.Equal(*goneToo.RemovedAt) {
		t.Fatalf("%+v", m)
	}
	if m.Location != "x" || len(m.Sources) != 2 {
		t.Fatalf("a removed event keeps the last known data of every row: %+v", m.Event)
	}
}

func TestRemovalHiddenByDefaultShownWithInclude(t *testing.T) {
	db := removalDB(t, "2026-11-02T09:05:00Z", false)
	if len(day(t, db, AgendaQuery{}).Items) != 0 {
		t.Fatal("hidden by default")
	}
	res := day(t, db, AgendaQuery{IncludeRemoved: true})
	if len(res.Items) != 1 {
		t.Fatalf("%+v", res.Items)
	}
	it := res.Items[0]
	if !it.Removed || !reflect.DeepEqual(it.RemovedBy, []Source{SourceTeams}) || !it.RemovedAt.Equal(mustTime(t, "2026-11-02T15:00:00Z")) || it.Location != "Teams Room" {
		t.Fatalf("%+v", it.Merged)
	}
}

func TestRemovalReappearingSourceClears(t *testing.T) {
	db := removalDB(t, "2026-11-02T09:05:00Z", false)
	snap(t, db, SourceTeams, acctTeams, "2026-11-02T17:00:00Z", "2026-11-02T17:00:00Z", twin(t, SourceTeams, "2026-11-02T09:00:00Z"))
	res := day(t, db, AgendaQuery{})
	if len(res.Items) != 1 || res.Items[0].Removed {
		t.Fatalf("a reappearing source clears its own removal: %+v", res.Items)
	}
}

func TestAgendaLoadsRemovedRowsForCandidateKeys(t *testing.T) {
	// Only the removed Teams row is near the range (Outlook's live copy was moved far away), and it
	// still reaches the merge, so the live copy is judged against the removal.
	db := removalDB(t, "2026-11-02T08:00:00Z", false)
	exec(t, db, `UPDATE calendar_source_events SET start_at='2026-11-20T13:00:00.000Z', end_at='2026-11-20T14:00:00.000Z' WHERE source='outlook'`)
	if res := day(t, db, AgendaQuery{IncludeRemoved: true}); len(res.Items) != 1 || !res.Items[0].Removed {
		t.Fatalf("the removed row must be loaded as a candidate: %+v", res.Items)
	}
}

func TestAgendaTwinOrderIndependence(t *testing.T) {
	var want []string
	for _, order := range [][]Source{{SourceTeams, SourceOutlook}, {SourceOutlook, SourceTeams}} {
		db := linked(t)
		for _, s := range order {
			snap(t, db, s, acctOf(s), "2026-11-02T09:30:00Z", "2026-11-02T09:30:00Z", twin(t, s, "2026-11-02T09:00:00Z", func(e *Event) {
				if s == SourceTeams {
					e.Location = "Room"
				} else {
					e.Unknown = []Field{FieldLocation}
				}
			}))
		}
		var got []string
		for _, it := range day(t, db, AgendaQuery{}).Items {
			got = append(got, it.Key+"|"+it.Location+"|"+string(it.Source))
		}
		if want == nil {
			want = got
		} else if !reflect.DeepEqual(want, got) {
			t.Fatalf("%v vs %v", want, got)
		}
	}
}

func TestLinkRecapsSpansAccountsOfThePrincipal(t *testing.T) {
	db := linked(t)
	// The meeting is held by the Outlook account only; the recap is under the Teams account.
	snap(t, db, SourceOutlook, acctOutlook, "2026-11-02T09:30:00Z", "2026-11-02T09:30:00Z", twin(t, SourceOutlook, "2026-11-02T09:00:00Z", func(e *Event) { e.ICalUID = "uid-1" }))
	recap := Recap{AccountID: acctTeams, CallID: "c1", MeetingStartAt: tp(t, "2026-11-02T13:00:00Z"), MeetingEndAt: tp(t, "2026-11-02T14:00:00Z")}
	w := Window{Source: SourceTeams, AccountID: acctTeams, Start: mustTime(t, "2026-11-01T00:00:00Z"), End: mustTime(t, "2026-12-01T00:00:00Z"),
		SyncedAt: mustTime(t, "2026-11-02T10:00:00Z"), CacheFreshAt: mustTime(t, "2026-11-02T10:00:00Z")}
	tx, _ := db.BeginTx(ctx, nil)
	c, err := ApplyBatch(ctx, tx, Batch{Window: w, Recaps: []Recap{recap}}, ApplyOptions{}, mustTime(t, "2026-11-02T10:00:00Z"))
	if err != nil || c.Linked != 1 {
		t.Fatalf("%+v %v", c, err)
	}
	_ = tx.Commit()
	var uid string
	if err := db.QueryRow(`SELECT ical_uid FROM calendar_recaps WHERE call_id='c1'`).Scan(&uid); err != nil || uid != "uid-1" {
		t.Fatalf("%q %v", uid, err)
	}
	// The same meeting held by both sources is one event, not two candidates.
	db = linked(t)
	snap(t, db, SourceOutlook, acctOutlook, "2026-11-02T09:30:00Z", "2026-11-02T09:30:00Z", twin(t, SourceOutlook, "2026-11-02T09:00:00Z", func(e *Event) { e.ICalUID = "uid-1" }))
	snap(t, db, SourceTeams, acctTeams, "2026-11-02T09:30:00Z", "2026-11-02T09:30:00Z", twin(t, SourceTeams, "2026-11-02T09:00:00Z", func(e *Event) { e.ICalUID = "" }))
	tx, _ = db.BeginTx(ctx, nil)
	c, err = ApplyBatch(ctx, tx, Batch{Window: w, Recaps: []Recap{recap}}, ApplyOptions{}, mustTime(t, "2026-11-02T10:00:00Z"))
	if err != nil || c.Linked != 1 {
		t.Fatalf("a twin must count once: %+v %v", c, err)
	}
	_ = tx.Commit()
}

func TestLinkRecapsOneEventKeyHeldByTwoSourcesWithOneUid(t *testing.T) {
	db := linked(t)
	w := Window{Source: SourceTeams, AccountID: acctTeams, Start: mustTime(t, "2026-11-01T00:00:00Z"), End: mustTime(t, "2026-12-01T00:00:00Z"),
		SyncedAt: mustTime(t, "2026-11-02T10:00:00Z"), CacheFreshAt: mustTime(t, "2026-11-02T10:00:00Z")}
	snap(t, db, SourceOutlook, acctOutlook, "2026-11-02T09:30:00Z", "2026-11-02T09:30:00Z", twin(t, SourceOutlook, "2026-11-02T09:00:00Z"))
	snap(t, db, SourceTeams, acctTeams, "2026-11-02T09:30:00Z", "2026-11-02T09:30:00Z", twin(t, SourceTeams, "2026-11-02T09:00:00Z", func(e *Event) { e.ICalUID = "uid-1" }))
	recap := Recap{AccountID: acctTeams, CallID: "c1", MeetingStartAt: tp(t, "2026-11-02T13:00:00Z"), MeetingEndAt: tp(t, "2026-11-02T14:00:00Z")}
	tx, _ := db.BeginTx(ctx, nil)
	c, err := ApplyBatch(ctx, tx, Batch{Window: w, Recaps: []Recap{recap}}, ApplyOptions{}, mustTime(t, "2026-11-02T10:00:00Z"))
	_ = tx.Commit()
	if err != nil || c.Linked != 1 {
		t.Fatalf("the uid of the row that has one is used: %+v %v", c, err)
	}
}

func TestAgendaReportsCorruptUnknownFieldsAndLateLoadErrors(t *testing.T) {
	db := seedForPlans(t, false)
	from, to := mustTime(t, "2026-10-05T00:00:00Z"), mustTime(t, "2026-10-06T00:00:00Z")
	exec(t, db, `UPDATE calendar_source_events SET unknown_fields='bogus'`)
	if _, err := Agenda(ctx, db, AgendaQuery{From: from, To: to}); err == nil {
		t.Fatal("a name outside the vocabulary is damage")
	}
	// A column only the second step reads is gone: the candidate step succeeds, the load fails.
	db = seedForPlans(t, false)
	exec(t, db, `DROP INDEX calendar_source_events_composite`)
	exec(t, db, `ALTER TABLE calendar_source_events DROP COLUMN show_as`)
	if _, err := Agenda(ctx, db, AgendaQuery{From: from, To: to}); err == nil {
		t.Fatal("want an error from the second step")
	}
}
