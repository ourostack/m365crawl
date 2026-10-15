package syncer

import (
	"context"
	"testing"

	"github.com/ourostack/m365crawl/internal/store"
)

func TestAttendeeLinkScopeReadFailure(t *testing.T) {
	isolateTmp(t)
	db := newDB(t)
	root := outlookRoot(t, "HxStore.hxd")
	ctx := context.Background()
	s, err := store.Open(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if _, err := openRaw(t, db).Exec(`drop table calendar_account_links`); err != nil {
		t.Fatal(err)
	}
	r := runner{st: s, o: outlookOpts(db, root)}
	r.o.OutlookLink = OutlookLinkNone
	if _, err := r.linkProfiles(ctx); err == nil {
		t.Fatal("missing current owner scope was treated as unlinked")
	}
}
