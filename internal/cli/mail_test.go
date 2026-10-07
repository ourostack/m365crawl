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

const mailAccount = "outlook/Main"

var mailT0 = time.Date(2026, 9, 10, 8, 0, 0, 0, time.UTC)

const (
	fInbox    = 10
	fSent     = 11
	fProjects = 15
	fDrafts   = 16
)

func mailFolderList() []outlookmail.Folder {
	return []outlookmail.Folder{
		{Key: fInbox, Parent: 1, Name: "Inbox", Kind: "inbox"},
		{Key: fSent, Parent: 1, Name: "Sent Items", Kind: "sent"},
		{Key: fProjects, Parent: 1, Name: "Projects", Kind: "other"},
		{Key: fDrafts, Parent: 1, Name: "Drafts", Kind: "drafts"},
	}
}

func kindOf(key uint32) string {
	for _, f := range mailFolderList() {
		if f.Key == key {
			return f.Kind
		}
	}
	return "other"
}

// mailMsg builds a synthetic message received days before mailT0.
func mailMsg(dk, folder uint32, msgID, replyTo, subject string, daysAgo int, unread bool) outlookmail.Message {
	recv := mailT0.AddDate(0, 0, -daysAgo)
	m := outlookmail.Message{Account: mailAccount, Folder: outlookmail.Folder{Key: folder, Kind: kindOf(folder)}, Copies: 1}
	m.DetailKey, m.FolderKey = dk, folder
	m.MessageID, m.InReplyTo, m.Subject, m.Received, m.Sent = msgID, replyTo, subject, recv, recv.Add(-time.Minute)
	m.SenderName, m.SenderAddress = "Ann Sender", "ann@example.test"
	m.Unread, m.ReadState, m.Flag, m.Importance = &unread, "read", "none", "normal"
	if unread {
		m.ReadState = "unread"
	}
	m.Recipients = []outlookmail.Recipient{{Name: "Bob Reader", Address: "bob@example.test", KindRaw: 1}}
	m.Class = "IPM.Note"
	m.Body = outlookmail.Body{State: outlookmail.BodyInline, HTML: []byte("<p>body of " + subject + "</p>")}
	return m
}

func seedMessages() []outlookmail.Message {
	plan := mailMsg(101, fInbox, "<a@x.test>", "", "Quarterly plan", 5, true)
	plan.Recipients = append(plan.Recipients,
		outlookmail.Recipient{Name: "Cy Copy", Address: "cy@example.test", KindRaw: 2},
		outlookmail.Recipient{Name: "Di Dee", Address: "di@example.test", KindRaw: 2},
		outlookmail.Recipient{Name: "", Address: "eve@example.test", KindRaw: 2},
		outlookmail.Recipient{Name: "No Address", Address: "", KindRaw: 2})
	plan.Attachments = []outlookmail.Attachment{{Key: 1, MessageKey: 101, Name: "plan.pdf", Size: 2048, ContentType: "application/pdf", Downloaded: true}}
	plan.Flag, plan.Importance, plan.Preview = "flagged", "high", "Short plan preview"
	reply := mailMsg(102, fInbox, "<b@x.test>", "<a@x.test>", "Re: Quarterly plan", 4, false)
	reply.SenderName, reply.SenderAddress = "Bob Reader", "bob@example.test"
	reply.Recipients = []outlookmail.Recipient{{Name: "Ann Sender", Address: "ann@example.test", KindRaw: 1}}
	reply.Attachments = []outlookmail.Attachment{{Key: 2, MessageKey: 102, Name: "logo.png", Size: 10, ContentType: "image/png", Inline: true}}
	sent := mailMsg(103, fSent, "<c@x.test>", "<b@x.test>", "Re: Re: Quarterly plan", 3, false)
	lone := mailMsg(104, fInbox, "<d@x.test>", "", "Lunch on Friday", 2, true)
	lone.Unread, lone.ReadState = nil, "unknown"
	lone.Preview = "Short preview of the lunch plan"
	lone.Body = outlookmail.Body{State: outlookmail.BodyNone}
	lone.Sent = time.Time{}
	lone.SenderName = ""
	old := mailMsg(105, fProjects, "<e@x.test>", "", "Old report", 40, true)
	lunch2 := mailMsg(106, fInbox, "<f@x.test>", "", "Lunch on Friday", 1, false)
	lunch2.Recipients = []outlookmail.Recipient{{Address: "zed@example.test", KindRaw: 1}}
	return []outlookmail.Message{plan, reply, sent, lone, old, lunch2}
}

