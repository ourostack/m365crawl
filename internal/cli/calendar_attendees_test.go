package cli

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/calendar"
	"github.com/ourostack/m365crawl/internal/outlookattendees"
	"github.com/ourostack/m365crawl/internal/outlookcal"
	"github.com/ourostack/m365crawl/internal/outlookmeetingattendees"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/syncer"
)

func attendeeCLIArchive(t *testing.T, e *env, copies int, ambiguous bool) string {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(ctx, e.db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	}()
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	event := calendar.Event{Source: calendar.SourceOutlook, AccountID: "outlook/Main", SourceID: "AA11", GlobalID: "AA11", ICalUID: "AA11", SeriesKey: "AA11",
		UnknownDeclared: true,
		EventType:       calendar.EventSingle, Start: at, End: at.Add(time.Hour), AllDay: calendar.TriFalse, Subject: "Synthetic attendee meeting",
		AttendeesJSON: `[{"name":"Legacy A","address":"a@example.invalid"},{"name":"Legacy B","address":"b@example.invalid"}]`}
	cal, err := s.CommitOutlook(ctx, store.OutlookBatch{Account: "outlook/Main", Origin: "/synthetic/native/Main/HxStore.hxd", At: at, Zone: time.UTC, Stamp: "synthetic", Events: []calendar.Event{event}},
		func(store.CalendarResult) store.Run {
			return store.Run{StartedAt: at, FinishedAt: at, Source: "outlook|Main", Fingerprint: "synthetic", Status: "ok"}
		})
	if err != nil {
		t.Fatal(err)
	}
	if cal.Counts.Events.Inserted != 1 {
		t.Fatalf("synthetic calendar admission failed: %+v", cal)
	}
	a, err := s.BeginAttendeeAttempt(ctx, store.KnowledgeInput{Kind: store.AttendeeKind, Platform: "synthetic", Locator: "/synthetic/native/Main/HxStore.hxd", AcquisitionSignature: "synthetic", At: at},
		store.AttendeeRequest{Root: "/synthetic/native", Discover: true})
	if err != nil {
		t.Fatal(err)
	}
	name, email := "Literal Native \x00 Name", ""
	native := outlookattendees.Result{Evidence: []outlookattendees.Evidence{{DetailKey: 3, DetailCopies: 1, EventLinks: 1}}}
	for i := 0; i < copies; i++ {
		native.Attendees = append(native.Attendees, outlookattendees.Attendee{Key: 11, DetailKey: 3, VersionRaw: math.MaxUint64, BlockOffset: math.MaxInt64, PayloadPos: i, Name: &name, Email: &email})
	}
	if ambiguous {
		conflict := native.Attendees[0]
		other := "Outside parent"
		conflict.Name = &other
		conflict.DetailKey = 4
		conflict.PayloadPos = copies
		native.Attendees = append(native.Attendees, conflict)
		native.AmbiguousKeys = []uint32{11}
		native.Evidence = append(native.Evidence, outlookattendees.Evidence{DetailKey: 4, DetailCopies: 1})
		native.Losses = []outlookattendees.Loss{{Code: "outlook_attendees_event_link_missing", Count: 1}}
	}
	model, census, err := outlookmeetingattendees.Build(ctx, native, outlookcal.AttendeeSelections{Rows: []outlookcal.SelectionReceipt{{SourceID: "AA11", DetailKey: 3, DetailCopies: 1, DetailPresent: true, BlockOffset: 64, PayloadPos: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	zero := time.Time{}
	if err := s.CommitAttendeeCapture(ctx, a, store.AttendeeCapture{Evidence: model, Census: census, InputDigest: strings.Repeat("a", 64), CalendarReadID: cal.OutlookReadID, AcquiredAt: at, AdmittedAt: at, SourceObservedAt: &zero}); err != nil {
		t.Fatal(err)
	}
	return calendar.Key(event)
}

func TestAttendeeCLIExplicitAndOffline(t *testing.T) {
	e := newEnv(t)
	ref := attendeeCLIArchive(t, e, 2, false)
	old := runAttendeeSync
	t.Cleanup(func() { runAttendeeSync = old })
	calls := 0
	runAttendeeSync = func(context.Context, syncer.AttendeeOptions) (syncer.AttendeeReport, error) {
		calls++
		return syncer.AttendeeReport{}, nil
	}
	before, err := os.ReadFile(e.db)
	if err != nil {
		t.Fatal(err)
	}
	stat, err := os.Stat(e.db)
	if err != nil {
		t.Fatal(err)
	}
	for _, command := range [][]string{
		{"calendar", "attendees", "sources"},
		{"calendar", "attendees", "list", ref},
		{"calendar", "attendees", "show", "attendee-observation:1"},
	} {
		code, out, stderr := e.run(append([]string{"--max-age", "1ns"}, command...)...)
		if code != 0 || !containsJSONKey(out, "native_attendee_evidence") {
			t.Fatalf("offline native command: %d %s %s", code, out, stderr)
		}
	}
	after, err := os.ReadFile(e.db)
	if err != nil {
		t.Fatal(err)
	}
	afterStat, err := os.Stat(e.db)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 0 || string(after) != string(before) || !stat.ModTime().Equal(afterStat.ModTime()) {
		t.Fatal("archive-only native read captured/refreshed or changed archive")
	}
	for _, flags := range [][]string{
		{"--root", "/synthetic", "--source-id", "1"},
		{"--source-id", "0"},
		{"--source-id", "-1"},
		{"--root", "/synthetic", "--fields", "not_a_field"},
	} {
		args := append([]string{"calendar", "attendees", "sync"}, flags...)
		if code, _, _ := e.run(args...); code == 0 {
			t.Fatal("invalid write selector accepted")
		}
	}
	if calls != 0 {
		t.Fatal("invalid usage reached writer")
	}
	if code, _, stderr := e.run("calendar", "attendees", "sync", "--root", "/synthetic"); code != 0 || calls != 1 {
		t.Fatal("explicit sync did not bind selected archive", code, stderr)
	}
}

func TestAttendeeCLIProjectionAndFullRecord(t *testing.T) {
	e := newEnv(t)
	ref := attendeeCLIArchive(t, e, 1, false)
	code, out, stderr := e.run("--max-age", "0", "--max-text", "5", "calendar", "attendees", "list", ref)
	if code != 0 {
		t.Fatal(code, out, stderr)
	}
	result := decode(t, out)
	row := items(t, result)[0]
	if row["version_raw"] != "18446744073709551615" || row["block_offset"] != "9223372036854775807" || row["email"] != "" || row["text_truncated"] != true {
		t.Fatal("exact transport/literal display", row)
	}
	query, ok := row["full_record_sql"].(string)
	if !ok || !strings.Contains(query, "json_extract(s.result_json,'$.attendees['||o.ordinal||']')") {
		t.Fatal("full record repeated blob or lost indexed native object", row)
	}
	if command := row["full_record_command"].(string); !strings.Contains(command, "--db") || !strings.Contains(command, e.db) || !strings.Contains(command, "--max-age 0") || strings.Contains(command, " content ") {
		t.Fatal("full record escaped selected archive", command)
	}
	s, err := store.OpenReadOnly(context.Background(), e.db)
	if err != nil {
		t.Fatal(err)
	}
	cols, rows, _, err := s.SQL(context.Background(), query, 1)
	if closeErr := s.Close(); err != nil || closeErr != nil || len(rows) != 1 {
		t.Fatal("actual emitted SQL failed", err, closeErr)
	}
	found := false
	for i, column := range cols {
		if column == "native_observation" {
			var native map[string]any
			if json.Unmarshal([]byte(rows[0][i].(string)), &native) != nil || native["name"] != "Literal Native \x00 Name" {
				t.Fatal("full record lost original literal", rows)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("native object missing")
	}
	code, out, _ = e.run("--max-age", "0", "--fields", "id", "calendar", "attendees", "list", ref, "--source-id", "999")
	if code != 0 || !containsJSONKey(out, "native_attendee_evidence") || len(items(t, decode(t, out))) != 0 {
		t.Fatal("zero/source/fields projection lost independent evidence", out)
	}
}

func TestAttendeeCalendarSummaryScope(t *testing.T) {
	e := newEnv(t)
	ref := attendeeCLIArchive(t, e, 1, false)
	code, out, stderr := e.run("--max-age", "0", "--fields", "attendees", "calendar", "event", ref)
	if code != 0 || !containsJSONKey(out, "native_attendee_evidence") {
		t.Fatal("event fields hid independent native scope", code, out, stderr)
	}
	value := decode(t, out)
	if len(value["attendees"].([]any)) != 2 {
		t.Fatal("native roster replaced retained display roster", out)
	}
	coverage := value["native_attendee_evidence"].(map[string]any)
	if coverage["scope"] != "native_source_selected_detail_not_display_roster" {
		t.Fatal("source/display scope collapsed", out)
	}
}

func TestAttendeeAmbiguityAcrossListShowFormats(t *testing.T) {
	e := newEnv(t)
	ref := attendeeCLIArchive(t, e, 101, true)
	for _, command := range [][]string{{"calendar", "attendees", "list", ref}, {"calendar", "attendees", "show", "attendee-observation:1"}} {
		code, out, stderr := e.run(append([]string{"--max-age", "0"}, command...)...)
		if code != 0 {
			t.Fatal(code, out, stderr)
		}
		rows := items(t, decode(t, out))
		if len(rows) == 0 || rows[0]["ambiguous"] != true {
			t.Fatal("own complete snapshot ambiguity disappeared", out)
		}
		code, out, stderr = e.run(append([]string{"--max-age", "0", "--format", "text"}, command...)...)
		if code != 0 || !strings.Contains(out, "ambiguous") {
			t.Fatal("text lost source-local ambiguity", code, out, stderr)
		}
	}
}
