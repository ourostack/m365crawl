package cli

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/teamsdesktop"
)

func TestDraftsWindowsEmitterLiteral(t *testing.T) {
	id := "teams-draft:" + strings.Repeat("a", 64)
	revision := strings.Repeat("b", 64)
	got := draftInspectionCommand("windows", `C:\review\%REVIEW_ARCHIVE%\cached.db`, "tenant/user", id, revision, true)
	for _, text := range []string{"& {", "$PSNativeCommandArgumentPassing = 'Standard'", `--db 'C:\review\%REVIEW_ARCHIVE%\cached.db'`, "--account 'tenant/user'", "--no-sync --max-text 0", "--include-removed"} {
		if !strings.Contains(got, text) {
			t.Fatalf("Windows emitter is not declared literal PowerShell: missing %q in %s", text, got)
		}
	}
}

func TestDraftsShellMetadata(t *testing.T) {
	if draftInspectionShell("windows") != "powershell_7_3_standard" || draftInspectionShell("darwin") != "posix" {
		t.Fatal("follow-up shell dialect is not explicit")
	}
	if got := draftPowerShellLiteral("'‘’‚‛$`%!&🌱"); got != "'"+"''‘‘’’‚‚‛‛$`%!&🌱"+"'" {
		t.Fatal("ASCII or interpreted smart quote not literal", got)
	}
	e := draftCLI(t)
	row := draftOne(t, e)
	if row["full_inspection_shell"] != draftInspectionShell(goruntime.GOOS) {
		t.Fatal(row)
	}
}

func TestDraftsMissingArchiveExplanation(t *testing.T) {
	e := newEnv(t)
	var previous string
	for _, digit := range []string{"a", "b"} {
		id := "teams-draft:" + strings.Repeat(digit, 64)
		code, out, stderr := draftRun(e, "drafts", "show", id)
		body := errorOf(t, stderr)
		if code == 0 || out != "" || body["code"] != "teams_draft_unknown" || !strings.Contains(stderr, "selected archive is missing") || strings.Contains(stderr, id) {
			t.Fatal("missing selected archive context lost or leaked", code, out, stderr)
		}
		if previous != "" && previous != stderr {
			t.Fatal("missing-archive error depends on requested ID")
		}
		previous = stderr
	}
	if _, err := os.Stat(e.db); !os.IsNotExist(err) {
		t.Fatal("missing archive was acquired", err)
	}
}

func TestDraftsPowerShellArgvHelper(t *testing.T) {
	if os.Getenv("M365CRAWL_DRAFT_ARGV_HELPER") != "1" {
		return
	}
	i := slices.Index(os.Args, "--")
	if i < 0 {
		t.Fatal("missing helper delimiter")
	}
	data, err := json.Marshal(os.Args[i+1:])
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("DRAFT_ARGV:%s\n", base64.StdEncoding.EncodeToString(data))
}