// seedMail syncs the fixture and then commits the synthetic mailbox into the archive.
func seedMail(t *testing.T, e *env, msgs []outlookmail.Message) {
	t.Helper()
	e.sync()
	st, err := store.Open(context.Background(), e.db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	cov := map[uint32]*outlookmail.Coverage{}
	for _, m := range msgs {
		c := cov[m.FolderKey]
		if c == nil {
			c = &outlookmail.Coverage{FolderKey: m.FolderKey, Oldest: m.Received, Newest: m.Received}
			cov[m.FolderKey] = c
		}
		c.Count++
		if m.Received.Before(c.Oldest) {
			c.Oldest = m.Received
		}
		if m.Received.After(c.Newest) {
			c.Newest = m.Received
		}
	}
	res := outlookmail.Result{Messages: msgs, Folders: mailFolderList()}
	for _, f := range mailFolderList() {
		if c := cov[f.Key]; c != nil {
			res.Coverage = append(res.Coverage, *c)
		}
	}
	read := mailT0.Add(time.Hour)
	if _, err := st.CommitMail(context.Background(), store.MailBatch{Account: mailAccount, ReadAt: read, FreshAt: read, Result: res, Trusted: true}); err != nil {
		t.Fatal(err)
	}
}

// pinMailPlatform makes the mail commands run as on a Mac, whatever the host is.
func pinMailPlatform(t *testing.T) {
	t.Helper()
	old := mailPlatform
	mailPlatform = "darwin"
	t.Cleanup(func() { mailPlatform = old })
}

func mailEnv(t *testing.T) *env {
	t.Helper()
	pinMailPlatform(t)
	e := textEnv(t)
	seedMail(t, e, seedMessages())
	return e
}

func (e *env) mail(args ...string) (int, string, string) {
	return e.run(append([]string{"--max-age", "0"}, args...)...)
}

func mailJSON(t *testing.T, e *env, args ...string) map[string]any {
	t.Helper()
	code, out, errOut := e.mail(append([]string{"--json"}, args...)...)
	if code != 0 {
		t.Fatalf("%v: exit %d: %s", args, code, errOut)
	}
	return decode(t, out)
}

func mailFails(t *testing.T, e *env, wantCode string, wantExit int, args ...string) map[string]any {
	t.Helper()
	code, _, errOut := e.mail(append([]string{"--json"}, args...)...)
	if code != wantExit {
		t.Fatalf("%v: exit %d, want %d: %s", args, code, wantExit, errOut)
	}
	er := errorOf(t, errOut)
	if er["code"] != wantCode {
		t.Fatalf("%v: code %v, want %s: %v", args, er["code"], wantCode, er)
	}
	return er
}

func ids(its []map[string]any) []string {
	var out []string
	for _, it := range its {
		out = append(out, it["id"].(string))
	}
	return out
}

func TestMailListEnvelope(t *testing.T) {
	e := mailEnv(t)
	m := mailJSON(t, e, "mail", "list")
	its := items(t, m)
	if got := strings.Join(ids(its), " "); got != "outlook/Main:106 outlook/Main:104 outlook/Main:103 outlook/Main:102 outlook/Main:101 outlook/Main:105" {
		t.Fatalf("ids = %s", got)
	}
	if m["count"] != float64(6) || m["truncated"] != false || m["synced_at"] == nil {
		t.Fatalf("envelope = %v", m)
	}
	cov := m["coverage"].([]any)
	if len(cov) != 3 || cov[0].(map[string]any)["oldest_at"] == nil {
		t.Fatalf("coverage = %v", cov)
	}
	first := its[4] // message 101
	if first["recipient_count"] != float64(5) || len(first["recipients_preview"].([]any)) != 3 {
		t.Fatalf("recipients = %v", first)
	}
	if _, has := first["recipients"]; has {
		t.Fatal("list must not carry the full recipient list")
	}
	if its[1]["is_read"] != nil || its[1]["from_name"] != nil || its[1]["sent_at"] != nil {
		t.Fatalf("absent fields must be null: %v", its[1])
	}
	if its[0]["recipients_preview"] == nil {
		t.Fatal("recipients_preview must be a list, never null")
	}
}

func TestMailListFilters(t *testing.T) {
	e := mailEnv(t)
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"--folder", "inbox"}, "106 104 102 101"},
		{[]string{"--folder", "Projects"}, "105"},
		{[]string{"--folder", "sent items"}, "103"},
		{[]string{"--from", "bob"}, "102"},
		{[]string{"--unread"}, "101 105"},
		{[]string{"--flagged"}, "101"},
		{[]string{"--has-attachments"}, "101"},
		{[]string{"--since", "2026-09-07"}, "106 104 103"},
		{[]string{"--until", "2026-09-06"}, "101 105"},
		{[]string{"--folder", "inbox", "--unread", "--from", "ann"}, "101"},
		{[]string{"--limit", "2"}, "106 104"},
	}
	for _, c := range cases {
		m := mailJSON(t, e, append([]string{"mail", "list"}, c.args...)...)
		var got []string
		for _, id := range ids(items(t, m)) {
			got = append(got, strings.TrimPrefix(id, "outlook/Main:"))
		}
		if strings.Join(got, " ") != c.want {
			t.Errorf("%v: got %v, want %s", c.args, got, c.want)
		}
	}
	m := mailJSON(t, e, "mail", "list", "--limit", "2")
	if m["truncated"] != true {
		t.Fatalf("truncated = %v", m["truncated"])
	}
}

