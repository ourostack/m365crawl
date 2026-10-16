package outlookmeetingattendees

import (
	"context"
	"errors"
	"testing"

	"github.com/ourostack/m365crawl/internal/outlookattendees"
	"github.com/ourostack/m365crawl/internal/outlookcal"
)

func TestMeetingAttendeeSelectionAudit(t *testing.T) {
	native, selections := sample()
	e, _, err := Build(context.Background(), native, selections)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := Encode(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	rows, bindings := 0, 0
	summary, err := AuditVisitors(context.Background(), raw,
		func(int, outlookattendees.Attendee) error { rows++; return nil },
		func(i int, s outlookcal.SelectionReceipt) error {
			if i != 0 || s.SourceID != "synthetic-event" || s.DetailKey != 3 || !s.DetailPresent {
				t.Fatal("selection visitor lost actual receipt")
			}
			bindings++
			return nil
		})
	if err != nil || rows != 2 || bindings != 1 || summary.AttendeesTotal != 2 {
		t.Fatal("bounded staged dual visitor lost source evidence")
	}
	stop := errors.New("synthetic selection visitor failure")
	summary, err = AuditVisitors(context.Background(), raw, nil,
		func(int, outlookcal.SelectionReceipt) error { return stop })
	if !errors.Is(err, stop) || summary.AttendeesTotal != 0 {
		t.Fatal("failed selection visitor published staged summary")
	}
	called := false
	_, err = AuditVisitors(context.Background(), append(append([]byte{}, raw...), '\n'), nil,
		func(int, outlookcal.SelectionReceipt) error { called = true; return nil })
	if err == nil || called {
		t.Fatal("binding visitor ran before whole canonical audit")
	}
}
