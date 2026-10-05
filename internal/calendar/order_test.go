package calendar

import (
	"fmt"
	"math/rand"
	"reflect"
	"testing"
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
			next := Capture(stored, copies[i])
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
	thinNoReminder.ReminderStated = true
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
	got := Capture(nil, thin(t, t1))
	if got.FieldClocksJSON != "" || got.DetailAsOf != nil {
		t.Fatalf("a thin copy claimed a clock: %+v", got)
	}
	got = Capture(&got, rich(t, t2))
	if !ParseFieldClocks(got.FieldClocksJSON)["reminder"].Equal(mustTime(t, t2)) || !ParseFieldClocks(got.FieldClocksJSON)["categories"].Equal(mustTime(t, t2)) {
		t.Fatalf("a stating copy did not set the clocks: %v", got.FieldClocksJSON)
	}
	onlyCat := thin(t, t3)
	onlyCat.CategoriesJSON = `["B"]`
	got = Capture(&got, onlyCat)
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
	old := Capture(nil, rich(t, t1)) // html body
	typeOnly := thin(t, t3)
	typeOnly.BodyType = "text"
	got := Capture(&old, typeOnly)
	if got.BodyType != "html" || !got.DetailAsOf.Equal(mustTime(t, t1)) {
		t.Fatalf("a body type alone changed the row: %q as of %v", got.BodyType, got.DetailAsOf)
	}
	// A thin copy with only a body type claims no clock on a fresh row either.
	if bare := Capture(nil, typeOnly); bare.DetailAsOf != nil {
		t.Fatalf("a body type alone claimed the detail clock: %v", bare.DetailAsOf)
	}
	// The copy that supplies the body supplies its type.
	text := rich(t, t2)
	text.BodyHTML, text.BodyText, text.BodyType = "", "Plain body, longer than before", "text"
	got = Capture(&old, text)
	if got.BodyType != "text" {
		t.Fatalf("body type %q, want the supplier's", got.BodyType)
	}
	// A stale copy that does not supply the body leaves the type alone; one that fills an empty
	// body brings its type with it.
	staleText := rich(t, t1)
	staleText.BodyType = "text"
	cur := Capture(nil, rich(t, t3))
	if got := Capture(&cur, staleText); got.BodyType != "html" {
		t.Fatalf("stale copy changed the type: %q", got.BodyType)
	}
	noBody := Capture(nil, thin(t, t1))
	if got := Capture(&noBody, rich(t, t2)); got.BodyType != "html" {
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
			if f := full.Field(i); f.Kind() == reflect.String {
				f.SetString("x")
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
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		g, ok := fieldGroups[name]
		if !ok {
			t.Errorf("Event.%s is in no group: add it to fieldGroups (and to a unit if it is detail or small)", name)
			continue
		}
		_, inUnit := clocked[name]
		raw := name == "DetailRawJSON" // clocked by captureRaw
		if (g == groupDetail || g == groupSmall) && !inUnit && !raw {
			t.Errorf("Event.%s is in group %s but no unit clocks it", name, g)
		}
		if g != groupDetail && g != groupSmall && inUnit {
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
	e.Cancelled = r.Intn(5) == 0
	e.IsOnlineMeeting = r.Intn(4) != 0
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
	e.TeamsThreadID = pick("", "19:meeting_A@thread.v2")
	e.LocationsJSON = pick("", roomJSON)
	e.AttachmentsJSON = pick("", `[{"name":"a"}]`)
	e.HasAttachments = r.Intn(2) == 0
	e.CategoriesJSON = pick("", "", `["B"]`, `["C"]`, `[]`)
	e.RecurrenceJSON = pick("", `{"p":1}`, `{"p":2}`)
	switch r.Intn(5) {
	case 0:
		e.ReminderMinutes = intp(0)
	case 1:
		e.ReminderMinutes = intp(15)
	case 2:
		e.ReminderStated = true // the source says no reminder is set
	}
	return e
}

func TestCaptureConvergesForRandomSubsetsOfGroups(t *testing.T) {
	r := rand.New(rand.NewSource(20261005)) //nolint:gosec // a fixed seed makes the sweep reproducible, not secret
	for n := 0; n < 1000; n++ {
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
