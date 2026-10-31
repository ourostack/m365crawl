package cli

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/store"
)

// Catches doctor treating a calendar refusal as healthy, or as an unread mailbox.
func TestCalendarOWADoctorIndependentHealth(t *testing.T) {
	for _, tc := range []struct {
		variant string
		want    string
		failed  bool
	}{
		{"calendar", "calendar read ", false},
		{"calendar-empty", "calendar read ", false},
		{"calendar-damaged", "calendar read ", false},
		{"mail", "last calendar read failed (outlook_layout_unsupported)", true},
	} {
		t.Run(tc.variant, func(t *testing.T) {
			e := textEnv(t)
			root := createdOWARoot(t)
			fixture := "outlook-owa-calendar-local-fixture"
			if tc.variant == "mail" {
				fixture = "outlook-owa-fixture"
			}
			copyTree(t, filepath.Join("..", "..", "testdata", fixture, tc.variant), filepath.Join(root, "Default"))
			code, _, stderr := e.run("--outlook-root", root, "sync")
			if code != 0 && !tc.failed || code == 0 && tc.failed {
				t.Fatalf("sync exit %d: %s", code, stderr)
			}
			st, err := store.OpenReadOnly(context.Background(), e.db)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = st.Close() }()
			rt := &runtime{ctx: context.Background(), outlookOn: true, outlookRoot: root, now: time.Now}
			c := rt.outlookStoreCheck(st)
			if c.Warn != tc.failed || !strings.Contains(c.Detail, tc.want) || !strings.Contains(c.Detail, "mail read ") {
				t.Fatalf("independent native health = %+v", c)
			}
			for _, wrong := range []string{"calendar is unsupported", "Full Disk Access", "HxStore"} {
				if strings.Contains(c.Detail+c.Fix, wrong) {
					t.Fatalf("wrong native guidance = %+v", c)
				}
			}
			if m := rt.mailReadableCheck(st); m.Warn || !strings.Contains(m.Detail, "mail read ") {
				t.Fatalf("calendar state poisoned mail health = %+v", m)
			}
			row := sourceRows(t, e)["outlook/Default"]
			wantEvents := 2
			if tc.variant == "calendar-empty" || tc.failed {
				wantEvents = 0
			}
			if row == nil || num(t, row, "events_live") != wantEvents ||
				row["principal"] != "outlook/Default" || row["link"] != "none" {
				t.Fatalf("native calendar census or unknown principal = %v", row)
			}
			if tc.failed != (row["error"] != nil) {
				t.Fatalf("calendar failure hidden in source output = %v", row)
			}
			if row["deletions"] != "not_inferred" {
				t.Fatalf("OWA source claims unsupported deletion inference = %v", row)
			}
			if _, has := row["blocks_invalid_ratio"]; has {
				t.Fatalf("OWA source invents HxStore block health = %v", row)
			}
			agenda := mailJSON(t, e, "--no-sync", "calendar", "--from", "2031-03-01", "--to", "2031-04-01")
			if num(t, agenda, "count") != wantEvents {
				t.Fatalf("cached calendar query count = %v", agenda)
			}
			for _, event := range items(t, agenda) {
				if event["account_id"] != "outlook/Default" {
					t.Fatalf("unverified principal linked an event = %v", event)
				}
			}
			status := mailJSON(t, e, "--no-sync", "--outlook-root", root, "status")
			if m := mailBlockOf(t, status); m["state"] != "ok" || m["messages"] != float64(5) {
				t.Fatalf("calendar outcome poisoned cached mail status = %v", m)
			}
		})
	}
}

func TestCalendarOWADoctorIntervalAndUnchanged(t *testing.T) {
	e := textEnv(t)
	root := createdOWARoot(t)
	copyTree(t, filepath.Join("..", "..", "testdata", "outlook-owa-calendar-local-fixture", "calendar"), filepath.Join(root, "Default"))
	if code, _, stderr := e.run("--outlook-root", root, "sync"); code != 0 {
		t.Fatalf("sync exit %d: %s", code, stderr)
	}
	first := sourceRows(t, e)["outlook/Default"]
	if code, _, stderr := e.run("--outlook-root", root, "sync"); code != 0 {
		t.Fatalf("interval sync exit %d: %s", code, stderr)
	}
	skipped := sourceRows(t, e)["outlook/Default"]
	if skipped["status"] != "skipped_interval" || skipped["last_read_at"] != first["last_read_at"] ||
		skipped["census_as_of"] != first["census_as_of"] {
		t.Fatalf("interval check manufactured a calendar read = %v", skipped)
	}
	e.exec(`update meta set value='2000-01-01T00:00:00.000Z' where key='outlook_last_attempt:Default'`)
	if code, _, stderr := e.run("--outlook-root", root, "sync"); code != 0 {
		t.Fatalf("unchanged sync exit %d: %s", code, stderr)
	}
	unchanged := sourceRows(t, e)["outlook/Default"]
	if unchanged["status"] != "ok" || unchanged["last_read_at"] != first["last_read_at"] ||
		unchanged["census_as_of"] != first["census_as_of"] || num(t, unchanged, "events_live") != 2 ||
		unchanged["deletions"] != "not_inferred" {
		t.Fatalf("unchanged check manufactured a calendar read = %v", unchanged)
	}
	e.exec(`insert into meta(key,value) values('outlook_failure:outlook/Default','{"code":"outlook_layout_unsupported","message":"fixed failure","owa":true}')`)
	failed := sourceRows(t, e)["outlook/Default"]
	if failed["deletions"] != "not_inferred" || failed["error"] == nil ||
		failed["census_as_of"] != first["census_as_of"] || num(t, failed, "events_live") != 2 {
		t.Fatalf("failure changed last good OWA census or policy = %v", failed)
	}
	if _, has := failed["blocks_invalid_ratio"]; has {
		t.Fatalf("failed OWA source invents block health = %v", failed)
	}
}

func TestCalendarOWADoctorUnreadAndStateFailure(t *testing.T) {
	e := textEnv(t)
	e.emptyArchive()
	root := createdOWARoot(t)
	copyTree(t, filepath.Join("..", "..", "testdata", "outlook-owa-calendar-local-fixture", "calendar"), filepath.Join(root, "Default"))
	st, err := store.OpenReadOnly(context.Background(), e.db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	rt := &runtime{ctx: context.Background(), outlookOn: true, outlookRoot: root, now: time.Now}
	for _, archive := range []*store.Store{nil, st} {
		c := rt.outlookStoreCheck(archive)
		if c.Warn || !strings.Contains(c.Detail, "calendar not read yet") || !strings.Contains(c.Detail, "mail not read yet") {
			t.Fatalf("unread paths manufactured a read = %+v", c)
		}
	}
	old := readCalendarSources
	t.Cleanup(func() { readCalendarSources = old })
	readCalendarSources = func(*store.Store, context.Context, store.CalendarSourcesFilter) (store.CalendarSources, error) {
		return store.CalendarSources{}, errors.New("synthetic-private-state-path")
	}
	c := rt.outlookStoreCheck(st)
	if !c.Warn || !strings.Contains(c.Detail, "cannot read the archived calendar state") || strings.Contains(c.Detail+c.Fix, "synthetic-private") {
		t.Fatalf("calendar state error hidden or disclosed = %+v", c)
	}
}
