package store

import (
	"context"
	"testing"
	"time"

	"github.com/ourostack/teamscrawl/internal/calendar"
	"github.com/ourostack/teamscrawl/internal/outlookcal"
	"github.com/ourostack/teamscrawl/internal/teamscal"
)

func outlookEvent(allDay calendar.Tri, lm time.Time) calendar.Event {
	return calendar.Event{
		Source: calendar.SourceOutlook, AccountID: "outlook/Main", SourceID: "AA11", GlobalID: "AA11", ICalUID: "AA11", SeriesKey: "AA11",
		EventType: calendar.EventSingle, Start: time.Date(2031, 3, 5, 9, 30, 0, 0, time.UTC), End: time.Date(2031, 3, 5, 10, 0, 0, 0, time.UTC),
		LastModified: &lm, Subject: "Fixture", AllDay: allDay, UnknownDeclared: true,
		Unknown: []calendar.Field{calendar.FieldUTCOffset, calendar.FieldShortJoinURL, calendar.FieldRooms, calendar.FieldAttachments,
			calendar.FieldCategories, calendar.FieldRecurrence, calendar.FieldReminder, calendar.FieldJoinURL, calendar.FieldDialIn,
			calendar.FieldMeetingChatID, calendar.FieldBody, calendar.FieldAttendees, calendar.FieldTimeZone, calendar.FieldTimeZoneIANA,
			calendar.FieldShowAs, calendar.FieldResponse},
	}
}

func outlookRun(CalendarResult) Run {
	return Run{StartedAt: base, FinishedAt: base, Source: "outlook|Main", Status: "ok"}
}

