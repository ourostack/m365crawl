package cli

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/syncer"
)

func TestWorkIQSyncWithASinceSelectsAWholeSecondMailWindow(t *testing.T) {
	e := newEnv(t)
	e.emptyArchive()
	old, oldNow := runWorkIQSyncer, workIQWindowNow
	t.Cleanup(func() { runWorkIQSyncer, workIQWindowNow = old, oldNow })
	now := time.Date(2026, 10, 31, 12, 0, 0, 700e6, time.UTC)
	workIQWindowNow = func() time.Time { return now }
	var got syncer.WorkIQOptions
	runWorkIQSyncer = func(_ context.Context, options syncer.WorkIQOptions) (syncer.WorkIQReport, error) {
		got = options
		return syncer.WorkIQReport{Window: &syncer.WorkIQWindowReport{Since: options.Window.Since, Until: options.Window.Until, Clock: "received", Covered: true,
			Folders: []syncer.WorkIQWindowFolder{{FolderID: "a", Mode: "delta", Covered: true}, {FolderID: "b", Mode: "listing"}}}}, nil
	}
	for _, tc := range []struct {
		args         []string
		since, until string
		coverage     string
	}{
		{[]string{"--source", "mail", "--since", "2026-09-01T00:00:00.9Z", "--until", "2026-09-08T00:00:00.1Z"}, "2026-09-01T00:00:00Z", "2026-09-08T00:00:01Z", ""},
		{[]string{"--source", "mail", "--since", "2026-09-01T00:00:00Z"}, "2026-09-01T00:00:00Z", "2026-10-31T12:00:00Z", ""},
		{[]string{"--source", "mail", "--since", "2026-09-01T00:00:00Z", "--until", "2027-01-01T00:00:00Z", "--coverage", "proof"}, "2026-09-01T00:00:00Z", "2026-10-31T12:00:00Z", "proof"},
		{[]string{"--since", "2026-09-01T00:00:00Z", "--until", "2026-09-02T00:00:00Z", "--coverage", "fast"}, "2026-09-01T00:00:00Z", "2026-09-02T00:00:00Z", "fast"},
	} {
		args := append([]string{"--json", "--account", mailAccount, "workiq", "sync"}, tc.args...)
		code, out, stderr := e.run(args...)
		if code != 0 || got.Window == nil || got.Window.Since.Format(time.RFC3339) != tc.since || got.Window.Until.Format(time.RFC3339) != tc.until ||
			got.Window.Coverage != tc.coverage || !got.Since.IsZero() {
			t.Fatalf("%v: %d %s %+v", tc.args, code, stderr, got.Window)
		}
		if doc := decode(t, out); doc["window"] == nil {
			t.Fatalf("the result has no window: %s", out)
		}
	}
	code, out, _ := e.run("--format", "text", "--account", mailAccount, "workiq", "sync", "--source", "mail", "--since", "2026-09-01T00:00:00Z")
	if code != 0 || !strings.Contains(out, "mail window 2026-09-01T00:00:00Z to 2026-10-31T12:00:00Z (received): proven complete; 2 folders (1 delta, 1 listing)") {
		t.Fatalf("text output = %q", out)
	}
	runWorkIQSyncer = func(_ context.Context, options syncer.WorkIQOptions) (syncer.WorkIQReport, error) {
		return syncer.WorkIQReport{Window: &syncer.WorkIQWindowReport{Since: options.Window.Since, Until: options.Window.Until}}, nil
	}
	if _, out, _ = e.run("--format", "text", "--account", mailAccount, "workiq", "sync", "--source", "mail", "--since", "2026-09-01T00:00:00Z"); !strings.Contains(out, "not proven complete; 0 folders") {
		t.Fatalf("text output = %q", out)
	}
	// Without --since, mail sync stays the whole account.
	runWorkIQSyncer = func(_ context.Context, options syncer.WorkIQOptions) (syncer.WorkIQReport, error) {
		got = options
		return syncer.WorkIQReport{}, nil
	}
	if code, _, _ := e.run("--json", "--account", mailAccount, "workiq", "sync", "--source", "mail"); code != 0 || got.Window != nil {
		t.Fatal("a whole-account sync became a window")
	}
}

func TestWorkIQRouteDurationSaysHoursForLongProofs(t *testing.T) {
	if workIQRouteDuration(119) != "119 minutes" || workIQRouteDuration(121) != "3 hours" {
		t.Fatal(workIQRouteDuration(121))
	}
}

func TestRangeCoverageFailsTheReadWhenWindowSpansCannotBeRead(t *testing.T) {
	e := routeEnv(t)
	old := mailWindowSpans
	t.Cleanup(func() { mailWindowSpans = old })
	mailWindowSpans = func(*store.Store, context.Context, string) ([]store.TimeSpan, error) {
		return nil, errors.New("broken")
	}
	mailFails(t, e, "db_error", 1, "mail", "list", "--since", "2026-01-01T00:00:00Z")
}