func TestMailListIncludeFlags(t *testing.T) {
	e := mailEnv(t)
	e.exec(`update mail_messages set gone_at='2026-09-09T00:00:00.000Z' where detail_key=102`)
	e.exec(`update mail_messages set evicted_at='2026-09-09T00:00:00.000Z' where detail_key=101`)
	if n := len(items(t, mailJSON(t, e, "mail", "list"))); n != 4 {
		t.Fatalf("default list = %d", n)
	}
	if n := len(items(t, mailJSON(t, e, "mail", "list", "--include-gone"))); n != 5 {
		t.Fatalf("include-gone = %d", n)
	}
	m := mailJSON(t, e, "mail", "list", "--include-evicted", "--include-gone")
	if len(items(t, m)) != 6 {
		t.Fatalf("both = %d", len(items(t, m)))
	}
	if items(t, m)[4]["evicted_at"] == nil || items(t, m)[3]["gone_at"] == nil {
		t.Fatalf("times = %v", items(t, m))
	}
}

func TestMailUnknownFolder(t *testing.T) {
	e := mailEnv(t)
	for _, cmd := range []string{"list", "unread"} {
		er := mailFails(t, e, "unknown_folder", 2, "mail", cmd, "--folder", "nope")
		if !strings.Contains(er["fix"].(string), "Inbox") || !strings.Contains(er["fix"].(string), "Projects") {
			t.Fatalf("fix does not list the folders: %v", er)
		}
	}
	e2 := textEnv(t)
	e2.sync()
	er := mailFails(t, e2, "unknown_folder", 2, "mail", "list", "--folder", "x")
	if !strings.Contains(er["fix"].(string), "sync") {
		t.Fatalf("fix = %v", er)
	}
}

func TestMailUsageErrors(t *testing.T) {
	e := mailEnv(t)
	for _, args := range [][]string{
		{"mail", "list", "--limit", "0"},
		{"mail", "list", "--since", "garbage"},
		{"mail", "list", "--until", "garbage"},
		{"mail", "list", "--fields", "nope"},
		{"mail", "show", "outlook/Main:101", "--fields", "nope"},
		{"mail", "thread", "outlook/Main:101", "--fields", "nope"},
		{"mail", "folders", "--fields", "nope"},
		{"mail", "unread", "--fields", "nope"},
		{"mail", "unread", "--limit", "0"},
	} {
		mailFails(t, e, "usage", 2, args...)
	}
}

func TestMailLimitCap(t *testing.T) {
	e := mailEnv(t)
	for _, args := range [][]string{{"mail", "list"}, {"mail", "unread"}} {
		mailJSON(t, e, append(args, "--limit", "1000")...)
		er := mailFails(t, e, "usage", 2, append(args, "--limit", "1001")...)
		if !strings.Contains(er["fix"].(string), "1000") {
			t.Fatalf("fix does not name the maximum: %v", er)
		}
	}
}

