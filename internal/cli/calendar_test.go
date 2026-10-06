package cli

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/teamscrawl/internal/calendar"
	"github.com/ourostack/teamscrawl/internal/store"
)

func TestParseRange(t *testing.T) {
	loc := time.FixedZone("T", -7*3600)
	now := time.Date(2026, 10, 6, 15, 30, 0, 0, time.UTC) // 08:30 on the 6th in loc
	midnight := time.Date(2026, 10, 6, 0, 0, 0, 0, loc)
	ok := map[string]time.Time{
		"today":                midnight,
		"yesterday":            midnight.AddDate(0, 0, -1),
		"tomorrow":             midnight.AddDate(0, 0, 1),
		"2026-10-01":           time.Date(2026, 10, 1, 0, 0, 0, 0, loc),
		"2026-10-01T12:00:00Z": time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC),
		"+3d":                  now.Add(72 * time.Hour),
		"-1d":                  now.Add(-24 * time.Hour),
		"+2w":                  now.Add(14 * 24 * time.Hour),
		"+90m":                 now.Add(90 * time.Minute),
		"+1.5h":                now.Add(90 * time.Minute),
	}
	for in, want := range ok {
		got, err := parseRange("--from", in, now, loc)
		if err != nil || !got.Equal(want) || got.Location() != loc {
			t.Errorf("%s: %v %v, want %v", in, got, err, want)
		}
	}
	for in, msg := range map[string]string{"7d": "unsigned duration", "banana": "cannot read", "": "cannot read", "+3x": "cannot read"} {
		_, err := parseRange("--to", in, now, loc)
		if err == nil || !strings.Contains(err.Error(), msg) || !strings.Contains(err.Error(), "--to") {
			t.Errorf("%q: %v", in, err)
		}
	}
	// A number too large for a duration is not a time either.
	if _, err := parseRange("--from", "+"+strings.Repeat("9", 400)+"d", now, loc); err == nil {
		t.Error("an absurd offset was accepted")
	}
}

func calEnv(t *testing.T) *env {
	t.Helper()
	e := textEnv(t)
	e.sync()
	return e
}

func agenda(t *testing.T, e *env, args ...string) map[string]any {
	t.Helper()
	code, out, errOut := e.run(append([]string{"--max-age", "0", "calendar"}, args...)...)
	if code != 0 {
		t.Fatalf("calendar %v: exit %d: %s", args, code, errOut)
	}
	return decode(t, out)
}

func itemBySubject(t *testing.T, m map[string]any, subject string) map[string]any {
	t.Helper()
	for _, it := range items(t, m) {
		if it["subject"] == subject {
			return it
		}
	}
	t.Fatalf("no %q in %v", subject, m)
	return nil
}

func keysOf(m map[string]any) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestCalendarAgendaShape(t *testing.T) {
	e := calEnv(t)
	m := agenda(t, e, "--from", "2023-11-22", "--days", "1", "--account", tenantA+"/"+userA)
	wide := agenda(t, e, "--from", "2023-11-01", "--to", "2023-12-31", "--account", tenantA+"/"+userA)
	for _, k := range []string{"items", "count", "truncated", "coverage_gap", "coverage_as_of", "range", "archive_age_seconds"} {
		if _, ok := m[k]; !ok {
			t.Errorf("the agenda has no %q: %v", k, keysOf(m))
		}
	}
	r := m["range"].(map[string]any)
	if r["from"] != "2023-11-22T00:00:00Z" || r["to"] != "2023-11-23T00:00:00Z" || r["zone"] != "UTC" {
		t.Errorf("range %v", r)
	}
	standup := itemBySubject(t, m, "Fixture standup")
	for _, k := range []string{"event_id", "event_key", "account_id", "tenant_id", "user_id", "sources", "start", "end", "status", "detail_level", "unknown_fields"} {
		if _, ok := standup[k]; !ok {
			t.Errorf("an item has no %q: %v", k, keysOf(standup))
		}
	}
	if srcs := standup["sources"].([]any); len(srcs) != 1 || srcs[0] != "teams" {
		t.Errorf("sources %v", srcs)
	}
	if !strings.HasPrefix(standup["event_id"].(string), "ev_") || standup["account_id"] != tenantA+"/"+userA || standup["tenant_id"] != tenantA || standup["user_id"] != userA {
		t.Errorf("identity %v", standup)
	}
	if standup["has_recap"] != true || standup["recording_count"] != float64(3) {
		t.Errorf("recap %v %v", standup["has_recap"], standup["recording_count"])
	}
	// A known false flag is printed; an unknown flag has no key and is named in unknown_fields.
	known, unknown := 0, 0
	for _, it := range items(t, wide) {
		for _, f := range []string{"all_day", "cancelled", "is_organizer", "is_private", "is_online_meeting", "has_attachments"} {
			_, present := it[f]
			named := false
			for _, u := range asStrings(it["unknown_fields"]) {
				named = named || u == f
			}
			if present == named {
				t.Errorf("%s of %v: present %v, named unknown %v", f, it["subject"], present, named)
			}
			if present {
				known++
			} else {
				unknown++
			}
		}
	}
	if known == 0 || unknown == 0 {
		t.Errorf("the fixture should hold both known and unknown flags: %d %d", known, unknown)
	}
}

