package cli

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/openclaw/crawlkit/output"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/syncer"
)

type attendeeFailWriter struct{ at, calls int }

func (w *attendeeFailWriter) Write(p []byte) (int, error) {
	w.calls++
	if w.calls == w.at {
		return 0, io.ErrClosedPipe
	}
	return len(p), nil
}

func TestAttendeeReadOutputAndUsageFailures(t *testing.T) {
	e := newEnv(t)
	ref := attendeeCLIArchive(t, e, 1, false)
	for _, args := range [][]string{
		{"--fields", "nope", "calendar", "attendees", "list", ref},
		{"--fields", "nope", "calendar", "attendees", "show", "attendee-observation:1"},
		{"--fields", "nope", "calendar", "attendees", "sources"},
		{"calendar", "attendees", "list", ref, "--source-id", "-1"},
		{"calendar", "attendees", "list", ref, "--source-id", "0"},
		{"calendar", "attendees", "sources", "--source-id", "0"},
		{"calendar", "attendees", "show", "attendee-observation:999"},
		{"calendar", "attendees", "list", "no-such-event"},
	} {
		if code, _, _ := e.run(append([]string{"--no-sync"}, args...)...); code == 0 {
			t.Fatal("invalid native read accepted", args)
		}
	}
	rt := &runtime{ctx: context.Background(), g: &Globals{}, dbPath: e.db, stdout: io.Discard, format: output.JSON}
	oldOpen, oldSync, oldWD := openAttendeeRead, runAttendeeSync, getwd
	t.Cleanup(func() { openAttendeeRead, runAttendeeSync, getwd = oldOpen, oldSync, oldWD })
	fail := errors.New("synthetic read failure")
	openAttendeeRead = func(context.Context, string) (*store.Store, error) { return nil, fail }
	if err := (&attendeeSourcesCmd{}).Run(rt); err == nil {
		t.Fatal("open error disappeared")
	}
	openAttendeeRead = oldOpen
	if err := rt.attendeeRead("calendar attendees sources", func(*store.Store) (result, error) { return nil, fail }); err == nil {
		t.Fatal("read error disappeared")
	}
	rt.stdout = &attendeeFailWriter{at: 1}
	if err := (&attendeeSourcesCmd{}).Run(rt); err == nil {
		t.Fatal("JSON output error disappeared")
	}
	rt.stdout = io.Discard
	rt.fields = []string{"nope"}
	if err := (&attendeeSyncCmd{}).Run(rt); err == nil {
		t.Fatal("sync fields reached writer")
	}
	rt.fields = nil
	rt.g.Account = "synthetic/account"
	if err := (&attendeeSyncCmd{}).Run(rt); err == nil {
		t.Fatal("subject account used for native acquisition")
	}
	rt.g.Account = ""
	runAttendeeSync = func(context.Context, syncer.AttendeeOptions) (syncer.AttendeeReport, error) {
		return syncer.AttendeeReport{}, fail
	}
	if err := (&attendeeSyncCmd{}).Run(rt); err == nil {
		t.Fatal("sync failure disappeared")
	}
	runAttendeeSync = func(context.Context, syncer.AttendeeOptions) (syncer.AttendeeReport, error) {
		return syncer.AttendeeReport{}, nil
	}
	openAttendeeRead = func(ctx context.Context, path string) (*store.Store, error) {
		s, err := oldOpen(ctx, path)
		if err == nil {
			err = s.Close()
		}
		return s, err
	}
	if err := (&attendeeSyncCmd{}).Run(rt); err == nil {
		t.Fatal("post-sync native archive read failure disappeared")
	}
	openAttendeeRead = oldOpen
	rt.dbPath = "relative-synthetic.db"
	getwd = func() (string, error) { return "", fail }
	if err := (&attendeeSourcesCmd{}).Run(rt); err == nil {
		t.Fatal("unresolved relative archive emitted executable routes")
	}
	if err := (&attendeeSyncCmd{}).Run(rt); err == nil {
		t.Fatal("unresolved relative archive reached native writer")
	}
}

func TestAttendeeTextOutputFailures(t *testing.T) {
	coverage := &AttendeeEvidenceCoverage{Scope: store.AttendeeScope, BindingsTotal: "1",
		Sources:     []attendeeSourceItem{{RegistrationID: "1", FullCaptureCommand: "synthetic full capture"}},
		OwnCaptures: []attendeeCaptureItem{{SnapshotID: "1", FullCaptureCommand: "synthetic own capture"}},
		Bindings:    []attendeeBindingItem{{ID: "1", NativeEventID: "AA11", DetailKey: 3}}, Notices: []string{"synthetic scope"}}
	rt := &runtime{stdout: io.Discard}
	if err := rt.renderAttendeeCoverage(nil); err != nil {
		t.Fatal(err)
	}
	for n := 1; n <= 6; n++ {
		rt.stdout = &attendeeFailWriter{at: n}
		if err := rt.renderAttendeeCoverage(coverage); !errors.Is(err, io.ErrClosedPipe) {
			t.Fatal("coverage output failure disappeared", n, err)
		}
	}
	for n := 1; n <= 2; n++ {
		rt.stdout = &attendeeFailWriter{at: n}
		r := &attendeeResult{newList([]any{AttendeeEvidenceItem{ID: "attendee-observation:1"}}, false)}
		if err := r.renderAttendees(rt); !errors.Is(err, io.ErrClosedPipe) {
			t.Fatal("row output failure disappeared", n, err)
		}
	}
	rt.stdout = &attendeeFailWriter{at: 1}
	if err := (&attendeeSyncResult{}).renderAttendees(rt); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatal("sync output failure disappeared", err)
	}
	rt.stdout = io.Discard
	if err := (&attendeeSyncResult{NativeAttendeeEvidence: emptyAttendeeCoverage()}).renderAttendees(rt); err != nil {
		t.Fatal(err)
	}
}
