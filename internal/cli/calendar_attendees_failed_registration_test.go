package cli

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/syncer"
)

func TestAttendeeInitialFailureExactRootRetry(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	s, err := store.Open(ctx, e.db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	root := "/synthetic/exact copied root"
	a, err := s.BeginAttendeeAttempt(ctx, store.KnowledgeInput{Kind: store.AttendeeKind, Platform: "synthetic", Locator: filepath.Join(attendeeCLIFixtureRoot, "Other", "HxStore.hxd"), At: time.Now().UTC()},
		store.AttendeeRequest{Root: root, Discover: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecordAttendeeFailure(ctx, a, store.KnowledgeOutcome{Status: "failed", Reason: "outlook_attendees_read_failed", At: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	code, out, stderr := e.run("--max-age", "1ns", "--fields", "registration_id,source_id,reason,retry", "calendar", "attendees", "sources")
	if code != 0 {
		t.Fatal(code, out, stderr)
	}
	row := items(t, decode(t, out))[0]
	if row["source_id"] != nil || row["registration_id"] != "1" || row["reason"] != "outlook_attendees_read_failed" {
		t.Fatal("failure invented positive native source", row)
	}
	retry := row["retry"].(string)
	if !strings.Contains(retry, root) || strings.Contains(retry, filepath.Join(attendeeCLIFixtureRoot, "Other", "HxStore.hxd")) || strings.Contains(retry, "--source-id 0") {
		t.Fatal("retry guessed a root/source", retry)
	}
	old := runAttendeeSync
	t.Cleanup(func() { runAttendeeSync = old })
	runAttendeeSync = func(_ context.Context, options syncer.AttendeeOptions) (syncer.AttendeeReport, error) {
		if options.Root != root || options.DBPath != e.db || !options.Explicit {
			t.Fatal("emitted retry lost original selected request", options)
		}
		return syncer.AttendeeReport{}, nil
	}
	words := attendeeCommandWords(t, retry)
	if code, _, stderr := e.run(words[1:]...); code != 0 {
		t.Fatal("emitted exact retry failed", code, stderr)
	}
}