func asStrings(v any) []string {
	var out []string
	if l, ok := v.([]any); ok {
		for _, s := range l {
			out = append(out, s.(string))
		}
	}
	return out
}

func TestCalendarAgendaFlags(t *testing.T) {
	e := calEnv(t)
	whole := "--from 2023-11-01 --to 2023-12-31"
	all := agenda(t, e, strings.Fields(whole)...)
	if all["count"] != float64(22) || all["coverage_gap"] != true {
		t.Fatalf("count %v gap %v", all["count"], all["coverage_gap"])
	}
	one := agenda(t, e, "--from", "2023-11-22", "--days", "1")
	if one["coverage_gap"] != false {
		t.Errorf("a covered day has no gap: %v", one["coverage_gap"])
	}
	if got := agenda(t, e, "--from", "2023-11-01", "--to", "2023-12-31", "--query", "STANDUP"); got["count"] != float64(6) {
		t.Errorf("--query ignores case: %v", got["count"])
	}
	lim := agenda(t, e, "--from", "2023-11-01", "--to", "2023-12-31", "--limit", "3")
	if lim["truncated"] != true || lim["count"] != float64(3) || lim["total"] != float64(22) {
		t.Errorf("limit: %v %v %v", lim["truncated"], lim["count"], lim["total"])
	}
	fields := agenda(t, e, "--from", "2023-11-22", "--days", "1", "--fields", "event_id,subject")
	for _, it := range items(t, fields) {
		if len(it) != 2 {
			t.Errorf("--fields kept %v", it)
		}
	}
	// Flags that show more events are accepted.
	for _, f := range []string{"--include-cancelled", "--include-declined", "--include-masters", "--include-removed"} {
		if got := agenda(t, e, "--from", "2023-11-22", "--days", "1", f); got["count"] == nil {
			t.Errorf("%s: %v", f, got)
		}
	}
	// Another zone's range prints its own zone and bounds.
	rng := agenda(t, e, "--from", "2023-11-22T00:00:00-08:00", "--to", "2023-11-23T00:00:00-08:00")["range"].(map[string]any)
	if rng["from"] != "2023-11-22T08:00:00Z" {
		t.Errorf("range %v", rng) // read in displayZone (UTC), which is where the bounds are shown
	}
}

func TestCalendarAgendaUsageErrors(t *testing.T) {
	e := calEnv(t)
	for name, c := range map[string]struct {
		args []string
		msg  string
	}{
		"to and days":   {[]string{"--to", "2023-11-23", "--days", "2"}, "use one"},
		"negative days": {[]string{"--days=-1"}, "at least 1"},
		"empty range":   {[]string{"--from", "2023-11-23", "--to", "2023-11-22"}, "after --from"},
		"bad from":      {[]string{"--from", "soon"}, "cannot read"},
		"bad to":        {[]string{"--from", "2023-11-22", "--to", "later"}, "cannot read"},
		"unsigned":      {[]string{"--from", "7d"}, "unsigned"},
		"bad limit":     {[]string{"--limit", "0"}, "limit"},
		"event field":   {[]string{"--fields", "attendees"}, "calendar event"},
		"unknown field": {[]string{"--fields", "nope"}, "nope"},
	} {
		code, _, errOut := e.run(append([]string{"--max-age", "0", "calendar"}, c.args...)...)
		if code != 2 || !strings.Contains(errOut, c.msg) {
			t.Errorf("%s: exit %d %s", name, code, errOut)
		}
	}
	// The command named in a fields error is the one the reader typed.
	_, _, errOut := e.run("--max-age", "0", "calendar", "--fields", "attendees")
	if !strings.Contains(errOut, "teamscrawl calendar event <event_id> --fields attendees") {
		t.Errorf("fix: %s", errOut)
	}
}

