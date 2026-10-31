package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/openclaw/crawlkit/output"
)

// registeredNotices runs the registered-source caution of a --no-sync read at now, for sources
// last attempted at attempts, and returns the result's notices and stderr.
func registeredNotices(t *testing.T, db string, now time.Time, format output.Format, attempts ...*time.Time) ([]string, string) {
	t.Helper()
	var stderr strings.Builder
	rt := &runtime{ctx: t.Context(), g: &Globals{}, dbPath: db, now: func() time.Time { return now }, syncOff: syncOffNoSync, format: format, stderr: &stderr}
	res := newList(nil, false)
	rt.noteRegisteredSyncOff(res, "document", attempts)
	if res.ImplicitSync != implicitSyncDisabled {
		t.Fatalf("implicit_sync = %q", res.ImplicitSync)
	}
	return res.Notices, stderr.String()
}

// Issue #283: two reads of the same archive a moment apart, straddling a minute boundary, must
// print the same caution. The caution used to carry the source's age in minutes.
func TestNoSyncCautionIsTheSameForTheSameArchive(t *testing.T) {
	attempted := time.Date(2026, 10, 11, 3, 0, 0, 0, time.UTC)
	db := t.TempDir() + "/missing.db"
	first, _ := registeredNotices(t, db, attempted.Add(483*time.Hour+4*time.Minute+59*time.Second), output.JSON, &attempted)
	second, _ := registeredNotices(t, db, attempted.Add(483*time.Hour+5*time.Minute), output.JSON, &attempted)
	if len(first) != 1 || len(second) != 1 || first[0] != second[0] {
		t.Fatalf("the same archive gave different cautions:\n%q\n%q", first, second)
	}
	if want := "last read at 2026-10-11T03:00:00Z"; !strings.Contains(first[0], want) {
		t.Fatalf("caution %q lacks %q", first[0], want)
	}
}

// The 15-minute boundary: at exactly 15 minutes a --no-sync read is quiet; one second later it
// cautions, in the result and (text mode) on stderr.
func TestNoSyncCautionBoundary(t *testing.T) {
	attempted := time.Date(2026, 10, 11, 3, 0, 0, 0, time.UTC)
	db := t.TempDir() + "/missing.db"
	for _, format := range []output.Format{output.JSON, output.Text} {
		notices, stderr := registeredNotices(t, db, attempted.Add(defaultMaxAge), format, &attempted)
		if len(notices) != 0 || stderr != "" {
			t.Fatalf("%s at 15m: notices %q stderr %q", format, notices, stderr)
		}
		notices, stderr = registeredNotices(t, db, attempted.Add(defaultMaxAge+time.Second), format, &attempted)
		if len(notices) != 1 || (format == output.Text) != (stderr != "") {
			t.Fatalf("%s at 15m1s: notices %q stderr %q", format, notices, stderr)
		}
	}
}

// The flake of #283 end to end: the same --no-sync document read at two moments a second apart,
// across a minute boundary, prints the same output; at the 15-minute boundary it is quiet.
func TestNoSyncDocumentReadAcrossClockBoundaries(t *testing.T) {
	e := newEnv(t)
	seedDocumentsCLI(t, e) // its source was attempted at 2026-10-11T01:00:00Z
	attempted := time.Date(2026, 10, 11, 1, 0, 0, 0, time.UTC)
	clock := runtimeClock
	t.Cleanup(func() { runtimeClock = clock })
	readAt := func(now time.Time) (string, string) {
		t.Helper()
		runtimeClock = func() time.Time { return now }
		code, stdout, stderr := e.run("--no-sync", "--format", "text", "docs", "list")
		if code != 0 {
			t.Fatalf("exit %d: %s", code, stderr)
		}
		return stdout, stderr
	}
	out1, err1 := readAt(attempted.Add(483*time.Hour + 4*time.Minute + 59*time.Second))
	out2, err2 := readAt(attempted.Add(483*time.Hour + 5*time.Minute))
	if out1 != out2 || err1 != err2 || !strings.Contains(out1, "last read at 2026-10-11T01:00:00Z") || err1 == "" {
		t.Fatalf("not repeatable across a minute boundary:\n%s\n%s\nstderr %q / %q", out1, out2, err1, err2)
	}
	if out, stderr := readAt(attempted.Add(defaultMaxAge)); strings.Contains(out, "did not refresh") || stderr != "" {
		t.Fatalf("at 15m: %s / %q", out, stderr)
	}
	if out, stderr := readAt(attempted.Add(defaultMaxAge + time.Second)); !strings.Contains(out, "did not refresh") || stderr == "" {
		t.Fatalf("at 15m1s: %s / %q", out, stderr)
	}
}
