package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/scoutsessions"
)

const (
	scoutDevA = "0123456789abcdef0123456789abcdef"
	scoutDevB = "fedcba9876543210fedcba9876543210"
)

var scoutBase = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

// scoutBody is a synthetic session file; every id, name and text is invented.
func scoutBody(id, name string, automation bool, messages ...string) string {
	auto := ""
	if automation {
		auto = `,"source":"automation","automationId":"auto-1"`
	}
	body := fmt.Sprintf(`{"id":%q,"name":%q,"createdAt":"2026-03-01T09:00:00Z","updatedAt":"2026-03-01T10:00:00Z","model":"model-x"%s,"messages":[`, id, name, auto)
	for i, text := range messages {
		if i > 0 {
			body += ","
		}
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		body += fmt.Sprintf(`{"id":"m%d","role":%q,"content":%q,"timestamp":"2026-03-01T09:%02d:00Z"}`, i, role, text, i)
	}
	return body + "]}"
}

func scoutRead(t testing.TB, device, name, body string, modified time.Time) ScoutFileInput {
	t.Helper()
	session, err := scoutsessions.Parse([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(body))
	return ScoutFileInput{Device: device, Name: name, State: "read", Size: int64(len(body)), Modified: modified,
		Digest: hex.EncodeToString(sum[:]), Raw: []byte(body), Session: &session}
}

func mustBeginScout(t testing.TB, s *Store, root string, at time.Time) ScoutRun {
	t.Helper()
	run, err := s.BeginScoutRun(context.Background(), "darwin", root, at)
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func mustCommitScout(t testing.TB, s *Store, run ScoutRun, in ScoutFileInput, at time.Time) string {
	t.Helper()
	outcome, err := s.CommitScoutFile(context.Background(), run, in, at)
	if err != nil {
		t.Fatal(err)
	}
	return outcome
}

func mustFinishScout(t testing.TB, s *Store, run ScoutRun, end ScoutRunEnd, at time.Time) int {
	t.Helper()
	gone, err := s.FinishScoutRun(context.Background(), run, end, at)
	if err != nil {
		t.Fatal(err)
	}
	return gone
}

// setupScoutArchive archives two device folders: an interactive session read twice (two
// observations), an automation run, a cloud-only file and an unparsable one.
func setupScoutArchive(t *testing.T, s *Store) {
	t.Helper()
	run := mustBeginScout(t, s, "/synthetic/Apps/Scout", scoutBase)
	mustCommitScout(t, s, run, scoutRead(t, scoutDevA, "a-1.json", scoutBody("a-1", "Quarterly plan", false, "draft the quarterly plan", "here is the plan"), scoutBase), scoutBase)
	mustCommitScout(t, s, run, scoutRead(t, scoutDevB, "b-1.json", scoutBody("b-1", "Inbox triage", true, "triage the inbox", "two urgent threads"), scoutBase), scoutBase)
	mustCommitScout(t, s, run, ScoutFileInput{Device: scoutDevB, Name: "c-1.json", State: "cloud_only", Reason: "scout_file_cloud_only", Size: 900}, scoutBase)
	mustCommitScout(t, s, run, ScoutFileInput{Device: scoutDevB, Name: "d-1.json", State: "read", Size: 3, Digest: "x"}, scoutBase)
	mustFinishScout(t, s, run, ScoutRunEnd{Outcome: "partial", DevicesListed: true, SeenDevices: []string{scoutDevA, scoutDevB},
		ListedDevices: []string{scoutDevA, scoutDevB}, Listed: 4, Admitted: 2, CloudOnly: 1, Failed: 1}, scoutBase)
	later := scoutBase.Add(time.Hour)
	run = mustBeginScout(t, s, "/synthetic/Apps/Scout", later)
	mustCommitScout(t, s, run, scoutRead(t, scoutDevA, "a-1.json", scoutBody("a-1", "Quarterly plan", false, "draft the quarterly plan", "here is the plan", "add the budget"), later), later)
	mustCommitScout(t, s, run, ScoutFileInput{Device: scoutDevB, Name: "b-1.json", State: "unchanged"}, later)
	mustCommitScout(t, s, run, ScoutFileInput{Device: scoutDevB, Name: "c-1.json", State: "cloud_only", Reason: "scout_file_cloud_only", Size: 900}, later)
	mustCommitScout(t, s, run, ScoutFileInput{Device: scoutDevB, Name: "d-1.json", State: "read", Size: 3, Digest: "x"}, later)
	mustFinishScout(t, s, run, ScoutRunEnd{Outcome: "partial", DevicesListed: true, SeenDevices: []string{scoutDevA, scoutDevB},
		ListedDevices: []string{scoutDevA, scoutDevB}, Listed: 4, Admitted: 1, Unchanged: 1, CloudOnly: 1, Failed: 1}, later)
}

func TestScoutCommitOutcomesAndImmutableObservations(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	run := mustBeginScout(t, s, "/synthetic/Apps/Scout", scoutBase)
	if run.SourceID != 1 || run.RunID != 1 || len(run.Known) != 0 {
		t.Fatalf("first run: %+v", run)
	}
	v1 := scoutRead(t, scoutDevA, "a-1.json", scoutBody("a-1", "Plan", false, "one"), scoutBase)
	v2 := scoutRead(t, scoutDevA, "a-1.json", `{"id":"a-1","messages":[{"role":"user","content":"one"},{"role":"assistant","content":"two"}]}`, scoutBase.Add(time.Minute))
	for i, step := range []struct {
		in   ScoutFileInput
		want string
	}{
		{v1, "admitted"}, {v1, "unchanged"}, {v2, "admitted"},
		{v1, "unchanged"}, // A-B-A reuses A's observation
		{ScoutFileInput{Device: scoutDevA, Name: "a-1.json", State: "unchanged"}, "unchanged"},
		{ScoutFileInput{Device: scoutDevA, Name: "a-1.json", State: "cloud_only", Reason: "scout_file_cloud_only"}, ScoutCloudOnly},
		{ScoutFileInput{Device: scoutDevA, Name: "z.json", State: "too_large", Reason: "scout_file_too_large"}, ScoutTooLarge},
		{ScoutFileInput{Device: scoutDevA, Name: "y.json", State: "read"}, ScoutFailed},
	} {
		if got := mustCommitScout(t, s, run, step.in, scoutBase); got != step.want {
			t.Fatalf("step %d: %s, want %s", i, got, step.want)
		}
	}
	var observations, latest, messages int
	if err := s.db.QueryRowContext(ctx, `select (select count(*) from scout_observations),(select latest_observation from scout_files where file_name='a-1.json'),
(select count(*) from scout_messages)`).Scan(&observations, &latest, &messages); err != nil {
		t.Fatal(err)
	}
	if observations != 2 || latest != 1 || messages != 3 {
		t.Fatalf("observations %d latest %d messages %d", observations, latest, messages)
	}
	again := mustBeginScout(t, s, "/synthetic/Apps/Scout", scoutBase)
	if again.SourceID != 1 || again.RunID != 2 || again.Known[ScoutFileKey{scoutDevA, "a-1.json"}] != (ScoutKnown{v1.Size, scoutBase.UnixNano()}) || len(again.Known) != 1 {
		t.Fatalf("known: %+v", again)
	}
	other, err := s.BeginScoutRun(ctx, "linux", "/synthetic/Apps/Scout", scoutBase)
	if err != nil || other.SourceID != 2 {
		t.Fatalf("platform is part of the source: %+v %v", other, err)
	}
}

func TestScoutFinishMarksGoneOnlyAfterCompleteListings(t *testing.T) {
	s := newStore(t)
	setupScoutArchive(t, s)
	ctx := context.Background()
	gone := func() map[string]bool {
		rows, err := s.db.QueryContext(ctx, `select file_name, gone_at is not null from scout_files order by id`)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = rows.Close() }()
		out := map[string]bool{}
		for rows.Next() {
			var name string
			var g bool
			if err := rows.Scan(&name, &g); err != nil {
				t.Fatal(err)
			}
			out[name] = g
		}
		return out
	}
	at := scoutBase.Add(2 * time.Hour)
	// Device B could not be listed and device folders were listed: only device A's missing files are gone.
	run := mustBeginScout(t, s, "/synthetic/Apps/Scout", at)
	if n := mustFinishScout(t, s, run, ScoutRunEnd{Outcome: "partial", DevicesListed: true, SeenDevices: []string{scoutDevA, scoutDevB}, ListedDevices: []string{scoutDevA}}, at); n != 1 {
		t.Fatalf("gone %d", n)
	}
	if g := gone(); !g["a-1.json"] || g["b-1.json"] || g["c-1.json"] {
		t.Fatalf("after partial listing: %v", g)
	}
	// The device folders themselves could not be listed: nothing changes.
	run = mustBeginScout(t, s, "/synthetic/Apps/Scout", at)
	if n := mustFinishScout(t, s, run, ScoutRunEnd{Outcome: "failed", Reason: "scout_devices_unreadable"}, at); n != 0 {
		t.Fatalf("gone %d", n)
	}
	// Device B's folder is no longer there: its files are gone; a-1 is listed again and comes back.
	run = mustBeginScout(t, s, "/synthetic/Apps/Scout", at)
	mustCommitScout(t, s, run, ScoutFileInput{Device: scoutDevA, Name: "a-1.json", State: "unchanged"}, at)
	if n := mustFinishScout(t, s, run, ScoutRunEnd{Outcome: "complete", DevicesListed: true, SeenDevices: []string{scoutDevA}, ListedDevices: []string{scoutDevA}, Listed: 1}, at); n != 3 {
		t.Fatalf("gone %d", n)
	}
	if g := gone(); g["a-1.json"] || !g["b-1.json"] || !g["c-1.json"] || !g["d-1.json"] {
		t.Fatalf("after device removal: %v", g)
	}
	sources, ok, err := s.ScoutSources(ctx)
	if err != nil || !ok || len(sources) != 1 {
		t.Fatalf("%+v %v %v", sources, ok, err)
	}
	src := sources[0]
	if src.Files != (ScoutFileCounts{Present: 1, Archived: 1, Gone: 3, Sessions: 2}) || src.Devices != 1 || src.LastRun.Gone != 3 || src.LastRun.Outcome != "complete" {
		t.Fatalf("source: %+v run %+v", src, src.LastRun)
	}
	// Gone files keep their archived sessions.
	sessions, _, err := s.ScoutSessions(ctx, ScoutFilter{})
	if err != nil || len(sessions) != 2 || sessions[1].GoneAt == nil {
		t.Fatalf("%+v %v", sessions, err)
	}
}

func TestScoutReads(t *testing.T) {
	s := newStore(t)
	setupScoutArchive(t, s)
	ctx := context.Background()
	sources, ok, err := s.ScoutSources(ctx)
	if err != nil || !ok || len(sources) != 1 {
		t.Fatal(sources, ok, err)
	}
	src := sources[0]
	if src.Platform != "darwin" || src.Root != "/synthetic/Apps/Scout" || !src.RegisteredAt.Equal(scoutBase) || src.Devices != 2 ||
		src.Files != (ScoutFileCounts{Present: 4, Archived: 2, CloudOnly: 1, Failed: 1, Sessions: 2}) {
		t.Fatalf("source: %+v", src)
	}
	if r := src.LastRun; r == nil || r.ID != 2 || r.Outcome != "partial" || r.Devices != 2 || r.Listed != 4 || r.Admitted != 1 || r.Unchanged != 1 ||
		r.CloudOnly != 1 || r.Failed != 1 || r.FinishedAt == nil || !r.StartedAt.Equal(scoutBase.Add(time.Hour)) {
		t.Fatalf("run: %+v", src.LastRun)
	}
	sessions, truncated, err := s.ScoutSessions(ctx, ScoutFilter{})
	if err != nil || truncated || len(sessions) != 2 {
		t.Fatal(sessions, truncated, err)
	}
	a := sessions[0]
	if a.FileName != "a-1.json" || a.Observations != 2 || a.LatestObservation != 3 || a.Observation.ID != 3 || a.Observation.MessageCount != 3 ||
		a.Observation.Name != "Quarterly plan" || a.Observation.Model != "model-x" || a.Observation.Automation || a.State != ScoutArchived ||
		a.Observation.LastMessageAt.Minute() != 2 || a.Observation.FirstMessageAt.Minute() != 0 || a.Observation.CreatedAt.Hour() != 9 || len(a.Observation.Losses) != 0 {
		t.Fatalf("session a: %+v", a)
	}
	b := sessions[1]
	if b.FileName != "b-1.json" || b.Device != scoutDevB || !b.Observation.Automation || b.Observation.Origin != "automation" || b.Observations != 1 {
		t.Fatalf("session b: %+v", b)
	}
	yes, no := true, false
	for name, f := range map[string]ScoutFilter{
		"automation": {Automation: &yes}, "device": {Device: scoutDevB}, "source": {SourceID: 1, Automation: &yes},
		"since": {Since: time.Date(2026, 3, 1, 9, 0, 30, 0, time.UTC), Automation: &yes},
		"until": {Until: time.Date(2026, 3, 1, 9, 0, 30, 0, time.UTC), Automation: &yes},
	} {
		if got, _, err := s.ScoutSessions(ctx, f); err != nil || len(got) != 1 || got[0].FileName != "b-1.json" {
			t.Fatalf("%s: %+v %v", name, got, err)
		}
	}
	if got, _, _ := s.ScoutSessions(ctx, ScoutFilter{Automation: &no, Until: time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC)}); len(got) != 0 {
		t.Fatalf("window: %+v", got)
	}
	if got, truncated, _ := s.ScoutSessions(ctx, ScoutFilter{Limit: 1}); len(got) != 1 || !truncated {
		t.Fatalf("limit: %+v %v", got, truncated)
	}

	hits, truncated, err := s.ScoutSearch(ctx, "plan", ScoutFilter{})
	if err != nil || truncated {
		t.Fatal(err)
	}
	// "draft the quarterly plan" and "here is the plan" are in both observations of a-1 and are
	// returned once each, from the newer observation; the session name matches every message.
	if len(hits) != 3 || hits[0].Message.Text != "add the budget" || hits[0].Session.Observation.ID != 3 || hits[2].Message.Ordinal != 0 {
		for _, h := range hits {
			t.Logf("%d %s %d", h.Message.ID, h.Message.Text, h.Session.Observation.ID)
		}
		t.Fatalf("hits: %d", len(hits))
	}
	for _, h := range hits {
		if h.Session.Observation.ID != 3 || h.Message.At == nil || h.Message.Role == "" {
			t.Fatalf("hit: %+v", h)
		}
	}
	if hits, truncated, _ := s.ScoutSearch(ctx, "plan", ScoutFilter{Limit: 2}); len(hits) != 2 || !truncated {
		t.Fatalf("limit: %d %v", len(hits), truncated)
	}
	if hits, _, _ := s.ScoutSearch(ctx, "urgent", ScoutFilter{Device: scoutDevB, Since: scoutBase.Add(-4 * time.Hour), Until: scoutBase}); len(hits) != 1 || hits[0].Message.Role != "assistant" {
		t.Fatalf("filtered: %+v", hits)
	}
	if _, _, err := s.ScoutSearch(ctx, "  ", ScoutFilter{}); err == nil {
		t.Fatal("empty query accepted")
	}

	detail, err := s.ScoutSessionDetail(ctx, a.FileID, 0, 1, 1)
	if err != nil || detail.Session.Observation.ID != 3 || detail.MessagesTotal != 3 || len(detail.Messages) != 1 || detail.Messages[0].Ordinal != 1 {
		t.Fatalf("detail: %+v %v", detail, err)
	}
	detail, err = s.ScoutSessionDetail(ctx, a.FileID, 1, 0, 0)
	if err != nil || detail.Session.Observation.ID != 1 || detail.MessagesTotal != 2 || len(detail.Messages) != 2 || detail.Session.LatestObservation != 3 {
		t.Fatalf("older observation: %+v %v", detail, err)
	}
	for _, ids := range [][2]int64{{a.FileID, 2}, {99, 0}} {
		if _, err := s.ScoutSessionDetail(ctx, ids[0], ids[1], 0, 0); !errors.Is(err, ErrScoutSessionNotFound) {
			t.Fatalf("%v: %v", ids, err)
		}
	}
}

