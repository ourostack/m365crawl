package syncer

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/outlookdesktop"
	"github.com/ourostack/m365crawl/internal/outlookpeople"
	"github.com/ourostack/m365crawl/internal/store"
)

func TestPeopleSchedulingTerminalAndCancellationErrors(t *testing.T) {
	for _, stage := range []string{"begin", "diagnostic", "cancel"} {
		t.Run(stage, func(t *testing.T) {
			db := newDB(t)
			s, err := store.Open(context.Background(), db)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.Close() }()
			path := filepath.Join(t.TempDir(), "missing")
			if _, err := s.BeginKnowledgeAttempt(context.Background(), store.KnowledgeInput{Kind: "outlook_people",
				Platform: "synthetic", Locator: path, At: time.Now()}); err != nil {
				t.Fatal(err)
			}
			now := peopleNow
			t.Cleanup(func() { peopleNow = now })
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			count := 0
			peopleNow = func() time.Time {
				count++
				if stage == "begin" && count == 1 || stage == "diagnostic" && count == 2 {
					if err := s.Close(); err != nil {
						t.Fatal(err)
					}
				}
				return time.Now()
			}
			options := PeopleOptions{Explicit: true}
			if stage == "cancel" {
				root := t.TempDir()
				options.Root, options.Discover = root, true
				resetPeopleSeams(t)
				peopleDiscover = func(string) ([]outlookdesktop.Profile, []string, []outlookdesktop.SkippedProfile, error) {
					cancel()
					return []outlookdesktop.Profile{{Name: "missing", StorePath: filepath.Join(root, "missing", "HxStore.hxd")}}, nil, nil, nil
				}
			}
			if _, err := syncPeople(ctx, s, options); err == nil {
				t.Fatal("scheduling/terminal error hidden")
			}
		})
	}
}

func TestPeoplePartialStandaloneAndSharedUnrelatedLocator(t *testing.T) {
	root := mailRoot(t)
	db := newDB(t)
	old := peopleCollect
	t.Cleanup(func() { peopleCollect = old })
	peopleCollect = func(context.Context, outlookdesktop.Info) (outlookpeople.Result, string, error) {
		return outlookpeople.Result{People: []outlookpeople.Person{{Class: 0xd2, Key: 1, Email: "fixture"}},
			Losses: []outlookpeople.Loss{{Code: "outlook_blocks_damaged", Count: 1}}}, "partial", nil
	}
	report, err := SyncPeople(context.Background(), PeopleOptions{DBPath: db, Root: root, Explicit: true, Discover: true})
	if err != nil || report.Sources[0].Coverage != "partial" {
		t.Fatalf("partial acquisition: %+v %v", report, err)
	}
	other := filepath.Join(t.TempDir(), "other")
	if err := os.WriteFile(other, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if pending, err := beginSharedPeople(context.Background(), s, other, "", time.Now(), true); err != nil || pending != nil {
		t.Fatalf("unrelated shared registration: %v", err)
	}
}
