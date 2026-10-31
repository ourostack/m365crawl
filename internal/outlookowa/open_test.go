package outlookowa

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ourostack/m365crawl/internal/hxstore"
	"github.com/ourostack/m365crawl/internal/indexeddb"
	"github.com/ourostack/m365crawl/internal/leveldb"
	"github.com/ourostack/m365crawl/internal/teamsdesktop"
	gl "github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"
	"github.com/syndtr/goleveldb/leveldb/util"
)

func TestOpenFiltersValuesBeforeCollection(t *testing.T) {
	base := filepath.Join("..", "..", "testdata", "outlook-owa-fixture", "mail", "IndexedDB")
	o, err := Open(context.Background(), filepath.Join(base, fixtureOrigin+".indexeddb.leveldb"), filepath.Join(base, fixtureOrigin+".indexeddb.blob"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = o.Close() }()
	dbs, err := o.Databases()
	if err != nil {
		t.Fatal(err)
	}
	kept := 0
	for _, db := range dbs {
		for _, s := range db.Stores {
			n := 0
			if err := o.Records(db.ID, s.ID, func(indexeddb.Record) error { n++; return nil }); err != nil {
				t.Fatal(err)
			}
			switch s.Name {
			case "folders", "messages", "messageBodies":
				kept += n
			default:
				if n != 0 {
					t.Fatalf("nonmail values loaded: %d", n)
				}
			}
		}
	}
	// Seven folders, five headers, four bodies (the sparse header has no body).
	if kept != 16 || o.Stats().Skipped == 0 {
		t.Fatalf("filter discarded mail or retained decoys: %d %+v", kept, o.Stats())
	}
	r, err := Collect(context.Background(), o, "outlook/Default", true)
	if err != nil || len(r.Messages) != 5 || r.Doubtful {
		t.Fatalf("filtered real collection: %+v %v", r.Notes, err)
	}
}

func TestOpenRefusesLayoutsBeforeLoadingValues(t *testing.T) {
	for _, tc := range []struct{ variant, reason string }{{"unknown", "content_database_missing"}, {"missing-store", "required_store_missing"}} {
		t.Run(tc.variant, func(t *testing.T) {
			base := filepath.Join("..", "..", "testdata", "outlook-owa-fixture", tc.variant, "IndexedDB")
			o, err := Open(context.Background(), filepath.Join(base, fixtureOrigin+".indexeddb.leveldb"), "")
			if o != nil {
				_ = o.Close()
			}

			var guard *hxstore.GuardError
			if o != nil || !errors.As(err, &guard) || guard.Detail != tc.reason {
				t.Fatalf("layout loaded: %v %v", o, err)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Open(ctx, filepath.Join(t.TempDir(), "absent"), ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled open: %v", err)
	}
	if _, err := Open(context.Background(), filepath.Join(t.TempDir(), "absent"), ""); err == nil || err.Error() != "outlookowa: metadata_unreadable" {
		t.Fatalf("metadata diagnostic: %v", err)
	}
}

// Compact a disposable copy of the exact synthetic bytes using indexeddb's existing lazy-table test tooling; neither the committed fixture nor its generator changes.
func tableProfile(t *testing.T) Profile {
	t.Helper()
	p := fixtureProfile(t, sourceTempDir(t), "Default", "mail", true)
	src, err := leveldb.Load(p.Source.LevelDBDir)
	if err != nil {
		t.Fatal(err)
	}
	kv := map[string][]byte{}
	if err := src.Scan(nil, func(k, v []byte) error {
		kv[string(k)] = append([]byte(nil), v...)
		return nil
	}); err != nil {
		_ = src.Close()
		t.Fatal(err)
	}
	if err := src.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(p.Source.LevelDBDir, p.Source.LevelDBDir+".original-log-copy"); err != nil {
		t.Fatal(err)
	}
	db, err := gl.OpenFile(p.Source.LevelDBDir, &opt.Options{Compression: opt.SnappyCompression, BlockSize: 2048})
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range kv {
		if err := db.Put([]byte(k), v, nil); err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
	}
	if err := db.CompactRange(util.Range{}); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return p
}

func mailHeaderStore(t *testing.T, o *indexeddb.Origin) (int64, int64) {
	t.Helper()
	dbs, err := o.Databases()
	if err != nil {
		t.Fatal(err)
	}
	for _, db := range dbs {
		if db.Name == "owa-offline-data-synthetic" {
			for _, s := range db.Stores {
				if s.Name == "messages" {
					return db.ID, s.ID
				}
			}
		}
	}
	t.Fatal("synthetic mail header metadata missing")
	return 0, 0
}

func TestOpenCancellationAndDisappearingDataDoNotReturnAnOrigin(t *testing.T) {
	for _, where := range []string{"after-census", "data-open", "after-open"} {
		t.Run(where, func(t *testing.T) {
			p := tableProfile(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			census, open := mailCensus, mailOpenWith
			t.Cleanup(func() { mailCensus, mailOpenWith = census, open })
			var retained *indexeddb.Origin
			var dbID, storeID int64
			if where == "after-open" {
				mailOpenWith = func(dir, blob string, opts indexeddb.OpenOptions) (*indexeddb.Origin, error) {
					o, err := open(dir, blob, opts)
					if err == nil {
						retained = o
						dbID, storeID = mailHeaderStore(t, o)
						cancel()
					}
					return o, err
				}
			} else {
				mailCensus = func(dir string) (map[int64]int64, []indexeddb.Database, error) {
					held, dbs, err := census(dir)
					if err == nil {
						if where == "after-census" {
							cancel()
						} else {
							err = os.Remove(filepath.Join(dir, "CURRENT"))
						}
					}
					return held, dbs, err
				}
			}
			o, err := Open(ctx, p.Source.LevelDBDir, p.Source.BlobDir)
			if o != nil {
				_ = o.Close()
				t.Fatal("failed/canceled data opening returned an eligible origin")
			}
			if where == "data-open" {
				if err == nil || err.Error() != "outlookowa: records_unreadable" {
					t.Fatalf("Census-to-Open data loss was not an error: %v", err)
				}
			} else if !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation was hidden: %v", err)
			}
			if retained != nil {
				if err := retained.Records(dbID, storeID, func(indexeddb.Record) error { return nil }); !errors.Is(err, leveldb.ErrClosed) {
					t.Fatalf("canceled open retained readable lazy tables: %v", err)
				}
			}
		})
	}
}

func TestOpenRealLazySnapshotReadsCloseBeforeCleanup(t *testing.T) {
	p := tableProfile(t)
	tmp := sourceTempDir(t)
	for _, name := range []string{"TEMP", "TMP", "TMPDIR"} {
		t.Setenv(name, tmp)
	}
	snap, cleanup, err := teamsdesktop.Snapshot(context.Background(), p.Source)
	defer cleanup()
	if err != nil {
		t.Fatal(err)
	}
	o, err := Open(context.Background(), filepath.Join(snap, "leveldb"), filepath.Join(snap, "blob"))
	if err != nil {
		t.Fatal(err)
	}
	dbID, storeID := mailHeaderStore(t, o)
	large := false
	if err := o.Records(dbID, storeID, func(r indexeddb.Record) error {
		large = large || len(r.Raw) >= leveldb.LazyMin()
		return nil
	}); err != nil {
		_ = o.Close()
		t.Fatal(err)
	}
	if !large || o.Stats().Tables == 0 {
		_ = o.Close()
		t.Fatal("scenario did not exercise an actual lazy table value")
	}
	res, err := Collect(context.Background(), o, "outlook/Default", true)
	if closeErr := o.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil || len(res.Messages) != 5 || res.Doubtful {
		t.Fatalf("real lazy mail collection: %+v %v", res.Notes, err)
	}
	if err := o.Records(dbID, storeID, func(indexeddb.Record) error { return nil }); !errors.Is(err, leveldb.ErrClosed) {
		t.Fatalf("lazy records remained readable after Close: %v", err)
	}
	cleanup()
	if _, err := os.Stat(snap); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("closed private table snapshot was not cleaned: %v", err)
	}
}

func TestOpenAdmitsSeveralMailboxesWithoutProvingTheirIdentity(t *testing.T) {
	base := filepath.Join("..", "..", "testdata", "outlook-owa-fixture", "multiple", "IndexedDB")
	o, err := Open(context.Background(), filepath.Join(base, fixtureOrigin+".indexeddb.leveldb"), filepath.Join(base, fixtureOrigin+".indexeddb.blob"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = o.Close() })
	mailboxes, err := CollectMailboxes(context.Background(), o)
	if err != nil || len(mailboxes) != 2 {
		t.Fatalf("several mailboxes = %d, %v", len(mailboxes), err)
	}
	for _, mb := range mailboxes {
		// The synthetic fixture carries no mailboxInfo, so neither mailbox is attributable.
		if mb.Identity != "" || len(mb.Result.Messages) != 5 {
			t.Fatalf("fixture mailbox = %q, %d messages", mb.Identity, len(mb.Result.Messages))
		}
	}
	var guard *hxstore.GuardError
	if _, err := Collect(context.Background(), o, "outlook/Default", true); !errors.As(err, &guard) || guard.Detail != "multiple_mailboxes" {
		t.Fatalf("single-mailbox collection must still refuse several mailboxes: %v", err)
	}
	if _, err := CollectMailboxes(context.Background(), nil); err == nil {
		t.Fatal("nil origin accepted")
	}
}
