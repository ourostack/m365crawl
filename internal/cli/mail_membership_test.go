package cli

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/outlookmail"
	"github.com/ourostack/m365crawl/internal/store"
)

// A message sent to oneself, shown from Inbox with a Sent Items copy: --folder sent lists it,
// folders names both, and the unread note of --folder inbox --unread counts Inbox only (#270).
func TestMailListFindsAMessageThroughEveryFolderItIsIn(t *testing.T) {
	e := textEnv(t)
	self := mailMsg(201, fInbox, "<self@x.test>", "", "Note to self", 2, true)
	read, unread := false, true
	self.FolderCopies = []outlookmail.FolderCopy{{Key: fInbox, Unread: &unread}, {Key: fSent, Unread: &read}, {Key: fProjects, Unread: &unread}}
	seedMail(t, e, []outlookmail.Message{self, mailMsg(202, fSent, "<s@x.test>", "", "Sent", 1, false)})

	its := items(t, mailJSON(t, e, "mail", "list", "--folder", "sent"))
	if got := strings.Join(ids(its), " "); got != "outlook/Main:202 outlook/Main:201" {
		t.Fatalf("ids %s", got)
	}
	it := its[1]
	folders, _ := it["folders"].([]any)
	if it["folder_kind"] != "inbox" || len(folders) != 3 ||
		folders[0].(map[string]any)["folder_kind"] != "inbox" || folders[1].(map[string]any)["folder"] != "Sent Items" || folders[2].(map[string]any)["folder_kind"] != "other" {
		t.Fatalf("item %v", it)
	}
	if folders[0].(map[string]any)["is_read"] != false || folders[1].(map[string]any)["is_read"] != true {
		t.Fatalf("per-copy read state %v", folders)
	}
	if one := items(t, mailJSON(t, e, "mail", "list", "--folder", "sent"))[0]["folders"].([]any); len(one) != 1 || one[0].(map[string]any)["is_read"] != true {
		t.Fatalf("a message in one folder carries its own read state: %v", one)
	}
	if its := items(t, mailJSON(t, e, "mail", "list", "--folder", "sent", "--unread")); len(its) != 0 {
		t.Fatalf("read in Sent Items, listed unread: %v", ids(its))
	}
	m := mailJSON(t, e, "mail", "list", "--folder", "inbox", "--unread")
	if got := strings.Join(ids(items(t, m)), " "); got != "outlook/Main:201" || !strings.Contains(m["note"].(string), "holds 1 message in this folder") {
		t.Fatalf("ids %s, note %v", got, m["note"])
	}
	show := mailJSON(t, e, "mail", "show", "outlook/Main:201")
	if f, _ := show["folders"].([]any); len(f) != 3 {
		t.Fatalf("show %v", show)
	}
	// Unread in Inbox and in Projects: two folders count it, the total counts it once.
	u := mailJSON(t, e, "mail", "unread")
	if u["unread_total"] != float64(1) {
		t.Fatalf("unread_total %v, folders %v", u["unread_total"], u["folders"])
	}
	unreadIn := map[string]float64{}
	for _, f := range u["folders"].([]any) {
		unreadIn[f.(map[string]any)["folder"].(string)] = f.(map[string]any)["unread"].(float64)
	}
	if unreadIn["Inbox"] != 1 || unreadIn["Projects"] != 1 || unreadIn["Sent Items"] != 0 {
		t.Fatalf("folders %v", unreadIn)
	}
}

func TestMailUnreadTotalFailure(t *testing.T) {
	e := mailEnv(t)
	old := mailCount
	mailCount = func(*store.Store, context.Context, store.MailFilter) (int, error) { return 0, errors.New("boom") }
	t.Cleanup(func() { mailCount = old })
	mailFails(t, e, "db_error", 1, "mail", "unread")
}
