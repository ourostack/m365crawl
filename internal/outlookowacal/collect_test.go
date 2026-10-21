package outlookowacal

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/hxstore"
	"github.com/ourostack/m365crawl/internal/indexeddb"
	"github.com/ourostack/m365crawl/internal/leveldb"
	"github.com/ourostack/m365crawl/internal/outlookcal"
)

type calendarReader struct {
	dbs                               []indexeddb.Database
	values                            []any
	metadataErr, scanErr, decodeErr   error
	recordErr                         error
	beforeScan, afterScan, beforeMeta func()
	stats                             leveldb.Stats
}

func content() indexeddb.Database {
	return indexeddb.Database{ID: 2, Name: "owa-offline-data-Synthetic", Stores: []indexeddb.Store{
		{ID: 3, Name: "events"}, {ID: 4, Name: "messages"}, {ID: 5, Name: "credential-decoy"},
	}}
}

func (r *calendarReader) Databases() ([]indexeddb.Database, error) {
	if r.beforeMeta != nil {
		r.beforeMeta()
	}
	return r.dbs, r.metadataErr
}

func (r *calendarReader) Stats() leveldb.Stats { return r.stats }

func (r *calendarReader) Records(db, store int64, fn func(indexeddb.Record) error) error {
	if db != 2 || store != 3 {
		return errors.New("collector read outside events allowlist")
	}
	if r.beforeScan != nil {
		r.beforeScan()
	}
	for i := range r.values {
		if i > 255 {
			return errors.New("synthetic record count exceeds byte range")
		}
		if err := fn(indexeddb.Record{Raw: []byte{byte(i)}, Err: r.recordErr}); err != nil {
			return err
		}
	}
	if r.afterScan != nil {
		r.afterScan()
	}
	return r.scanErr
}

func (r *calendarReader) Decode(db int64, raw []byte) (any, error) {
	if db != 2 || len(raw) != 1 || int(raw[0]) >= len(r.values) {
		return nil, errors.New("synthetic decode outside source")
	}
	return r.values[raw[0]], r.decodeErr
}

func lossCount(r Result, code string) int {
	for _, loss := range r.Losses {
		if loss.Code == code {
			return loss.Count
		}
	}
	return 0
}

func TestCollectCalendarEventsOnlyAndCanonicalOrder(t *testing.T) {
	a, b := event(), event()
	replace(a, "UID", "BB")
	replace(b, "UID", "AA")
	input := &calendarReader{dbs: []indexeddb.Database{content()}, values: []any{a, b, a}}
	got, err := collect(context.Background(), input, "outlook/Synthetic", true)
	if err != nil {
		t.Fatal(err)
	}
	if got.Doubtful || len(got.Losses) != 0 || len(got.Events) != 2 || got.Events[0].SourceID != "aa" || got.Events[1].SourceID != "bb" ||
		!reflect.DeepEqual(got.SeenIDs, []string{"aa", "bb"}) || got.Notes.Records != 3 || got.Notes.Events != 2 {
		t.Fatalf("canonical event collection: %+v", got)
	}
	want, _, err := MapEvent("outlook/Synthetic", b)
	if err != nil || !reflect.DeepEqual(got.Events[0], want) {
		t.Fatal("collector changed the mapper's event contract")
	}
}

func TestCalendarStoreLayoutGuards(t *testing.T) {
	missing := content()
	missing.Stores = []indexeddb.Store{{ID: 4, Name: "messages"}}
	ambiguous := content()
	ambiguous.Stores = append(ambiguous.Stores, indexeddb.Store{ID: 8, Name: "events"})
	for _, tc := range []struct {
		dbs    []indexeddb.Database
		detail string
	}{
		{nil, "owa_content_database_missing"},
		{[]indexeddb.Database{{ID: 1, Name: "OWA-OFFLINE-DATA", Stores: []indexeddb.Store{{ID: 3, Name: "events"}}}}, "owa_content_database_missing"},
		{[]indexeddb.Database{{Name: "owa-offline-data"}}, "owa_events_missing"},
		{[]indexeddb.Database{content(), content()}, "owa_multiple_mailboxes"},
		{[]indexeddb.Database{missing}, "owa_events_missing"},
		{[]indexeddb.Database{ambiguous}, "owa_events_ambiguous"},
	} {
		got, err := collect(context.Background(), &calendarReader{dbs: tc.dbs}, "A", false)
		var guard *hxstore.GuardError
		if !errors.As(err, &guard) || guard.Code != outlookcal.CodeLayoutUnsupported || guard.Detail != tc.detail || len(got.Events) != 0 {
			t.Fatalf("%s layout accepted: %v", tc.detail, err)
		}
	}
	db := content()
	db.Name = "owa-offline-data"
	_, err := collect(context.Background(), &calendarReader{dbs: []indexeddb.Database{{Name: "owa-offline-data-stale"}, db}}, "A", false)
	if err != nil {
		t.Fatal("empty metadata-only names blocked the sole populated content database")
	}
}

