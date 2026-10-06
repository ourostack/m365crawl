package calendar

import (
	"errors"
	"fmt"
	"math/rand"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

// permutations returns every ordering of 0..n-1.
func permutations(n int) [][]int {
	if n == 0 {
		return [][]int{{}}
	}
	var out [][]int
	for _, rest := range permutations(n - 1) {
		for i := 0; i <= len(rest); i++ {
			p := append(append(append([]int{}, rest[:i]...), n-1), rest[i:]...)
			out = append(out, p)
		}
	}
	return out
}

// converge captures the copies in every arrival order and fails unless all orders store the same
// row. It returns that row.
func converge(t *testing.T, name string, copies []Event) Event {
	t.Helper()
	var first *Event
	for _, order := range permutations(len(copies)) {
		var stored *Event
		for _, i := range order {
			next := mustCapture(t, stored, copies[i])
			stored = &next
		}
		if first == nil {
			first = stored
			continue
		}
		if !reflect.DeepEqual(*first, *stored) {
			var diff []string
			a, b := reflect.ValueOf(*first), reflect.ValueOf(*stored)
			for i := 0; i < a.NumField(); i++ {
				if !reflect.DeepEqual(a.Field(i).Interface(), b.Field(i).Interface()) {
					diff = append(diff, fmt.Sprintf("%s: %v vs %v", a.Type().Field(i).Name, a.Field(i), b.Field(i)))
				}
			}
			t.Fatalf("%s: order %v stores a different row: %v\ncopies: %+v", name, order, diff, copies)
		}
	}
	return *first
}

// randomTri is a random flag: unknown, false or true.
func randomTri(r *rand.Rand) Tri { return []Tri{TriUnknown, TriFalse, TriTrue}[r.Intn(3)] }

func intp(n int) *int { return &n }

func TestCaptureSmallFieldsConvergeInAnyArrivalOrder(t *testing.T) {
	const t4, t5 = "2026-10-04T10:00:00Z", "2026-10-05T10:00:00Z"
	r1, r2 := rich(t, t1), rich(t, t2)
	r2.ReminderMinutes, r2.CategoriesJSON = intp(20), `["B"]`

	thinNone := thin(t, t3) // states neither small field
	thinRem := thin(t, t3)
	thinRem.ReminderMinutes = intp(30)
	thinCat := thin(t, t4)
	thinCat.CategoriesJSON = `["C"]`
	thinNoReminder := thin(t, t5) // states "no reminder is set"
	thinNoReminder.Unknown = without(thinNoReminder.Unknown, FieldReminder)
	zero := thin(t, t3) // a reminder of zero minutes is a real value
	zero.ReminderMinutes = intp(0)

	tests := []struct {
		name       string
		copies     []Event
		reminder   *int
		categories string
	}{
		{"thin stating nothing", []Event{thinNone, r1, r2}, intp(20), `["B"]`},
		{"thin stating a reminder", []Event{thinRem, r1, r2}, intp(30), `["B"]`},
		{"thin stating categories only", []Event{thinCat, r1, r2}, intp(20), `["C"]`},
		{"thin stating no reminder", []Event{thinNoReminder, r1, r2}, nil, `["B"]`},
		{"thin stating zero minutes", []Event{zero, r1, r2}, intp(0), `["B"]`},
		{"all of them", []Event{thinNone, thinRem, thinCat, thinNoReminder, r1, r2}, nil, `["C"]`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := converge(t, tt.name, tt.copies)
			if !reflect.DeepEqual(got.ReminderMinutes, tt.reminder) {
				t.Errorf("reminder %v, want %v", got.ReminderMinutes, tt.reminder)
			}
			if got.CategoriesJSON != tt.categories {
				t.Errorf("categories %q, want %q", got.CategoriesJSON, tt.categories)
			}
		})
	}
}

