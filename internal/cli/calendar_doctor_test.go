package cli

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/teamscrawl/internal/store"
)

func TestOneUnit(t *testing.T) {
	for d, want := range map[time.Duration]string{90 * time.Minute: "1h", 49*time.Hour + 5*time.Minute: "49h", 5*time.Minute + 40*time.Second: "5m", 12 * time.Second: "12s", 0: "0s"} {
		if got := oneUnit(d); got != want {
			t.Errorf("oneUnit(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestCalendarCacheCheck(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	rt := &runtime{ctx: context.Background(), now: func() time.Time { return now }}
	old := readCalendarCache
	t.Cleanup(func() { readCalendarCache = old })
	for _, c := range []struct {
		name   string
		state  store.CalendarCache
		err    error
		warn   bool
		detail string
		fix    string
	}{
		{"unreadable", store.CalendarCache{}, errors.New("disk on fire"), true, "cannot read the calendar state: disk on fire", "teamscrawl sync"},
		{"no accounts", store.CalendarCache{}, nil, false, "no accounts archived yet", ""},
		{"no calendar database", store.CalendarCache{Accounts: 2, WithoutDatabase: 1, HasTables: true}, nil, true, "1 of 2 accounts have no Teams calendar database in the archive", "Open the Teams calendar once"},
		{"old archive", store.CalendarCache{Accounts: 1}, nil, true, "no calendar tables yet", "teamscrawl sync"},
		{"nothing derived", store.CalendarCache{Accounts: 1, HasTables: true}, nil, true, "no calendar has been derived yet", "Open the Teams calendar once"},
		{"stale", store.CalendarCache{Accounts: 1, HasTables: true, FreshAt: now.Add(-8 * 24 * time.Hour)}, nil, true, "was last fresh 192h ago", "Open the Teams calendar once"},
		{"fresh", store.CalendarCache{Accounts: 1, HasTables: true, FreshAt: now.Add(-3 * time.Hour)}, nil, false, "was fresh 3h ago", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			readCalendarCache = func(*store.Store, context.Context) (store.CalendarCache, error) { return c.state, c.err }
			got := rt.calendarCacheCheck(nil)
			if got.Name != "calendar_cache" || !got.OK || got.Warn != c.warn || !strings.Contains(got.Detail, c.detail) || !strings.Contains(got.Fix, c.fix) {
				t.Fatalf("check = %+v", got)
			}
		})
	}
}

// The doctor reports the calendar cache for a real archive too, and says nothing of any event.
func TestDoctorCalendarCacheOnTheFixtureArchive(t *testing.T) {
	e := newEnv(t)
	e.sync()
	code, cs, _ := doctorChecksFor(t, e)
	c := cs["calendar_cache"]
	if code != 0 || c["ok"] != true || c["warn"] != true || !strings.Contains(c["detail"].(string), "last fresh") {
		t.Fatalf("exit %d, calendar_cache = %v", code, c)
	}
}

func TestSyncTextTableHasCalendarRows(t *testing.T) {
	e := newEnv(t)
	code, out, errb := e.run("sync", "--format", "text", "--no-color")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errb)
	}
	for _, want := range []string{"calendar events", "calendar recaps", "calendar recap items"} {
		if !strings.Contains(out, want) {
			t.Errorf("sync table lacks %q:\n%s", want, out)
		}
	}
}
