package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openclaw/crawlkit/output"

	da "github.com/ourostack/m365crawl/internal/directedactions"
	"github.com/ourostack/m365crawl/internal/directedwrites"
	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/teamsdesktop"
)

type fakeTerm struct {
	in        *strings.Reader
	out       bytes.Buffer
	failWrite bool
}

func (f *fakeTerm) Read(p []byte) (int, error) { return f.in.Read(p) }
func (f *fakeTerm) Write(p []byte) (int, error) {
	if f.failWrite {
		return 0, errors.New("terminal gone")
	}
	return f.out.Write(p)
}
func (f *fakeTerm) Close() error { return nil }

// useTerminal makes the controlling terminal a fake on which the person types typed.
func useTerminal(t *testing.T, typed string) *fakeTerm {
	t.Helper()
	term := &fakeTerm{in: strings.NewReader(typed)}
	old := openTerminal
	openTerminal = func() (io.ReadWriteCloser, error) { return term, nil }
	t.Cleanup(func() { openTerminal = old })
	return term
}

func noTerminal(t *testing.T) {
	t.Helper()
	old := openTerminal
	openTerminal = func() (io.ReadWriteCloser, error) { return nil, errors.New("no tty") }
	t.Cleanup(func() { openTerminal = old })
}

// writesEnv is an archive that reads the synthetic WorkIQ user's Teams account, with WorkIQ on.
func writesEnv(t *testing.T) (*env, string) {
	t.Helper()
	e := newEnv(t)
	e.emptyArchive()
	st, err := store.Open(context.Background(), e.db)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.ApplyAccount(context.Background(), teamsdesktop.Account{TenantID: "synthetic-tenant", UserID: "synthetic-user"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetWorkIQEnabled(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	_ = st.Close()
	transcript := workIQCLITransport(t, e.db)
	t.Setenv("WORKIQ_WRITES_STATE", filepath.Join(t.TempDir(), "writes.json"))
	t.Setenv("WORKIQ_WRITES_MODE", "")
	return e, transcript
}

const synthetic = "test.user@example.invalid"

func sendArgs(text string, extra ...string) []string {
	return append([]string{"--json", "writes", "send-teams-message", "--chat", "48:notes", "--to", synthetic,
		"--identity", synthetic, "--agent", "test-agent", "--reason", "the person asked", "--text", text}, extra...)
}

func writeErrorCode(t *testing.T, stderr string) string {
	t.Helper()
	var doc struct{ Error struct{ Code string } }
	if err := json.Unmarshal([]byte(stderr), &doc); err != nil {
		t.Fatalf("stderr is not one error line: %q", stderr)
	}
	return doc.Error.Code
}

func TestWritesThroughTheSyntheticWorkIQ(t *testing.T) {
	e, transcript := writesEnv(t)

	code, out, stderr := e.run("--json", "writes", "status")
	if code != 0 || stderr != "" || decode(t, out)["writes"] != "off" {
		t.Fatal(code, out, stderr)
	}
	code, out, stderr = e.run(sendArgs("hello")...)
	if doc := decode(t, out); code != 3 || doc["code"] != "writes_off" || doc["tell_person"] != true || writeErrorCode(t, stderr) != "write_person_needed" {
		t.Fatal(code, out, stderr)
	}
	if raw, _ := os.ReadFile(transcript); len(raw) != 0 { //nolint:gosec // G304: own synthetic transcript.
		t.Fatal("WorkIQ started while writes were off")
	}

	term := useTerminal(t, "on\n")
	if code, out, stderr = e.run("--json", "writes", "on"); code != 0 || decode(t, out)["writes"] != "on" || !strings.Contains(term.out.String(), "Type on") {
		t.Fatal(code, out, stderr)
	}

	code, out, stderr = e.run("--json", "writes", "target", "--chat", "48:notes")
	target := decode(t, out)
	if code != 0 || target["known"] != true || target["version"] != "1000" || target["identity"] != synthetic ||
		target["recipients"].([]any)[0] != synthetic {
		t.Fatal(code, out, stderr)
	}

	code, out, stderr = e.run(sendArgs("hello", "--intent-key", "k1", "--expected-version", "1000")...)
	sent := decode(t, out)
	if code != 0 || stderr != "" || sent["decision"] != "accept" || sent["outcome"] != "sent" || sent["verification"] != "found" ||
		sent["exit_class"] != "done" || sent["request_id"] != "1" || sent["server_id"] != "1700000000001" {
		t.Fatal(code, out, stderr)
	}
	code, out, _ = e.run(sendArgs("hello", "--intent-key", "k1", "--reason", "retry")...)
	if replay := decode(t, out); code != 0 || replay["decision"] != "replay" || replay["replay_of"] != "1" {
		t.Fatal(code, out)
	}
	code, out, stderr = e.run(sendArgs("other", "--intent-key", "k1")...)
	if code != 2 || decode(t, out)["code"] != "intent_key_reused" || writeErrorCode(t, stderr) != "write_request_problem" {
		t.Fatal(code, out, stderr)
	}
	code, out, _ = e.run(sendArgs("later", "--expected-version", "1000")...)
	if code != 2 || decode(t, out)["code"] != "stale_version" {
		t.Fatal(code, out)
	}

	old := stdinReader
	stdinReader = strings.NewReader("draft body\n")
	t.Cleanup(func() { stdinReader = old })
	code, out, stderr = e.run("--json", "writes", "save-mail-draft", "--subject", "Plan", "--to", "friend@example.invalid",
		"--agent", "test-agent", "--reason", "asked", "--text-file", "-")
	if draft := decode(t, out); code != 0 || draft["outcome"] != "sent" || draft["effect"] != "only_you" || draft["server_id"] != "draft-1700000000002" {
		t.Fatal(code, out, stderr)
	}

	t.Setenv("WORKIQ_WRITES_MODE", "reject")
	code, out, stderr = e.run(sendArgs("rejected text")...)
	if code != 1 || decode(t, out)["outcome"] != "failed" || writeErrorCode(t, stderr) != "write_failed" || strings.Contains(stderr+out, "Synthetic private") {
		t.Fatal(code, out, stderr)
	}

	t.Setenv("WORKIQ_WRITES_MODE", "lost")
	code, out, stderr = e.run(sendArgs("lost text")...)
	lost := decode(t, out)
	if code != 5 || lost["outcome"] != "unknown" || lost["tell_person"] != true || writeErrorCode(t, stderr) != "write_outcome_unknown" {
		t.Fatal(code, out, stderr)
	}
	id := lost["request_id"].(string)
	code, out, _ = e.run(sendArgs("lost text")...)
	if code != 3 || decode(t, out)["code"] != "outcome_unknown" {
		t.Fatal("an unknown write was resent", code, out)
	}
	t.Setenv("WORKIQ_WRITES_MODE", "")
	code, out, _ = e.run("--json", "writes", "verify", id)
	if code != 5 || decode(t, out)["outcome"] != "unknown" {
		t.Fatal(code, out)
	}
	code, out, _ = e.run("--json", "writes", "verify", "1")
	if code != 0 || decode(t, out)["outcome"] != "sent" {
		t.Fatal(code, out)
	}

	useTerminal(t, "wrong\n")
	if code, _, stderr = e.run("--json", "writes", "settle", id, "--sent"); code != 3 || writeErrorCode(t, stderr) != "write_terminal_required" {
		t.Fatal(code, stderr)
	}
	useTerminal(t, "1\n")
	if code, _, stderr = e.run("--json", "writes", "settle", "1", "--sent"); code != 2 || !strings.Contains(stderr, "already sent") {
		t.Fatal(code, stderr)
	}
	noTerminal(t)
	code, _, stderr = e.run("--json", "writes", "settle", id, "--not-sent")
	if code != 3 || writeErrorCode(t, stderr) != "write_terminal_required" {
		t.Fatal(code, stderr)
	}
	useTerminal(t, id+"\n")
	code, out, stderr = e.run("--json", "writes", "settle", id, "--not-sent")
	if settled := decode(t, out); code != 0 || settled["outcome"] != "failed" || settled["verification"] != "person" {
		t.Fatal(code, out, stderr)
	}
	if code, out, _ = e.run(sendArgs("lost text")...); code != 0 || decode(t, out)["outcome"] != "sent" {
		t.Fatal("a write settled as not sent stayed blocked", code, out)
	}

	code, out, _ = e.run("--json", "writes", "status", "--limit", "2")
	status := decode(t, out)
	if code != 0 || status["writes"] != "on" || status["unknown"] != float64(0) || len(status["items"].([]any)) != 2 {
		t.Fatal(code, out)
	}
	useTerminal(t, "off\n")
	if code, out, _ = e.run("--json", "writes", "off"); code != 0 || decode(t, out)["writes"] != "off" {
		t.Fatal(code, out)
	}

	raw, err := os.ReadFile(transcript) //nolint:gosec // G304: own synthetic transcript.
	if err != nil {
		t.Fatal(err)
	}
	creates := 0
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var entry struct {
			Tool, Path string
			Pinned     bool
		}
		if json.Unmarshal([]byte(line), &entry) != nil || !entry.Pinned && entry.Tool == "create_entity" {
			t.Fatal("an unpinned or unreadable call:", line)
		}
		if entry.Tool == "create_entity" {
			creates++
			if entry.Path != "/chats/48:notes/messages" && entry.Path != "/me/messages" {
				t.Fatal("a write outside the two fixed paths:", entry.Path)
			}
		}
	}
	if creates != 5 { // hello, the draft, the rejected text, the lost text, the lost text after settling
		t.Fatal("create calls", creates)
	}
}

func TestWritesTextOutput(t *testing.T) {
	e, _ := writesEnv(t)
	useTerminal(t, "on\n")
	if code, out, stderr := e.run("--format", "text", "writes", "on"); code != 0 || out != "writes: on\n" {
		t.Fatal(code, out, stderr)
	}
	args := sendArgs("hello\u202e there")[1:]
	code, out, _ := e.run(append([]string{"--format", "text"}, args...)...)
	if code != 2 || out != "send_teams_message refused: invalid_request (content.text); nothing was sent\n" {
		t.Fatal(code, out)
	}
	code, out, _ = e.run(append([]string{"--format", "text"}, sendArgs("hello", "--intent-key", "k")[1:]...)...)
	if code != 0 || out != "write 1 send_teams_message: sent\n  server id 1700000000001\n" {
		t.Fatal(code, out)
	}
	code, out, _ = e.run(append([]string{"--format", "text"}, sendArgs("hello", "--intent-key", "k")[1:]...)...)
	if code != 0 || out != "send_teams_message replayed write 1 (sent); nothing was sent again\n" {
		t.Fatal(code, out)
	}
	code, out, _ = e.run("--format", "text", "writes", "save-mail-draft", "--agent", "a", "--reason", "r", "--text", "body")
	if code != 0 || out != "write 2 save_mail_draft: draft saved\n  server id draft-1700000000002\n" {
		t.Fatal(code, out)
	}
	code, out, _ = e.run("--format", "text", "writes", "target", "--chat", "48:notes")
	if code != 0 || out != "chat 48:notes as test.user@example.invalid\nrecipients: test.user@example.invalid\nversion: 1700000000001\n" {
		t.Fatal(code, out)
	}
	code, out, _ = e.run("--format", "text", "writes", "target", "--chat", "48:saved")
	if code != 0 || out != "chat 48:saved: not a chat a write can name (unknown_target)\n" {
		t.Fatal(code, out)
	}
	code, out, _ = e.run("--format", "text", "writes", "status")
	if code != 0 || !strings.HasPrefix(out, "writes: on; unknown outcomes: 0\n2 ") || !strings.Contains(out, "save_mail_draft sent (verified) by a: r\n  body\n") ||
		!strings.Contains(out, "  chat 48:notes\n  hello\n") {
		t.Fatal(code, out)
	}
	t.Setenv("WORKIQ_WRITES_MODE", "lost")
	code, out, _ = e.run(append([]string{"--format", "text"}, sendArgs("gone")[1:]...)...)
	if code != 5 || out != "write 3 send_teams_message: outcome unknown: do not send it again\n" {
		t.Fatal(code, out)
	}
}

func TestWritesUsageAndTerminalRules(t *testing.T) {
	e, _ := writesEnv(t)
	for _, args := range [][]string{
		{"writes", "status", "--limit", "0"},
		{"writes", "verify", "x"},
		{"writes", "verify", "0"},
		{"writes", "settle", "1"},
		{"writes", "settle", "1", "--sent", "--not-sent"},
		{"writes", "settle", "9", "--sent"},
		{"writes", "save-mail-draft", "--text", "a", "--text-file", "b"},
		{"writes", "send-teams-message", "--text", "a", "--text-file", "b"},
		{"writes", "settle", "x", "--sent"},
		{"writes", "save-mail-draft", "--text-file", filepath.Join(t.TempDir(), "missing")},
	} {
		if code, _, stderr := e.run(append([]string{"--json"}, args...)...); code != 2 || writeErrorCode(t, stderr) != "usage" {
			t.Fatal(args, code, stderr)
		}
	}
	file := filepath.Join(t.TempDir(), "text.txt")
	if err := os.WriteFile(file, []byte("from a file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, out, _ := e.run("--json", "writes", "save-mail-draft", "--agent", "a", "--reason", "r", "--text-file", file); code != 3 || decode(t, out)["code"] != "writes_off" {
		t.Fatal(code, out)
	}
	for _, tc := range []struct {
		typed string
		fail  bool
	}{{"yes\n", false}, {"", false}, {"on\n", true}} {
		term := useTerminal(t, tc.typed)
		term.failWrite = tc.fail
		if code, _, stderr := e.run("--json", "writes", "on"); code != 3 || writeErrorCode(t, stderr) != "write_terminal_required" {
			t.Fatal(tc, code, stderr)
		}
	}
	if directedwrites.ReadSwitch(e.db).On {
		t.Fatal("a refused confirmation turned writes on")
	}
	noTerminal(t)
	if code, _, stderr := e.run("--json", "writes", "off"); code != 3 || writeErrorCode(t, stderr) != "write_terminal_required" {
		t.Fatal(code, stderr)
	}
	other := newEnv(t)
	useTerminal(t, "on\n")
	if code, _, stderr := other.run("--json", "writes", "on"); code != 2 || writeErrorCode(t, stderr) != "usage" {
		t.Fatal("turned writes on without an archive", code, stderr)
	}
	if err := os.Mkdir(directedwrites.SwitchPath(e.db)+".tmp", 0o700); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := e.run("--json", "writes", "on"); code == 0 || writeErrorCode(t, stderr) != "db_error" {
		t.Fatal(code, stderr)
	}
}

func TestWritesReportErrorsBeforeAnyDecision(t *testing.T) {
	e, _ := writesEnv(t)
	broken := errs.Usage("synthetic failure")
	old := sendDirected
	sendDirected = func(context.Context, directedwrites.Options, da.Request) (directedwrites.Result, error) {
		return directedwrites.Result{}, broken
	}
	t.Cleanup(func() { sendDirected = old })
	if code, out, stderr := e.run(sendArgs("x")...); code != 2 || out != "" || !strings.Contains(stderr, "synthetic failure") {
		t.Fatal(code, out, stderr)
	}
	if err := os.Remove(e.db); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"writes", "target", "--chat", "48:notes"}, {"writes", "verify", "1"}} {
		if code, out, stderr := e.run(append([]string{"--json"}, args...)...); code == 0 || out != "" || stderr == "" {
			t.Fatal(args, code, out, stderr)
		}
	}
	if err := os.WriteFile(e.db, []byte("not a database"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := e.run("--json", "writes", "status"); code == 0 || stderr == "" {
		t.Fatal(code, stderr)
	}
	useTerminal(t, "1\n")
	if code, _, stderr := e.run("--json", "writes", "settle", "1", "--sent"); code == 0 || stderr == "" {
		t.Fatal(code, stderr)
	}
}

func TestWritesOutcomeErrorsAndSafeText(t *testing.T) {
	if outcomeError(directedwrites.Result{ExitClass: da.ExitDone}) != nil {
		t.Fatal("done is not an error")
	}
	var coded *errs.Coded
	if err := outcomeError(directedwrites.Result{ExitClass: da.ExitUnknown, RequestID: "7"}); !errors.As(err, &coded) || coded.Exit != 5 || !strings.Contains(coded.Fix, "writes verify 7") {
		t.Fatal(err)
	}
	if err := outcomeError(directedwrites.Result{ExitClass: da.ExitUnknown, ReplayOf: "3"}); !errors.As(err, &coded) || !strings.Contains(coded.Fix, "writes verify 3") {
		t.Fatal("a replayed unknown write lost its id", err)
	}
	if got := safeLine("a\nb\tc\x1b[31m\u202e\u200bz\U000E0041"); got != `a b c\u001b[31m\u202e\u200bz\ue0041` {
		t.Fatalf("%q", got)
	}
	var out bytes.Buffer
	rt := &runtime{stdout: &out}
	r := writesOutcome{directedwrites.Result{Action: "send_teams_message", RequestID: "4", Outcome: da.Sent, Notice: "the write log could not be saved"}}
	if err := r.renderWrites(rt); err != nil || out.String() != "write 4 send_teams_message: sent\n  note: the write log could not be saved\n" {
		t.Fatal(out.String(), err)
	}
}

func TestWindowsConsoleReadsAndWritesSeparateHandles(t *testing.T) {
	dir := t.TempDir()
	in, err := os.Create(filepath.Join(dir, "in")) //nolint:gosec // G304: test file in t.TempDir.
	if err != nil {
		t.Fatal(err)
	}
	if _, err := in.WriteString("typed\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := in.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	out, err := os.Create(filepath.Join(dir, "out")) //nolint:gosec // G304: test file in t.TempDir.
	if err != nil {
		t.Fatal(err)
	}
	c := console{in: in, out: out}
	buf := make([]byte, 6)
	if n, err := c.Read(buf); err != nil || string(buf[:n]) != "typed\n" {
		t.Fatal(n, err)
	}
	if _, err := c.Write([]byte("prompt")); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(filepath.Join(dir, "out")); string(raw) != "prompt" { //nolint:gosec // G304: test file in t.TempDir.
		t.Fatal(string(raw))
	}
}

func TestWritesOutcomeThatCannotBePrinted(t *testing.T) {
	rt := newRuntime(context.Background(), &Globals{}, failingWriter{}, &bytes.Buffer{})
	rt.format = output.JSON
	if err := rt.writeOutcome(writesOutcome{directedwrites.Result{ExitClass: da.ExitUnknown}}); err == nil || !strings.Contains(err.Error(), "pipe closed") {
		t.Fatal(err)
	}
}

func TestTheConfirmationDoesNotWaitForever(t *testing.T) {
	reader, writer := io.Pipe()
	t.Cleanup(func() { _ = writer.Close() })
	old, oldWait := openTerminal, confirmWait
	openTerminal = func() (io.ReadWriteCloser, error) {
		return struct {
			io.Reader
			io.Writer
			io.Closer
		}{reader, io.Discard, reader}, nil
	}
	confirmWait = 20 * time.Millisecond
	t.Cleanup(func() { openTerminal, confirmWait = old, oldWait })
	err := confirmOnTerminal("Type on: ", "on")
	var coded *errs.Coded
	if !errors.As(err, &coded) || coded.Code != "write_terminal_required" || !strings.Contains(coded.Message, "in time") {
		t.Fatal(err)
	}
}

func TestReplyMailThroughTheSyntheticWorkIQ(t *testing.T) {
	e, transcript := writesEnv(t)
	useTerminal(t, "on\n")
	if code, _, stderr := e.run("--json", "writes", "on"); code != 0 {
		t.Fatal(code, stderr)
	}
	const self = "<self@example.invalid>"
	code, out, stderr := e.run("--json", "writes", "target", "--message", self)
	target := decode(t, out)
	if code != 0 || target["known"] != true || target["version"] != "ck-1" || target["message"] != self || target["recipients"].([]any)[0] != synthetic {
		t.Fatal(code, out, stderr)
	}
	replyArgs := func(text string, extra ...string) []string {
		return append([]string{"--json", "writes", "reply-mail", "--message", self, "--to", synthetic, "--identity", synthetic,
			"--agent", "test-agent", "--reason", "the person asked", "--text", text}, extra...)
	}
	code, out, stderr = e.run(replyArgs("Thanks & bye", "--intent-key", "r1", "--expected-version", "ck-1")...)
	sent := decode(t, out)
	if code != 0 || stderr != "" || sent["outcome"] != "sent" || sent["verification"] != "found" || sent["server_id"] != "sent-reply-1" {
		t.Fatal(code, out, stderr)
	}
	if code, out, _ = e.run(replyArgs("Thanks & bye", "--intent-key", "r1")...); code != 0 || decode(t, out)["decision"] != "replay" {
		t.Fatal(code, out)
	}
	if code, out, _ = e.run(replyArgs("x", "--to", "other@example.invalid")...); code != 2 || decode(t, out)["code"] != "recipients_changed" {
		t.Fatal(code, out)
	}
	t.Setenv("WORKIQ_WRITES_MODE", "hidden")
	code, out, stderr = e.run(replyArgs("Second reply")...)
	hidden := decode(t, out)
	if code != 5 || hidden["outcome"] != "unknown" || writeErrorCode(t, stderr) != "write_outcome_unknown" {
		t.Fatal(code, out, stderr)
	}
	id := hidden["request_id"].(string)
	if code, out, _ = e.run("--json", "writes", "verify", id); code != 5 {
		t.Fatal(code, out)
	}
	t.Setenv("WORKIQ_WRITES_MODE", "")
	if code, out, _ = e.run("--json", "writes", "verify", id); code != 0 || decode(t, out)["outcome"] != "sent" {
		t.Fatal(code, out)
	}
	code, out, _ = e.run("--format", "text", "writes", "status")
	if code != 0 || !strings.Contains(out, "  reply to <self@example.invalid>\n") {
		t.Fatal(code, out)
	}
	code, out, _ = e.run("--format", "text", "writes", "target", "--message", self)
	if code != 0 || out != "mail <self@example.invalid> as test.user@example.invalid\nrecipients: test.user@example.invalid\nversion: ck-1\n" {
		t.Fatal(code, out)
	}
	code, out, _ = e.run("--format", "text", "writes", "target", "--message", "nope")
	if code != 0 || out != "mail nope: not a mail a write can name (unknown_target)\n" {
		t.Fatal(code, out)
	}
	for _, args := range [][]string{{"writes", "target"}, {"writes", "target", "--chat", "48:notes", "--message", self}, {"writes", "reply-mail", "--text", "a", "--text-file", "b"}} {
		if code, _, stderr := e.run(append([]string{"--json"}, args...)...); code != 2 || writeErrorCode(t, stderr) != "usage" {
			t.Fatal(args, code, stderr)
		}
	}
	raw, err := os.ReadFile(transcript) //nolint:gosec // G304: own synthetic transcript.
	if err != nil {
		t.Fatal(err)
	}
	actions := 0
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var entry struct {
			Tool, Path string
			Pinned     bool
		}
		if json.Unmarshal([]byte(line), &entry) != nil || entry.Tool == "create_entity" {
			t.Fatal("unexpected call:", line)
		}
		if entry.Tool == "do_action" {
			actions++
			if !entry.Pinned || entry.Path != "/me/messages/inbox-self/createReply" && !strings.HasPrefix(entry.Path, "/me/messages/reply-") {
				t.Fatal("a write outside the reply paths:", line)
			}
		}
	}
	if actions != 4 { // two replies, each a createReply and a send
		t.Fatal("actions", actions)
	}
}