func draftPowerShellWords(t *testing.T, command string) []string {
	t.Helper()
	name := "pwsh"
	if goruntime.GOOS == "windows" {
		name = "pwsh.exe"
	}
	shell, err := exec.LookPath(name)
	if err != nil {
		if goruntime.GOOS == "windows" {
			t.Fatal("required actual Windows PowerShell7.3+ gate is unavailable", err)
		}
		t.Skip("PowerShell unavailable locally; actual Windows gate remains pending")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	script := `if ($PSVersionTable.PSVersion -lt [version]'7.3') { throw 'PowerShell7.3+ required' }
$PSNativeCommandArgumentPassing = 'Legacy'
function m365crawl { & $env:M365CRAWL_DRAFT_TEST_EXE '-test.run=^TestDraftsPowerShellArgvHelper$' '--' @args; if ($LASTEXITCODE -ne 0) { throw 'native argv helper failed' } }
` + command + `
if ($PSNativeCommandArgumentPassing -ne 'Legacy') { exit 73 }`
	// Encode both wire directions so the host console cannot reinterpret Unicode before the argv assertion.
	var encoded []byte
	for _, unit := range utf16.Encode([]rune(script)) {
		encoded = binary.LittleEndian.AppendUint16(encoded, unit)
	}
	// #nosec G204 -- Real native argv is the assertion; only the fixed pwsh engine and owned synthetic commands are executed.
	cmd := exec.Command(shell, "-NoLogo", "-NoProfile", "-NonInteractive", "-EncodedCommand", base64.StdEncoding.EncodeToString(encoded))
	cmd.Env = append(os.Environ(), "M365CRAWL_DRAFT_ARGV_HELPER=1", "M365CRAWL_DRAFT_TEST_EXE="+exe, "REVIEW_ARCHIVE=expanded-wrong-archive", "DRAFT_PATH=expanded-wrong-archive", "TERM=dumb")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("actual %s shell/native argv roundtrip on %s: %v\n%s", shell, goruntime.GOOS, err, out)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if _, data, ok := strings.Cut(line, "DRAFT_ARGV:"); ok {
			payload, err := base64.StdEncoding.DecodeString(strings.TrimSpace(data))
			if err != nil {
				t.Fatal(err)
			}
			var args []string
			if err := json.Unmarshal(payload, &args); err != nil {
				t.Fatal(err)
			}
			return append([]string{"m365crawl"}, args...)
		}
	}
	t.Fatalf("actual shell did not execute native argv helper: %s", out)
	return nil
}

func draftFollowupWords(t *testing.T, command string) []string {
	t.Helper()
	if goruntime.GOOS == "windows" {
		return draftPowerShellWords(t, command)
	}
	return contentFollowupWords(t, command)
}

func TestDraftsPowerShellArgumentRoundTrip(t *testing.T) {
	id, revision := "teams-draft:"+strings.Repeat("a", 64), strings.Repeat("b", 64)
	for _, db := range []string{
		`C:\review\%REVIEW_ARCHIVE%\cached.db`,
		"C:\\review\\compose's $HOME ` %DRAFT_PATH% !DRAFT_PATH! ‘left’ ‚low‛ “double” & (1) 🌱.db",
	} {
		account := "tenant/user's $HOME ` %DRAFT_PATH% !DRAFT_PATH! ‘‚‛’ \"quoted\""
		command := draftInspectionCommand("windows", db, account, id, revision, true)
		got := draftPowerShellWords(t, command)
		want := []string{"m365crawl", "--db", db, "--no-sync", "--max-text", "0", "--account", account, "drafts", "show", id, "--body-revision", revision, "--include-removed"}
		if !slices.Equal(got, want) {
			t.Fatalf("actual %s shell/native argv changed selected context:\ngot %#v\nwant %#v", goruntime.GOOS, got, want)
		}
	}
	t.Logf("actual PowerShell/native-process argv roundtrip host=%s; non-Windows result is not Windows-host acceptance", goruntime.GOOS)
}