func TestMailFieldsAndMaxText(t *testing.T) {
	e := mailEnv(t)
	m := mailJSON(t, e, "--fields", "id,subject", "mail", "list", "--limit", "1")
	if got := items(t, m)[0]; len(got) != 2 || got["subject"] == nil {
		t.Fatalf("fields = %v", got)
	}
	m = mailJSON(t, e, "--max-text", "5", "mail", "list", "--folder", "inbox", "--unread")
	if it := items(t, m)[0]; it["preview"] != "Short…" || it["text_truncated"] != true {
		t.Fatalf("max-text = %v", it)
	}
	m = mailJSON(t, e, "--max-text", "5", "mail", "show", "outlook/Main:101")
	if m["body_text"] != "body …" || m["text_truncated"] != true {
		t.Fatalf("show max-text = %v", m)
	}
	m = mailJSON(t, e, "--fields", "id,body_text", "mail", "show", "outlook/Main:101")
	if len(m) < 2 || m["id"] != "outlook/Main:101" || m["subject"] != nil {
		t.Fatalf("show fields = %v", m)
	}
}

func TestMailShow(t *testing.T) {
	e := mailEnv(t)
	m := mailJSON(t, e, "mail", "show", "outlook/Main:101")
	if m["subject"] != "Quarterly plan" || m["body_text"] != "body of Quarterly plan" || m["folder_kind"] != "inbox" {
		t.Fatalf("show = %v", m)
	}
	rc := m["recipients"].([]any)
	if len(rc) != 5 || rc[0].(map[string]any)["kind_raw"] != float64(1) || rc[4].(map[string]any)["address"] != nil {
		t.Fatalf("recipients = %v", rc)
	}
	at := m["attachments"].([]any)[0].(map[string]any)
	if at["name"] != "plan.pdf" || at["size"] != float64(2048) || at["content_type"] != "application/pdf" {
		t.Fatalf("attachments = %v", at)
	}
	if m["importance"] != "high" || m["flag"] != "flagged" {
		t.Fatalf("show = %v", m)
	}
	m = mailJSON(t, e, "mail", "show", "outlook/Main:104")
	if m["body_text"] != nil || m["is_read"] != nil || m["recipients"] == nil {
		t.Fatalf("show of an absent body = %v", m)
	}
}

func TestMailShowErrors(t *testing.T) {
	e := mailEnv(t)
	for _, id := range []string{"101", "outlook/Main:x", ":5", "outlook/Main:99999999999"} {
		er := mailFails(t, e, "bad_mail_id", 2, "mail", "show", id)
		if !strings.Contains(er["fix"].(string), "account:detail_key") {
			t.Fatalf("fix = %v", er)
		}
	}
	mailFails(t, e, "mail_not_found", 2, "mail", "show", "outlook/Main:999")
	mailFails(t, e, "mail_not_found", 2, "mail", "thread", "outlook/Main:999")
	mailFails(t, e, "bad_mail_id", 2, "mail", "thread", "nope")
	// no archive at all
	e2 := textEnv(t)
	mailFails(t, e2, "mail_not_found", 2, "mail", "show", "outlook/Main:1")
	mailFails(t, e2, "mail_not_found", 2, "mail", "thread", "outlook/Main:1")
}

func TestMailThread(t *testing.T) {
	e := mailEnv(t)
	m := mailJSON(t, e, "mail", "thread", "outlook/Main:102")
	if m["grouping"] != "reply_chain" || len(items(t, m)) != 3 {
		t.Fatalf("chain = %v", m)
	}
	people := m["participants"].([]any)
	if len(people) < 3 || people[0].(map[string]any)["address"] != "ann@example.test" {
		t.Fatalf("participants = %v", people)
	}
	m = mailJSON(t, e, "mail", "thread", "outlook/Main:104")
	if m["grouping"] != "subject" {
		t.Fatalf("grouping = %v", m["grouping"])
	}
}

func TestMailFolders(t *testing.T) {
	e := mailEnv(t)
	m := mailJSON(t, e, "mail", "folders")
	its := items(t, m)
	if len(its) != 4 || its[0]["folder"] != "Inbox" || its[0]["messages"] != float64(4) || its[0]["unread"] != float64(1) || its[0]["oldest_at"] == nil {
		t.Fatalf("folders = %v", its)
	}
	m = mailJSON(t, e, "--fields", "folder,unread", "mail", "folders")
	if len(items(t, m)[0]) != 2 {
		t.Fatalf("fields = %v", m)
	}
}

