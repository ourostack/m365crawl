package syncer

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ourostack/m365crawl/internal/outlookdesktop"
)

func TestPeopleReviewedSymlinkSourceIsExplicitlyRefused(t *testing.T) {
	root, db := mailRoot(t), newDB(t)
	source := filepath.Join(root, "Main", outlookdesktop.StoreFileName)
	target := source + ".target"
	if err := os.Rename(source, target); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, source); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	report, err := SyncPeople(context.Background(), PeopleOptions{DBPath: db, Root: root, Explicit: true, Discover: true})
	if err != nil || report.Failed != 1 || report.Captured != 0 {
		t.Fatalf("unsafe alias was admitted: %+v %v", report, err)
	}
	if len(report.Sources) != 1 || report.Sources[0].Reason != "outlook_people_source_unsafe" {
		t.Fatalf("alias refusal not explicit: %+v", report)
	}
}
