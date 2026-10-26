package cli

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/store"
)

func TestOfflineMailSearchWithoutLegacyMetaRemainsReadOnly(t *testing.T) {
	e := newEnv(t)
	e.emptyArchive()
	e.exec("drop table meta")
	before, err := os.ReadFile(e.db)
	if err != nil {
		t.Fatal(err)
	}
	doc := mailJSON(t, e, "search", "fixture", "--source", "mail")
	if doc["count"] != float64(0) || doc["notices"] != nil || doc["needs_sync"] != true {
		t.Fatalf("old archive result: %v", doc)
	}
	after, err := os.ReadFile(e.db)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("offline read changed the old archive")
	}
}

func TestArchivedMailFailureSurvivesOfflineReads(t *testing.T) {
	e := mailEnv(t)
	st, err := store.Open(context.Background(), e.db)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetMailFailure(context.Background(), mailAccount, store.OutlookFailure{
		Code: "outlook_mail_layout_unsupported", Message: "Fixture layout moved", Fix: "Update m365crawl.",
	}); err != nil {
		t.Fatal(err)
	}
	_ = st.Close()
	for _, args := range [][]string{
		{"mail", "list"}, {"mail", "list", "--since", "2099-01-01", "--fields", "id"},
		{"mail", "folders"}, {"mail", "unread"}, {"mail", "show", mailAccount + ":101"},
		{"mail", "thread", mailAccount + ":101"}, {"search", "no-match", "--source", "mail", "--fields", "id"},
	} {
		doc := mailJSON(t, e, args...)
		notices, _ := doc["notices"].([]any)
		if len(notices) != 1 || !strings.Contains(notices[0].(string), "outlook_mail_layout_unsupported") ||
			!strings.Contains(notices[0].(string), "may be stale") {
			t.Errorf("%v hid the stored mail failure: %v", args, doc["notices"])
		}
	}
	for _, args := range [][]string{
		{"--account", "outlook/Other", "mail", "list"}, {"search", "no-match", "--source", "chats"},
	} {
		doc := mailJSON(t, e, args...)
		if doc["notices"] != nil {
			t.Errorf("%v inherited an unrelated mail failure: %v", args, doc["notices"])
		}
	}
	code, out, errOut := e.mail("--format", "text", "mail", "list")
	if code != 0 || !strings.Contains(out, "may be stale") {
		t.Fatalf("text read hid failure: %d %s %s", code, out, errOut)
	}
}