func eventDoc(t *testing.T, e *env, ref string, args ...string) map[string]any {
	t.Helper()
	code, out, errOut := e.run(append([]string{"--max-age", "0", "calendar", "event", ref}, args...)...)
	if code != 0 {
		t.Fatalf("calendar event %s: exit %d: %s", ref, code, errOut)
	}
	return decode(t, out)
}

func TestCalendarEventDetail(t *testing.T) {
	e := calEnv(t)
	m := agenda(t, e, "--from", "2023-11-20", "--days", "1", "--account", tenantA+"/"+userA)
	review := itemBySubject(t, m, "Fixture planning review")
	id := review["event_id"].(string)
	d := eventDoc(t, e, id)
	for _, k := range []string{"attendees", "response_counts", "recaps", "chat", "series", "archive_age_seconds"} {
		if _, ok := d[k]; !ok {
			t.Errorf("the rich event has no %q: %v", k, keysOf(d))
		}
	}
	recaps := d["recaps"].([]any)
	if len(recaps) == 0 || len(recaps[0].(map[string]any)["action_items"].([]any)) == 0 {
		t.Errorf("recaps %v", recaps)
	}
	// The key, a unique prefix of the id, and the same event printed twice by the agenda all name it.
	for _, ref := range []string{review["event_key"].(string), id[:len(id)-2]} {
		if got := eventDoc(t, e, ref, "--account", tenantA+"/"+userA)["event_id"]; got != id {
			t.Errorf("%s: %v", ref, got)
		}
	}
	// --fields keeps the keys asked for, in that order.
	f := eventDoc(t, e, id, "--fields", "subject,attendees")
	if _, ok := f["event_id"]; ok || f["subject"] != "Fixture planning review" || f["attendees"] == nil {
		t.Errorf("fields: %v", f)
	}
	// --max-text cuts long text and says so.
	cut := eventDoc(t, e, id, "--max-text", "5")
	if cut["text_truncated"] != true {
		t.Errorf("max-text: %v", cut["text_truncated"])
	}
	// A thin event has a basic detail level and no attendee list.
	thin := itemBySubject(t, m, "Fixture planning review")
	_ = thin
	thinDay := agenda(t, e, "--from", "2023-11-21", "--days", "1", "--account", tenantA+"/"+userA)
	thinID := itemBySubject(t, thinDay, "Fixture thin event")["event_id"].(string)
	td := eventDoc(t, e, thinID)
	if td["detail_level"] != "basic" || td["attendees"] != nil {
		t.Errorf("thin event: %v %v", td["detail_level"], td["attendees"])
	}
}

func TestCalendarEventErrors(t *testing.T) {
	e := calEnv(t)
	code, _, errOut := e.run("--max-age", "0", "calendar", "event", "ev_nothing")
	if code != 2 || !strings.Contains(errOut, "no calendar event matches") {
		t.Errorf("not found: %d %s", code, errOut)
	}
	// "ev_" names every event.
	code, _, errOut = e.run("--max-age", "0", "calendar", "event", "ev_")
	if code != 2 || !strings.Contains(errOut, "matches") || !strings.Contains(errOut, "full event_id") {
		t.Errorf("ambiguous: %d %s", code, errOut)
	}
	code, _, errOut = e.run("--max-age", "0", "calendar", "event", "ev_x", "--fields", "nope")
	if code != 2 || !strings.Contains(errOut, "valid keys") {
		t.Errorf("fields: %d %s", code, errOut)
	}
}

func TestCalendarOldArchiveAndNoArchive(t *testing.T) {
	e := calEnv(t)
	e.exec(`drop table calendar_recaps`)
	for _, args := range [][]string{{"calendar"}, {"calendar", "event", "ev_x"}} {
		code, out, errOut := e.run(append([]string{"--max-age", "0"}, args...)...)
		m := decode(t, out)
		if code != 0 || m["needs_sync"] != true || !strings.Contains(out, "run teamscrawl sync") {
			t.Errorf("%v on an old archive: %d %s %s", args, code, out, errOut)
		}
		if _, has := m["items"]; args[len(args)-1] == "calendar" && !has {
			t.Errorf("an empty agenda still has items: %s", out)
		}
	}
	empty := newEnv(t)
	for _, args := range [][]string{{"calendar"}, {"calendar", "event", "ev_x"}} {
		code, out, errOut := empty.run(append([]string{"--max-age", "0"}, args...)...)
		if code != 0 || decode(t, out)["needs_sync"] != true {
			t.Errorf("%v with no archive: %d %s %s", args, code, out, errOut)
		}
	}
}

