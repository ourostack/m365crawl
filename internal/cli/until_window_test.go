package cli

import (
	"strings"
	"testing"
	"time"
)

// setZone makes loc the local zone of the reads, the zone a date alone is read in.
func setZone(t *testing.T, loc *time.Location) {
	t.Helper()
	oldLocal, oldDisplay := time.Local, displayZone
	time.Local, displayZone = loc, loc
	t.Cleanup(func() { time.Local, displayZone = oldLocal, oldDisplay })
}

func readJSON(t *testing.T, e *env, args ...string) map[string]any {
	t.Helper()
	code, out, errOut := e.run(append([]string{"--max-age", "0", "--json"}, args...)...)
	if code != 0 {
		t.Fatalf("%v: exit %d: %s", args, code, errOut)
	}
	return decode(t, out)
}

func idsOf(its []map[string]any) []string {
	var out []string
	for _, it := range its {
		out = append(out, it["id"].(string))
	}
	return out
}

// A date alone as --until includes that whole local day in every read, so the same --since D
// --until D selects the same day of chats, mail and the merged search (#257).
func TestUntilDateIncludesThatWholeDayInEveryRead(t *testing.T) {
	setZone(t, time.UTC)
	e := searchMailEnv(t)
	const day = "2023-11-14"
	from, to := time.Date(2023, 11, 14, 0, 0, 0, 0, time.UTC), time.Date(2023, 11, 15, 0, 0, 0, 0, time.UTC)
	want := 0
	for _, it := range items(t, readJSON(t, e, "messages", "--limit", "1000")) {
		at := itemAt(t, it)
		if !at.Before(from) && at.Before(to) {
			want++
		}
	}
	if want == 0 {
		t.Fatal("the Teams fixture has chats on " + day)
	}
	m := readJSON(t, e, "messages", "--since", day, "--until", day, "--limit", "1000")
	if got := len(items(t, m)); got != want {
		t.Fatalf("messages --since %s --until %s: %d messages, want the %d of that day (note %v)", day, day, got, want, m["note"])
	}
	if got := idsOf(items(t, readJSON(t, e, "mail", "list", "--since", day, "--until", day))); len(got) != 1 || !strings.HasSuffix(got[0], ":201") {
		t.Fatalf("mail list: %v", got)
	}
	if got := idsOf(items(t, searchJSON(t, e, "--source", "mail", "--since", day, "--until", day))); len(got) != 1 || !strings.HasSuffix(got[0], ":201") {
		t.Fatalf("search --source mail: %v", got)
	}
	src := searchJSON(t, e, "--since", day, "--until", day, "--limit", "1000")["sources"].(map[string]any)
	if src["chats"].(map[string]any)["matched"] != float64(want) || src["mail"].(map[string]any)["matched"] != float64(1) {
		t.Fatalf("search: %v", src)
	}
}

// Adjacent windows tile: a message stamped exactly at local midnight is in the window that starts
// there and not in the one that ends there, for a date alone and for a timestamp (#257).
func TestUntilWindowsCountAMidnightMessageOnce(t *testing.T) {
	e := searchMailEnv(t)
	var id string
	var at time.Time
	for _, it := range items(t, readJSON(t, e, "messages", "--limit", "1000")) {
		if at = itemAt(t, it); at.Nanosecond() == 0 {
			id = it["id"].(string)
			break
		}
	}
	if id == "" {
		t.Fatal("no fixture message on a whole second")
	}
	secs := at.Hour()*3600 + at.Minute()*60 + at.Second()
	offset := -secs
	if secs > 12*3600 {
		offset = 24*3600 - secs
	}
	zone := time.FixedZone("Midnight", offset)
	setZone(t, zone)
	local := at.In(zone)
	if local.Hour() != 0 || local.Minute() != 0 || local.Second() != 0 {
		t.Fatalf("not local midnight: %v", local)
	}
	copies := 0
	for _, it := range items(t, readJSON(t, e, "messages", "--limit", "1000")) {
		if it["id"] == id {
			copies++ // one per account that holds the conversation
		}
	}
	held := func(since, until string) int {
		n := 0
		for _, it := range items(t, readJSON(t, e, "messages", "--since", since, "--until", until, "--limit", "1000")) {
			if it["id"] == id {
				n++
			}
		}
		return n
	}
	day, before := local.Format("2006-01-02"), local.AddDate(0, 0, -1).Format("2006-01-02")
	if a, b := held(before, before), held(day, day); a != 0 || b != copies {
		t.Fatalf("date windows: %d in %s, %d in %s; want 0 and %d", a, before, b, day, copies)
	}
	ts := func(t time.Time) string { return t.UTC().Format(time.RFC3339) }
	if a, b := held(ts(local.AddDate(0, 0, -1)), ts(local)), held(ts(local), ts(local.AddDate(0, 0, 1))); a != 0 || b != copies {
		t.Fatalf("timestamp windows: %d before midnight, %d after; want 0 and %d", a, b, copies)
	}
}

// A day is one local calendar day, 23 or 25 hours long when daylight saving starts or ends, the
// same interval calendar --from D --days 1 reads.
func TestUntilDateIsOneLocalDayAcrossDaylightSaving(t *testing.T) {
	la, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}
	setZone(t, la)
	rt := &runtime{now: time.Now}
	for day, hours := range map[string]float64{"2026-03-08": 23, "2026-11-01": 25, "2026-10-29": 24} {
		since, until, err := rt.window("--since", day, "--until", day)
		if err != nil {
			t.Fatal(err)
		}
		if got := until.Sub(since).Hours(); got != hours {
			t.Errorf("%s: %v hours, want %v", day, got, hours)
		}
		from, to, err := (&calendarCmd{From: day, Days: 1}).bounds(rt)
		if err != nil || !from.Equal(since) || !to.Equal(until) {
			t.Errorf("%s: calendar reads %v to %v, --since/--until %v to %v (%v)", day, from, to, since, until, err)
		}
	}
}

// A window that ends at or before it starts can match nothing, and the result says so instead of
// only that nothing matched (#257).
func TestEmptyTimeWindowCarriesANotice(t *testing.T) {
	setZone(t, time.UTC)
	e := searchMailEnv(t)
	for _, args := range [][]string{
		{"messages", "--since", "2023-11-14", "--until", "2023-11-13"},
		{"messages", "--since", "2023-11-14T22:13:21Z", "--until", "2023-11-14T22:13:21Z"},
		{"mail", "list", "--since", "2023-11-14", "--until", "2023-11-13"},
		{"search", "--source", "all", "--since", "2023-11-15", "--until", "2023-11-14"},
		{"content", "list", "--modified-since", "2023-11-14", "--modified-until", "2023-11-13"},
	} {
		m := readJSON(t, e, args...)
		if len(items(t, m)) != 0 {
			t.Errorf("%v: %d items", args, len(items(t, m)))
		}
		notices, _ := m["notices"].([]any)
		found := false
		for _, n := range notices {
			found = found || strings.Contains(n.(string), "the time window is empty")
		}
		if !found {
			t.Errorf("%v: no empty-window notice: %v", args, notices)
		}
	}
	m := readJSON(t, e, "messages", "--since", "2023-11-14", "--until", "2023-11-14")
	if m["notices"] != nil {
		t.Fatalf("a one-day window is not empty: %v", m["notices"])
	}
	code, out, _ := e.run("--max-age", "0", "--format", "text", "messages", "--since", "2023-11-14", "--until", "2023-11-13")
	if code != 0 || !strings.Contains(out, "the time window is empty") {
		t.Fatalf("text output: exit %d: %s", code, out)
	}
}
