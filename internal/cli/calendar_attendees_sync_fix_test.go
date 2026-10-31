package cli

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/syncer"
)

func TestAttendeeSyncFailureFixKeepsRequest(t *testing.T) {
	e := newEnv(t)
	old := runAttendeeSync
	t.Cleanup(func() { runAttendeeSync = old })
	runAttendeeSync = func(context.Context, syncer.AttendeeOptions) (syncer.AttendeeReport, error) {
		return syncer.AttendeeReport{}, &errs.Coded{Code: "outlook_attendee_origin_conflict", Exit: errs.ExitEnvironment, Message: "synthetic native conflict"}
	}
	code, _, stderr := e.run("calendar", "attendees", "sync", "--root", "/synthetic/exact root")
	if code == 0 {
		t.Fatal("native sync conflict hidden")
	}
	fix, _ := errorOf(t, stderr)["fix"].(string)
	if !strings.Contains(fix, e.db) || !strings.Contains(fix, "--no-sync") || !strings.Contains(fix, "/synthetic/exact root") || strings.Contains(fix, " content ") {
		t.Fatal("failure remedy escaped selected request", fix)
	}
	for _, flags := range [][]string{{}, {"--source-id", "1"}} {
		code, _, stderr = e.run(append([]string{"calendar", "attendees", "sync"}, flags...)...)
		if code == 0 {
			t.Fatal("failed native retry became success")
		}
		if fix, _ := errorOf(t, stderr)["fix"].(string); !strings.Contains(fix, e.db) {
			t.Fatal("default/source retry lost archive", fix)
		}
	}
	runAttendeeSync = func(context.Context, syncer.AttendeeOptions) (syncer.AttendeeReport, error) {
		return syncer.AttendeeReport{}, context.Canceled
	}
	if code, _, _ := e.run("calendar", "attendees", "sync"); code == 0 {
		t.Fatal("cancellation became success")
	}
	sentinel := errors.New("synthetic diagnostic failure")
	runAttendeeSync = func(context.Context, syncer.AttendeeOptions) (syncer.AttendeeReport, error) {
		return syncer.AttendeeReport{}, errors.Join(&errs.Coded{Code: "outlook_attendees_read_failed", Message: "synthetic fixed failure"}, sentinel)
	}
	rt := &runtime{ctx: context.Background(), g: &Globals{}, dbPath: e.db, stdout: io.Discard}
	if err := (&attendeeSyncCmd{Root: "/synthetic/exact root"}).Run(rt); !errors.Is(err, sentinel) {
		t.Fatal("scoped remedy dropped diagnostic failure", err)
	}
}