func TestCalendarTextGoldens(t *testing.T) {
	e := calEnv(t)
	cases := []struct {
		name string
		args []string
	}{
		{"calendar", []string{"calendar", "--from", "2023-11-22", "--days", "2", "--account", tenantA + "/" + userA}},
		{"calendar_gap", []string{"calendar", "--from", "2023-11-01", "--to", "2023-11-03"}},
		{"calendar_fields", []string{"--fields", "subject,start,status", "calendar", "--from", "2023-11-22", "--days", "1"}},
		{"calendar_event", []string{"calendar", "event", "STANDUP"}},
	}
	for _, c := range cases {
		args := c.args
		if c.name == "calendar_event" {
			m := agenda(t, e, "--from", "2023-11-20", "--days", "1", "--account", tenantA+"/"+userA)
			args = []string{"calendar", "event", itemBySubject(t, m, "Fixture planning review")["event_id"].(string)}
		}
		for _, color := range []bool{false, true} {
			t.Setenv("CLICOLOR_FORCE", "")
			suffix := "plain"
			if color {
				suffix = "color"
				t.Setenv("CLICOLOR_FORCE", "1")
			}
			code, out, errOut := e.run(append([]string{"--format", "text", "--max-age", "0"}, args...)...)
			if code != 0 {
				t.Fatalf("%s: exit %d: %s", c.name, code, errOut)
			}
			checkGolden(t, c.name+"."+suffix, e.scrub(out))
		}
	}
}

func TestCalendarStatusAndLocalTime(t *testing.T) {
	cancelled := calendar.Event{Cancelled: calendar.TriTrue}
	declined := calendar.Event{Response: "declined"}
	if calendarStatus(cancelled) != "cancelled" || calendarStatus(declined) != "declined" || calendarStatus(calendar.Event{}) != "confirmed" {
		t.Error("status")
	}
	at := time.Date(2026, 10, 6, 15, 0, 0, 0, time.UTC)
	for name, c := range map[string]struct {
		t    time.Time
		iana string
		want string
	}{"zone": {at, "Asia/Kolkata", "2026-10-06T20:30:00+05:30"}, "unknown zone": {at, "Not/AZone", ""}, "no zone": {at, "", ""}, "zero": {time.Time{}, "UTC", ""}} {
		if got := localTime(c.t, c.iana); got != c.want {
			t.Errorf("%s: %q", name, got)
		}
	}
	if !asOf(nil).IsZero() || !asOf(&at).Equal(at) {
		t.Error("asOf")
	}
	local := time.Date(2026, 10, 6, 0, 0, 0, 0, time.Local)
	if z := rangeOf(local, local.Add(time.Hour)).Zone; z == "Local" || z == "" {
		t.Errorf("a local range names its zone: %q", z)
	}
}

// An item from two sources carries the tenant and user of its Teams principal, the fields one
// source filled for the other, its rooms and the as-of times.
func TestItemOfFilledRoomsAndSources(t *testing.T) {
	asAt := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	lat := 1.5
	row := store.CalendarRow{
		EventID: "ev_x", DetailLevel: calendar.DetailStale,
		AgendaItem: calendar.AgendaItem{
			Key: "k", Principal: "tenant/user",
			Merged: calendar.Merged{
				Sources: []calendar.Source{calendar.SourceTeams, calendar.SourceOutlook}, RemovedBy: []calendar.Source{calendar.SourceOutlook}, Removed: true,
				Filled: []calendar.Fill{{Field: "location", Source: calendar.SourceOutlook, AsOf: &asAt}, {Field: "body"}},
				Event: calendar.Event{
					Subject: "S", Start: asAt, End: asAt.Add(time.Hour), TimeZoneIANA: "UTC", DetailAsOf: &asAt, LastModified: &asAt,
					AttendeesJSON: `[{"name":"a"},{"name":"b"}]`,
				},
			},
		},
		Rooms: []calendar.Room{{Name: "Room", Kind: "conferenceRoom", Latitude: &lat}, {Name: "Text", Kind: calendar.RoomText}},
	}
	it := itemOf(row)
	if it.TenantID != "tenant" || it.UserID != "user" || len(it.FilledFields) != 2 || it.FilledFields[0].From != "outlook" || it.FilledFields[0].AsOf != asAt || !it.FilledFields[1].AsOf.IsZero() {
		t.Errorf("%+v", it)
	}
	if len(it.Rooms) != 2 || it.RoomsAsOf != asAt || it.AttendeeCount == nil || *it.AttendeeCount != 2 || !it.Removed || len(it.RemovedBy) != 1 {
		t.Errorf("%+v", it)
	}
	// A room list the source never stated is not printed, whatever text rooms the location gives.
	row.Unknown = []calendar.Field{calendar.FieldRooms}
	if it = itemOf(row); len(it.Rooms) != 0 || !it.RoomsAsOf.IsZero() {
		t.Errorf("rooms next to unknown rooms: %+v", it)
	}
	row.Unknown = nil
	// A basic copy counts no attendees, and an outlook-only event has no tenant.
	row.DetailLevel, row.Sources = calendar.DetailBasic, []calendar.Source{calendar.SourceOutlook}
	if it = itemOf(row); it.AttendeeCount != nil || it.TenantID != "" {
		t.Errorf("%+v", it)
	}
	row.DetailLevel, row.AttendeesJSON = calendar.DetailFull, "not json"
	if it = itemOf(row); it.AttendeeCount != nil {
		t.Errorf("%+v", it)
	}
}

