package outlookowacal

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"strings"

	"github.com/ourostack/m365crawl/internal/calendar"
	"github.com/ourostack/m365crawl/internal/hxstore"
	"github.com/ourostack/m365crawl/internal/indexeddb"
	"github.com/ourostack/m365crawl/internal/leveldb"
	"github.com/ourostack/m365crawl/internal/outlookcal"
)

// CollectNotes contains counts of events and observation loss.
type CollectNotes struct {
	Records, Events, DecodeFailures, UnmappedEvents, DuplicateConflicts int
	UnmappedFields, UnknownZone, AllDayUnaligned, Redacted              int
}

// Result distinguishes observed identities from confidently mapped events.
type Result struct {
	Events   []calendar.Event
	SeenIDs  []string
	Notes    CollectNotes
	Losses   []outlookcal.Loss
	Doubtful bool
}

type reader interface {
	Databases() ([]indexeddb.Database, error)
	Records(int64, int64, func(indexeddb.Record) error) error
	Decode(int64, []byte) (any, error)
	Stats() leveldb.Stats
}

// Collect reads only the sole recognized content database's events store.
func Collect(ctx context.Context, origin *indexeddb.Origin, account string, expectEvents bool) (Result, error) {
	if origin == nil {
		return Result{}, errors.New("outlook_owa_calendar_source_missing")
	}
	return collect(ctx, origin, account, expectEvents)
}

func collect(ctx context.Context, origin reader, account string, expectEvents bool) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if strings.TrimSpace(account) == "" {
		return Result{}, errors.New("outlook_owa_calendar_account_missing")
	}
	dbs, err := origin.Databases()
	if err != nil {
		return Result{}, errors.New("outlook_owa_calendar_metadata_unreadable")
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	dbID, storeID, err := selectEvents(dbs)
	if err != nil {
		return Result{}, err
	}
	var result Result
	if tails := origin.Stats().TruncatedLogTails; tails > 0 {
		result.loss("outlook_owa_reader_truncated_log_tail", tails)
	}
	values := map[string]any{}
	events := map[string]calendar.Event{}
	conflicts := map[string]bool{}
	seen := map[string]bool{}
	err = origin.Records(dbID, storeID, func(rec indexeddb.Record) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		result.Notes.Records++
		var value any
		decodeErr := rec.Err
		if decodeErr == nil {
			value, decodeErr = origin.Decode(dbID, rec.Raw)
		}
		if decodeErr != nil {
			result.Notes.DecodeFailures++
			result.loss(outlookcal.CodeEventUnmapped, 1)
			return nil
		}
		event, notes, mapErr := MapEvent(account, value)
		if mapErr != nil {
			result.Notes.UnmappedEvents++
			result.loss(outlookcal.CodeEventUnmapped, 1)
			return nil
		}
		result.Notes.UnmappedFields += notes.Unmapped
		result.Notes.UnknownZone += notes.UnknownZone
		result.Notes.AllDayUnaligned += notes.AllDayUnaligned
		result.Notes.Redacted += notes.Redacted
		if losses := notes.Unmapped + notes.UnknownZone + notes.AllDayUnaligned; losses > 0 {
			result.loss(outlookcal.CodeEventUnmapped, losses)
		}
		id := event.SourceID
		seen[id] = true
		if conflicts[id] {
			return nil
		}
		if old, exists := values[id]; exists {
			if !reflect.DeepEqual(old, value) {
				delete(events, id)
				delete(values, id)
				conflicts[id] = true
				result.Notes.DuplicateConflicts++
				result.loss("outlook_owa_duplicate_conflict", 1)
			}
			return nil
		}
		values[id], events[id] = value, event
		return nil
	})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return Result{}, err
		}
		return Result{}, errors.New("outlook_owa_calendar_records_unreadable")
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if result.Notes.Records == 0 && expectEvents {
		return Result{}, layout("owa_no_event_objects")
	}
	if result.Notes.Records > 0 && len(events) == 0 {
		return Result{}, layout("owa_no_mapped_events")
	}
	for _, event := range events {
		result.Events = append(result.Events, event)
	}
	for id := range seen {
		result.SeenIDs = append(result.SeenIDs, id)
	}
	sort.Slice(result.Events, func(i, j int) bool { return result.Events[i].SourceID < result.Events[j].SourceID })
	sort.Strings(result.SeenIDs)
	sort.Slice(result.Losses, func(i, j int) bool { return result.Losses[i].Code < result.Losses[j].Code })
	result.Notes.Events = len(result.Events)
	return result, nil
}

func selectEvents(dbs []indexeddb.Database) (int64, int64, error) {
	matched := false
	var populated []indexeddb.Database
	for _, db := range dbs {
		if db.Name == "owa-offline-data" || strings.HasPrefix(db.Name, "owa-offline-data-") {
			matched = true
			if len(db.Stores) > 0 {
				populated = append(populated, db)
			}
		}
	}
	if !matched {
		return 0, 0, layout("owa_content_database_missing")
	}
	if len(populated) == 0 {
		return 0, 0, layout("owa_events_missing")
	}
	if len(populated) != 1 {
		return 0, 0, layout("owa_multiple_mailboxes")
	}
	db := populated[0]
	found, storeID := false, int64(0)
	for _, store := range db.Stores {
		if store.Name == "events" {
			if found {
				return 0, 0, layout("owa_events_ambiguous")
			}
			found, storeID = true, store.ID
		}
	}
	if !found {
		return 0, 0, layout("owa_events_missing")
	}
	return db.ID, storeID, nil
}

func layout(detail string) error {
	return &hxstore.GuardError{Code: outlookcal.CodeLayoutUnsupported, Detail: detail}
}

func (r *Result) loss(code string, count int) {
	r.Doubtful = true
	for i := range r.Losses {
		if r.Losses[i].Code == code {
			r.Losses[i].Count += count
			return
		}
	}
	r.Losses = append(r.Losses, outlookcal.Loss{Code: code, Count: count})
}
