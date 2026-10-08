package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/outlookmail"
	"github.com/ourostack/m365crawl/internal/store"
)

// planningUID is the fixture's "Fixture planning review" iCalUId; an Outlook invite carries it
// upper-case.
const planningUID = "040000008200E00074C5B7101A82E00800000000464958545552452D524943482D4D454554494E4700000000000000000000000000000000"

// prepEnv syncs the Teams and Outlook fixtures (and nothing of this machine's) into a fresh archive
// and adds a synthetic mailbox around the fixture's planning review (2023-11-20 17:00 UTC).
func prepEnv(t *testing.T) (*env, string) {
	t.Helper()
	e := textEnv(t)
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "Main"), 0o750); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile("../../testdata/outlook-fixture/HxStore.hxd")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Main", "HxStore.hxd"), b, 0o600); err != nil { //nolint:gosec // a test temp dir
		t.Fatal(err)
	}
	t.Setenv("M365CRAWL_TEAMS_ROOT", e.root)
	t.Setenv("M365CRAWL_OUTLOOK_ROOT", root)
	if code, _, stderr := e.run("--outlook-root", root, "sync"); code != 0 {
		t.Fatalf("sync exit %d: %s", code, stderr)
	}
	day := func(d int, h int) time.Time { return time.Date(2023, 11, d, h, 0, 0, 0, time.UTC) }
	msg := func(key uint32, subject, fromName, from string, at time.Time, to ...string) outlookmail.Message {
		read := false
		m := outlookmail.Message{Account: "outlook/Main", Folder: outlookmail.Folder{Key: 1, Name: "Inbox", Kind: "inbox"}, Copies: 1}
		m.DetailKey, m.FolderKey, m.MessageID = key, 1, "<prep-"+subject+"@example.invalid>"
		m.Subject, m.SenderName, m.SenderAddress, m.Received, m.Sent = subject, fromName, from, at, at
		m.Unread, m.ReadState, m.Flag, m.Importance, m.Class = &read, "read", "none", "normal", "IPM.Note"
		m.Preview = "About " + subject
		for _, addr := range to {
			m.Recipients = append(m.Recipients, outlookmail.Recipient{Name: strings.Split(addr, "@")[0], Address: addr})
		}
		return m
	}
	invite := msg(9001, "Fixture planning review", "Pat Example", "pat@example.invalid", day(13, 9), "me1@example.invalid")
	invite.MessageID, invite.ICalUID = "<invite@example.invalid>", planningUID
	reply := msg(9002, "RE: Fixture planning review", "Sam Example", "SAM@example.invalid", day(19, 10), "me1@example.invalid")
	tooLate := msg(9003, "Fixture planning review", "Pat Example", "pat@example.invalid", day(28, 9), "me1@example.invalid")
	stranger := msg(9004, "Fixture planning review", "Zed Other", "zed@other.invalid", day(18, 9), "yan@other.invalid")
	lunch := msg(9005, "Lunch", "Quinn Mail", "quinn@example.invalid", day(21, 12), "me1@example.invalid")
	st, err := store.Open(context.Background(), e.db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	res := outlookmail.Result{Messages: []outlookmail.Message{invite, reply, tooLate, stranger, lunch}, Folders: []outlookmail.Folder{{Key: 1, Name: "Inbox", Kind: "inbox"}}}
	if _, err := st.CommitMail(context.Background(), store.MailBatch{Account: "outlook/Main", ReadAt: day(28, 12), FreshAt: day(28, 12), Result: res}); err != nil {
		t.Fatal(err)
	}
	return e, root
}

func planningID(t *testing.T, e *env, source string) string {
	t.Helper()
	m := agenda(t, e, "--from", "2023-11-20", "--days", "1", "--limit", "200")
	return itemBySubjectFrom(t, m, "Fixture planning review", source)["event_id"].(string)
}

func relatedOf(t *testing.T, d map[string]any) []string {
	t.Helper()
	raw, ok := d["related_mail"].([]any)
	if !ok {
		t.Fatalf("related_mail is not a list: %v", d["related_mail"])
	}
	var out []string
	for _, r := range raw {
		m := r.(map[string]any)
		out = append(out, m["subject"].(string)+"/"+m["match"].(string))
	}
	return out
}