func TestCaptureSmallFieldClocksAdvanceOnlyWhenStated(t *testing.T) {
	got := mustCapture(t, nil, thin(t, t1))
	// Every copy states the location text, so that is the one clock a thin copy sets.
	if clocks := ParseFieldClocks(got.FieldClocksJSON); len(clocks) != 1 || got.DetailAsOf != nil {
		t.Fatalf("a thin copy claimed a clock: %+v", got)
	}
	got = mustCapture(t, &got, rich(t, t2))
	if !ParseFieldClocks(got.FieldClocksJSON)["reminder"].Equal(mustTime(t, t2)) || !ParseFieldClocks(got.FieldClocksJSON)["categories"].Equal(mustTime(t, t2)) {
		t.Fatalf("a stating copy did not set the clocks: %v", got.FieldClocksJSON)
	}
	onlyCat := thin(t, t3)
	onlyCat.CategoriesJSON = `["B"]`
	got = mustCapture(t, &got, onlyCat)
	if !ParseFieldClocks(got.FieldClocksJSON)["reminder"].Equal(mustTime(t, t2)) || !ParseFieldClocks(got.FieldClocksJSON)["categories"].Equal(mustTime(t, t3)) {
		t.Fatalf("clocks moved together: %v", got.FieldClocksJSON)
	}
}

func TestCaptureRichCopiesConvergeWhenNewestDiffersOnlyInASmallField(t *testing.T) {
	const mid = "2026-10-02T15:00:00Z"
	r1 := rich(t, t1)
	r1.AttendeesJSON = attendeesOne
	r2 := rich(t, t2)
	r2.AttendeesJSON = attendeesTwo
	between := rich(t, mid)
	between.AttendeesJSON = attendeesThree
	r3 := rich(t, t3)
	r3.AttendeesJSON = attendeesTwo // same substantive content as r2
	r3.ReminderMinutes, r3.CategoriesJSON = intp(45), `["Other"]`
	got := converge(t, "rich copies", []Event{r1, r2, between, r3})
	if got.AttendeesJSON != attendeesTwo || !got.DetailAsOf.Equal(mustTime(t, t3)) || got.DetailRawJSON != r3.DetailRawJSON {
		t.Fatalf("detail %q as of %v raw %q", got.AttendeesJSON, got.DetailAsOf, got.DetailRawJSON)
	}
}

func TestCaptureBodyTypeTravelsWithTheBody(t *testing.T) {
	old := mustCapture(t, nil, rich(t, t1)) // html body
	typeOnly := thin(t, t3)
	typeOnly.BodyType = "text"
	got := mustCapture(t, &old, typeOnly)
	if got.BodyType != "html" || !got.DetailAsOf.Equal(mustTime(t, t1)) {
		t.Fatalf("a body type alone changed the row: %q as of %v", got.BodyType, got.DetailAsOf)
	}
	// A thin copy with only a body type claims no clock on a fresh row either.
	if bare := mustCapture(t, nil, typeOnly); bare.DetailAsOf != nil {
		t.Fatalf("a body type alone claimed the detail clock: %v", bare.DetailAsOf)
	}
	// The copy that supplies the body supplies its type.
	text := rich(t, t2)
	text.BodyHTML, text.BodyText, text.BodyType = "", "Plain body, longer than before", "text"
	got = mustCapture(t, &old, text)
	if got.BodyType != "text" {
		t.Fatalf("body type %q, want the supplier's", got.BodyType)
	}
	// A stale copy that does not supply the body leaves the type alone; one that fills an empty
	// body brings its type with it.
	staleText := rich(t, t1)
	staleText.BodyType = "text"
	cur := mustCapture(t, nil, rich(t, t3))
	if got := mustCapture(t, &cur, staleText); got.BodyType != "html" {
		t.Fatalf("stale copy changed the type: %q", got.BodyType)
	}
	noBody := mustCapture(t, nil, thin(t, t1))
	if got := mustCapture(t, &noBody, rich(t, t2)); got.BodyType != "html" {
		t.Fatalf("filled body lost its type: %q", got.BodyType)
	}
}