func TestMailUnread(t *testing.T) {
	e := mailEnv(t)
	m := mailJSON(t, e, "mail", "unread")
	fs := m["folders"].([]any)
	if len(fs) != 3 || fs[0].(map[string]any)["folder"] != "Inbox" || fs[0].(map[string]any)["unread"] != float64(1) || fs[0].(map[string]any)["cached"] != float64(4) {
		t.Fatalf("folders = %v", fs)
	}
	if got := ids(items(t, m)); strings.Join(got, " ") != "outlook/Main:101 outlook/Main:105" && strings.Join(got, " ") != "outlook/Main:101" {
		t.Fatalf("items = %v", got)
	}
	if note := m["note"].(string); !strings.Contains(note, "Inbox: the local cache holds 4 messages in this folder; Outlook may show more") {
		t.Fatalf("note = %q", note)
	}
	m = mailJSON(t, e, "mail", "unread", "--folder", "inbox")
	if m["note"] != "the local cache holds 4 messages in this folder; Outlook may show more" {
		t.Fatalf("note = %v", m["note"])
	}
	m = mailJSON(t, e, "mail", "list", "--unread", "--folder", "inbox")
	if !strings.Contains(m["note"].(string), "the local cache holds 4 messages in this folder; Outlook may show more") {
		t.Fatalf("list --unread note = %v", m["note"])
	}
	m = mailJSON(t, e, "mail", "unread", "--limit", "1")
	if m["truncated"] != true || len(items(t, m)) != 1 {
		t.Fatalf("limit = %v", m)
	}
	m = mailJSON(t, e, "mail", "list", "--unread", "--from", "nobody")
	if !strings.Contains(m["note"].(string), "Outlook may show more") || !strings.Contains(m["note"].(string), "no message matched") {
		t.Fatalf("note = %v", m["note"])
	}
	// nothing unread
	e.exec(`update mail_messages set is_read=1`)
	m = mailJSON(t, e, "mail", "unread")
	if m["note"] != "no unread mail in the cache; Outlook may show more" {
		t.Fatalf("note = %v", m["note"])
	}
}

func TestMailAccountFlag(t *testing.T) {
	e := mailEnv(t)
	if n := len(items(t, mailJSON(t, e, "--account", mailAccount, "mail", "list"))); n != 6 {
		t.Fatalf("own account = %d", n)
	}
	m := mailJSON(t, e, "--account", "outlook/Other", "mail", "list")
	if len(items(t, m)) != 0 || !strings.Contains(m["note"].(string), "no mail is archived for account outlook/Other") {
		t.Fatalf("other account = %v", m)
	}
	mailJSON(t, e, "--account", "outlook/Other", "mail", "folders")
}

func TestMailEmptyNotes(t *testing.T) {
	pinMailPlatform(t)
	old := mailProfileCount
	t.Cleanup(func() { mailProfileCount = old })
	note := func(e *env, args ...string) string {
		t.Helper()
		m := mailJSON(t, e, args...)
		if len(items(t, m)) != 0 {
			t.Fatalf("not empty: %v", m)
		}
		n, _ := m["note"].(string)
		return n
	}
	e := textEnv(t)
	if n := note(e, "mail", "list"); !strings.Contains(n, "no archive yet") {
		t.Fatalf("no archive: %q", n)
	}
	if n := note(e, "mail", "unread"); !strings.Contains(n, "no archive yet") {
		t.Fatalf("no archive unread: %q", n)
	}
	if n := note(e, "mail", "folders"); !strings.Contains(n, "no archive yet") {
		t.Fatalf("no archive folders: %q", n)
	}
	e.sync()
	if n := note(e, "mail", "list"); !strings.Contains(n, "the Outlook source is off") {
		t.Fatalf("source off: %q", n)
	}
	root := t.TempDir()
	mailProfileCount = func(string) int { return 0 }
	if n := note(e, "--outlook-root", root, "mail", "list"); !strings.Contains(n, "no Outlook for Mac profile") {
		t.Fatalf("no profile: %q", n)
	}
	mailProfileCount = func(string) int { return 1 }
	if n := note(e, "--outlook-root", root, "mail", "unread"); !strings.Contains(n, "no mail has been read yet") {
		t.Fatalf("not read: %q", n)
	}
	// An empty mailbox: folders and a coverage row, no messages.
	e2 := textEnv(t)
	seedMail(t, e2, seedMessages()[:1])
	e2.exec(`delete from mail_messages`)
	if n := note(e2, "mail", "list"); !strings.Contains(n, "the mailbox is empty") {
		t.Fatalf("empty mailbox: %q", n)
	}
	if n := note(e2, "mail", "list", "--from", "zzz"); n != "no message matched the filters" {
		t.Fatalf("filter: %q", n)
	}
	if n := note(e2, "mail", "list", "--since", "2020-01-01"); !strings.Contains(n, "the cache covers only since 2026-09-05") {
		t.Fatalf("window: %q", n)
	}
	if n := note(e2, "mail", "list", "--since", "2030-01-01"); n != "no message matched the filters" {
		t.Fatalf("future since: %q", n)
	}
}

