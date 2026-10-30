package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/scoutcapture"
	"github.com/ourostack/m365crawl/internal/scoutsessions"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/syncer"
)

const (
	scoutTestDevA = "0123456789abcdef0123456789abcdef"
	scoutTestDevB = "fedcba9876543210fedcba9876543210"
)

// Every session body here is synthetic.
const (
	scoutPlanBody   = `{"id":"s-plan","name":"Synthetic launch plan","createdAt":"2026-05-01T09:00:00Z","updatedAt":"2026-05-01T09:30:00Z","model":"model-x","messages":[{"id":"m0","role":"user","content":"outline the synthetic launch","timestamp":"2026-05-01T09:00:00Z"},{"id":"m1","role":"assistant","content":"Here is a launch outline with three steps.","timestamp":"2026-05-01T09:01:00Z","toolCalls":[{}],"reasoning":"r"},{"id":"m2","role":"user","content":"untimed follow-up"}]}`
	scoutTriageBody = `{"id":"s-triage","name":"Synthetic inbox triage","source":"automation","automationId":"auto-1","messages":[{"id":"m0","role":"user","content":"triage the synthetic inbox","timestamp":"2026-05-02T08:00:00Z"},{"id":"m1","role":"assistant","content":"No urgent launch mail.","timestamp":"2026-05-02T08:00:30Z","fileNames":["a.txt"]}]}`
	scoutBareBody   = `{"id":"s-bare","automationId":"auto-2","messages":"odd"}`
)