// TestEveryEventFieldHasAGroup walks Event by reflection: a field that no group claims, or a
// detail or small field that no unit clocks, fails here, so a field added later cannot be
// forgotten.
func TestEveryEventFieldHasAGroup(t *testing.T) {
	typ := reflect.TypeOf(Event{})
	clocked := map[string]string{}
	for _, u := range units {
		var probe Event
		// Each unit names the fields it assigns: find them by assigning from a fully set copy.
		full := reflect.New(typ).Elem()
		for i := 0; i < typ.NumField(); i++ {
			switch f := full.Field(i); {
			case f.Kind() == reflect.String:
				f.SetString("x")
			case f.Type() == reflect.TypeOf(TriTrue):
				f.Set(reflect.ValueOf(TriTrue))
			case f.Type() == reflect.TypeOf(time.Time{}):
				f.Set(reflect.ValueOf(time.Unix(1, 0)))
			}
		}
		fullEvent := full.Interface().(Event)
		fullEvent.ReminderMinutes = intp(7)
		u.assign(&probe, fullEvent)
		pv := reflect.ValueOf(probe)
		for i := 0; i < typ.NumField(); i++ {
			if !pv.Field(i).IsZero() {
				name := typ.Field(i).Name
				if prev, dup := clocked[name]; dup {
					t.Errorf("field %s is in units %s and %s", name, prev, u.name)
				}
				clocked[name] = u.name
				if g := fieldGroups[name]; g != u.group {
					t.Errorf("unit %s holds %s, which is in group %q, not %q", u.name, name, g, u.group)
				}
			}
		}
	}
	// Fields that describe one thing share a family, and the grouping is pinned.
	families := map[string][]string{}
	for name, u := range clocked {
		for _, unit := range units {
			if unit.name == u {
				families[unit.family] = append(families[unit.family], name)
			}
		}
	}
	for _, want := range [][]string{
		{"OnlineMeetingURL", "ShortJoinURL", "DialInConferenceID", "DialInTollNumber", "TeamsThreadID"},
		{"Location", "LocationsJSON"},
		{"BodyHTML", "BodyText", "BodyType"},
	} {
		fam := ""
		for _, u := range units {
			if _, ok := reflect.TypeOf(Event{}).FieldByName(want[0]); ok && clocked[want[0]] == u.name {
				fam = u.family
			}
		}
		got := append([]string(nil), families[fam]...)
		sort.Strings(got)
		w := append([]string(nil), want...)
		sort.Strings(w)
		if fam == "" || !reflect.DeepEqual(got, w) {
			t.Errorf("family of %s is %v, want %v", want[0], got, w)
		}
	}
	for _, apart := range [][2]string{{"BodyPreview", "BodyHTML"}, {"RecurrenceJSON", "Start"}} {
		if clocked[apart[0]] == clocked[apart[1]] {
			t.Errorf("%s and %s must not share a unit", apart[0], apart[1])
		}
	}
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		g, ok := fieldGroups[name]
		if !ok {
			t.Errorf("Event.%s is in no group: add it to fieldGroups (and to a unit if it is detail or small)", name)
			continue
		}
		_, inUnit := clocked[name]
		raw := name == "DetailRawJSON" || name == "LastModified" // clocked by captureRaw, and the row's own clock
		if (g == groupDetail || g == groupSmall || g == groupSchedule) && !inUnit && !raw {
			t.Errorf("Event.%s is in group %s but no unit clocks it", name, g)
		}
		if g != groupDetail && g != groupSmall && g != groupSchedule && inUnit {
			t.Errorf("Event.%s is in group %s but unit %s clocks it", name, g, clocked[name])
		}
	}
	for name := range fieldGroups {
		if _, ok := typ.FieldByName(name); !ok {
			t.Errorf("fieldGroups names %s, which Event does not have", name)
		}
	}
}

