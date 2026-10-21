package outlookowacal

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/hxstore"
	"github.com/ourostack/m365crawl/internal/indexeddb"
	"github.com/ourostack/m365crawl/internal/leveldb"
)

func fixture(variant string) (string, string) {
	base := filepath.Join("..", "..", "testdata", "outlook-owa-fixture", variant, "IndexedDB")
	return filepath.Join(base, "https_outlook.office.com_0.indexeddb.leveldb"),
		filepath.Join(base, "https_outlook.office.com_0.indexeddb.blob")
}

func TestOpenCalendarLoadsNoMailOrCredentialValues(t *testing.T) {
	dir, blob := fixture("mail")
	o, err := Open(context.Background(), dir, blob)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := o.Close(); err != nil {
			t.Error(err)
		}
	}()
	dbs, err := o.Databases()
	if err != nil {
		t.Fatal(err)
	}
	events := 0
	for _, db := range dbs {
		for _, store := range db.Stores {
			count := 0
			if err := o.Records(db.ID, store.ID, func(indexeddb.Record) error { count++; return nil }); err != nil {
				t.Fatal(err)
			}
			if store.Name == "events" {
				events += count
			} else if count != 0 {
				t.Fatal("noncalendar values were loaded")
			}
		}
	}
	if events != 1 || o.Stats().Skipped == 0 {
		t.Fatal("event allowlist did not retain exactly the synthetic event decoy")
	}
	// The mail fixture deliberately contains no valid event, only a host-object decoy.
	_, err = Collect(context.Background(), o, "A", false)
	var guard *hxstore.GuardError
	if !errors.As(err, &guard) || guard.Detail != "owa_no_mapped_events" {
		t.Fatal("unmappable real fixture bytes became an empty calendar")
	}
}

func TestOpenCalendarRefusesMetadataAndLayoutsBeforeValues(t *testing.T) {
	for _, tc := range []struct{ variant, detail string }{
		{"unknown", "owa_content_database_missing"}, {"multiple", "owa_multiple_mailboxes"},
	} {
		dir, blob := fixture(tc.variant)
		o, err := Open(context.Background(), dir, blob)
		var guard *hxstore.GuardError
		if o != nil || !errors.As(err, &guard) || guard.Detail != tc.detail {
			t.Fatal("unsupported calendar layout was opened")
		}
	}
	_, err := Open(context.Background(), filepath.Join(t.TempDir(), "absent"), "")
	if err == nil || strings.Contains(err.Error(), "absent") {
		t.Fatal("metadata failure leaked its private path or became success")
	}
	census := calendarCensus
	t.Cleanup(func() { calendarCensus = census })
	calendarCensus = func(string) (map[int64]int64, []indexeddb.Database, leveldb.Stats, error) {
		return nil, []indexeddb.Database{content()}, leveldb.Stats{TruncatedLogTails: 1}, nil
	}
	_, err = Open(context.Background(), "unused", "")
	var guard *hxstore.GuardError
	if !errors.As(err, &guard) || guard.Detail != "owa_metadata_truncated" {
		t.Fatal("truncated metadata selected a potentially incomplete mailbox set")
	}
}

func TestOpenCalendarCancellationAndDataFailuresReleaseOrigin(t *testing.T) {
	for _, where := range []string{"entry", "after-census", "open-failed", "after-open", "close-failed"} {
		t.Run(where, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			census, open, closeOrigin := calendarCensus, calendarOpenWith, calendarClose
			t.Cleanup(func() { calendarCensus, calendarOpenWith, calendarClose = census, open, closeOrigin })
			closed := false
			switch where {
			case "entry":
				cancel()
			case "after-census":
				calendarCensus = func(dir string) (map[int64]int64, []indexeddb.Database, leveldb.Stats, error) {
					held, dbs, stats, err := census(dir)
					cancel()
					return held, dbs, stats, err
				}
			case "open-failed":
				calendarOpenWith = func(string, string, indexeddb.OpenOptions) (*indexeddb.Origin, error) {
					return nil, errors.New("private-open-failure")
				}
			default:
				calendarOpenWith = func(dir, blob string, opts indexeddb.OpenOptions) (*indexeddb.Origin, error) {
					o, err := open(dir, blob, opts)
					if err == nil {
						cancel()
					}
					return o, err
				}
				calendarClose = func(o *indexeddb.Origin) error {
					closed = true
					err := closeOrigin(o)
					if where == "close-failed" && err == nil {
						return errors.New("private-close-failure")
					}
					return err
				}
			}
			dir, blob := fixture("mail")
			o, err := Open(ctx, dir, blob)
			if o != nil || err == nil || strings.Contains(err.Error(), "private") {
				t.Fatal("failed open returned a live origin or private diagnostic")
			}
			if where != "open-failed" && !errors.Is(err, context.Canceled) {
				t.Fatal("open cancellation was lost")
			}
			if (where == "after-open" || where == "close-failed") && !closed {
				t.Fatal("canceled origin was not closed")
			}
			if where == "close-failed" && !strings.Contains(err.Error(), "outlook_owa_calendar_close_failed") {
				t.Fatal("failed close was hidden by cancellation")
			}
			if where == "close-failed" && !errors.Is(err, ErrReaderClose) {
				t.Fatal("failed open did not expose its unproved reader release")
			}
		})
	}
}
