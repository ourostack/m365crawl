package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/syncer"
)

func TestAttendeeUnknownOriginArchivedRequestFix(t *testing.T) {
	e := newEnv(t)
	old := runAttendeeSync
	t.Cleanup(func() { runAttendeeSync = old })
	runAttendeeSync = func(context.Context, syncer.AttendeeOptions) (syncer.AttendeeReport, error) {
		return syncer.AttendeeReport{}, &syncer.AttendeeBootstrapRequired{Root: "/synthetic/saved 'root"}
	}
	for _, selector := range [][]string{nil, {"--source-id", "7"}} {
		code, _, stderr := e.run(append([]string{"calendar", "attendees", "sync"}, selector...)...)
		if code == 0 {
			t.Fatal("unqualified saved source became success")
		}
		fix := errorOf(t, stderr)["fix"].(string)
		routes := strings.Split(fix, "`")
		if len(routes) < 5 {
			t.Fatal("no ordinary and exact native retry routes", fix)
		}
		words := attendeeCommandWords(t, routes[1])
		for flag, want := range map[string]string{"--db": e.db, "--max-age": "0", "--outlook-root": "/synthetic/saved 'root"} {
			found := false
			for i := 0; i+1 < len(words); i++ {
				found = found || words[i] == flag && words[i+1] == want
			}
			if !found {
				t.Fatal("ordinary route escaped archive/request", words, flag)
			}
		}
		if len(selector) > 0 && !strings.Contains(routes[3], "--source-id 7") {
			t.Fatal("post-bootstrap retry lost exact source selector", fix)
		}
	}
}

func TestAttendeeUnknownOriginFixRequiresOrdinaryBootstrap(t *testing.T) {
	e := newEnv(t)
	old := runAttendeeSync
	t.Cleanup(func() { runAttendeeSync = old })
	runAttendeeSync = func(context.Context, syncer.AttendeeOptions) (syncer.AttendeeReport, error) {
		return syncer.AttendeeReport{}, &errs.Coded{Code: "outlook_attendee_origin_unknown", Exit: errs.ExitEnvironment, Message: "synthetic unknown old origin"}
	}
	code, _, stderr := e.run("calendar", "attendees", "sync", "--root", "/synthetic/original root")
	if code == 0 {
		t.Fatal("unknown origin became successful capture")
	}
	fix, _ := errorOf(t, stderr)["fix"].(string)
	if !strings.Contains(fix, "sync --outlook-root") || !strings.Contains(fix, "/synthetic/original root") ||
		!strings.Contains(fix, e.db) || !strings.Contains(fix, "--max-age 0") || strings.Contains(fix, "--full-read") {
		t.Fatal("unknown-origin remedy must use selected archive and ordinary eligible bootstrap, not repeat standalone takeover", fix)
	}
}