// randomCopy states a random subset of the groups at one of a few times; equal times are likely.
func randomCopy(t *testing.T, r *rand.Rand) Event {
	times := []string{t1, "2026-10-01T15:00:00Z", t2, t3}
	e := thin(t, times[r.Intn(len(times))])
	pick := func(vals ...string) string { return vals[r.Intn(len(vals))] }
	e.Subject = pick("Sync A", "Sync B", "Sync C")
	e.Location = pick("Fixture Room Alpha", "", "Fixture Room Beta")
	e.Cancelled = randomTri(r)
	e.IsOnlineMeeting = randomTri(r)
	e.IsOrganizer, e.IsPrivate = randomTri(r), randomTri(r)
	// The all-day block: known true carries dates, known false and unknown carry none.
	e.AllDay = randomTri(r)
	midnight := func(d string) time.Time { return mustTime(t, d+"T00:00:00Z") }
	switch {
	case e.AllDay == TriTrue:
		// A known all-day copy carries instants that agree with its dates.
		e.StartDate, e.EndDate = pick("2026-10-05", "2026-10-06"), pick("", "2026-10-07", "2026-10-08")
		e.Start, e.End = midnight(e.StartDate), midnight(e.StartDate).Add(24*time.Hour)
		if e.EndDate != "" {
			e.End = midnight(e.EndDate)
		}
	case r.Intn(3) == 0:
		e.Start, e.End = e.Start.Add(time.Hour), e.End.Add(2*time.Hour)
	}
	e.TimeZone, e.UTCOffset = pick("PacificSt", "UTC", ""), pick("", "-07:00", "+01:00")
	e.Organizer, e.Response = pick("Alex Fixture", "", "Blake Fixture"), pick("accepted", "declined", "")
	for _, f := range []struct {
		name Field
		ptr  *string
	}{
		{FieldSubject, &e.Subject}, {FieldLocation, &e.Location}, {FieldOrganizer, &e.Organizer},
		{FieldResponse, &e.Response}, {FieldShowAs, &e.ShowAs}, {FieldTimeZone, &e.TimeZone}, {FieldUTCOffset, &e.UTCOffset},
	} {
		if r.Intn(4) == 0 {
			*f.ptr = ""
			e.Unknown = append(e.Unknown, f.name)
		}
	}
	e.AttendeesJSON = pick("", "", attendeesOne, attendeesTwo, attendeesThree)
	switch r.Intn(5) {
	case 0:
		e.BodyHTML, e.BodyText, e.BodyType = "<p>A</p>", "A", "html"
	case 1:
		e.BodyHTML, e.BodyText, e.BodyType = "", "A", "text" // same body text, different type
	case 2:
		e.BodyHTML, e.BodyText, e.BodyType = "<p>Longer body</p>", "Longer body", "html"
	case 3:
		e.BodyType = pick("html", "text") // a type alone states nothing
	}
	if e.AttendeesJSON != "" || e.BodyText != "" || e.BodyHTML != "" {
		e.DetailRawJSON = pick("", `{"r":1}`, `{"r":22}`, `{"r":2}`)
	}
	e.BodyPreview = pick("", "", "p1", "p22")
	e.OnlineMeetingURL = pick("", "", "https://teams.example.test/a", "https://teams.example.test/b")
	e.TeamsThreadID = pick("", "19:meeting_A@thread.v2", "19:meeting_B@thread.v2")
	e.DialInConferenceID = pick("", "111", "222")
	e.LocationsJSON = pick("", roomJSON)
	e.AttachmentsJSON = pick("", `[{"name":"a"}]`)
	e.HasAttachments = randomTri(r)
	e.CategoriesJSON = pick("", "", `["B"]`, `["C"]`, `[]`)
	e.RecurrenceJSON = pick("", `{"p":1}`, `{"p":2}`)
	switch r.Intn(5) {
	case 0:
		e.ReminderMinutes = intp(0)
	case 1:
		e.ReminderMinutes = intp(15)
	case 2:
		e.Unknown = without(e.Unknown, FieldReminder) // the source says no reminder is set
	}
	return e
}

