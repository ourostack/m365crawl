package cli

import (
	goruntime "runtime"
	"time"

	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/syncer"
)

// listQuery is what a Teams list command asked for, so an empty result can say why it is empty:
// whether a filter narrowed it, its time bounds, and what "none" means for this command.
type listQuery struct {
	filtered     bool
	since, until time.Time
	none         string // the note of an unfiltered empty result; empty means noneOfThisKind
}

// noneOfThisKind is the note of an unfiltered empty list whose command says nothing more precise.
const noneOfThisKind = "the archive holds no items of this kind"

// teamsReadHere says whether this operating system has a Teams cache to read; a test seam.
var teamsReadHere = func() bool { return goruntime.GOOS == "darwin" || goruntime.GOOS == "windows" }

// messageWindowOf reads the span of the archived Teams messages; a test seam.
var messageWindowOf = (*store.Store).MessageWindow

// noteEmpty sets note on an empty list that has none yet, naming its cause: no archive, a platform
// with no Teams, an archive without Teams data, bounds outside the archived window, filters that
// matched nothing, or nothing of the kind archived.
func (rt *runtime) noteEmpty(st *store.Store, res result) error {
	l, ok := res.(*listResult)
	if !ok || l.Count > 0 || l.Note != "" {
		return nil
	}
	note, err := rt.emptyListNote(st)
	l.Note = note
	return err
}

func (rt *runtime) emptyListNote(st *store.Store) (string, error) {
	q := rt.query
	if st == nil {
		return "no archive yet: run m365crawl sync", nil
	}
	if note, err := rt.unknownAccountNote(st); note != "" || err != nil {
		return note, err
	}
	oldest, newest, err := messageWindowOf(st, rt.ctx, rt.account)
	if err != nil {
		return "", err
	}
	day := func(t time.Time) string { return t.In(displayZone).Format("2006-01-02") }
	switch {
	case newest.IsZero() && !teamsReadHere():
		return "Teams is read on macOS and Windows only, so this archive holds no Teams data on this operating system", nil
	case newest.IsZero():
		return "the archive holds no Teams messages yet: run m365crawl sync, and m365crawl doctor if it stays empty", nil
	case !q.since.IsZero() && q.since.After(newest), !q.until.IsZero() && q.until.Before(oldest):
		return "nothing matched: the time range is outside the archived Teams messages, which run from " + day(oldest) + " to " + day(newest), nil
	case q.filtered:
		return "nothing matched the filters", nil
	case q.none != "":
		return q.none, nil
	}
	return noneOfThisKind, nil
}

// unknownAccountNote is the note of a read whose --account names a Teams account the archive does
// not hold; empty when there is no --account or the archive holds it.
func (rt *runtime) unknownAccountNote(st *store.Store) (string, error) {
	if rt.account == nil {
		return "", nil
	}
	ok, err := st.HasAccount(rt.ctx, *rt.account)
	if err != nil || ok {
		return "", err
	}
	return "the archive holds no data for account " + rt.account.TenantID + "/" + rt.account.UserID + ": m365crawl whoami lists the accounts it holds", nil
}

// calendarSourcesOf is the test seam of the calendar sources an empty calendar list is explained by.
var calendarSourcesOf = (*store.Store).CalendarSources

// calendarWindow is the span the archived calendar covers, over every source of the accounts this
// read covers, and its live event count; zero times when no calendar is archived.
func (rt *runtime) calendarWindow(st *store.Store) (start, end time.Time, events int, err error) {
	res, err := calendarSourcesOf(st, rt.ctx, store.CalendarSourcesFilter{Account: rt.account, Now: rt.now(), ReadInterval: syncer.OutlookMinReadInterval})
	if err != nil {
		return start, end, 0, err
	}
	for _, r := range res.Rows {
		events += r.EventsLive
		if !r.WindowStart.IsZero() && (start.IsZero() || r.WindowStart.Before(start)) {
			start = r.WindowStart
		}
		if r.WindowEnd.After(end) {
			end = r.WindowEnd
		}
	}
	return start, end, events, nil
}

// calendarEmptyNote says why a calendar list of the range [from, to) is empty: no calendar
// archived, a range outside the archived window, a range partly uncovered, filters, or a range
// with nothing in it. what names the items: "event" or "action item".
func (rt *runtime) calendarEmptyNote(st *store.Store, from, to time.Time, gap, filtered bool, what string) (string, error) {
	if note, err := rt.unknownAccountNote(st); note != "" || err != nil {
		return note, err
	}
	start, end, _, err := rt.calendarWindow(st)
	if err != nil {
		return "", err
	}
	day := func(t time.Time) string { return t.In(displayZone).Format("2006-01-02") }
	switch {
	case start.IsZero():
		return "the archive holds no calendar yet: run m365crawl sync, and m365crawl calendar sources if it stays empty", nil
	case !to.After(start) || !from.Before(end):
		return "no " + what + ": the range is outside the archived calendar, which covers " + day(start) + " to " + day(end), nil
	case gap:
		return "no " + what + " in the covered part of the range, and part of it is not covered (uncovered_days), so a missing " + what + " is not evidence", nil
	case filtered:
		return "no " + what + " in the range matched the filters", nil
	}
	return "no " + what + " in the range", nil
}
