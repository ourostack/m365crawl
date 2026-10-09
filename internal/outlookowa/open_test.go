package outlookowa

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/ourostack/m365crawl/internal/hxstore"
	"github.com/ourostack/m365crawl/internal/indexeddb"
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
	for _, tc := range []struct{ variant, reason string }{{"unknown", "content_database_missing"}, {"multiple", "multiple_mailboxes"}, {"missing-store", "required_store_missing"}} {
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