func TestDraftsRelativeArchiveFollowup(t *testing.T) {
	e := draftCLI(t)
	selected := e.db
	e.db = filepath.Base(selected)
	old := getwd
	defer func() { getwd = old }()
	calls := 0
	getwd = func() (string, error) {
		calls++
		if calls > 1 {
			return "", os.ErrInvalid
		}
		return filepath.Dir(selected), nil
	}
	code, out, stderr := draftRun(e, "drafts", "list")
	if code != 0 {
		t.Fatal(code, stderr)
	}
	rows := items(t, decode(t, out))
	if len(rows) != 1 || calls != 1 {
		t.Fatal("archive resolved more than once", calls, rows)
	}
	words := draftFollowupWords(t, rows[0]["full_inspection"].(string))
	found := false
	for i, word := range words {
		if word == "--db" && i+1 < len(words) {
			found = words[i+1] == selected
		}
	}
	if !found {
		t.Fatal("selected absolute archive lost", words)
	}
	getwd = func() (string, error) { return "", os.ErrInvalid }
	if code, _, _ := draftRun(e, "drafts", "list"); code == 0 {
		t.Fatal("unresolved relative archive admitted")
	}
}
func TestDraftsErrorAndOutputBranches(t *testing.T) {
	e := draftCLI(t)
	b := draftOne(t, e)
	id := b["id"].(string)
	e.exec(`update records set removed_at='2026-01-03'`)
	if code, _, err := draftRun(e, "drafts", "show", id); code == 0 || errorOf(t, err)["code"] != "teams_draft_removed" {
		t.Fatal(code, err)
	}
	code, out, err := draftRun(e, "--account", "tenant/user", "drafts", "show", id, "--include-removed")
	if code != 0 {
		t.Fatal(err)
	}
	row := items(t, decode(t, out))[0]
	if row["archive_presence"] != "removed" || !strings.Contains(row["full_inspection"].(string), "--include-removed") {
		t.Fatal(row)
	}
	for _, tc := range []struct {
		err  error
		code string
	}{{store.ErrTeamsDraftUnknown, "teams_draft_unknown"}, {store.ErrTeamsDraftRemoved, "teams_draft_removed"}, {store.ErrTeamsDraftAmbiguous, "teams_draft_ambiguous"}, {store.ErrTeamsDraftBodyRevisionChanged, "teams_draft_body_revision_changed"}, {store.ErrTeamsDraftScanIncomplete, "teams_draft_scan_incomplete"}} {
		var coded *errs.Coded
		if !errors.As(draftError(tc.err), &coded) || coded.Code != tc.code {
			t.Fatal(coded)
		}
	}
	if draftError(os.ErrPermission) == nil {
		t.Fatal("I/O error swallowed")
	}
	for _, args := range [][]string{{"drafts", "list", "--fields", "unknown"}, {"drafts", "show", id, "--body-revision", "bad"}} {
		if code, _, _ := draftRun(e, args...); code == 0 {
			t.Fatal(args)
		}
	}
	missing := newEnv(t)
	if code, _, _ := draftRun(missing, "drafts", "show", id); code == 0 {
		t.Fatal("missing archive show")
	}
	for at := 1; at <= 5; at++ {
		w := &attendeeFailWriter{at: at}
		r := &draftsResult{listResult: newList([]any{map[string]any{"text": "synthetic"}}, false)}
		if err := r.renderDrafts(&runtime{stdout: w}); err == nil {
			t.Fatal("write failure swallowed", at)
		}
	}
}