func TestCalendarEventPrepAddsRelatedMailAndRecentChat(t *testing.T) {
	e, _ := prepEnv(t)
	id := planningID(t, e, "teams")
	d := eventDoc(t, e, id)
	// The invite by its iCalUId (stored upper-case, matched without case), then the reply by subject;
	// mail after the window or among strangers is left out.
	if got := strings.Join(relatedOf(t, d), ", "); got != "Fixture planning review/invite, RE: Fixture planning review/subject" {
		t.Fatalf("related_mail %s", got)
	}
	first := d["related_mail"].([]any)[0].(map[string]any)
	if first["id"] != "outlook/Main:9001" || first["from_address"] != "pat@example.invalid" || first["folder"] != "Inbox" || first["preview"] == nil {
		t.Fatalf("a related message %v", first)
	}
	// The meeting chat carries its newest messages, newest first, in the messages shape.
	chat := d["chat"].(map[string]any)
	recent := chat["recent_messages"].([]any)
	if len(recent) != 9 || recent[0].(map[string]any)["sent_at"] != "2023-12-05T18:31:00Z" || recent[0].(map[string]any)["conversation_id"] != chat["conversation_id"] {
		t.Fatalf("recent_messages %d %v", len(recent), recent[0])
	}
	if d["notices"] != nil {
		t.Fatalf("a Teams meeting has no chat notice: %v", d["notices"])
	}
	// --fields names both keys.
	f := eventDoc(t, e, id, "--fields", "related_mail")
	if _, extra := f["subject"]; extra || len(relatedOf(t, f)) != 2 {
		t.Fatalf("--fields related_mail: %v", keysOf(f))
	}
	f = eventDoc(t, e, id, "--fields", "chat")
	if f["chat"].(map[string]any)["recent_messages"] == nil {
		t.Fatalf("--fields chat: %v", f)
	}
	// --max-text cuts the chat text and the mail preview.
	cut := eventDoc(t, e, id, "--max-text", "3", "--fields", "chat,related_mail")
	msg0 := cut["chat"].(map[string]any)["recent_messages"].([]any)[0].(map[string]any)
	mail0 := cut["related_mail"].([]any)[0].(map[string]any)
	if cut["text_truncated"] != true || msg0["text"] != "Fix…" || msg0["text_truncated"] != true || mail0["preview"] != "Abo…" {
		t.Fatalf("max-text: %v %v %v", cut["text_truncated"], msg0, mail0)
	}
	// The agenda points to calendar event for related_mail.
	code, _, errOut := e.run("--max-age", "0", "calendar", "--fields", "related_mail")
	if code != 2 || !strings.Contains(errOut, "calendar event") {
		t.Fatalf("agenda --fields related_mail: %d %s", code, errOut)
	}
}

// An Outlook-only event has no Teams meeting chat: chat is null and a notice says so; its mail
// still comes with it.
func TestCalendarEventPrepForAnOutlookOnlyEvent(t *testing.T) {
	e, root := prepEnv(t)
	id := planningID(t, e, "outlook")
	code, out, errOut := e.run("--max-age", "0", "--outlook-root", root, "calendar", "event", id)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	d := decode(t, out)
	if v, ok := d["chat"]; !ok || v != nil {
		t.Fatalf("chat %v %v", v, ok)
	}
	if n := asStrings(d["notices"]); len(n) != 1 || !strings.Contains(n[0], "no Teams meeting chat") {
		t.Fatalf("notices %v", n)
	}
	if got := relatedOf(t, d); len(got) == 0 || got[0] != "Fixture planning review/invite" {
		t.Fatalf("related_mail %v", got)
	}
}

// An event whose subject no message shares, without an invite, has an empty related_mail list.
func TestCalendarEventPrepWithNoRelatedMail(t *testing.T) {
	e, _ := prepEnv(t)
	m := agenda(t, e, "--from", "2023-11-21", "--days", "1", "--account", tenantA+"/"+userA)
	d := eventDoc(t, e, itemBySubject(t, m, "Fixture thin event")["event_id"].(string))
	if got := relatedOf(t, d); len(got) != 0 {
		t.Fatalf("related_mail %v", got)
	}
}

// A mail read that fails fails the command instead of printing a meeting without its mail.
func TestCalendarEventPrepReportsAMailReadError(t *testing.T) {
	e, _ := prepEnv(t)
	id := planningID(t, e, "teams")
	e.exec(`drop table mail_recipients`)
	if code, _, errOut := e.run("--max-age", "0", "calendar", "event", id); code == 0 || !strings.Contains(errOut, "mail_recipients") {
		t.Fatalf("exit %d: %s", code, errOut)
	}
}

func TestPeopleIncludesMailCorrespondents(t *testing.T) {
	e, _ := prepEnv(t)
	code, out, errOut := e.run("--max-age", "0", "people", "--limit", "200")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	byID := map[string]map[string]any{}
	for _, it := range items(t, decode(t, out)) {
		byID[it["id"].(string)] = it
	}
	sam := byID["mail:sam@example.invalid"]
	if sam == nil || sam["email"] != "sam@example.invalid" || sam["display_name"] != "Sam Example" || strings.Join(asStrings(sam["sources"]), ",") != "mail" || sam["tenant_id"] != "" {
		t.Fatalf("a mail correspondent: %v", sam)
	}
	for id, it := range byID {
		if !strings.HasPrefix(id, "mail:") && (strings.Join(asStrings(it["sources"]), ",") != "chats" || it["email"] != nil) {
			t.Fatalf("a Teams person: %v", it)
		}
	}
	// A query matches a mail address; a mail id matches exactly.
	for _, q := range []string{"quinn@", "mail:quinn@example.invalid"} {
		code, out, _ := e.run("--max-age", "0", "people", "--query", q)
		if got := items(t, decode(t, out)); code != 0 || len(got) != 1 || got[0]["id"] != "mail:quinn@example.invalid" {
			t.Fatalf("%s: %v", q, got)
		}
	}
}

func TestMeetingPrepGoldens(t *testing.T) {
	e, _ := prepEnv(t)
	id := planningID(t, e, "teams")
	for _, c := range []struct {
		name string
		args []string
	}{
		{"calendar_event_prep", []string{"calendar", "event", id}},
		{"people_mail", []string{"people", "--query", "example.invalid"}},
	} {
		for _, color := range []bool{false, true} {
			t.Setenv("CLICOLOR_FORCE", "")
			suffix := "plain"
			if color {
				suffix = "color"
				t.Setenv("CLICOLOR_FORCE", "1")
			}
			code, out, errOut := e.run(append([]string{"--format", "text", "--max-age", "0"}, c.args...)...)
			if code != 0 {
				t.Fatalf("%s: exit %d: %s", c.name, code, errOut)
			}
			checkGolden(t, c.name+"."+suffix, e.scrub(out))
		}
	}
}