func TestEventOfCountsResponsesAndTruncates(t *testing.T) {
	d := store.CalendarDetail{
		CalendarRow: store.CalendarRow{DetailLevel: calendar.DetailFull, AgendaItem: calendar.AgendaItem{Merged: calendar.Merged{Event: calendar.Event{
			Subject: "S", BodyText: "0123456789", BodyHTML: "<p>0123456789</p>", BodyPreview: "0123456789", AttachmentsJSON: `[{"name":"a","size":3}]`, CategoriesJSON: `["Red"]`,
			Organizer: "Org", OrganizerAddress: "o@example.invalid", RecurrenceJSON: `{"p":1}`,
		}}}},
		Attendees: []store.CalendarAttendee{{Response: "accepted"}, {Response: "tentative"}, {Response: "declined"}, {Response: "none"}, {Response: ""}},
		Series:    store.CalendarSeries{Key: "S", MasterID: "ev_m", Occurrences: 4},
		Recaps: []store.CalendarRecap{{CallID: "c", ShortSummary: "0123456789", Outline: "0123456789", Headline: "0123456789", RecordingURL: "https://r.example.invalid",
			ActionItems: []store.CalendarRecapItem{{Text: "0123456789"}}, Mentions: []store.CalendarRecapItem{{Text: "0123456789"}}}},
	}
	ev := eventOf(d)
	if c := ev.ResponseCounts; c == nil || c.Accepted != 1 || c.Tentative != 1 || c.Declined != 1 || c.None != 2 {
		t.Fatalf("%+v", ev.ResponseCounts)
	}
	if ev.Organizer == nil || len(ev.Attachments) != 1 || ev.Categories[0] != "Red" || ev.Series == nil || ev.Series.OccurrenceCountKnown != 4 || ev.Recaps[0].Recording == nil {
		t.Fatalf("%+v", ev)
	}
	ev.truncate(4)
	if !ev.TextTruncated || ev.BodyText == "0123456789" || ev.Recaps[0].ActionItems[0].Text == "0123456789" || ev.Recaps[0].Mentions[0].Text == "0123456789" || ev.Recaps[0].Headline == "0123456789" {
		t.Fatalf("%+v", ev)
	}
	short := eventOf(store.CalendarDetail{CalendarRow: store.CalendarRow{DetailLevel: calendar.DetailBasic}})
	short.truncate(100)
	if short.TextTruncated || short.Organizer != nil || short.ResponseCounts != nil {
		t.Fatalf("%+v", short)
	}
}

func TestEventResultJSONAndJoin(t *testing.T) {
	ev := &calendarEvent{calendarItem: calendarItem{EventID: "ev_x", Subject: "S"}}
	ev.Series = &calendarSeries{Key: "S", Rule: []byte(`{`)} // not JSON: the event cannot be encoded
	if _, err := (&eventResult{event: ev, keys: []string{"subject"}}).MarshalJSON(); err == nil {
		t.Error("an event that cannot be encoded must fail")
	}
	ev.Series = nil
	if b, err := (&eventResult{event: ev, keys: []string{"subject", "nope"}}).MarshalJSON(); err != nil || !strings.HasPrefix(string(b), `{"subject":"S"`) {
		t.Errorf("%s %v", b, err)
	}
	// With no event the document is the meta keys alone.
	if b, err := (&eventResult{}).MarshalJSON(); err != nil || !strings.HasPrefix(string(b), `{"archive_age_seconds"`) {
		t.Errorf("%s %v", b, err)
	}
	bad := make(chan int)
	if _, err := joinJSON(bad, struct{}{}); err == nil {
		t.Error("the first value cannot be encoded")
	}
	if _, err := joinJSON(struct{}{}, bad); err == nil {
		t.Error("the second value cannot be encoded")
	}
	if b, _ := joinJSON(map[string]int{"a": 1}, struct{}{}); string(b) != `{"a":1}` {
		t.Errorf("%s", b)
	}
	if b, _ := joinJSON(struct{}{}, map[string]int{"b": 2}); string(b) != `{"b":2}` {
		t.Errorf("%s", b)
	}
	if b, _ := joinJSON(map[string]int{"a": 1}, map[string]int{"b": 2}); string(b) != `{"a":1,"b":2}` {
		t.Errorf("%s", b)
	}
}

