package syncer

import (
	"context"
	"os"
	"testing"

	"github.com/ourostack/m365crawl/internal/outlookdesktop"
	"github.com/ourostack/m365crawl/internal/outlookpeople"
)

func TestPeopleReviewedStandalonePanicIsTerminalAndClean(t *testing.T) {
	root := mailRoot(t)
	oldCollect, oldSnapshot := peopleCollect, peopleSnapshot
	t.Cleanup(func() { peopleCollect, peopleSnapshot = oldCollect, oldSnapshot })
	var path string
	peopleSnapshot = func(ctx context.Context, source string) (outlookdesktop.Info, func(), error) {
		info, cleanup, err := oldSnapshot(ctx, source)
		path = info.Path
		t.Cleanup(cleanup)
		return info, cleanup, err
	}
	peopleCollect = func(context.Context, outlookdesktop.Info) (outlookpeople.Result, string, error) {
		panic("synthetic standalone")
	}
	report, err := SyncPeople(context.Background(), PeopleOptions{DBPath: newDB(t), Root: root, Explicit: true, Discover: true})
	if err != nil || report.Failed != 1 || report.Sources[0].Status != "failed" {
		t.Fatalf("standalone panic: %+v %v", report, err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatal("standalone panic retained private copy")
	}
}
