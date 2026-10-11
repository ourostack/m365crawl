package syncer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/outlookdesktop"
	"github.com/ourostack/m365crawl/internal/outlookpeople"
	"github.com/ourostack/m365crawl/internal/store"
)

func TestPeopleActualPrivateCollector(t *testing.T) {
	root := mailRoot(t)
	source := filepath.Join(root, "Main", outlookdesktop.StoreFileName)
	info, cleanup, err := outlookdesktop.Snapshot(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	result, digest, err := collectPeopleCapture(context.Background(), info)
	if err != nil || digest == "" || len(result.People) != 0 {
		t.Fatalf("collector: %d %q %v", len(result.People), digest, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := collectPeopleCapture(ctx, info); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled collector: %v", err)
	}
	cleanup()
	if _, _, err := collectPeopleCapture(context.Background(), info); err == nil {
		t.Fatal("missing private copy accepted")
	}
	bad := filepath.Join(t.TempDir(), "bad")
	if err := os.WriteFile(bad, []byte("not HxStore"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := collectPeopleCapture(context.Background(), outlookdesktop.Info{Path: bad}); err == nil {
		t.Fatal("invalid private copy accepted")
	}
}

func TestPeopleSharedCaptureSuccessAndFailureStates(t *testing.T) {
	root := mailRoot(t)
	source, err := filepath.EvalSymlinks(filepath.Join(root, "Main", outlookdesktop.StoreFileName))
	if err != nil {
		t.Fatal(err)
	}
	db := newDB(t)
	old := peopleCollect
	t.Cleanup(func() { peopleCollect = old })
	peopleCollect = func(context.Context, outlookdesktop.Info) (outlookpeople.Result, string, error) {
		return outlookpeople.Result{People: []outlookpeople.Person{{Class: 0xd2, Key: 1, Email: "fixture"}}}, "one", nil
	}
	if _, err := SyncPeople(context.Background(), PeopleOptions{DBPath: db, Root: root, Explicit: true, Discover: true}); err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	// Recent registered state and unregistered paths do not allocate another capture attempt.
	if p, err := beginSharedPeople(context.Background(), s, source, "signature", peopleNow().UTC(), false); err != nil || p != nil {
		t.Fatalf("recent attempt: %v", err)
	}
	if p, err := beginSharedPeople(context.Background(), s, filepath.Join(root, "missing"), "", time.Now(), true); err == nil || p != nil {
		t.Fatal("missing path accepted")
	}
	for _, state := range []string{"unchanged", "partial", "capture-failed", "stale", "link-failed", "admission-failed"} {
		t.Run(state, func(t *testing.T) {
			p, err := beginSharedPeople(context.Background(), s, source, "signature", peopleNow().UTC(), true)
			if err != nil || p == nil {
				t.Fatalf("pending: %v", err)
			}
			info, cleanup, err := outlookdesktop.Snapshot(context.Background(), source)
			if err != nil {
				t.Fatal(err)
			}
			p.acquired = peopleNow().UTC()
			peopleCollect = func(context.Context, outlookdesktop.Info) (outlookpeople.Result, string, error) {
				result := outlookpeople.Result{People: []outlookpeople.Person{{Class: 0xd2, Key: 1, Email: "fixture"}}}
				digest := "one"
				if state == "partial" {
					result.Losses = []outlookpeople.Loss{{Code: "outlook_blocks_damaged", Count: 1}}
					digest = "partial"
				}
				return result, digest, nil
			}
			var captureErr error
			if state == "capture-failed" {
				captureErr = errors.New("capture")
			}
			if state == "stale" {
				if _, err := s.BeginKnowledgeAttempt(context.Background(), store.KnowledgeInput{Kind: "outlook_people",
					Platform: p.previous.Platform, Locator: source, At: time.Now()}); err != nil {
					t.Fatal(err)
				}
			}
			if state == "link-failed" || state == "admission-failed" {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				out := finishSharedPeople(ctx, s, "Main", info, cleanup, nil, *p)
				if out.report.Error == nil {
					t.Fatal("cancelled shared capture accepted")
				}
				return
			}
			out := finishSharedPeople(context.Background(), s, "Main", info, cleanup, captureErr, *p)
			if !out.optional {
				t.Fatal("shared outcome entered legacy settlement")
			}
			if state == "stale" || state == "capture-failed" {
				if out.report.Error == nil {
					t.Fatal("failed outcome hidden")
				}
			} else if out.report.Error != nil || out.report.Status != state {
				t.Fatalf("shared outcome: %+v", out.report)
			}
		})
	}
}
