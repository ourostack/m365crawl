package cli

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/outlookmeetingattendees"
	"github.com/ourostack/m365crawl/internal/store"
)

func TestAttendeeHistoricalOwnCaptureRouteExecutes(t *testing.T) {
	e := newEnv(t)
	e.db = filepath.Join(t.TempDir(), "selected 'archive", "native.db")
	ref := attendeeCLIArchive(t, e, 1, false)
	ctx := context.Background()
	ro, err := store.OpenReadOnly(ctx, e.db)
	if err != nil {
		t.Fatal(err)
	}
	_, rows, _, err := ro.SQL(ctx, `select result_json from outlook_attendee_snapshots where rowid=1`, 1)
	if err != nil {
		t.Fatal(err)
	}
	model, err := outlookmeetingattendees.Decode(ctx, []byte(rows[0][0].(string)))
	if err != nil {
		t.Fatal(err)
	}
	if err := ro.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(ctx, e.db)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	admit := func() {
		t.Helper()
		a, err := s.BeginAttendeeAttempt(ctx, store.KnowledgeInput{Kind: store.AttendeeKind, Platform: "synthetic",
			Locator: attendeeCLIFixtureOrigin, AcquisitionSignature: "synthetic", At: at},
			store.AttendeeRequest{Root: attendeeCLIFixtureRoot, Discover: true})
		if err != nil {
			t.Fatal(err)
		}
		read, err := s.OutlookReadReceipt(ctx, "outlook/Main")
		if err != nil {
			t.Fatal(err)
		}
		if err := s.CommitAttendeeCapture(ctx, a, store.AttendeeCapture{Evidence: model, InputDigest: strings.Repeat("a", 64),
			CalendarReadID: read.ID, AcquiredAt: at, AdmittedAt: at}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 1; i < 101; i++ {
		selection := model.Selections[0]
		selection.PayloadPos = i + 100
		model.Selections = append(model.Selections, selection)
	}
	model.Parents[0].EventLinks = 101
	want := slices.Clone(model.Selections)
	admit() // A is snapshot2, physical observation2.
	model.Selections = model.Selections[:1]
	model.Parents[0].EventLinks = 1
	current := "Synthetic current B"
	model.Attendees[0].Name = &current
	admit() // B is snapshot3; independent current health must remain B.
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"show", "attendee-observation:2"},
		{"show", "attendee-observation:2", "--fields", "name"},
		{"list", ref, "--history", "--limit", "1"},
		{"list", ref, "--history", "--limit", "1", "--fields", "name"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			code, output, stderr := e.run(append([]string{"--max-age", "1ns", "calendar", "attendees"}, args...)...)
			if code != 0 {
				t.Fatal(code, output, stderr)
			}
			result := decode(t, output)
			coverage := result["native_attendee_evidence"].(map[string]any)
			if coverage["bindings_truncated"] != true || len(coverage["bindings"].([]any)) != 100 {
				t.Fatal("invalid bounded historical preview", coverage)
			}
			source := coverage["sources"].([]any)[0].(map[string]any)
			if source["snapshot_id"] != "3" {
				t.Fatal("historical recovery relabeled current health", source)
			}
			query := "SELECT * FROM outlook_attendee_snapshots WHERE rowid=2"
			command := attendeeFindCaptureRoute(result, query)
			if command == "" {
				t.Fatal("historical A101 binding preview has no executable own-snapshot full capture route; current route is", source["full_capture_sql"])
			}
			for _, route := range []string{command, source["full_capture_command"].(string)} {
				words := attendeeCommandWords(t, route)
				if !strings.Contains(route, "--max-age 0") || !strings.Contains(route, "--max-text 0") {
					t.Fatal("full-capture route clips or refreshes", route)
				}
				var stdout, stderr bytes.Buffer
				if code := Main(append(words[1:], "--json"), &stdout, &stderr); code != 0 {
					t.Fatal("actual emitted command failed", code, stdout.String(), stderr.String())
				}
				full := decode(t, stdout.String())
				columns, records := full["columns"].([]any), full["rows"].([]any)
				if len(records) != 1 {
					t.Fatal("route selected another archive or snapshot", full)
				}
				var body string
				for i, column := range columns {
					if column == "result_json" {
						body = records[0].([]any)[i].(string)
					}
				}
				captured, err := outlookmeetingattendees.Decode(ctx, []byte(body))
				if err != nil {
					t.Fatal("full capture is not canonical complete evidence", err)
				}
				if route == command {
					if !reflect.DeepEqual(captured.Selections, want) {
						t.Fatalf("own route lost A's complete 101 selections: got %d", len(captured.Selections))
					}
				} else if len(captured.Selections) != 1 || *captured.Attendees[0].Name != current {
					t.Fatal("current source route did not independently recover B")
				}
			}
		})
	}
	code, text, stderr := e.run("--format", "text", "--fields", "name", "--max-age", "0", "calendar", "attendees", "show", "attendee-observation:2")
	if code != 0 || !strings.Contains(text, "own snapshot 2 full capture:") || !strings.Contains(text, "WHERE rowid=2") || !strings.Contains(text, "WHERE rowid=3") {
		t.Fatal("text projection lost independent historical/current capture recovery", code, text, stderr)
	}
}

func attendeeFindCaptureRoute(value any, query string) string {
	switch v := value.(type) {
	case map[string]any:
		if v["full_capture_sql"] == query {
			return fmt.Sprint(v["full_capture_command"])
		}
		for _, child := range v {
			if command := attendeeFindCaptureRoute(child, query); command != "" {
				return command
			}
		}
	case []any:
		for _, child := range v {
			if command := attendeeFindCaptureRoute(child, query); command != "" {
				return command
			}
		}
	}
	return ""
}