func scoutInput(t *testing.T, device, name, body string) store.ScoutFileInput {
	t.Helper()
	session, err := scoutsessions.Parse([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(body))
	return store.ScoutFileInput{Device: device, Name: name, State: "read", Size: int64(len(body)), Modified: time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
		Digest: hex.EncodeToString(sum[:]), Raw: []byte(body), Session: &session}
}

// seedScout writes an archive with one folder: two device folders, three sessions, a cloud-only
// file and one session that is gone.
func seedScout(t *testing.T, e *env) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, e.db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	at := time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)
	run, err := st.BeginScoutRun(ctx, "darwin", "/Users/someone/OneDrive/Apps/Synthetic Scout", scoutsessions.Stamp(), at)
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range []store.ScoutFileInput{
		scoutInput(t, scoutTestDevA, "s-plan.json", scoutPlanBody), scoutInput(t, scoutTestDevB, "s-triage.json", scoutTriageBody),
		scoutInput(t, scoutTestDevB, "s-bare.json", scoutBareBody), scoutInput(t, scoutTestDevB, "s-old.json", `{"id":"s-old","name":"Old synthetic chat","messages":[]}`),
		{Device: scoutTestDevB, Name: "s-cloud.json", State: "cloud_only", Reason: "scout_file_cloud_only", Size: 10},
	} {
		if _, err := st.CommitScoutFile(ctx, run, in, at); err != nil {
			t.Fatal(err)
		}
	}
	end := store.ScoutRunEnd{Outcome: "partial", DevicesListed: true, SeenDevices: []string{scoutTestDevA, scoutTestDevB}, ListedDevices: []string{scoutTestDevA, scoutTestDevB}}
	if _, err := st.FinishScoutRun(ctx, run, end, at); err != nil {
		t.Fatal(err)
	}
	run, err = st.BeginScoutRun(ctx, "darwin", "/Users/someone/OneDrive/Apps/Synthetic Scout", scoutsessions.Stamp(), at.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range []store.ScoutFileInput{{Device: scoutTestDevA, Name: "s-plan.json", State: "unchanged"}, {Device: scoutTestDevB, Name: "s-triage.json", State: "unchanged"},
		{Device: scoutTestDevB, Name: "s-bare.json", State: "unchanged"}, {Device: scoutTestDevB, Name: "s-cloud.json", State: "cloud_only", Reason: "scout_file_cloud_only"}} {
		if _, err := st.CommitScoutFile(ctx, run, in, at.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.FinishScoutRun(ctx, run, end, at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
}

func TestScoutReadsFromTheArchive(t *testing.T) {
	e := newEnv(t)
	seedScout(t, e)
	code, out, stderr := e.run("--json", "scout", "list")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	m := decode(t, out)
	list := items(t, m)
	if len(list) != 4 || list[0]["name"] != "Synthetic inbox triage" || list[0]["origin"] != "automation" || list[1]["name"] != "Synthetic launch plan" ||
		list[1]["origin"] != nil || list[1]["message_count"] != float64(3) || list[1]["file_state"] != "archived" || list[2]["file_state"] != "gone" ||
		list[2]["gone_at"] == nil || list[3]["automation"] != true || list[3]["losses"].(map[string]any)["messages_not_array"] != float64(1) {
		t.Fatalf("list: %v", list)
	}
	coverage := m["scout_coverage"].(map[string]any)
	totals := coverage["totals"].(map[string]any)
	if coverage["state"] != "registered" || coverage["format"] != "unofficial" || totals["cloud_only_files"] != float64(1) || totals["gone_files"] != float64(1) ||
		!strings.Contains(coverage["note"].(string), "Always keep on this device") || m["archive_age_seconds"] == nil {
		t.Fatalf("coverage: %v", m)
	}
	source := coverage["sources"].([]any)[0].(map[string]any)
	if source["locator_tail"] != "Apps/Synthetic Scout" || strings.Contains(out, "someone") || source["fix"] == nil {
		t.Fatalf("source: %v", source)
	}
	for args, want := range map[string]int{"--origin automation": 2, "--origin interactive": 2, "--device " + scoutTestDevA: 1,
		"--since 2026-05-02": 1, "--until 2026-04-15": 2, "--source-id 1 --limit 1": 1} {
		code, out, stderr := e.run(append([]string{"--json", "scout", "list"}, strings.Fields(args)...)...)
		if code != 0 || len(items(t, decode(t, out))) != want {
			t.Fatalf("%s: %d %s %s", args, code, out, stderr)
		}
	}

	code, out, _ = e.run("--json", "--max-text", "12", "scout", "search", "launch")
	hits := items(t, decode(t, out))
	if code != 0 || len(hits) != 4 || hits[0]["session_name"] != "Synthetic i…" || hits[0]["text_truncated"] != true || hits[3]["at"] != nil {
		t.Fatalf("search: %v", hits)
	}
	code, out, _ = e.run("--json", "scout", "search", "urgent", "--origin", "automation", "--since", "2026-05-02")
	hits = items(t, decode(t, out))
	if code != 0 || len(hits) != 1 || hits[0]["role"] != "assistant" || hits[0]["file_names"] != float64(1) || hits[0]["session"] != "scout-session:2" {
		t.Fatalf("filtered search: %v", hits)
	}

	code, out, _ = e.run("--json", "scout", "show", "scout-session:1", "--limit", "2")
	detail := items(t, decode(t, out))
	messages := detail[0]["messages"].([]any)
	if code != 0 || len(messages) != 2 || detail[0]["messages_truncated"] != true || messages[1].(map[string]any)["tool_calls"] != float64(1) ||
		messages[1].(map[string]any)["reasoning"] != true || detail[0]["latest_observation"] != true {
		t.Fatalf("show: %v", detail)
	}
	code, out, _ = e.run("--json", "--fields", "id,messages", "scout", "show", "scout-session:1", "--offset", "2", "--observation", "1")
	detail = items(t, decode(t, out))
	if code != 0 || len(detail[0]) != 2 || len(detail[0]["messages"].([]any)) != 1 {
		t.Fatalf("show fields: %v", detail)
	}
	code, out, _ = e.run("--json", "scout", "sources")
	sources := items(t, decode(t, out))
	if code != 0 || len(sources) != 1 || sources[0]["present_files"] != float64(4) || sources[0]["sessions"] != float64(4) || sources[0]["last_outcome"] != "partial" {
		t.Fatalf("sources: %v", sources)
	}
	// Text output of every read.
	for _, args := range [][]string{{"scout", "list"}, {"scout", "search", "launch"}, {"scout", "show", "scout-session:1", "--limit", "1"}, {"scout", "sources"}, {"scout", "search", "nomatchword"}} {
		code, out, stderr := e.run(append([]string{"--format", "text"}, args...)...)
		if code != 0 || !strings.Contains(out, "scout: registered") || strings.Contains(out, "someone") {
			t.Fatalf("%v: %d %s %s", args, code, out, stderr)
		}
	}
	if _, out, _ := e.run("--format", "text", "scout", "show", "scout-session:1", "--limit", "1"); !strings.Contains(out, "more messages follow: use --offset 1") {
		t.Fatalf("show text: %s", out)
	}
	if _, out, _ := e.run("--json", "scout", "search", "nomatchword"); !strings.Contains(decode(t, out)["note"].(string), "nothing matched;") {
		t.Fatalf("empty search: %s", out)
	}
}

func TestScoutReadsWithoutASource(t *testing.T) {
	e := newEnv(t)
	for _, args := range [][]string{{"scout", "list"}, {"scout", "sources"}} {
		code, out, _ := e.run(append([]string{"--json"}, args...)...)
		m := decode(t, out)
		if code != 0 || m["note"] != "no Scout folder has been read; run m365crawl scout sync to opt in" || m["scout_coverage"].(map[string]any)["state"] != "not_registered" || m["archive_age_seconds"] != nil {
			t.Fatalf("%v: %v", args, m)
		}
	}
	if code, _, stderr := e.run("--json", "scout", "show", "scout-session:1"); code != 2 || !strings.Contains(stderr, "scout_session_unknown") {
		t.Fatalf("missing archive show: %d %s", code, stderr)
	}
	st, err := store.Open(context.Background(), e.db)
	if err != nil {
		t.Fatal(err)
	}
	_ = st.Close()
	code, _, stderr := e.run("--json", "scout", "show", "scout-session:1")
	if code != 2 || !strings.Contains(stderr, "scout_session_unknown") {
		t.Fatalf("unknown session: %d %s", code, stderr)
	}
	// A source whose only file is fine shows the plain empty note.
	ctx := context.Background()
	st, err = store.Open(ctx, e.db)
	if err != nil {
		t.Fatal(err)
	}
	run, err := st.BeginScoutRun(ctx, "linux", "/synthetic/Scout", scoutsessions.Stamp(), time.Now())
	if err == nil {
		_, err = st.FinishScoutRun(ctx, run, store.ScoutRunEnd{Outcome: "complete"}, time.Now())
	}
	if err := errors.Join(err, st.Close()); err != nil {
		t.Fatal(err)
	}
	code, out, _ := e.run("--json", "scout", "list")
	if code != 0 || !strings.HasPrefix(decode(t, out)["note"].(string), "nothing matched in the archived Scout sessions") {
		t.Fatalf("plain empty: %s", out)
	}
}

func TestScoutRefusesBadArguments(t *testing.T) {
	e := newEnv(t)
	seedScout(t, e)
	for _, args := range [][]string{
		{"--outlook-account", "x", "scout", "list"}, {"scout", "list", "--limit=-1"}, {"scout", "list", "--since", "soon"}, {"scout", "list", "--until", "later"},
		{"scout", "search", " "}, {"scout", "search", "x", "--since", "soon"}, {"scout", "search", "x", "--until", "later"}, {"scout", "show", "scout-session:0"}, {"scout", "show", "loop-channel:1"},
		{"scout", "show", "scout-session:01"}, {"scout", "show", "scout-session:1", "--offset=-1"}, {"scout", "show", "scout-session:1", "--observation", "2"},
		{"--fields", "nope", "scout", "list"}, {"--fields", "nope", "scout", "search", "x"}, {"--fields", "nope", "scout", "show", "scout-session:1"},
		{"--fields", "nope", "scout", "sources"}, {"--outlook-profile", "p", "scout", "sync"}, {"scout", "list", "--origin", "robots"},
	} {
		if code, _, stderr := e.run(append([]string{"--json"}, args...)...); code != 2 {
			t.Fatalf("%v: exit %d %s", args, code, stderr)
		}
	}
	db, err := sql.Open("sqlite", e.db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`update scout_observations set losses_json='x'`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	for _, args := range [][]string{{"scout", "list"}, {"scout", "search", "launch"}, {"scout", "show", "scout-session:1"}} {
		if code, _, stderr := e.run(append([]string{"--json"}, args...)...); code == 0 || !strings.Contains(stderr, "db_error") {
			t.Fatalf("%v: corrupt archive read: %d %s", args, code, stderr)
		}
	}
	db, err = sql.Open("sqlite", e.db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`update scout_observations set losses_json='null'`); err != nil {
		t.Fatal(err)
	}
	code, out, _ := e.run("--json", "scout", "list")
	if code != 0 || items(t, decode(t, out))[0]["losses"] == nil {
		t.Fatalf("null losses: %s", out)
	}
	if _, err := db.Exec(`update scout_sources set registered_at='bad'`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	for _, args := range [][]string{{"scout", "list"}, {"scout", "search", "launch"}, {"scout", "show", "scout-session:1"}, {"scout", "sources"}} {
		if code, _, stderr := e.run(append([]string{"--json"}, args...)...); code == 0 || !strings.Contains(stderr, "db_error") {
			t.Fatalf("%v: corrupt source read: %d %s", args, code, stderr)
		}
	}
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := Main([]string{"--db", dir, "--json", "scout", "list"}, &stdout, &stderr); code == 0 {
		t.Fatalf("a directory read as an archive: %s", stdout.String())
	}
}

func stubScoutSync(t *testing.T, report syncer.ScoutReport, err error) *syncer.ScoutOptions {
	t.Helper()
	seen := &syncer.ScoutOptions{}
	old := runScoutSync
	runScoutSync = func(_ context.Context, opts syncer.ScoutOptions) (syncer.ScoutReport, error) {
		*seen = opts
		return report, err
	}
	t.Cleanup(func() { runScoutSync = old })
	return seen
}

func TestScoutSyncReports(t *testing.T) {
	e := newEnv(t)
	seedScout(t, e)
	folder := syncer.ScoutFolderReport{SourceID: 1, Root: "/Users/someone/OneDrive/Apps/Synthetic Scout", Outcome: "partial", Devices: 2, Listed: 5,
		Admitted: 1, Unchanged: 2, CloudOnly: 1, Unsafe: 1, Gone: 1, Redacted: 1}
	folder.Rescrubbed = 1
	seen := stubScoutSync(t, syncer.ScoutReport{Discovered: true, Rescrubbed: 2, Folders: []syncer.ScoutFolderReport{folder}}, nil)
	code, out, stderr := e.run("--json", "scout", "sync")
	m := decode(t, out)
	if code != 0 || m["discovered"] != true || m["admitted"] != float64(1) || m["failed"] != float64(1) || m["rescrubbed"] != float64(3) || len(m["notices"].([]any)) != 3 || seen.Home == "" || seen.DBPath != e.db {
		t.Fatalf("sync: %d %v %s", code, m, stderr)
	}
	if f := m["folders"].([]any)[0].(map[string]any); f["locator_tail"] != "Apps/Synthetic Scout" || !strings.Contains(f["fix"].(string), "Always keep") {
		t.Fatalf("folder: %v", f)
	}
	if code, out, _ := e.run("--format", "text", "scout", "sync", "--root", "/synthetic/a", "--root", "/synthetic/b"); code != 0 || !strings.Contains(out, "scout: 1 admitted") || !strings.Contains(out, "notice:") ||
		len(seen.Roots) != 2 {
		t.Fatalf("text sync: %s %v", out, seen)
	}
	// Every folder failed: the result is printed and the first reason is the error.
	stubScoutSync(t, syncer.ScoutReport{Folders: []syncer.ScoutFolderReport{{Root: "/x/y", Outcome: "failed", Reason: "scout_root_invalid"}}}, nil)
	code, out, stderr = e.run("--json", "scout", "sync", "--root", "/x/y")
	if code != 2 || !strings.Contains(stderr, "scout_root_invalid") || decode(t, out)["folders"] == nil {
		t.Fatalf("invalid root: %d %s %s", code, out, stderr)
	}
	stubScoutSync(t, syncer.ScoutReport{Folders: []syncer.ScoutFolderReport{{SourceID: 1, Root: "/x/y", Outcome: "failed", Reason: "scout_devices_unreadable"}}}, nil)
	if code, _, stderr := e.run("--json", "scout", "sync"); code != 3 || !strings.Contains(stderr, "scout_devices_unreadable") {
		t.Fatalf("unreadable: %d %s", code, stderr)
	}
	// A run error after a folder was written prints the result first.
	stubScoutSync(t, syncer.ScoutReport{Folders: []syncer.ScoutFolderReport{folder}}, context.Canceled)
	if code, out, _ := e.run("--json", "scout", "sync"); code == 0 || decode(t, out)["admitted"] != float64(1) {
		t.Fatalf("run error: %d %s", code, out)
	}
	for err, want := range map[error]string{
		scoutcapture.ErrPlatformUnqualified:                  "scout_platform_unqualified",
		syncer.ErrScoutFolderNotFound:                        "scout_folder_not_found",
		&scoutcapture.Error{Code: "scout_guard_unavailable"}: "scout_guard_unavailable",
		errors.New("synthetic database failure"):             "error",
	} {
		stubScoutSync(t, syncer.ScoutReport{}, err)
		code, out, stderr := e.run("--json", "scout", "sync")
		if code == 0 || out != "" || !strings.Contains(stderr, want) {
			t.Fatalf("%v: %d %q %s", err, code, out, stderr)
		}
	}
}

func TestScoutSyncEdges(t *testing.T) {
	e := newEnv(t)
	stubScoutSync(t, syncer.ScoutReport{Folders: []syncer.ScoutFolderReport{{SourceID: 1, Root: "/x/y", Outcome: "complete", Listed: 1, Admitted: 1}}}, nil)
	// No archive yet: coverage is empty, not an error.
	code, out, stderr := e.run("--json", "scout", "sync", "--root", "/x/y")
	if code != 0 || decode(t, out)["scout_coverage"].(map[string]any)["state"] != "not_registered" {
		t.Fatalf("no archive: %d %s %s", code, out, stderr)
	}
	// An archive that cannot be read after the run is the command's error.
	dir := filepath.Join(t.TempDir(), "dir.db")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	var stdout, stderrBuf bytes.Buffer
	if code := Main([]string{"--db", dir, "--json", "scout", "sync", "--root", "/x/y"}, &stdout, &stderrBuf); code == 0 || stdout.Len() == 0 {
		t.Fatalf("unreadable archive accepted: %d %s", code, stdout.String())
	}
	old := scoutHome
	scoutHome = func() (string, error) { return "", errors.New("no home") }
	t.Cleanup(func() { scoutHome = old })
	if code, _, stderr := e.run("--json", "scout", "sync"); code != 3 || !strings.Contains(stderr, "scout_folder_not_found") {
		t.Fatalf("no home: %d %s", code, stderr)
	}
	if code, _, _ := e.run("--json", "scout", "sync", "--root", "/x/y"); code != 0 {
		t.Fatal("explicit root needs no home")
	}
}

func TestScoutHelpers(t *testing.T) {
	origin := "teams"
	if scoutOrigin(scoutSessionItem{Origin: &origin}) != "teams" || scoutOrigin(scoutSessionItem{Automation: true}) != "automation" || scoutOrigin(scoutSessionItem{}) != "interactive" {
		t.Fatal("origin")
	}
	if shortDevice("abc") != "abc" || shortDevice(scoutTestDevA) != "01234567" {
		t.Fatal("device")
	}
	if scoutFix("", 0) != "" || scoutFix("", 1) != scoutCloudOnlyFix || scoutFix("scout_root_invalid", 1) == scoutCloudOnlyFix {
		t.Fatal("fix")
	}
	updated := time.Now()
	row := scoutSessionRow(scoutSessionItem{UpdatedAt: &updated})
	if row[0] == "-" || row[6] != "(untitled)" {
		t.Fatalf("row %v", row)
	}
	if scoutSessionRow(scoutSessionItem{})[0] != "-" {
		t.Fatal("file time")
	}
	(&runtime{}).renderScoutCoverage(nil)
	if (&runtime{}).scoutAge([]store.ScoutSource{{LastRun: &store.ScoutRunRow{}}}) != nil {
		t.Fatal("age of an unfinished run")
	}
}

func TestScoutSyncReadsARealFolder(t *testing.T) {
	if !scoutcapture.Supported() {
		t.Skip("acquisition is not qualified on this platform")
	}
	e := newEnv(t)
	root := t.TempDir()
	device := filepath.Join(root, "sessions", "devices", scoutTestDevA)
	if err := os.MkdirAll(device, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(device, "s-plan.json"), []byte(scoutPlanBody), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, stderr := e.run("--json", "scout", "sync", "--root", root)
	if code != 0 || decode(t, out)["admitted"] != float64(1) {
		t.Fatalf("%d %s %s", code, out, stderr)
	}
	code, out, _ = e.run("--json", "scout", "search", "outline")
	if code != 0 || len(items(t, decode(t, out))) != 2 {
		t.Fatalf("search after sync: %s", out)
	}
}
