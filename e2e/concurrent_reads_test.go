//go:build e2e

package e2e

import (
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

// waitResult waits up to 60 s for a started process and returns what it printed and its exit.
func (s *started) waitResult() result {
	s.t.Helper()
	select {
	case <-s.done:
	case <-time.After(60 * time.Second):
		s.t.Fatalf("process did not finish\nstdout: %s\nstderr: %s", s.stdout.String(), s.stderr.String())
	}
	return finish(s.t, s.cmd, s.err, s.stdout.String(), s.stderr.String())
}

// Agents run reads in parallel. Two reads that both find the archive stale must share one sync:
// the first runs it, the second waits for it and reads its result, and neither reports `locked`.
func TestConcurrentReadsShareOneSync(t *testing.T) {
	e := newEnv(t)
	args := append([]string{"search", "Fixture", "--json"}, e.baseArgs()...)
	first := e.start([]string{"M365CRAWL_TEST_PAUSE_AFTER_SNAPSHOT=1s"}, args...)
	first.waitFor("the first read's sync", func() bool { return strings.Contains(first.stderr.String(), pausedMarker) })
	second := e.start(nil, args...)
	a, b := first.waitResult(), second.waitResult()
	for _, res := range []result{a, b} {
		mustExit(t, res, 0)
		if strings.Contains(res.stderr, `"locked"`) {
			t.Fatalf("a read reported locked:\n%s", res.stderr)
		}
	}
	ma, mb := mustJSON(t, a.stdout), mustJSON(t, b.stdout)
	for _, m := range []map[string]any{ma, mb} {
		if m["sync_error"] != nil {
			t.Fatalf("sync_error = %v", m["sync_error"])
		}
		if hits, _ := m["items"].([]any); len(hits) == 0 {
			t.Fatalf("no hits: %v", m)
		}
	}
	if ma["synced"] == nil || ma["implicit_sync"] != nil {
		t.Fatalf("first read: synced %v, implicit_sync %v; want its own sync", ma["synced"], ma["implicit_sync"])
	}
	if mb["synced"] != nil || mb["implicit_sync"] != "waited_for_other_sync" {
		t.Fatalf("second read: synced %v, implicit_sync %v; want waited_for_other_sync", mb["synced"], mb["implicit_sync"])
	}
	if !strings.Contains(b.stderr, `"notice":"waiting_for_other_sync"`) {
		t.Fatalf("second read stderr: %s", b.stderr)
	}
}

// When the other run outlasts the read's wait, the read answers from the archive as it is and
// says so, and still exits 0.
func TestReadAnswersWhenAnotherSyncOutlastsItsWait(t *testing.T) {
	e := newEnv(t)
	e.sync()
	holder := e.start([]string{"M365CRAWL_TEST_PAUSE_AFTER_SNAPSHOT=30s"}, append([]string{"sync", "--full-read"}, e.baseArgs()...)...)
	holder.waitFor("the pause", func() bool { return strings.Contains(holder.stderr.String(), pausedMarker) })
	began := time.Now()
	res := e.runWith([]string{"M365CRAWL_TEST_LOCK_WAIT=1s"}, append([]string{"--max-age", "1ns", "search", "Fixture", "--json"}, e.baseArgs()...)...)
	mustExit(t, res, 0)
	if took := time.Since(began); took < time.Second {
		t.Fatalf("the read took %s; it should wait its 1s budget", took)
	}
	m := mustJSON(t, res.stdout)
	if m["sync_error"] != nil || m["implicit_sync"] != "other_sync_in_progress" {
		t.Fatalf("sync_error %v, implicit_sync %v", m["sync_error"], m["implicit_sync"])
	}
	if notices, _ := m["notices"].([]any); len(notices) == 0 || !strings.Contains(notices[0].(string), "still syncing") {
		t.Fatalf("notices = %v", m["notices"])
	}
	if hits, _ := m["items"].([]any); len(hits) == 0 || m["archive_age_seconds"] == nil {
		t.Fatalf("want hits and the archive's age: %s", res.stdout)
	}
	if runtime.GOOS == "windows" {
		if err := holder.cmd.Process.Kill(); err != nil {
			t.Fatal(err)
		}
		<-holder.done
	} else {
		holder.signal(syscall.SIGINT)
	}
}
