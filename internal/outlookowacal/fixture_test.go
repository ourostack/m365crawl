package outlookowacal

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/ourostack/m365crawl/internal/calendar"
	"github.com/ourostack/m365crawl/internal/hxstore"
	"github.com/ourostack/m365crawl/internal/indexeddb"
	"github.com/ourostack/m365crawl/internal/outlookcal"
	"github.com/ourostack/m365crawl/internal/outlookowa"
)

func chromiumCalendar(t *testing.T, fixture, variant string) *indexeddb.Origin {
	t.Helper()
	base := filepath.Join("..", "..", "testdata", fixture, variant, "IndexedDB")
	o, err := Open(context.Background(), filepath.Join(base, "https_outlook.office.com_0.indexeddb.leveldb"), filepath.Join(base, "https_outlook.office.com_0.indexeddb.blob"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := o.Close(); err != nil {
			t.Error(err)
		}
	})
	return o
}

func TestChromiumCalendarFixtureDetailsAndAllowlist(t *testing.T) {
	o := chromiumCalendar(t, "outlook-owa-calendar-local-fixture", "calendar")
	res, err := Collect(context.Background(), o, "outlook/Synthetic", false)
	if err != nil || res.Doubtful || len(res.Losses) != 0 || res.Notes != (CollectNotes{Records: 2, Events: 2}) || len(res.Events) != 2 {
		t.Fatalf("browser-written event census: %+v %v", res.Notes, err)
	}
	a, b := res.Events[0], res.Events[1]
	if a.SourceID != "aa11" || b.SourceID != "bb22" || a.AccountID != "outlook/Synthetic" ||
		a.BodyHTML != "<p>Synthetic &amp; cached calendar</p>" || a.BodyText != "Synthetic & cached calendar" || a.BodyType != "html" ||
		a.BodyPreview != "Synthetic calendar preview" || !a.AllDay.Is(false) || !a.Cancelled.Is(false) {
		t.Fatal("Chromium event identity, clocks, body or flags did not reach the mapper")
	}
	wantAttendees := `[{"name":"Synthetic required","address":"required@example.invalid","type":"required","response":"accepted"},{"name":"Synthetic optional","address":"optional@example.invalid","type":"optional","response":"tentative"},{"name":"Synthetic room","address":"room@example.invalid","type":"resource","response":"none"}]`
	if a.AttendeesJSON != wantAttendees || !b.AllDay.Is(true) || b.StartDate != "2031-03-06" || b.EndDate != "2031-03-07" || b.AttendeesJSON != "[]" {
		t.Fatal("browser-written roles or explicitly local-midnight all-day details were lost")
	}
	for _, event := range res.Events {
		if err := calendar.ValidateEvent(event); err != nil {
			t.Fatal(err)
		}
	}
	dbs, err := o.Databases()
	if err != nil {
		t.Fatal(err)
	}
	for _, db := range dbs {
		for _, store := range db.Stores {
			loaded := 0
			if err := o.Records(db.ID, store.ID, func(indexeddb.Record) error { loaded++; return nil }); err != nil {
				t.Fatal(err)
			}
			if store.Name != "events" && loaded != 0 {
				t.Fatal("calendar opening loaded unrelated mail, credential or payload values")
			}
		}
	}
}

func TestChromiumCalendarEmptyFixtureCannotErasePreviouslyObservedEvents(t *testing.T) {
	o := chromiumCalendar(t, "outlook-owa-calendar-local-fixture", "calendar-empty")
	res, err := Collect(context.Background(), o, "outlook/Synthetic", false)
	if err != nil || len(res.Events) != 0 || res.Doubtful || res.Notes.Records != 0 {
		t.Fatal("a newly observed empty event store was not representable")
	}
	_, err = Collect(context.Background(), o, "outlook/Synthetic", true)
	var guard *hxstore.GuardError
	if !errors.As(err, &guard) || guard.Detail != "owa_no_event_objects" {
		t.Fatal("an unexpectedly empty cached calendar became authoritative absence")
	}
}

func TestChromiumCalendarDamagedFixtureKeepsGoodEventsAndLoss(t *testing.T) {
	res, err := Collect(context.Background(), chromiumCalendar(t, "outlook-owa-calendar-local-fixture", "calendar-damaged"), "outlook/Synthetic", false)
	if err != nil || !res.Doubtful || len(res.Events) != 2 || len(res.SeenIDs) != 2 || res.Notes.Records != 3 ||
		res.Notes.DecodeFailures+res.Notes.UnmappedEvents != 1 || len(res.Losses) != 1 ||
		res.Losses[0].Code != outlookcal.CodeEventUnmapped || res.Losses[0].Count != 1 {
		t.Fatal("one bad browser-written event suppressed good rows or hid observation loss")
	}
}

func TestChromiumCalendarFixtureRetainsIndependentMail(t *testing.T) {
	base := filepath.Join("..", "..", "testdata", "outlook-owa-calendar-local-fixture", "calendar", "IndexedDB")
	o, err := outlookowa.Open(context.Background(), filepath.Join(base, "https_outlook.office.com_0.indexeddb.leveldb"), filepath.Join(base, "https_outlook.office.com_0.indexeddb.blob"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := o.Close(); err != nil {
			t.Error(err)
		}
	}()
	res, err := outlookowa.Collect(context.Background(), o, "outlook/Synthetic", false)
	if err != nil || len(res.Messages) != 5 || len(res.Losses) != 0 {
		t.Fatal("the new event fixture changed the independent mail census")
	}
}