// sweepSize is n sets, or many more with TEAMSCRAWL_SWEEP set (a local soak, not run in CI).
func sweepSize(n int) int {
	if os.Getenv("TEAMSCRAWL_SWEEP") != "" {
		return n * 40
	}
	return n
}

func TestCaptureConvergesForRandomSubsetsOfGroups(t *testing.T) {
	r := rand.New(rand.NewSource(20261005)) //nolint:gosec // a fixed seed makes the sweep reproducible, not secret
	for n := 0; n < sweepSize(1000); n++ {
		size := 2 + r.Intn(3)
		copies := make([]Event, size)
		for i := range copies {
			copies[i] = randomCopy(t, r)
		}
		converge(t, fmt.Sprintf("set %d", n), copies)
	}
}

func TestCaptureTiesAreDecidedByTheStatedValue(t *testing.T) {
	a, b := thin(t, t1), thin(t, t1)
	a.Subject, b.Subject = "A", "B"
	// Same body text, different body type.
	a.BodyText, a.BodyType = "same", "html"
	b.BodyText, b.BodyType = "same", "text"
	got := converge(t, "tie", []Event{a, b})
	if got.Subject != "B" || got.BodyType != "text" {
		t.Fatalf("greater bytes should win: %q %q", got.Subject, got.BodyType)
	}
	// A rich copy beats a thin one at the same time, in either order.
	rc, th := rich(t, t1), thin(t, t1)
	th.AttendeesJSON = ""
	rc.AttendeesJSON = attendeesTwo
	if got := converge(t, "rich vs thin", []Event{rc, th}); got.AttendeesJSON != attendeesTwo {
		t.Fatalf("attendees %q", got.AttendeesJSON)
	}
	// Equal raw records of different size: the larger record wins; equal size, the greater bytes.
	x, y := rich(t, t1), rich(t, t1)
	x.DetailRawJSON, y.DetailRawJSON = `{"a":1}`, `{"a":22}`
	if got := converge(t, "raw size", []Event{x, y}); got.DetailRawJSON != y.DetailRawJSON {
		t.Fatalf("raw %q", got.DetailRawJSON)
	}
	y.DetailRawJSON = `{"a":2}`
	if got := converge(t, "raw bytes", []Event{x, y}); got.DetailRawJSON != y.DetailRawJSON {
		t.Fatalf("raw %q", got.DetailRawJSON)
	}
}

// A copy that states only a body preview (the Teams list view) must not take the detail clock.
func TestCapturePartialCopyDoesNotClaimTheDetailClock(t *testing.T) {
	x, y := rich(t, t1), rich(t, "2026-10-01T15:00:00Z")
	x.AttendeesJSON, y.AttendeesJSON = attendeesTwo, attendeesThree
	p := thin(t, t2)
	p.BodyPreview = "only a preview"
	got := converge(t, "partial", []Event{x, y, p})
	if got.AttendeesJSON != attendeesThree || got.BodyPreview != "only a preview" {
		t.Fatalf("attendees %q preview %q", got.AttendeesJSON, got.BodyPreview)
	}
	if !got.DetailAsOf.Equal(mustTime(t, "2026-10-01T15:00:00Z")) {
		t.Fatalf("detail clock %v", got.DetailAsOf)
	}
}

// linkFields are the five meeting link fields; a stored row must hold one copy's version of them.
func linkFields(e Event) [5]string {
	return [5]string{e.OnlineMeetingURL, e.ShortJoinURL, e.DialInConferenceID, e.DialInTollNumber, e.TeamsThreadID}
}