func TestCalendarTextEdgeCases(t *testing.T) {
	yes, gap := true, true
	// The agenda footer names a gap and the unlinked accounts; a row says where and how.
	l := newList(shape(&runtime{}, []calendarItem{
		{Subject: "Online", Status: "confirmed", IsOnlineMeeting: &yes, Start: time.Date(2026, 10, 6, 9, 0, 0, 0, time.UTC), End: time.Date(2026, 10, 6, 10, 0, 0, 0, time.UTC)},
		{Subject: "Link", Status: "confirmed", JoinURL: "https://x.example.invalid"},
		{Subject: "Room", Status: "cancelled", Rooms: []calendarRoom{{Name: "R1"}}},
	}), false)
	l.CoverageGap, l.UnlinkedAccounts = &gap, []string{"t/u"}
	got := renderToString(t, "calendar", l)
	for _, want := range []string{"no cached data covers part of this range", "t/u", "online", "R1", "cancelled"} {
		if !strings.Contains(got, want) {
			t.Errorf("lacks %q:\n%s", want, got)
		}
	}
	if calendarWhere(calendarItem{}) != "" || calendarWhere(calendarItem{JoinURL: "u"}) != "online" {
		t.Error("where")
	}
	// An event block for an all-day, stale event with recordings and long text.
	allDay := true
	ev := &calendarEvent{calendarItem: calendarItem{Subject: "A  b", AllDay: &allDay, StartDate: "2026-10-06", Status: "confirmed", DetailLevel: "stale", UnknownFields: []string{"detail"}, Rooms: []calendarRoom{{Name: "R"}}},
		calendarExtras: calendarExtras{Chat: &calendarChat{DisplayName: "C", MessageCount: 2},
			Recaps:     []calendarRecap{{ShortSummary: "one two three four five six seven eight nine ten\nsecond paragraph", Outline: "o", ActionItems: []recapItemOut{{Owner: "O", Text: "do it"}}}},
			Recordings: []calendarRecording{{Kind: "recording", MatchedBy: "recap"}}}}
	got = renderToString(t, "calendar event", &eventResult{event: ev})
	for _, want := range []string{"2026-10-06 (all day)", "unknown: detail", "attendees as of", "do it", "recording", "second paragraph"} {
		if !strings.Contains(got, want) {
			t.Errorf("lacks %q:\n%s", want, got)
		}
	}
	if got = renderToString(t, "calendar event", &eventResult{}); !strings.Contains(got, "no calendar event to show") {
		t.Errorf("%s", got)
	}
	if firstOf("", "b") != "b" || firstOf() != "" {
		t.Error("firstOf")
	}
	if got := wrapText("aaa bbb ccc\n\nddd", 7); got != "aaa bbb\nccc\n\nddd" {
		t.Errorf("%q", got)
	}
}

func TestCalendarReadFailuresAreReported(t *testing.T) {
	e := calEnv(t)
	e.exec(`alter table calendar_source_events rename column event_type to et`)
	e.exec(`alter table calendar_source_events rename column time_zone to tz`)
	for _, args := range [][]string{{"calendar", "--from", "2023-11-22", "--days", "1"}, {"calendar", "event", "ev_x"}, {"calendar", "sources"}} {
		code, out, errOut := e.run(append([]string{"--max-age", "0"}, args...)...)
		if code == 0 || out != "" || !strings.Contains(errOut, `"error"`) {
			t.Errorf("%v: exit %d %q %q", args, code, out, errOut)
		}
	}
}