func commitOutlook(t *testing.T, s *Store, zone *time.Location, stamp string, events ...calendar.Event) CalendarResult {
	t.Helper()
	res, err := s.CommitOutlook(context.Background(), OutlookBatch{Account: "outlook/Main", Events: events, FreshAt: base, At: base, Zone: zone, Stamp: stamp}, outlookRun)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func applyOutlookBatch(t *testing.T, s *Store, stamp string, events ...calendar.Event) CalendarResult {
	t.Helper()
	return commitOutlook(t, s, time.UTC, stamp, events...)
}

func allDayColumn(t *testing.T, s *Store) (v *int) {
	t.Helper()
	if err := s.db.QueryRow(`select all_day from calendar_source_events where source='outlook' and source_id='AA11'`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

// A mapper that stored an all-day flag as false is replaced by one that says unknown only when the
// stamp changed: Capture keeps a stored flag over an unknown one, so the stamp makes the batch
// blank its events first.
func TestOutlookMapperBumpMakesStoredAllDayUnknown(t *testing.T) {
	s := newStore(t)
	v1, v3 := OutlookStamp(1, time.UTC), OutlookStamp(outlookcal.MapperVersion, time.UTC)
	lm := base
	applyOutlookBatch(t, s, v1, outlookEvent(calendar.TriFalse, lm))
	if v := allDayColumn(t, s); v == nil || *v != 0 {
		t.Fatalf("stored all-day %v, want false", v)
	}
	// The same stamp: the unknown flag does not replace the stored false.
	applyOutlookBatch(t, s, v1, outlookEvent(calendar.TriUnknown, lm))
	if v := allDayColumn(t, s); v == nil || *v != 0 {
		t.Fatalf("an unknown flag replaced the stored one: %v", v)
	}
	// A new stamp re-derives the event: the stored false becomes unknown, and the row keeps its key.
	res := applyOutlookBatch(t, s, v3, outlookEvent(calendar.TriUnknown, lm))
	if v := allDayColumn(t, s); v != nil {
		t.Fatalf("stored all-day %v, want unknown", *v)
	}
	if res.Counts.Events.Seen != 1 || res.Counts.Events.Inserted != 0 {
		t.Fatalf("%+v", res.Counts)
	}
	var rows, covered int
	_ = s.db.QueryRow(`select count(*) from calendar_source_events where source='outlook'`).Scan(&rows)
	_ = s.db.QueryRow(`select count(*) from calendar_covered_days where source='outlook'`).Scan(&covered)
	if rows != 1 || covered != 1 {
		t.Fatalf("%d rows, %d covered days", rows, covered)
	}
}

func TestOutlookZoneChangeTakesCoveredDaysAgain(t *testing.T) {
	s := newStore(t)
	applyOutlookBatch(t, s, OutlookStamp(3, time.UTC), outlookEvent(calendar.TriFalse, base))
	east := time.FixedZone("east", 15*3600) // 09:30 UTC is already the next day
	// The store no longer holds the first event: its day is taken again from the archive, in the new zone.
	other := outlookEvent(calendar.TriFalse, base)
	other.SourceID, other.GlobalID, other.ICalUID, other.SeriesKey = "DD44", "DD44", "DD44", "DD44"
	other.Start, other.End = time.Date(2031, 3, 10, 9, 30, 0, 0, time.UTC), time.Date(2031, 3, 10, 10, 0, 0, 0, time.UTC)
	commitOutlook(t, s, east, OutlookStamp(3, east), other)
	var days string
	if err := s.db.QueryRow(`select group_concat(day) from (select day from calendar_covered_days where source='outlook' order by day)`).Scan(&days); err != nil || days != "2031-03-06,2031-03-11" {
		t.Fatalf("%q %v", days, err)
	}
}

func TestOutlookState(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	st, err := s.OutlookState(ctx, "Main", "outlook|Main", "outlook/Main")
	if err != nil || !st.LastAttempt.IsZero() || st.Fingerprint != "" || st.Omissions != nil || st.HoldsEvents {
		t.Fatalf("%+v %v", st, err)
	}
	if err := s.SetOutlookLastAttempt(ctx, "Main", base); err != nil {
		t.Fatal(err)
	}
	applyOutlookBatch(t, s, OutlookStamp(3, time.UTC), outlookEvent(calendar.TriFalse, base))
	if err := s.RecordRun(ctx, Run{StartedAt: base, FinishedAt: base, Source: "outlook|Main", Fingerprint: "fp", Status: "ok_with_omissions", Omissions: map[string]int{"calendar_unmapped": 2}}); err != nil {
		t.Fatal(err)
	}
	st, err = s.OutlookState(ctx, "Main", "outlook|Main", "outlook/Main")
	if err != nil || !st.LastAttempt.Equal(base) || st.Fingerprint != "fp" || st.Omissions["calendar_unmapped"] != 2 || !st.HoldsEvents {
		t.Fatalf("%+v %v", st, err)
	}
	if _, err := s.db.Exec(`update sync_runs set omissions_json='{' where source='outlook|Main'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.OutlookState(ctx, "Main", "outlook|Main", "outlook/Main"); err == nil {
		t.Fatal("a damaged omissions row is an error")
	}
}

// Every database call of a commit and of a state read can fail, and a failed commit leaves nothing.
func TestOutlookFailuresRollBack(t *testing.T) {
	seed := func(t *testing.T, s *Store) {
		applyOutlookBatch(t, s, OutlookStamp(1, time.UTC), outlookEvent(calendar.TriFalse, base))
	}
	east := time.FixedZone("east", 15*3600)
	master := outlookEvent(calendar.TriFalse, base)
	master.SourceID, master.GlobalID, master.ICalUID, master.SeriesKey, master.EventType = "BB22", "BB22", "BB22", "BB22", calendar.EventMaster
	allDay := outlookEvent(calendar.TriTrue, base)
	allDay.SourceID, allDay.GlobalID, allDay.ICalUID, allDay.SeriesKey = "CC33", "CC33", "CC33", "CC33"
	allDay.Start, allDay.End, allDay.StartDate, allDay.EndDate = time.Date(2031, 3, 6, 0, 0, 0, 0, time.UTC), time.Date(2031, 3, 7, 0, 0, 0, 0, time.UTC), "2031-03-06", "2031-03-07"
	t.Run("commit", func(t *testing.T) {
		// A new stamp and a new zone: the stamp read, the covered-day delete, the blank, the batch and the run row.
		sweepCalendar(t, seed, func(ctx context.Context, s *Store) error {
			_, err := s.CommitOutlook(ctx, OutlookBatch{Account: "outlook/Main", Events: []calendar.Event{outlookEvent(calendar.TriUnknown, base), master, allDay}, FreshAt: base, At: base, Zone: east, Stamp: OutlookStamp(3, east)}, outlookRun)
			return err
		})
	})
	t.Run("state", func(t *testing.T) {
		sweepReadFaults(t, func(t *testing.T, s *Store) {
			seed(t, s)
			if err := s.RecordRun(context.Background(), Run{StartedAt: base, FinishedAt: base, Source: "outlook|Main", Fingerprint: "fp", Status: "ok_with_omissions", Omissions: map[string]int{"calendar_unmapped": 2}}); err != nil {
				t.Fatal(err)
			}
		}, func(ctx context.Context, s *Store) error {
			_, err := s.OutlookState(ctx, "Main", "outlook|Main", "outlook/Main")
			return err
		}, 3)
	})
}

func storedSubject(t *testing.T, s *Store) string {
	t.Helper()
	var subj string
	if err := s.db.QueryRow(`select subject from calendar_source_events where source='outlook' and source_id='AA11'`).Scan(&subj); err != nil {
		t.Fatal(err)
	}
	return subj
}

// A mapper change that newly refuses an event keeps the stored copy: the core writes nothing for a
// refused event, so blanking it first would erase the row.
func TestOutlookBlankSparesRefusedEvents(t *testing.T) {
	s := newStore(t)
	applyOutlookBatch(t, s, OutlookStamp(1, time.UTC), outlookEvent(calendar.TriFalse, base))
	bad := outlookEvent(calendar.TriFalse, base)
	bad.UnknownDeclared = false // the core refuses it
	res := applyOutlookBatch(t, s, OutlookStamp(3, time.UTC), bad)
	if res.Counts.Refused != 1 || res.Omissions[OmitCalendarRefused] != 1 {
		t.Fatalf("%+v %v", res.Counts, res.Omissions)
	}
	if got := storedSubject(t, s); got != "Fixture" {
		t.Fatalf("the refused event's stored subject is %q", got)
	}
}

// A Teams rebuild under new scrub rules blanks Teams rows only; Outlook has no records to rebuild from.
func TestTeamsRebuildLeavesOutlookRows(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	applyOutlookBatch(t, s, OutlookStamp(3, time.UTC), outlookEvent(calendar.TriFalse, base))
	if _, err := s.EnsureCalendar(ctx, time.UTC, base); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`update meta set value=? where key='calendar_derivation'`, calendarDerivation(teamscal.MapperVersion, zoneStamp(time.UTC), "0.old")); err != nil {
		t.Fatal(err)
	}
	if res, err := s.EnsureCalendar(ctx, time.UTC, base.Add(time.Hour)); err != nil || res == nil {
		t.Fatal(res, err)
	}
	if got := storedSubject(t, s); got != "Fixture" {
		t.Fatalf("a Teams rebuild blanked an Outlook row: %q", got)
	}
}

func TestOutlookFailureIsRemembered(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	f := &OutlookFailure{Code: "outlook_store_version", Message: "m", Fix: "f", Exit: 3}
	if err := s.SetOutlookFailure(ctx, "outlook/Main", f); err != nil {
		t.Fatal(err)
	}
	if st, err := s.OutlookState(ctx, "Main", "outlook|Main", "outlook/Main"); err != nil || st.Failure == nil || *st.Failure != *f {
		t.Fatalf("%+v %v", st.Failure, err)
	}
	// A successful commit forgets it.
	applyOutlookBatch(t, s, OutlookStamp(3, time.UTC), outlookEvent(calendar.TriFalse, base))
	if st, _ := s.OutlookState(ctx, "Main", "outlook|Main", "outlook/Main"); st.Failure != nil {
		t.Fatal("the failure outlived a good read")
	}
	if err := s.SetOutlookFailure(ctx, "outlook/Main", f); err != nil {
		t.Fatal(err)
	}
	if err := s.SetOutlookFailure(ctx, "outlook/Main", nil); err != nil {
		t.Fatal(err)
	}
	if st, _ := s.OutlookState(ctx, "Main", "outlook|Main", "outlook/Main"); st.Failure != nil {
		t.Fatal("not forgotten")
	}
	if _, err := s.db.Exec(`insert into meta(key, value) values('outlook_failure:outlook/Main', '{')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.OutlookState(ctx, "Main", "outlook|Main", "outlook/Main"); err == nil {
		t.Fatal("a damaged failure row is an error")
	}
}

// Outlook writes no recap row. The recap tables have no source column, so the Teams blank is
// scoped to Teams accounts by the "outlook/" prefix; if Outlook ever writes recaps, this fails and
// the scoping must be revisited.
func TestOutlookWritesNoRecaps(t *testing.T) {
	s := newStore(t)
	applyOutlookBatch(t, s, OutlookStamp(3, time.UTC), outlookEvent(calendar.TriFalse, base))
	var n int
	if err := s.db.QueryRow(`select (select count(*) from calendar_recaps) + (select count(*) from calendar_recap_items)`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("Outlook wrote %d recap rows: %v", n, err)
	}
}

// The Teams blank leaves a recap row of an Outlook account alone.
func TestTeamsBlankSparesOutlookAccountRecaps(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	for _, a := range []string{"outlook/Main", "tenant/user"} {
		if _, err := s.db.Exec(`insert into calendar_recaps(account_id, call_id, ical_uid, first_seen_at, updated_at) values(?, 'c1', 'UID', '2031-01-01T00:00:00Z', '2031-01-01T00:00:00Z')`, a); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.EnsureCalendar(ctx, time.UTC, base); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`update meta set value=? where key='calendar_derivation'`, calendarDerivation(teamscal.MapperVersion, zoneStamp(time.UTC), "0.old")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnsureCalendar(ctx, time.UTC, base.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	var out, teams string
	_ = s.db.QueryRow(`select ical_uid from calendar_recaps where account_id='outlook/Main'`).Scan(&out)
	_ = s.db.QueryRow(`select ical_uid from calendar_recaps where account_id='tenant/user'`).Scan(&teams)
	if out != "UID" || teams != "" {
		t.Fatalf("outlook %q teams %q", out, teams)
	}
}

func addTeamsAccount(t *testing.T, s *Store, account string) {
	t.Helper()
	if _, err := s.db.Exec(`insert into calendar_sources(source, account_id, window_start, window_end, synced_at, cache_fresh_at) values('teams', ?, ?, ?, ?, ?)`,
		account, "2026-11-01T00:00:00.000Z", "2026-12-01T00:00:00.000Z", "2026-11-02T00:00:00.000Z", "2026-11-02T00:00:00.000Z"); err != nil {
		t.Fatal(err)
	}
}

func inEffect(t *testing.T, s *Store, account, principal string) bool {
	t.Helper()
	ok, err := s.OutlookLinkInEffect(context.Background(), account, principal)
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

// An explicit link is idempotent, its rules come back as usage errors, and OutlookLinkInEffect
// follows it through every form of the question.
func TestSetOutlookLink(t *testing.T) {
	ctx, s := context.Background(), newStore(t)
	const teams, other = "t1/u1", "t2/u2"
	at := base

	if err := s.SetOutlookLink(ctx, "outlook/Main", teams, at); err == nil {
		t.Fatal("a link to a Teams account the archive has not seen was accepted")
	}
	addTeamsAccount(t, s, teams)
	addTeamsAccount(t, s, other)
	if !inEffect(t, s, "", "") || !inEffect(t, s, "outlook/Main", "") || inEffect(t, s, "", teams) || inEffect(t, s, "outlook/Main", teams) {
		t.Fatal("nothing is linked yet")
	}
	if err := s.SetOutlookLink(ctx, "outlook/Main", "", at); err != nil {
		t.Fatalf("an unlink of an account with no link is not an error: %v", err)
	}

	if err := s.SetOutlookLink(ctx, "outlook/Main", teams, at); err != nil {
		t.Fatal(err)
	}
	if !inEffect(t, s, "", teams) || !inEffect(t, s, "outlook/Main", teams) || inEffect(t, s, "outlook/Main", other) || inEffect(t, s, "outlook/Second", teams) {
		t.Fatal("the link is not seen")
	}
	if inEffect(t, s, "", "") || inEffect(t, s, "outlook/Main", "") || !inEffect(t, s, "outlook/Second", "") {
		t.Fatal("an unlink is pending for the linked account only")
	}
	var linkedAt string
	if err := s.db.QueryRow(`select linked_at from calendar_account_links`).Scan(&linkedAt); err != nil {
		t.Fatal(err)
	}
	if err := s.SetOutlookLink(ctx, "outlook/Main", teams, at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	var again string
	if err := s.db.QueryRow(`select linked_at from calendar_account_links`).Scan(&again); err != nil || again != linkedAt {
		t.Fatalf("a link that was already in place was written again: %q %q %v", linkedAt, again, err)
	}
	if err := s.SetOutlookLink(ctx, "outlook/Second", teams, at); err == nil {
		t.Fatal("a second Outlook account was linked to one Teams account")
	}

	if err := s.SetOutlookLink(ctx, "outlook/Main", "", at); err != nil {
		t.Fatal(err)
	}
	if !inEffect(t, s, "", "") || inEffect(t, s, "", teams) {
		t.Fatal("the link was not ended")
	}
	if err := s.SetOutlookLink(ctx, "outlook/Main", other, at); err != nil {
		t.Fatal(err)
	}
}

func TestOutlookLinkFailuresOnABrokenArchive(t *testing.T) {
	ctx, s := context.Background(), newStore(t)
	if _, err := s.db.Exec(`drop table calendar_account_links`); err != nil {
		t.Fatal(err)
	}
	if err := s.SetOutlookLink(ctx, "outlook/Main", "", base); err == nil {
		t.Fatal("no error")
	}
	if _, err := s.OutlookLinkInEffect(ctx, "", ""); err == nil {
		t.Fatal("no error")
	}
}
