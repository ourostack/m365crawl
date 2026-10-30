package cli

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/outlookmail"
	"github.com/ourostack/m365crawl/internal/store"
)

// Two folder sets of one account, shaped like a real archive after Outlook for Mac rebuilt the
// account in its store: set A (root 100) was read first, set B (root 200) is the current one.
const (
	copyRootA, copyRootB = 100, 200
	copyASent, copyBSent = 102, 202
)

func copySetFolders(root uint32) []outlookmail.Folder {
	return []outlookmail.Folder{
		{Key: root + 1, Parent: root, Name: "Inbox", Kind: "inbox"},
		{Key: root + 2, Parent: root, Name: "Sent Items", Kind: "sent"},
	}
}

func copyMsg(dk, folder uint32, msgID string, daysAgo int) outlookmail.Message {
	m := mailMsg(dk, folder, msgID, "", "Copy "+msgID, daysAgo, false)
	m.Folder = outlookmail.Folder{Key: folder, Kind: map[uint32]string{copyRootA + 1: "inbox", copyRootB + 1: "inbox"}[folder]}
	if m.Folder.Kind == "" {
		m.Folder.Kind = "sent"
	}
	return m
}

func commitCopySet(t *testing.T, st *store.Store, hour int, root uint32, mapped bool, msgs ...outlookmail.Message) {
	t.Helper()
	res := outlookmail.Result{RootKey: root}
	if mapped {
		res.Folders = copySetFolders(root)
	}
	cov := map[uint32]*outlookmail.Coverage{}
	for i, m := range msgs {
		if !mapped {
			msgs[i].Folder = outlookmail.Folder{Key: m.FolderKey, Kind: outlookmail.KindUnknown}
		}
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
	for _, c := range cov {
		res.Coverage = append(res.Coverage, *c)
	}
	res.Messages = msgs
	read := mailT0.Add(time.Duration(hour) * time.Hour)
	if _, err := st.CommitMail(context.Background(), store.MailBatch{Account: mailAccount, ReadAt: read, FreshAt: read, Result: res, Trusted: true}); err != nil {
		t.Fatal(err)
	}
}

// copyEnv archives set A, then set B first unmapped (as when the reader did not yet know B's
// folder layout, so B's copies could not be matched) and then mapped.
func copyEnv(t *testing.T) *env {
	t.Helper()
	e := textEnv(t)
	e.emptyArchive()
	st, err := store.Open(context.Background(), e.db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	a := func() []outlookmail.Message {
		return []outlookmail.Message{
			copyMsg(1001, copyASent, "<both@x.test>", 3), copyMsg(1002, copyASent, "<lost@x.test>", 3),
			copyMsg(1003, copyASent, "<early@x.test>", 20), copyMsg(1004, copyRootA+1, "<in@x.test>", 25),
		}
	}
	b := func() []outlookmail.Message {
		return []outlookmail.Message{copyMsg(2001, copyBSent, "<both@x.test>", 3), copyMsg(2002, copyBSent, "<oldest@x.test>", 10)}
	}
	commitCopySet(t, st, 1, copyRootA, true, a()...)
	commitCopySet(t, st, 2, copyRootB, false, b()...)
	commitCopySet(t, st, 3, copyRootB, true, b()...)
	return e
}

func dayFlags(daysAgo int) []string {
	d := mailT0.AddDate(0, 0, -daysAgo)
	return []string{"--since", d.Add(-time.Hour).Format(time.RFC3339), "--until", d.Add(time.Hour).Format(time.RFC3339)}
}

// A message held by two copies of Sent Items lists once; the day the current copy's range covers
// but an older copy holds a message it lacks is partial, in folder_copies and a notice.
func TestMailListCountsACopyOnceAndReportsAPartialDay(t *testing.T) {
	e := copyEnv(t)
	m := mailJSON(t, e, append([]string{"mail", "list", "--folder", "sent"}, dayFlags(3)...)...)
	its := items(t, m)
	if got := strings.Join(ids(its), " "); got != "outlook/Main:2001 outlook/Main:1002" {
		t.Fatalf("ids %s", got)
	}
	if its[0]["older_copy"] != false || its[0]["copy_of"] != nil || its[1]["older_copy"] != true || its[1]["copy_of"] != nil {
		t.Fatalf("items %v", its)
	}
	fc := m["folder_copies"].([]any)
	if len(fc) != 1 {
		t.Fatalf("folder_copies %v", fc)
	}
	c := fc[0].(map[string]any)
	if c["kind"] != "sent" || c["state"] != "partial" || c["messages"] != float64(2) || c["in_current_copy"] != float64(1) ||
		c["only_in_older_copies"] != float64(1) || c["lacking_in_current_range"] != float64(1) || c["copies"] != float64(2) || c["current_oldest_at"] == nil {
		t.Fatalf("folder_copies %v", c)
	}
	notices, _ := m["notices"].([]any)
	if len(notices) != 1 || !strings.Contains(notices[0].(string), "sent mail of outlook/Main is partial in this range") || !strings.Contains(notices[0].(string), "holds 1 of 2 messages") {
		t.Fatalf("notices %v", notices)
	}
	cov := m["coverage"].([]any)
	if len(cov) != 2 {
		t.Fatalf("coverage %v", cov)
	}
	for _, x := range cov {
		cv := x.(map[string]any)
		if cv["current"] != (cv["oldest_at"] == mailT0.AddDate(0, 0, -10).Format(time.RFC3339)) {
			t.Fatalf("coverage %v", cv)
		}
	}

	m = mailJSON(t, e, append([]string{"mail", "list", "--folder", "sent", "--include-copies"}, dayFlags(3)...)...)
	its = items(t, m)
	if got := strings.Join(ids(its), " "); got != "outlook/Main:2001 outlook/Main:1002 outlook/Main:1001" || its[2]["copy_of"] != "outlook/Main:2001" || its[2]["older_copy"] != true {
		t.Fatalf("with copies: %s %v", got, its)
	}
	code, out, _ := e.mail("--format", "text", "--no-color", "mail", "list", "--folder", "sent", "--include-copies", "--since", dayFlags(3)[1], "--until", dayFlags(3)[3])
	if code != 0 || !strings.Contains(out, "unread,older copy") && !strings.Contains(out, "older copy  ") || !strings.Contains(out, "partial in this range") {
		t.Fatalf("text: %d %s", code, out)
	}
}

// A day before the current copy's range that only an older copy reaches says so; a range with no
// selected folder kind that has older copies carries no folder_copies.
func TestMailListOlderCopyOnlyDay(t *testing.T) {
	e := copyEnv(t)
	m := mailJSON(t, e, append([]string{"mail", "list", "--folder", "sent"}, dayFlags(20)...)...)
	if got := strings.Join(ids(items(t, m)), " "); got != "outlook/Main:1003" {
		t.Fatalf("ids %s", got)
	}
	c := m["folder_copies"].([]any)[0].(map[string]any)
	notices, _ := m["notices"].([]any)
	if c["state"] != "older_copy_only" || len(notices) != 1 || !strings.Contains(notices[0].(string), "comes only from an older copy") {
		t.Fatalf("%v %v", c, notices)
	}
	m = mailJSON(t, e, "mail", "list", "--folder", "sent")
	if _, has := m["folder_copies"]; has {
		t.Fatalf("an unbounded list has no folder_copies: %v", m)
	}
}

// The empty-result note for --folder sent uses only the Sent copies: the current copy's range
// first, and an older copy that reaches further back is named.
func TestMailListEmptyNoteNamesAnOlderCopy(t *testing.T) {
	e := copyEnv(t)
	cases := []struct {
		daysAgo int
		want    string
	}{
		{15, "no message matched; the current cache covers only since 2026-08-31, and only an older copy of the folder, from a folder set Outlook replaced and no sync reads any more, reaches back to 2026-08-21: mail it did not hold from then is not in the archive"},
		{40, "no message matched; the current cache covers only since 2026-08-31, and an older copy of the folder, from a folder set Outlook replaced, reached back only to 2026-08-21: older mail is not in it"},
	}
	for _, tc := range cases {
		m := mailJSON(t, e, append([]string{"mail", "list", "--folder", "sent"}, dayFlags(tc.daysAgo)...)...)
		if m["note"] != tc.want {
			t.Fatalf("day %d: %v", tc.daysAgo, m["note"])
		}
	}
}

// mail folders and mail unread mark the older set's folders and count each message once.
func TestMailFoldersMarkOlderCopies(t *testing.T) {
	e := copyEnv(t)
	m := mailJSON(t, e, "mail", "folders")
	current := map[any]int{}
	for _, it := range items(t, m) {
		if it["kind"] == "sent" {
			current[it["current"]]++
			if it["messages"] != float64(2) {
				t.Fatalf("each Sent Items counts its messages once: %v", it)
			}
		}
	}
	if current[true] != 1 || current[false] != 1 {
		t.Fatalf("sent folders %v", current)
	}
	u := mailJSON(t, e, "mail", "unread")
	for _, f := range u["folders"].([]any) {
		if _, has := f.(map[string]any)["current"]; !has {
			t.Fatalf("%v", f)
		}
	}
}

// A failed copy check fails the list, like any other archive read.
func TestMailListCopyCheckFailureSurfaces(t *testing.T) {
	e := copyEnv(t)
	old := mailCopyChecks
	mailCopyChecks = func(*store.Store, context.Context, []store.MailCopyScope, store.MailCopyRange) ([]store.MailCopyCheck, error) {
		return nil, errors.New("injected")
	}
	t.Cleanup(func() { mailCopyChecks = old })
	if code, _, _ := e.mail(append([]string{"--json", "mail", "list", "--folder", "sent"}, dayFlags(3)...)...); code == 0 {
		t.Fatal("a failed copy check must fail the list")
	}
}
