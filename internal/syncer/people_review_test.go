package syncer

import (
	"context"
	"database/sql"
	"os"
	"testing"

	"github.com/ourostack/m365crawl/internal/outlookdesktop"
	"github.com/ourostack/m365crawl/internal/outlookpeople"
)

func TestPeopleReviewedPanicKeepsPeersAndCleansCopy(t *testing.T) {
	isolateTmp(t)
	root, db := mailRoot(t), newDB(t)
	oldCollect := peopleCollect
	t.Cleanup(func() { peopleCollect = oldCollect })
	peopleCollect = func(context.Context, outlookdesktop.Info) (outlookpeople.Result, string, error) {
		return outlookpeople.Result{}, "one", nil
	}
	if _, err := SyncPeople(context.Background(), PeopleOptions{DBPath: db, Root: root, Explicit: true, Discover: true}); err != nil {
		t.Fatal(err)
	}
	var copyPath string
	oldSnapshot := outlookSnapshot
	t.Cleanup(func() { outlookSnapshot = oldSnapshot })
	outlookSnapshot = func(ctx context.Context, path string) (outlookdesktop.Info, func(), error) {
		info, cleanup, err := oldSnapshot(ctx, path)
		copyPath = info.Path
		t.Cleanup(cleanup)
		return info, cleanup, err
	}
	peopleCollect = func(context.Context, outlookdesktop.Info) (outlookpeople.Result, string, error) {
		panic("synthetic optional failure")
	}
	opts := outlookOpts(db, root)
	opts.FullRead = true
	report, _, err := Run(context.Background(), opts)
	if err != nil || report.Status == "partial" || report.Status == "failed" {
		t.Errorf("optional panic poisoned peers: %s %v", report.Status, err)
	}
	if _, err := os.Lstat(copyPath); !os.IsNotExist(err) {
		t.Error("private copy survived optional panic")
	}
	handle, err := sql.Open("sqlite", db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = handle.Close() }()
	var unfinished int
	if err := handle.QueryRow("select count(*) from knowledge_attempts where finished_at is null").Scan(&unfinished); err != nil || unfinished != 0 {
		t.Errorf("nonterminal panic: %d %v", unfinished, err)
	}
}