// A recap whose meeting has no event is listed with its content, not hidden.
func TestCalendarListsRecapsWithNoEvent(t *testing.T) {
	e := calEnv(t)
	m := agenda(t, e, "--from", "2023-11-15", "--days", "1", "--account", tenantA+"/"+userA)
	if m["count"] != float64(0) {
		t.Fatalf("no event that day: %v", m["count"])
	}
	recaps, ok := m["unlinked_recaps"].([]any)
	if !ok || len(recaps) != 1 {
		t.Fatalf("unlinked_recaps %v", m["unlinked_recaps"])
	}
	r := recaps[0].(map[string]any)
	if r["account_id"] != tenantA+"/"+userA || r["call_id"] == nil || r["action_items"] == nil {
		t.Errorf("%v", r)
	}
	if _, has := m["unlinked_recaps_total"]; has {
		t.Error("the total is printed only when the limit cut the list")
	}
	text := e
	code, out, errOut := text.run("--format", "text", "--max-age", "0", "calendar", "--from", "2023-11-15", "--days", "1", "--account", tenantA+"/"+userA)
	if code != 0 || !strings.Contains(out, "recaps with no event in the archive") || !strings.Contains(out, "Orphan task") {
		t.Errorf("text: %d %s %s", code, out, errOut)
	}
	// A limit that cuts the recaps says how many there are.
	e.exec(`insert into calendar_recaps(account_id,call_id,ical_uid,recap_id,link_method,has_catchup,has_recap,headline,short_summary,outline,summary_sections_json,speakers_json,topics_json,recording_url,meeting_start_at,meeting_end_at,first_seen_at,updated_at)
	  select account_id,'extra-'||call_id,'','','',0,1,'H','S','','','','','','2023-11-15T11:50:00.000Z',meeting_end_at,first_seen_at,updated_at from calendar_recaps where call_id like '%orphan%'`)
	cut := agenda(t, e, "--from", "2023-11-15", "--days", "1", "--account", tenantA+"/"+userA, "--limit", "1")
	if len(cut["unlinked_recaps"].([]any)) != 1 || cut["unlinked_recaps_total"] != float64(2) {
		t.Errorf("cut: %v %v", cut["unlinked_recaps"], cut["unlinked_recaps_total"])
	}
	// A day that has none prints none.
	if m = agenda(t, e, "--from", "2023-11-22", "--days", "1"); m["unlinked_recaps"] != nil {
		t.Errorf("%v", m["unlinked_recaps"])
	}
}

func TestUnlinkedRecapTableSaysWhenTheLimitCutIt(t *testing.T) {
	l := newList(nil, false)
	l.UnlinkedRecaps = []unlinkedRecap{{AccountID: "a", calendarRecap: calendarRecap{CallID: "c", ShortSummary: "S"}}}
	l.UnlinkedRecapsTotal = 3
	got := renderToString(t, "calendar", l)
	if !strings.Contains(got, "1 of 3 recaps shown; raise --limit") {
		t.Errorf("%s", got)
	}
}

// rooms and an unknown "rooms" never appear together, in the agenda or in the event detail, and
// rooms_as_of never appears without rooms.
func TestRoomsNeverSitNextToUnknownRooms(t *testing.T) {
	e := calEnv(t)
	wide := agenda(t, e, "--from", "2023-11-01", "--to", "2023-12-31")
	stated, unstated := 0, 0
	for _, it := range items(t, wide) {
		_, rooms := it["rooms"]
		_, asOf := it["rooms_as_of"]
		named := contains(asStrings(it["unknown_fields"]), "rooms")
		if rooms == named || asOf && !rooms {
			t.Errorf("%v: rooms %v, rooms_as_of %v, rooms unknown %v", it["subject"], rooms, asOf, named)
		}
		if rooms {
			stated++
		} else {
			unstated++
		}
		ev := eventDoc(t, e, it["event_id"].(string), "--account", it["account_id"].(string))
		if _, has := ev["rooms"]; has == contains(asStrings(ev["unknown_fields"]), "rooms") {
			t.Errorf("event %v: rooms %v, unknown %v", it["subject"], has, ev["unknown_fields"])
		}
	}
	if unstated == 0 {
		t.Errorf("the fixture should hold events whose rooms are unknown (stated %d)", stated)
	}
}