func TestScoutReadsOfAnArchiveWithoutScoutTables(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	if _, err := s.db.ExecContext(ctx, `drop table scout_files`); err != nil {
		t.Fatal(err)
	}
	if sources, ok, err := s.ScoutSources(ctx); err != nil || ok || len(sources) != 0 {
		t.Fatal(sources, ok, err)
	}
	if sessions, _, err := s.ScoutSessions(ctx, ScoutFilter{}); err != nil || len(sessions) != 0 {
		t.Fatal(sessions, err)
	}
	if hits, _, err := s.ScoutSearch(ctx, "plan", ScoutFilter{}); err != nil || len(hits) != 0 {
		t.Fatal(hits, err)
	}
	if _, err := s.ScoutSessionDetail(ctx, 1, 0, 0, 0); !errors.Is(err, ErrScoutSessionNotFound) {
		t.Fatal(err)
	}
}

func TestScoutReadsRefuseCorruptRows(t *testing.T) {
	for name, corrupt := range map[string]string{
		"registered":   `update scout_sources set registered_at='bad'`,
		"run started":  `update scout_runs set started_at='bad'`,
		"modified":     `update scout_observations set file_modified_at='bad'`,
		"losses":       `update scout_observations set losses_json='x'`,
		"message time": `update scout_messages set at='bad'`,
	} {
		t.Run(name, func(t *testing.T) {
			s := newStore(t)
			setupScoutArchive(t, s)
			ctx := context.Background()
			if _, err := s.db.ExecContext(ctx, corrupt); err != nil {
				t.Fatal(err)
			}
			_, _, sourcesErr := s.ScoutSources(ctx)
			_, _, sessionsErr := s.ScoutSessions(ctx, ScoutFilter{})
			_, _, searchErr := s.ScoutSearch(ctx, "plan", ScoutFilter{})
			_, detailErr := s.ScoutSessionDetail(ctx, 1, 0, 0, 0)
			failed := 0
			for _, err := range []error{sourcesErr, sessionsErr, searchErr, detailErr} {
				if err != nil {
					failed++
				}
			}
			if failed == 0 {
				t.Fatal("corruption read as data")
			}
		})
	}
}