func TestCollectCalendarRefusesFalseEmptyAndPrivateErrors(t *testing.T) {
	for _, tc := range []struct {
		expect bool
		values []any
		detail string
	}{
		{true, nil, "owa_no_event_objects"},
		{false, []any{"private-invalid-event"}, "owa_no_mapped_events"},
	} {
		_, err := collect(context.Background(), &calendarReader{dbs: []indexeddb.Database{content()}, values: tc.values}, "A", tc.expect)
		var guard *hxstore.GuardError
		if !errors.As(err, &guard) || guard.Detail != tc.detail {
			t.Fatal("damaged calendar became an empty success")
		}
	}
	for _, input := range []*calendarReader{
		{metadataErr: errors.New("private-metadata")},
		{dbs: []indexeddb.Database{content()}, scanErr: errors.New("private-scan")},
	} {
		got, err := collect(context.Background(), input, "A", false)
		if err == nil || strings.Contains(err.Error(), "private") || len(got.Events) != 0 {
			t.Fatal("reader error leaked data or became a success")
		}
	}
	empty, err := collect(context.Background(), &calendarReader{dbs: []indexeddb.Database{content()}}, "A", false)
	if err != nil || len(empty.Events) != 0 || empty.Doubtful {
		t.Fatal("new empty calendar was refused")
	}
	if _, err := Collect(context.Background(), nil, "A", false); err == nil {
		t.Fatal("nil origin admitted")
	}
	if _, err := collect(context.Background(), &calendarReader{}, "", false); err == nil {
		t.Fatal("empty account admitted")
	}
}

func TestCollectCalendarLossesKeepGoodEventsAndPreventAbsence(t *testing.T) {
	bad := event()
	replace(bad, "Start", "private-invalid-time")
	damaged := event()
	replace(damaged, "Subject", 42.0)
	replace(damaged, "StartTimeZoneId", "Synthetic Unknown Zone")
	replace(damaged, "IsAllDayEvent", true)
	replace(damaged, "OnlineMeetingJoinUrl", "https://example.test?sig=synthetic-secret")
	replace(damaged, "RequiredAttendees", []any{})
	got, err := collect(context.Background(), &calendarReader{
		dbs: []indexeddb.Database{content()}, values: []any{bad, damaged},
		stats: leveldb.Stats{TruncatedLogTails: 1},
	}, "A", true)
	if err != nil || !got.Doubtful || len(got.Events) != 1 || got.Notes.UnmappedEvents != 1 || got.Notes.UnmappedFields != 1 ||
		got.Notes.UnknownZone != 1 || got.Notes.AllDayUnaligned != 1 || got.Notes.Redacted == 0 || got.Notes.PartialAttendees != 1 ||
		lossCount(got, outlookcal.CodeEventUnmapped) != 5 || lossCount(got, "outlook_owa_reader_truncated_log_tail") != 1 {
		t.Fatalf("losses=%+v err=%v", got, err)
	}
	for _, input := range []*calendarReader{
		{dbs: []indexeddb.Database{content()}, values: []any{event()}, recordErr: errors.New("private-record")},
		{dbs: []indexeddb.Database{content()}, values: []any{event()}, decodeErr: errors.New("private-decode")},
	} {
		_, err := collect(context.Background(), input, "A", false)
		if err == nil || strings.Contains(err.Error(), "private") {
			t.Fatal("undecodable records became a zero-event success or leaked values")
		}
	}
}

func TestCollectCalendarConflictingDuplicatesHaveNoWinner(t *testing.T) {
	first, other, good := event(), event(), event()
	replace(other, "Subject", "Synthetic conflict")
	replace(good, "UID", "CC")
	got, err := collect(context.Background(), &calendarReader{dbs: []indexeddb.Database{content()}, values: []any{first, other, first, good}}, "A", true)
	if err != nil || !got.Doubtful || got.Notes.DuplicateConflicts != 1 || len(got.Events) != 1 || got.Events[0].SourceID != "cc" ||
		len(got.SeenIDs) != 2 || lossCount(got, "outlook_owa_duplicate_conflict") != 1 {
		t.Fatalf("duplicate acquired a winner: %+v %v", got, err)
	}
}

func TestCollectCalendarCancellationAtEveryReaderBoundary(t *testing.T) {
	for _, boundary := range []string{"entry", "metadata", "scan", "after-scan", "scan-error"} {
		ctx, cancel := context.WithCancel(context.Background())
		input := &calendarReader{dbs: []indexeddb.Database{content()}, values: []any{event()}}
		switch boundary {
		case "entry":
			cancel()
		case "metadata":
			input.beforeMeta = cancel
		case "scan":
			input.beforeScan = cancel
		case "after-scan":
			input.afterScan = cancel
		case "scan-error":
			input.scanErr = context.Canceled
		}
		_, err := collect(ctx, input, "A", false)
		cancel()
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("%s cancellation lost: %v", boundary, err)
		}
	}
}
