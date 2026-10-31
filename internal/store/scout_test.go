package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/scoutsessions"
)

const (
	scoutDevA = "0123456789abcdef0123456789abcdef"
	scoutDevB = "fedcba9876543210fedcba9876543210"
)

var scoutBase = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

// Two stamps stand for the parser and scrub rules before and after a change.
const (
	scoutStampA = "stamp-a"
	scoutStampB = "stamp-b"
)

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
	run, err := s.BeginScoutRun(context.Background(), "darwin", root, scoutStampA, at)
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
	other, err := s.BeginScoutRun(ctx, "linux", "/synthetic/Apps/Scout", scoutStampA, scoutBase)
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
		_, err := s.BeginScoutRun(ctx, "darwin", "/synthetic/new", scoutStampA, scoutBase)
		return err
	})
	sweepFaults(t, setupScoutArchive, func(ctx context.Context, s *Store) error {
		_, err := s.BeginScoutRun(ctx, "darwin", "/synthetic/Apps/Scout", scoutStampA, scoutBase)
		return err
	})
	run := ScoutRun{SourceID: 1, RunID: 2, Stamp: scoutStampA}
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
	restamped := ScoutRun{SourceID: 1, RunID: 2, Stamp: scoutStampB}
	sweepFaults(t, setupScoutArchive, func(ctx context.Context, s *Store) error {
		_, err := s.CommitScoutFile(ctx, restamped, same, scoutBase.Add(3*time.Hour))
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

// redactWord is a scrub rule "added later": it replaces one word in the stored JSON, and with
// drop it also removes a session's last message, as a changed mapping might.
func redactWord(word string, drop bool) func([]byte) ([]byte, int) {
	pattern := regexp.MustCompile(`(?i)` + word)
	return func(raw []byte) ([]byte, int) {
		session, _ := scoutsessions.Parse(raw)
		n := len(pattern.FindAllIndex(raw, -1))
		out := pattern.ReplaceAllString(string(raw), "[redacted]")
		if drop && len(session.Messages) > 1 {
			cut := strings.LastIndex(out, `,{"id":"m`)
			out = out[:cut] + "]}"
		}
		return []byte(out), n
	}
}

func TestScoutStampChangeRereadsFilesAndRescrubsInPlace(t *testing.T) {
	s := newStore(t)
	setupScoutArchive(t, s)
	ctx := context.Background()
	// A run under a new stamp knows no last read, so every resident file is opened again.
	run, err := s.BeginScoutRun(ctx, "darwin", "/synthetic/Apps/Scout", scoutStampB, scoutBase.Add(2*time.Hour))
	if err != nil || len(run.Known) != 0 {
		t.Fatalf("%+v %v", run, err)
	}
	// The same bytes read under the new stamp are stored again on the same observation.
	body := scoutBody("b-1", "Inbox triage", true, "triage the inbox", "two urgent threads")
	in := scoutRead(t, scoutDevB, "b-1.json", body, scoutBase)
	in.Raw = []byte(strings.ReplaceAll(body, "urgent", "[redacted]"))
	clean, err := scoutsessions.Parse(in.Raw)
	if err != nil {
		t.Fatal(err)
	}
	in.Session, in.Redactions = &clean, 1
	if got := mustCommitScout(t, s, run, in, scoutBase.Add(2*time.Hour)); got != "rescrubbed" {
		t.Fatalf("outcome %s", got)
	}
	if hits, _, _ := s.ScoutSearch(ctx, "urgent", ScoutFilter{}); len(hits) != 0 {
		t.Fatalf("old text still searchable: %+v", hits)
	}
	if got := mustCommitScout(t, s, run, in, scoutBase.Add(2*time.Hour)); got != "unchanged" {
		t.Fatalf("second commit %s", got)
	}
	again := mustBeginScout(t, s, "/synthetic/Apps/Scout", scoutBase)
	if _, ok := again.Known[ScoutFileKey{scoutDevA, "a-1.json"}]; !ok || len(again.Known) != 1 {
		t.Fatalf("known under the old stamp: %+v", again.Known)
	}
}

func scoutMessageIDs(t *testing.T, s *Store, observation int) []int64 {
	t.Helper()
	rows, err := s.db.QueryContext(context.Background(), `select id from scout_messages where observation_id=? order by ordinal`, observation)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	return ids
}

func TestScoutRuleAddedLaterReachesArchivedAndGoneSessions(t *testing.T) {
	s := newStore(t)
	setupScoutArchive(t, s)
	ctx := context.Background()
	// a-1 leaves OneDrive: its sessions are gone but archived.
	run := mustBeginScout(t, s, "/synthetic/Apps/Scout", scoutBase.Add(2*time.Hour))
	if n := mustFinishScout(t, s, run, ScoutRunEnd{Outcome: "complete", DevicesListed: true, SeenDevices: []string{scoutDevB}, ListedDevices: []string{scoutDevB}}, scoutBase.Add(2*time.Hour)); n != 4 {
		t.Fatalf("gone %d", n)
	}
	before := scoutMessageIDs(t, s, 3)
	if hits, _, err := s.ScoutSearch(ctx, "budget", ScoutFilter{}); err != nil || len(hits) != 1 || len(before) != 3 {
		t.Fatalf("%+v %v %v", hits, before, err)
	}
	n, err := s.RescrubScout(ctx, scoutStampB, redactWord("quarterly", true))
	if err != nil || n != (ScoutRescrub{Rewritten: 3}) {
		t.Fatalf("rescrubbed %+v %v", n, err)
	}
	if hits, _, _ := s.ScoutSearch(ctx, "quarterly", ScoutFilter{}); len(hits) != 0 {
		t.Fatalf("redacted word still searchable: %d", len(hits))
	}
	var raw string
	var redactions, messages, stale int
	if err := s.db.QueryRowContext(ctx, `select raw_json,redactions,message_count,(select count(*) from scout_observations where stamp<>?) from scout_observations where id=3`, scoutStampB).
		Scan(&raw, &redactions, &messages, &stale); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "quarterly") || redactions != 2 || messages != 2 || stale != 0 {
		t.Fatalf("raw %s redactions %d messages %d stale %d", raw, redactions, messages, stale)
	}
	// The kept messages keep their IDs; the dropped one left the FTS index too.
	if after := scoutMessageIDs(t, s, 3); len(after) != 2 || after[0] != before[0] || after[1] != before[1] {
		t.Fatalf("ids moved: %v to %v", before, after)
	}
	if hits, _, _ := s.ScoutSearch(ctx, "budget", ScoutFilter{}); len(hits) != 0 {
		t.Fatalf("dropped message still searchable")
	}
	if n, err := s.RescrubScout(ctx, scoutStampB, redactWord("quarterly", true)); err != nil || n.Rewritten != 0 {
		t.Fatalf("second pass %+v %v", n, err)
	}
	// A mapping that grows a session adds messages under new IDs.
	grow := func(raw []byte) ([]byte, int) {
		return []byte(strings.Replace(string(raw), `]}`, `,{"id":"extra","role":"user","content":"an added synthetic turn"}]}`, 1)), 0
	}
	if n, err := s.RescrubScout(ctx, "stamp-c", grow); err != nil || n.Rewritten != 3 {
		t.Fatalf("grow %+v %v", n, err)
	}
	// One file's two versions share the added turn, so search returns it once per file.
	if hits, _, _ := s.ScoutSearch(ctx, "added", ScoutFilter{}); len(hits) != 2 {
		t.Fatalf("added turns: %d", len(hits))
	}
}

// A session that no longer maps after scrubbing fails closed: it is blanked under the new stamp
// and counted, and it cannot hold back a later run.
func TestScoutRescrubBlanksSessionsThatNoLongerParse(t *testing.T) {
	s := newStore(t)
	setupScoutArchive(t, s)
	ctx := context.Background()
	breakOne := func(raw []byte) ([]byte, int) {
		if strings.Contains(string(raw), `"b-1"`) {
			return []byte("not json"), 0
		}
		return raw, 0
	}
	n, err := s.RescrubScout(ctx, scoutStampB, breakOne)
	if err != nil || n != (ScoutRescrub{Rewritten: 3, Unparseable: 1}) {
		t.Fatalf("%+v %v", n, err)
	}
	var raw, losses, name string
	var messages, rows, stale int
	if err := s.db.QueryRowContext(ctx, `select raw_json,losses_json,name,message_count,(select count(*) from scout_messages where observation_id=2),
(select count(*) from scout_observations where stamp<>?) from scout_observations where id=2`, scoutStampB).Scan(&raw, &losses, &name, &messages, &rows, &stale); err != nil {
		t.Fatal(err)
	}
	if raw != "" || losses != `{"rescrub_unparseable":1}` || name != "" || messages != 0 || rows != 0 || stale != 0 {
		t.Fatalf("raw %q losses %s name %q messages %d rows %d stale %d", raw, losses, name, messages, rows, stale)
	}
	if hits, _, _ := s.ScoutSearch(ctx, "urgent", ScoutFilter{}); len(hits) != 0 {
		t.Fatalf("the blanked session is still searchable")
	}
	if n, err := s.RescrubScout(ctx, scoutStampB, breakOne); err != nil || n.Rewritten != 0 {
		t.Fatalf("second run %+v %v", n, err)
	}
}

// scoutRescrubConsistent checks the archive after a re-scrub that may have stopped part way: each
// observation is either under stamp A or fully rewritten under stamp B, and the FTS index agrees
// with its content.
func scoutRescrubConsistent(t *testing.T, s *Store) (rewritten int) {
	t.Helper()
	ctx := context.Background()
	rows, err := s.cs.DB().QueryContext(ctx, `select o.id,o.stamp,lower(o.raw_json),
(select count(*) from scout_messages m where m.observation_id=o.id and lower(m.text) like '%quarterly%') from scout_observations o order by o.id`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id int64
		var stamp, raw string
		var kept int
		if err := rows.Scan(&id, &stamp, &raw, &kept); err != nil {
			t.Fatal(err)
		}
		switch {
		case stamp == scoutStampB && (strings.Contains(raw, "quarterly") || kept != 0):
			t.Fatalf("observation %d is under the new stamp but not rewritten", id)
		case stamp == scoutStampB:
			rewritten++
		case stamp != scoutStampA:
			t.Fatalf("observation %d has stamp %q", id, stamp)
		}
	}
	if _, err := s.cs.DB().ExecContext(ctx, `insert into scout_fts(scout_fts) values('integrity-check')`); err != nil {
		t.Fatalf("FTS index disagrees with its content: %v", err)
	}
	return rewritten
}

// A failure at any database call (a full disk, an I/O error) leaves every observation either old
// or fully rewritten, and the next run resumes and finishes the rest.
func TestScoutRescrubResumesAfterAnyFailure(t *testing.T) {
	for _, kind := range faultKinds {
		for at := 1; ; at++ {
			s := newStore(t)
			setupScoutArchive(t, s)
			inj := injectFaults(t, s)
			inj.arm(kind, at)
			_, err := s.RescrubScout(context.Background(), scoutStampB, redactWord("quarterly", true))
			if !inj.disarm() {
				if err != nil {
					t.Fatalf("%s: unfaulted run failed: %v", kind, err)
				}
				break
			}
			if err == nil {
				t.Fatalf("%s #%d: failure swallowed", kind, at)
			}
			scoutRescrubConsistent(t, s)
			if _, err := s.RescrubScout(context.Background(), scoutStampB, redactWord("quarterly", true)); err != nil {
				t.Fatalf("%s #%d: resume failed: %v", kind, at, err)
			}
			if err := s.FinishScoutRescrub(context.Background()); err != nil {
				t.Fatal(err)
			}
			if got := scoutRescrubConsistent(t, s); got != 3 {
				t.Fatalf("%s #%d: %d rewritten after resume", kind, at, got)
			}
		}
	}
}

// archiveBytes is the database file and its write-ahead log, as they are on disk.
func archiveBytes(t *testing.T, s *Store) string {
	t.Helper()
	var seq int
	var name, path string
	if err := s.db.QueryRowContext(context.Background(), `select seq,name,file from pragma_database_list where name='main'`).Scan(&seq, &name, &path); err != nil {
		t.Fatal(err)
	}
	main, err := os.ReadFile(path) //nolint:gosec // G304: the test's own archive
	if err != nil {
		t.Fatal(err)
	}
	wal, _ := os.ReadFile(path + "-wal") //nolint:gosec // G304: the test's own archive
	return strings.ToLower(string(main) + string(wal))
}

func TestScoutRescrubLeavesNoCopyOfReplacedText(t *testing.T) {
	s := newStore(t)
	setupScoutArchive(t, s)
	ctx := context.Background()
	if !strings.Contains(archiveBytes(t, s), "quarterly") {
		t.Fatal("setup holds no copy to remove")
	}
	if n, err := s.RescrubScout(ctx, scoutStampB, redactWord("quarterly", false)); err != nil || n.Rewritten != 3 {
		t.Fatalf("%+v %v", n, err)
	}
	if err := s.FinishScoutRescrub(ctx); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(archiveBytes(t, s), "quarterly") {
		t.Fatal("the replaced text survives in the database file or its log")
	}
}

func TestScoutRescrubHonoursCancellation(t *testing.T) {
	s := newStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.RescrubScout(ctx, scoutStampB, redactWord("x", false)); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

// FinishScoutRescrub fails without losing its pending mark, so a later run merges and truncates.
func TestScoutFinishRescrubFaults(t *testing.T) {
	pending := func(t *testing.T, s *Store) {
		setupScoutArchive(t, s)
		if _, err := s.RescrubScout(context.Background(), scoutStampB, redactWord("quarterly", false)); err != nil {
			t.Fatal(err)
		}
	}
	sweepFaults(t, pending, func(ctx context.Context, s *Store) error { return s.FinishScoutRescrub(ctx) })
	s := newStore(t)
	if err := s.FinishScoutRescrub(context.Background()); err != nil {
		t.Fatal("nothing pending is not an error:", err)
	}
	pending(t, s)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.FinishScoutRescrub(ctx); err == nil {
		t.Fatal("cancelled finish succeeded")
	}
}