func TestScoutWriteFaultsLeaveArchiveUnchanged(t *testing.T) {
	sweepFaults(t, nil, func(ctx context.Context, s *Store) error {
		_, err := s.BeginScoutRun(ctx, "darwin", "/synthetic/new", scoutBase)
		return err
	})
	sweepFaults(t, setupScoutArchive, func(ctx context.Context, s *Store) error {
		_, err := s.BeginScoutRun(ctx, "darwin", "/synthetic/Apps/Scout", scoutBase)
		return err
	})
	run := ScoutRun{SourceID: 1, RunID: 2}
	body := scoutRead(t, scoutDevA, "a-1.json", scoutBody("a-1", "Quarterly plan", false, "a new turn", "and its answer"), scoutBase.Add(3*time.Hour))
	sweepFaults(t, setupScoutArchive, func(ctx context.Context, s *Store) error {
		_, err := s.CommitScoutFile(ctx, run, body, scoutBase.Add(3*time.Hour))
		return err
	})
	same := scoutRead(t, scoutDevB, "b-1.json", scoutBody("b-1", "Inbox triage", true, "triage the inbox", "two urgent threads"), scoutBase)
	sweepFaults(t, setupScoutArchive, func(ctx context.Context, s *Store) error {
		_, err := s.CommitScoutFile(ctx, run, same, scoutBase.Add(3*time.Hour))
		return err
	})
	sweepFaults(t, setupScoutArchive, func(ctx context.Context, s *Store) error {
		_, err := s.FinishScoutRun(ctx, ScoutRun{SourceID: 1, RunID: 3}, ScoutRunEnd{Outcome: "complete", DevicesListed: true, SeenDevices: []string{scoutDevA}, ListedDevices: []string{scoutDevA}}, scoutBase.Add(3*time.Hour))
		return err
	})
}

func TestScoutReadFaultsRemainTerminal(t *testing.T) {
	for name, op := range map[string]func(context.Context, *Store) error{
		"sources": func(ctx context.Context, s *Store) error { _, _, err := s.ScoutSources(ctx); return err },
		"sessions": func(ctx context.Context, s *Store) error {
			_, _, err := s.ScoutSessions(ctx, ScoutFilter{Limit: 5})
			return err
		},
		"search": func(ctx context.Context, s *Store) error {
			_, _, err := s.ScoutSearch(ctx, "plan", ScoutFilter{})
			return err
		},
		"detail": func(ctx context.Context, s *Store) error { _, err := s.ScoutSessionDetail(ctx, 1, 0, 0, 2); return err },
	} {
		t.Run(name, func(t *testing.T) {
			sweepReadFaults(t, setupScoutArchive, op)
			sweepFaults(t, setupScoutArchive, op)
		})
	}
}