func draftCLI(t *testing.T) *env {
	t.Helper()
	e := newEnv(t)
	name := "compose's $HOME ` %DRAFT_PATH% !DRAFT_PATH! ‘left’ ‚low‛ “double” & (1) 🌱.db"
	e.db = filepath.Join(filepath.Dir(e.db), name)
	s, err := store.Open(context.Background(), e.db)
	if err != nil {
		t.Fatal(err)
	}
	u := map[string]any{"$undefined": true}
	c := map[string]any{"scheduledSendTime": nil, "isForwardedMessage": false, "forwardPrivateReply": false, "forwardAppIdsToInstall": "[]", "forwardMessageIds": "[]", "forwardMessageRecipients": "[]", "forwardMessageCardContents": "null", "forwardMessageCardsCount": nil, "forwardMessageOriginalConvId": nil, "forwardTeamId": nil}
	m := map[string]any{"id": "inner", "creator": "8:orgid:user", "type": "Message", "draftType": "RegularDraft", "threadType": "streamofdrafts", "messageType": "RichText/Html", "contentType": "Text", "state": 0, "isSentByCurrentUser": true, "content": "<p>synthetic draft 🌱 longer than eight</p>", "clientMessageId": "client", "lieType": "Send", "sendPipelineStatus": u, "sendPipelineOAMState": u, "streamingMetadata": u, "skypeeditedid": u, "originalNonLieMessage": u, "properties": map[string]any{"sendAt": u, "activity": map[string]any{"activityType": "Draft", "activitySubtype": "Draft", "sourceUserId": "8:orgid:user", "sourceThreadId": "target", "sourceMessageId": "message"}}, "draftDetails": map[string]any{"clientDraftProperties": c}}
	raw, err := json.Marshal(map[string]any{"conversationId": "wrapper", "replyChainId": "reply", "messageMap": map[string]any{"member": m}})
	if err != nil {
		t.Fatal(err)
	}
	x, err := s.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_, err = x.UpsertRecords("P|https_teams.microsoft.com_0", []teamsdesktop.GenericRecord{{Account: &teamsdesktop.Account{TenantID: "tenant", UserID: "user"}, Database: "Teams:streams-replychain-manager:react-web-client:tenant:user:en", Store: "drafts", KeyJSON: []byte(`["wrapper","reply"]`), ValueJSON: raw}}, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if err = x.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	return e
}
func draftRun(e *env, args ...string) (int, string, string) {
	var out, err bytes.Buffer
	code := Main(append([]string{"--db", e.db}, args...), &out, &err)
	return code, out.String(), err.String()
}
func draftList(t *testing.T, e *env, extra ...string) map[string]any {
	t.Helper()
	code, out, err := draftRun(e, append([]string{"drafts", "list"}, extra...)...)
	if code != 0 {
		t.Fatalf("drafts list exit%d: %s", code, err)
	}
	return decode(t, out)
}
func draftOne(t *testing.T, e *env) map[string]any {
	t.Helper()
	rows := items(t, draftList(t, e))
	if len(rows) != 1 {
		t.Fatalf("want own body, got %v", rows)
	}
	return rows[0]
}
func TestDraftsArchiveOnly(t *testing.T) {
	e := draftCLI(t)
	for _, age := range []string{"0", "1ns", "15m"} {
		code, out, err := draftRun(e, "--max-age", age, "drafts", "list")
		if code != 0 || !strings.Contains(out, "synthetic draft") {
			t.Fatal(code, out, err)
		}
	}
	missing := newEnv(t)
	doc := draftList(t, missing)
	if doc["draft_coverage"].(map[string]any)["archive_state"] != "archive_missing" {
		t.Fatal(doc)
	}
	if _, err := os.Stat(missing.db); !os.IsNotExist(err) {
		t.Fatal("read created archive", err)
	}
}
func TestDraftsExplicitFlags(t *testing.T) {
	e := draftCLI(t)
	for _, flag := range []string{"--teams-root", "--outlook-root", "--outlook-account", "--outlook-profile"} {
		if code, _, _ := draftRun(e, flag+"=", "drafts", "list"); code == 0 {
			t.Fatal("explicit acquisition flag", flag)
		}
	}
	for _, name := range []string{"M365CRAWL_TEAMS_ROOT", "M365CRAWL_OUTLOOK_ROOT", "M365CRAWL_OUTLOOK_ACCOUNT", "M365CRAWL_OUTLOOK_PROFILE"} {
		t.Setenv(name, "/never/read/this")
	}
	if code, _, err := draftRun(e, "drafts", "list"); code != 0 {
		t.Fatal(err)
	}
	b := draftOne(t, e)
	id := b["id"].(string)
	for _, args := range [][]string{{"--max-age", "bad", "drafts", "list"}, {"--max-text", "-1", "drafts", "list"}, {"drafts", "list", "--limit", "0"}, {"drafts", "show", "bad"}, {"--fields=", "drafts", "show", id}, {"drafts", "show", id, "--revision", strings.Repeat("0", 64)}} {
		if code, _, _ := draftRun(e, args...); code == 0 {
			t.Fatal(args)
		}
	}
	if code, out, err := draftRun(e, "--max-text", "0", "drafts", "show", id); code != 0 || !strings.Contains(out, "longer than eight") {
		t.Fatal(code, out, err)
	}
}

func TestDraftsShowRejectsNonemptyFieldsBeforeArchive(t *testing.T) {
	e := newEnv(t)
	t.Setenv("PATH", "") // This guard needs neither a shell nor an existing archive.
	id := "teams-draft:" + strings.Repeat("a", 64)
	for _, args := range [][]string{
		{"--fields", "id", "drafts", "show", id},
		{"--fields=id", "drafts", "show", id},
		{"drafts", "show", id, "--fields", "id"},
	} {
		code, out, stderr := draftRun(e, args...)
		body := errorOf(t, stderr)
		if code != errs.ExitUsage || out != "" || body["code"] != "usage" || body["message"] != "--fields is list-only, not drafts show" {
			t.Fatalf("nonempty show fields must fail before archive lookup: args=%v exit=%d out=%q error=%s", args, code, out, stderr)
		}
		if strings.Contains(stderr, id) {
			t.Fatal("list-only usage error disclosed requested ID", stderr)
		}
	}
	if _, err := os.Stat(e.db); !os.IsNotExist(err) {
		t.Fatal("flag validation created or opened an archive", err)
	}
}

func TestDraftsScopeErrorParity(t *testing.T) {
	e := draftCLI(t)
	id := draftOne(t, e)["id"].(string)
	var codes []any
	for _, target := range []string{id, "teams-draft:" + strings.Repeat("0", 64)} {
		code, _, err := draftRun(e, "--account", "other/user", "drafts", "show", target)
		if code == 0 {
			t.Fatal("foreign body")
		}
		codes = append(codes, errorOf(t, err)["code"])
	}
	if codes[0] != codes[1] || codes[0] != "teams_draft_unknown" {
		t.Fatal(codes)
	}
}
func TestDraftsExplicitEmptyBodyRevision(t *testing.T) {
	e := draftCLI(t)
	id := draftOne(t, e)["id"].(string)
	for _, args := range [][]string{
		{"drafts", "show", id, "--body-revision", ""},
		{"drafts", "show", id, "--body-revision="},
	} {
		if code, out, err := draftRun(e, args...); code == 0 || !strings.Contains(err, "full SHA256") || strings.Contains(out, "synthetic draft") {
			t.Fatal("explicit empty guard became unguarded read", code, out, err)
		}
	}
}
func TestDraftsProjectionCoverage(t *testing.T) {
	e := draftCLI(t)
	doc := draftList(t, e, "--fields", "id")
	if len(items(t, doc)[0]) != 1 || doc["draft_coverage"] == nil {
		t.Fatal(doc)
	}
	if doc["draft_coverage"].(map[string]any)["acquisition"] != "none" {
		t.Fatal("acquisition policy not explicit", doc)
	}
	for _, format := range []string{"json", "text", "log"} {
		code, out, err := draftRun(e, "--format", format, "--max-text", "8", "drafts", "list")
		if code != 0 || !strings.Contains(out, "cached") {
			t.Fatal(code, out, err)
		}
	}
	id := draftOne(t, e)["id"].(string)
	code, out, err := draftRun(e, "--max-text", "8", "drafts", "show", id)
	if code != 0 {
		t.Fatal(err)
	}
	b := items(t, decode(t, out))[0]
	if len([]rune(b["text"].(string))) != 8 || b["text_truncated"] != true || b["target_guarded"] != false {
		t.Fatal(b)
	}
}
func TestDraftsTextListLimits(t *testing.T) {
	e := draftCLI(t)
	e.exec(`insert into records(source,tenant_id,user_id,database,store,key_json,value_json,content_hash,first_seen_at,updated_at)
		select 'Q|https_teams.microsoft.com_0',tenant_id,user_id,database,store,key_json,value_json,content_hash,first_seen_at,updated_at from records`)
	for _, tc := range []struct {
		account, count, truncated, total string
	}{{"tenant/user", `"count":1`, `"truncated":true`, `"total_exact":2`}, {"other/user", `"count":0`, `"truncated":false`, `"total_exact":0`}} {
		code, out, err := draftRun(e, "--format", "text", "--account", tc.account, "--fields", "id", "drafts", "list", "--limit", "1")
		if code != 0 || !strings.Contains(out, tc.count) || !strings.Contains(out, tc.truncated) || !strings.Contains(out, tc.total) || !strings.Contains(out, "draft_coverage") {
			t.Fatal(code, out, err)
		}
	}
}
func TestDraftsGuardedFollowupReplay(t *testing.T) {
	e := draftCLI(t)
	b := draftOne(t, e)
	cmd := b["full_inspection"].(string)
	words := draftFollowupWords(t, cmd)
	if !strings.Contains(cmd, "--no-sync") || !strings.Contains(cmd, "--max-text 0") || !strings.Contains(cmd, "--body-revision") {
		t.Fatal(cmd)
	}
	var out, err bytes.Buffer
	if code := Main(words[1:], &out, &err); code != 0 {
		t.Fatal(code, err.String())
	}
	shown := items(t, decode(t, out.String()))[0]
	if shown["body_revision"] != b["body_revision"] || shown["text"] != b["text"] {
		t.Fatal(shown, b)
	}
}
func TestDraftsTargetOutsideGuard(t *testing.T) {
	e := draftCLI(t)
	b := draftOne(t, e)
	if b["body_guard_scope"] != "retained_wrapper_body_frame_only" || b["target_guarded"] != false || b["unsent_now"] != "unknown" {
		t.Fatal(b)
	}
	e.exec(`insert into records(source,tenant_id,user_id,database,store,key_json,value_json,content_hash,first_seen_at,updated_at) values('P|https_teams.microsoft.com_0','tenant','user','Teams:messaging-slice-manager:react-web-client:tenant:user:fr','drafts-metadata-items','"meta"','{"sourceThreadId":"target","sourceMessageId":"message","clientMessageId":"client","conversationType":"Space"}','metadata','a','b')`)
	e.exec(`insert into conversations(tenant_id,user_id,id,kind,title,team_id,updated_at) values('tenant','user','target','Space','old title','team','a')`)
	for _, title := range []string{"old title", "new title"} {
		e.exec(`update conversations set title='` + title + `'`)
		code, out, err := draftRun(e, "drafts", "show", b["id"].(string), "--body-revision", b["body_revision"].(string))
		if code != 0 {
			t.Fatal(err)
		}
		row := items(t, decode(t, out))[0]
		association := row["association"].(map[string]any)
		if row["body_revision"] != b["body_revision"] || row["target_guarded"] != false || association["title"] != title || association["target_state"] != "resolved" {
			t.Fatal(row)
		}
	}
}
func TestDraftsSelectedAccountShape(t *testing.T) {
	e := draftCLI(t)
	doc := draftList(t, e, "--account", "tenant/user")
	account := doc["draft_coverage"].(map[string]any)["selected_account"].(map[string]any)
	if account["tenant_id"] != "tenant" || account["user_id"] != "user" {
		t.Fatal(account)
	}
}
func TestDraftsHelpAndGuide(t *testing.T) {
	for _, args := range [][]string{{"drafts", "--help"}, {"drafts", "list", "--help"}, {"drafts", "show", "--help"}} {
		var out, err bytes.Buffer
		if code := Main(args, &out, &err); code != 0 || !strings.Contains(out.String(), "cached") || !strings.Contains(out.String(), "--max-text 0") {
			t.Fatal(code, out.String(), err.String())
		}
	}
}
func TestDraftsNotSharedMessages(t *testing.T) {
	e := draftCLI(t)
	_ = draftOne(t, e)
	for _, args := range [][]string{{"--no-sync", "messages"}, {"--no-sync", "search", "synthetic draft"}} {
		code, out, err := draftRun(e, args...)
		if code != 0 {
			t.Fatal(err)
		}
		if strings.Contains(out, "synthetic draft 🌱") {
			t.Fatal("compose body leaked into shared output")
		}
	}
}

func TestDraftsBodyAbsenceAndPreviewPolicy(t *testing.T) {
	e := draftCLI(t)
	e.exec(`update records set value_json=json_remove(value_json,'$.messageMap.member.content')`)
	row := draftOne(t, e)
	if row["body_state"] != "missing" || row["text"] != nil {
		t.Fatal(row)
	}
	body := strings.Repeat("🌱", 400)
	e.exec(`update records set value_json=json_set(value_json,'$.messageMap.member.content','` + body + `')`)
	row = draftOne(t, e)
	if len([]rune(row["text"].(string))) != 300 || row["text_truncated"] != true {
		t.Fatal(row)
	}
	full := items(t, draftList(t, e, "--max-text", "0"))[0]
	if full["text"] != body || full["text_truncated"] != false {
		t.Fatal(full)
	}
	e.exec(`insert into schema_migrations(version) values(999)`)
	if code, _, _ := draftRun(e, "drafts", "list"); code == 0 {
		t.Fatal("newer schema accepted")
	}
}

func TestDraftsSelectedArchiveQuiescentFixture(t *testing.T) {
	e := draftRollbackFixture(t)
	assertDraftReadFixture(t, e, true)
}

func TestDraftsSelectedArchiveWALSidecars(t *testing.T) {
	e := draftCLI(t)
	assertDraftReadFixture(t, e, false)
}

func draftRollbackFixture(t *testing.T) *env {
	t.Helper()
	e := draftCLI(t)
	seed := e.db
	e.db = filepath.Join(filepath.Dir(seed), "rollback-compose.db")
	db, err := sql.Open("sqlite", e.db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	// Construct a separate authored fixture in SQLite's default rollback mode.
	// Never reset a frozen input's journal, permissions or reader configuration.
	if _, err = db.Exec(`attach database ? as seed`, seed); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"schema_migrations", "records", "conversations", "sync_runs", "meta",
		"workiq_accounts", "workiq_attempts", "workiq_frontiers", "workiq_pages",
		"workiq_rows", "workiq_aliases", "workiq_source_keys", "workiq_folders",
		"workiq_censuses", "workiq_census_members", "workiq_gaps", "workiq_mail_receipts"} {
		if _, err = db.Exec("create table main." + table + " as select * from seed." + table); err != nil {
			t.Fatal(err)
		}
	}
	var mode string
	if err = db.QueryRow(`pragma main.journal_mode`).Scan(&mode); err != nil || mode != "delete" {
		t.Fatal("unsuitable new fixture journal mode", mode, err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	return e
}

func assertDraftReadFixture(t *testing.T, e *env, invariantSidecars bool) {
	t.Helper()
	// Closed synthetic fixture: no writer and no WAL/SHM. This is not a
	// universal native-WAL sidecar immutability assertion (PL-M1).
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Stat(e.db + suffix); !os.IsNotExist(err) {
			if err != nil {
				t.Fatal(err)
			}
			t.Fatal("fixture is not sidecar-free")
		}
	}
	before, err := os.Stat(e.db)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Clean(e.db))
	if err != nil {
		t.Fatal(err)
	}
	_ = draftList(t, e)
	after, err := os.Stat(e.db)
	if err != nil {
		t.Fatal(err)
	}
	now, err := os.ReadFile(filepath.Clean(e.db))
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) || !bytes.Equal(raw, now) || before.Mode() != after.Mode() || !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("archive changed")
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		_, err := os.Stat(e.db + suffix)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		t.Logf("closed synthetic fixture post-read %s present=%t", suffix, err == nil)
		if invariantSidecars && err == nil {
			t.Fatal("chosen synthetic fixture is not sidecar-invariant", suffix)
		}
	}
}