func TestMailWindows(t *testing.T) {
	old := mailPlatform
	mailPlatform = "windows"
	t.Cleanup(func() { mailPlatform = old })
	e := textEnv(t)
	for _, args := range [][]string{{"mail", "list"}, {"mail", "show", "outlook/Main:1"}, {"mail", "thread", "outlook/Main:1"}, {"mail", "folders"}, {"mail", "unread"}} {
		er := mailFails(t, e, "mail_unsupported_platform", 3, args...)
		if er["message"] != "mail is not yet read on Windows; Teams chats and the calendar work here — try `m365crawl calendar`" {
			t.Fatalf("message = %v", er["message"])
		}
	}
}

func TestMailTextGoldens(t *testing.T) {
	e := mailEnv(t)
	cases := []struct {
		name string
		args []string
	}{
		{"mail_list", []string{"mail", "list"}},
		{"mail_list_fields", []string{"--fields", "id,subject", "mail", "list"}},
		{"mail_list_limit", []string{"mail", "list", "--limit", "2"}},
		{"mail_folders_fields", []string{"--fields", "folder,unread", "mail", "folders"}},
		{"mail_list_empty", []string{"mail", "list", "--from", "zzz"}},
		{"mail_show", []string{"mail", "show", "outlook/Main:101"}},
		{"mail_show_bare", []string{"mail", "show", "outlook/Main:104"}},
		{"mail_show_fields", []string{"--fields", "id,subject", "mail", "show", "outlook/Main:101"}},
		{"mail_thread", []string{"mail", "thread", "outlook/Main:102"}},
		{"mail_folders", []string{"mail", "folders"}},
		{"mail_unread", []string{"mail", "unread"}},
		{"mail_unread_folder", []string{"mail", "unread", "--folder", "inbox"}},
	}
	for _, c := range cases {
		for _, color := range []bool{false, true} {
			suffix := "plain"
			t.Setenv("CLICOLOR_FORCE", "")
			if color {
				suffix = "color"
				t.Setenv("CLICOLOR_FORCE", "1")
			}
			code, out, errOut := e.run(append([]string{"--format", "text", "--max-age", "0"}, c.args...)...)
			if code != 0 {
				t.Fatalf("%s: exit %d: %s", c.name, code, errOut)
			}
			if !color && strings.Contains(out, "\x1b") {
				t.Errorf("%s: escape in plain output", c.name)
			}
			if color && !strings.Contains(out, "\x1b[") {
				t.Errorf("%s: no color", c.name)
			}
			if strings.HasPrefix(c.name, "mail_list") || c.name == "mail_thread" || c.name == "mail_folders" || strings.HasPrefix(c.name, "mail_unread") {
				lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
				last := lines[len(lines)-1]
				if !strings.Contains(last, "synced ") || !strings.Contains(last, " · cache covers since ") {
					t.Errorf("%s: last line = %q", c.name, last)
				}
			}
			checkGolden(t, c.name+"."+suffix, e.scrub(out))
		}
	}
}

func TestMailTextTruncatedShow(t *testing.T) {
	e := mailEnv(t)
	_, out, _ := e.run("--format", "text", "--max-age", "0", "--max-text", "4", "mail", "show", "outlook/Main:101")
	if !strings.Contains(out, "cut by --max-text") {
		t.Fatalf("out = %s", out)
	}
}

func TestMailMetadata(t *testing.T) {
	m := manifest()
	for _, n := range []string{"mail list", "mail show", "mail thread", "mail folders", "mail unread"} {
		if _, ok := m.Commands[n]; !ok {
			t.Errorf("manifest lacks %s", n)
		}
	}
}

