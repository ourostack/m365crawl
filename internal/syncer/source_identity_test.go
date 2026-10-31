package syncer

import (
	"context"
	"testing"

	"github.com/ourostack/m365crawl/internal/store"
)

// A sync records the caller's source identity keys before it reads any source, so the archive says
// which machine's sources feed it; a sync without keys records none (#295).
func TestRunRecordsTheSourceIdentity(t *testing.T) {
	isolateTmp(t)
	db := newDB(t)
	run(t, Options{Root: fixtureRoot, DBPath: db})
	st, err := store.OpenReadOnly(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	got, err := st.SourceIdentity(context.Background())
	_ = st.Close()
	if err != nil || len(got.Recorded) != 0 || len(got.TeamsSources) == 0 {
		t.Fatalf("without keys = %+v, %v", got, err)
	}
	run(t, Options{Root: fixtureRoot, DBPath: db, SourceIdentity: []string{"teams:t1", "outlook:o1"}})
	st, err = store.OpenReadOnly(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if got, err = st.SourceIdentity(context.Background()); err != nil || !got.Recorded["teams:t1"] || !got.Recorded["outlook:o1"] {
		t.Fatalf("with keys = %+v, %v", got, err)
	}
}
