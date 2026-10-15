package store

import (
	"context"
	"testing"
)

func TestAttendeeBindingPreviewAndFlags(t *testing.T) {
	s := newStore(t)
	e := attendeeModel(t, "Shared", 3)
	for i := 1; i < 101; i++ {
		b := e.Selections[0]
		b.PayloadPos = 100 + i
		e.Selections = append(e.Selections, b)
	}
	e.Parents[0].EventLinks = 101
	admitAttendeeFixture(t, s, e, base)
	event := attendeeEventFixture(t, s, false)
	if len(event.Rows) != 1 || event.BindingsTotal != 101 || len(event.Bindings) != 100 || !event.BindingsTruncated ||
		event.Coverage.Sources[0].CalendarState != "multiple_selections" {
		t.Error("multiple actual selections were collapsed or unlabeled")
	}
	detail, err := s.AttendeeDetail(context.Background(), nil, event.Rows[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.BindingsTotal != 101 || len(detail.Bindings) != 100 || !detail.BindingsTruncated ||
		!detail.Bindings[0].MatchesRead || !detail.Bindings[0].MatchesOrigin || !detail.Bindings[0].MatchesLink || !detail.Bindings[0].CalendarRowPresent {
		t.Error("detail lost bounded independent binding qualification")
	}
	sources, err := s.AttendeeSources(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(sources.Rows) != 0 || sources.BindingsTotal != 101 || len(sources.Bindings) != 100 || !sources.BindingsTruncated {
		t.Error("body-free sources lost binding coverage")
	}
}