func TestMailHelpIsTheContract(t *testing.T) {
	e := textEnv(t)
	for cmd, want := range map[string]string{
		"list":    "List archived Outlook mail, newest first.",
		"show":    "Show one message: headers, recipients, attachments and body text.",
		"thread":  "Show the conversation a message belongs to: its reply chain, or messages with the same subject and a shared participant when Outlook kept no reply link.",
		"folders": "List mail folders with message and unread counts and how far back the cache reaches.",
		"unread":  "Unread mail by folder.",
	} {
		args := []string{"--format", "text", "mail", cmd, "--help"}
		if cmd == "show" || cmd == "thread" {
			args = []string{"--format", "text", "mail", cmd, "--help"}
		}
		_, out, _ := e.run(args...)
		flat := strings.Join(strings.Fields(out), " ")
		if !strings.Contains(flat, want) || !strings.Contains(flat, "likely") || !strings.Contains(flat, "To and Cc are not yet told apart") {
			t.Errorf("mail %s help:\n%s", cmd, out)
		}
	}
}

func TestMailStateAndInlineText(t *testing.T) {
	e := mailEnv(t)
	e.exec(`update mail_messages set gone_at='2026-09-09T00:00:00.000Z' where detail_key=103`)
	e.exec(`update mail_messages set evicted_at='2026-09-09T00:00:00.000Z' where detail_key=105`)
	e.exec(`update mail_messages set is_read=0 where detail_key=102`)
	_, out, _ := e.run("--format", "text", "--max-age", "0", "mail", "list", "--include-gone", "--include-evicted")
	if !strings.Contains(out, "gone") || !strings.Contains(out, "evicted") {
		t.Fatalf("list = %s", out)
	}
	for _, id := range []string{"103", "105"} {
		_, out, _ = e.run("--format", "text", "--max-age", "0", "mail", "show", "outlook/Main:"+id)
		if !strings.Contains(out, "gone") && !strings.Contains(out, "evicted") {
			t.Fatalf("show %s = %s", id, out)
		}
	}
	_, out, _ = e.run("--format", "text", "--max-age", "0", "mail", "show", "outlook/Main:102")
	if !strings.Contains(out, "image/png (inline)") {
		t.Fatalf("show = %s", out)
	}
	m := mailJSON(t, e, "mail", "list", "--unread", "--folder", "inbox")
	if len(items(t, m)) != 2 || !strings.Contains(m["note"].(string), "holds 4 messages") {
		t.Fatalf("two unread in one folder = %v", m)
	}
}

func TestMailDerefTime(t *testing.T) {
	if !derefTime(nil).IsZero() {
		t.Fatal("nil time")
	}
}

func TestMailProfileCount(t *testing.T) {
	root := t.TempDir()
	if n := mailProfileCount(root); n != 0 {
		t.Fatalf("empty root = %d", n)
	}
	if err := os.MkdirAll(filepath.Join(root, "Main"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Main", "HxStore.hxd"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if n := mailProfileCount(root); n != 1 {
		t.Fatalf("one profile = %d", n)
	}
	if n := mailProfileCount(filepath.Join(root, "missing")); n != 0 {
		t.Fatalf("missing root = %d", n)
	}
	t.Setenv("HOME", t.TempDir())
	if n := mailProfileCount(""); n != 0 {
		t.Fatalf("default root = %d", n)
	}
}

func TestMailDamagedArchive(t *testing.T) {
	for _, table := range []string{"mail_recipients", "mail_coverage", "mail_messages"} {
		e := mailEnv(t)
		e.exec(`drop table ` + table)
		for _, args := range [][]string{{"mail", "list"}, {"mail", "show", "outlook/Main:101"}, {"mail", "thread", "outlook/Main:101"}, {"mail", "folders"}, {"mail", "unread"}} {
			code, _, errOut := e.mail(append([]string{"--json"}, args...)...)
			if code == 0 {
				// folders reads no recipients, and show reads no coverage.
				if (table != "mail_recipients" || args[1] != "folders") && (table != "mail_coverage" || args[1] != "show") {
					t.Errorf("%s dropped, %v: exit 0", table, args)
				}
				continue
			}
			if er := errorOf(t, errOut); er["code"] != "db_error" {
				t.Errorf("%s dropped, %v: %v", table, args, er)
			}
		}
	}
}
