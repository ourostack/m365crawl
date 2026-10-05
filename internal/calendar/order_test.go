package calendar

import (
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
			t.Fatalf("%s: order %v stores a different row\nfirst: %+v\nthis:  %+v", name, order, *first, *stored)
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
	if got.ReminderAsOf != nil || got.CategoriesAsOf != nil || got.DetailAsOf != nil {
		t.Fatalf("a thin copy claimed a clock: %+v", got)
	}
	got = Capture(&got, rich(t, t2))
	if !got.ReminderAsOf.Equal(mustTime(t, t2)) || !got.CategoriesAsOf.Equal(mustTime(t, t2)) {
		t.Fatalf("a stating copy did not set the clocks: %v %v", got.ReminderAsOf, got.CategoriesAsOf)
	}
	onlyCat := thin(t, t3)
	onlyCat.CategoriesJSON = `["B"]`
	got = Capture(&got, onlyCat)
	if !got.ReminderAsOf.Equal(mustTime(t, t2)) || !got.CategoriesAsOf.Equal(mustTime(t, t3)) {
		t.Fatalf("clocks moved together: %v %v", got.ReminderAsOf, got.CategoriesAsOf)
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
