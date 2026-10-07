package syncer

import (
	"context"
	"fmt"
	"testing"

	"github.com/ourostack/m365crawl/internal/store"
)

// sameArchive must see the difference between archives that differ in any one row, and nothing else.
func TestSameArchiveSeesRowDifferences(t *testing.T) {
	a, b := newDB(t), newDB(t)
	for _, p := range []string{a, b} {
		st, err := store.Open(context.Background(), p)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if !sameArchive(t, a, b) {
		t.Fatal("two empty archives differ")
	}
	insert := `insert into people(tenant_id, id, display_name, first_seen_at, last_seen_at) values ('t', 'p', '%s', 'x', 'y')`
	execSQL(t, a, fmt.Sprintf(insert, "Ann"))
	if sameArchive(t, a, b) || sameArchive(t, b, a) {
		t.Fatal("an extra row went unseen")
	}
	execSQL(t, b, fmt.Sprintf(insert, "Bob"))
	if sameArchive(t, a, b) {
		t.Fatal("a changed value went unseen")
	}
	execSQL(t, b, `update people set display_name = 'Ann'`)
	if !sameArchive(t, a, b) {
		t.Fatal("equal archives differ")
	}
}

// The archive of syncedStart is what a first sync of a fresh copy makes, and the copy's cache reads
// as unchanged against it, as it does after a real first sync.
func TestSyncedStartIsWhatAFirstSyncMakes(t *testing.T) {
	root, db := syncedStart(t)
	freshRoot, freshDB := fixtureCopy(t), newDB(t)
	run(t, Options{Root: freshRoot, DBPath: freshDB})
	if !sameArchive(t, db, freshDB) {
		t.Fatal("the template archive differs from a first sync of a copy")
	}
	for name, r := range map[string]struct{ root, db string }{"template": {root, db}, "fresh": {freshRoot, freshDB}} {
		if rep, _ := run(t, Options{Root: r.root, DBPath: r.db}); rep.Status != StatusUnchanged {
			t.Fatalf("%s: a second sync of the unchanged cache is %q", name, rep.Status)
		}
	}
}