func TestCalendarSaysWhichDaysAndAccountsToDistrust(t *testing.T) {
	e := calEnv(t)
	m := agenda(t, e, "--from", "2023-11-01", "--to", "2023-11-03")
	days := asStrings(m["uncovered_days"])
	if m["coverage_gap"] != true || len(days) != 2 || days[0] != "2023-11-01" || days[1] != "2023-11-02" {
		t.Errorf("uncovered days %v of %v", days, m["coverage_gap"])
	}
	if _, has := m["uncovered_days_total"]; has {
		t.Error("a short list has no total")
	}
	accounts, _ := m["accounts"].([]any)
	if len(accounts) == 0 {
		t.Fatalf("no per-account freshness: %v", keysOf(m))
	}
	for _, a := range accounts {
		a := a.(map[string]any)
		if a["account_id"] == nil || a["synced_at"] == nil {
			t.Errorf("account %v", a)
		}
	}
	// A covered day lists no gap; a long range is cut and counted.
	day := agenda(t, e, "--from", "2023-11-22", "--days", "1", "--account", tenantA+"/"+userA)
	if day["coverage_gap"] != false || day["uncovered_days"] != nil {
		t.Errorf("covered day: %v %v", day["coverage_gap"], day["uncovered_days"])
	}
	long := agenda(t, e, "--from", "2023-01-01", "--to", "2024-01-01")
	if len(asStrings(long["uncovered_days"])) != maxUncoveredDays || long["uncovered_days_total"].(float64) < 300 {
		t.Errorf("long range: %d days, total %v", len(asStrings(long["uncovered_days"])), long["uncovered_days_total"])
	}
}

func TestUncoveredNote(t *testing.T) {
	for _, c := range []struct {
		r    listResult
		want string
	}{
		{listResult{}, "no cached data covers part of this range"},
		{listResult{UncoveredDays: []string{"2023-11-01"}}, "no cached data covers 1 day(s) of this range: 2023-11-01"},
		{listResult{UncoveredDays: []string{"a", "b", "c", "d", "e", "f"}}, "no cached data covers 6 days of this range, from a"},
		{listResult{UncoveredDays: []string{"a"}, UncoveredDaysTotal: 40}, "no cached data covers 40 days of this range, from a"},
	} {
		if got := uncoveredNote(&c.r); got != c.want {
			t.Errorf("got %q, want %q", got, c.want)
		}
	}
}

// The recap of 2023-11-15 has no event: the agenda lists it, in text as well as in JSON.
func TestCalendarGoldenForTheRecapWithNoEvent(t *testing.T) {
	e := calEnv(t)
	for _, color := range []bool{false, true} {
		t.Setenv("CLICOLOR_FORCE", "")
		suffix := "plain"
		if color {
			suffix = "color"
			t.Setenv("CLICOLOR_FORCE", "1")
		}
		code, out, errOut := e.run("--format", "text", "--max-age", "0", "calendar", "--from", "2023-11-15", "--days", "1", "--account", tenantA+"/"+userA)
		if code != 0 {
			t.Fatalf("exit %d: %s", code, errOut)
		}
		checkGolden(t, "calendar_unlinked_recap."+suffix, e.scrub(out))
	}
}

// With Outlook on, its events sit in the agenda beside the Teams ones, as their own principal: an
// unlinked profile is named in the envelope, and an Outlook-only event says what it does not know.
func TestCalendarShowsOutlookEvents(t *testing.T) {
	e := textEnv(t)
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "Main"), 0o750); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile("../../testdata/outlook-fixture/HxStore.hxd")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Main", "HxStore.hxd"), b, 0o600); err != nil { //nolint:gosec // a test temp dir
		t.Fatal(err)
	}
	if code, _, stderr := e.run("--outlook-root", root, "sync"); code != 0 {
		t.Fatalf("sync exit %d: %s", code, stderr)
	}
	m := agenda(t, e, "--from", "2023-11-20", "--to", "2023-11-25", "--limit", "200")
	if got := asStrings(m["unlinked_accounts"]); len(got) != 1 || got[0] != "outlook/Main" {
		t.Fatalf("unlinked_accounts %v", got)
	}
	plain := itemBySubject(t, m, "Fixture plain event")
	if src := asStrings(plain["sources"]); len(src) != 1 || src[0] != "outlook" {
		t.Fatalf("sources %v", src)
	}
	if !slices.Contains(asStrings(plain["unknown_fields"]), "rooms") {
		t.Fatalf("an Outlook event must say what it does not know: %v", plain["unknown_fields"])
	}
	// The Teams events are still there, and no item claims both sources while the profile is unlinked.
	teams := 0
	for _, it := range items(t, m) {
		src := asStrings(it["sources"])
		if len(src) > 1 {
			t.Fatalf("an unlinked Outlook event merged: %v", src)
		}
		if len(src) == 1 && src[0] == "teams" {
			teams++
		}
	}
	if teams == 0 {
		t.Fatal("no Teams events")
	}
	// `calendar event` opens the Outlook event by its id.
	ev := eventDoc(t, e, plain["event_id"].(string))
	if ev["account_id"] != "outlook/Main" || ev["subject"] != "Fixture plain event" {
		t.Fatalf("%v", ev)
	}
}