func TestCaptureMeetingLinksComeFromOneCopy(t *testing.T) {
	// The confirmed fault: a new join URL kept the old meeting's other link fields.
	old := rich(t, t1)
	newer := thin(t, t3)
	newer.OnlineMeetingURL = "https://teams.example.test/l/meetup-join/new"
	got := mustCapture(t, &[]Event{mustCapture(t, nil, old)}[0], newer)
	if linkFields(got) != [5]string{newer.OnlineMeetingURL, "", "", "", ""} {
		t.Fatalf("a new join URL kept old companions: %v", linkFields(got))
	}
	// And in every arrival order of random copies, the stored links equal some copy's links.
	r := rand.New(rand.NewSource(7)) //nolint:gosec // a fixed seed makes the sweep reproducible, not secret
	for n := 0; n < 300; n++ {
		copies := make([]Event, 2+r.Intn(3))
		for i := range copies {
			copies[i] = randomCopy(t, r)
			if copies[i].IsOnlineMeeting.Is(false) {
				copies[i].IsOnlineMeeting = TriUnknown // clearing is covered elsewhere
			}
			if r.Intn(2) == 0 {
				copies[i].ShortJoinURL = "https://teams.example.test/short"
				copies[i].DialInTollNumber = "+1 555 0101"
			}
		}
		got := converge(t, fmt.Sprintf("links %d", n), copies)
		ok := linkFields(got) == [5]string{}
		for _, c := range copies {
			ok = ok || linkFields(got) == linkFields(c)
		}
		if !ok {
			t.Fatalf("set %d: stored links %v come from no single copy", n, linkFields(got))
		}
	}
}

func TestCaptureNotOnlineIsAStatementNotADefault(t *testing.T) {
	old := mustCapture(t, nil, rich(t, t1))
	silent := thin(t, t3)
	silent.IsOnlineMeeting = TriUnknown // the source does not say
	if got := mustCapture(t, &old, silent); got.OnlineMeetingURL == "" || got.IsOnlineMeeting != TriTrue {
		t.Fatalf("a copy that does not know the flag cleared the links or the flag: %+v", got)
	}
	stated := silent
	stated.IsOnlineMeeting = TriFalse
	got := mustCapture(t, &old, stated)
	if got.OnlineMeetingURL != "" || got.IsOnlineMeeting != TriFalse {
		t.Fatalf("a known not-online must clear the links and be stored: %q %v", got.OnlineMeetingURL, got.IsOnlineMeeting)
	}
	// An online copy clears nothing.
	online := thin(t, t3)
	online.IsOnlineMeeting = TriTrue
	if got := mustCapture(t, &old, online); got.OnlineMeetingURL == "" {
		t.Fatal("an online copy cleared the links")
	}
}

func TestCaptureRefusesUnstorableEvents(t *testing.T) {
	zeroStart := thin(t, t1)
	zeroStart.Start = time.Time{}
	noDate := thin(t, t1)
	noDate.AllDay = TriTrue
	farEnd := thin(t, t1)
	farEnd.End = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	farMod := thin(t, t1)
	farMod.LastModified = tp(t, "2026-10-01T00:00:00Z")
	*farMod.LastModified = time.Date(-1, 1, 1, 0, 0, 0, 0, time.UTC)
	for name, e := range map[string]Event{"zero start": zeroStart, "all-day without date": noDate, "year 10000": farEnd, "year -1": farMod} {
		got, err := Capture(nil, e)
		var bad *InvalidEventError
		if !errors.As(err, &bad) || bad.SourceID != "ev1" || bad.Reason == "" || got.SourceID != "" {
			t.Errorf("%s: got %+v, %v", name, got, err)
		}
		if bad != nil && !strings.Contains(bad.Error(), "ev1") {
			t.Errorf("%s: error text %q", name, bad.Error())
		}
	}
	// A valid all-day event and an unset End are fine.
	ok := thin(t, t1)
	ok.End = time.Time{}
	setAllDay(&ok, "2026-10-05", "")
	if _, err := Capture(nil, ok); err != nil {
		t.Fatal(err)
	}
}
