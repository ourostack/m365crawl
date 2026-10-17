//go:build e2e

package e2e

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/teamsdesktop"
)

func TestDraftsOfflineProcess(t *testing.T) {
	e := newEnv(t)
	e.db = filepath.Join(filepath.Dir(e.db), "compose's $HOME ` %DRAFT_PATH% !DRAFT_PATH! ‘‚‛’ & 🌱.db")
	st, err := store.Open(context.Background(), e.db)
	if err != nil {
		t.Fatal(err)
	}
	u := map[string]any{"$undefined": true}
	c := map[string]any{"scheduledSendTime": nil, "isForwardedMessage": false, "forwardPrivateReply": false, "forwardAppIdsToInstall": "[]", "forwardMessageIds": "[]", "forwardMessageRecipients": "[]", "forwardMessageCardContents": "null", "forwardMessageCardsCount": nil, "forwardMessageOriginalConvId": nil, "forwardTeamId": nil}
	m := map[string]any{"id": "inner", "creator": "8:orgid:user", "type": "Message", "draftType": "RegularDraft", "threadType": "streamofdrafts", "messageType": "RichText/Html", "contentType": "Text", "state": 0, "isSentByCurrentUser": true, "content": "<p>authored synthetic compose</p>", "clientMessageId": "client", "lieType": "Send", "sendPipelineStatus": u, "sendPipelineOAMState": u, "streamingMetadata": u, "skypeeditedid": u, "originalNonLieMessage": u, "properties": map[string]any{"sendAt": u, "activity": map[string]any{"activityType": "Draft", "activitySubtype": "Draft", "sourceUserId": "8:orgid:user"}}, "draftDetails": map[string]any{"clientDraftProperties": c}}
	raw, err := json.Marshal(map[string]any{"conversationId": "wrapper", "replyChainId": "reply", "messageMap": map[string]any{"member": m}})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := st.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_, err = tx.UpsertRecords("P|https_teams.microsoft.com_0", []teamsdesktop.GenericRecord{{Account: &teamsdesktop.Account{TenantID: "tenant", UserID: "user"}, Database: "Teams:streams-replychain-manager:react-web-client:tenant:user:en", Store: "drafts", KeyJSON: []byte(`["wrapper","reply"]`), ValueJSON: raw}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = st.Close(); err != nil {
		t.Fatal(err)
	}
	res := e.runWith([]string{"M365CRAWL_TEAMS_ROOT=/not/a/live/source"}, "--db", e.db, "--max-age", "1ns", "drafts", "list")
	if res.code != 0 {
		t.Fatal(res.stderr)
	}
	doc := mustJSON(t, res.stdout)
	rows, ok := doc["items"].([]any)
	if !ok || len(rows) != 1 || doc["draft_coverage"] == nil {
		t.Fatal(doc)
	}
	item := rows[0].(map[string]any)
	shell := "posix"
	if runtime.GOOS == "windows" {
		shell = "powershell_7_3_standard"
	}
	if item["full_inspection_shell"] != shell {
		t.Fatal("follow-up dialect missing", item)
	}
	res = e.runWith(nil, "--db", e.db, "--max-text", "0", "drafts", "show", item["id"].(string), "--body-revision", item["body_revision"].(string))
	if res.code != 0 || !strings.Contains(res.stdout, "authored synthetic compose") {
		t.Fatal(res)
	}
	res = e.runWith(nil, "--db", e.db, "--max-text", "8", "drafts", "show", item["id"].(string))
	if res.code != 0 || !strings.Contains(res.stdout, `"text_truncated":true`) || !strings.Contains(res.stdout, `"html_truncated":true`) {
		t.Fatal(res)
	}
	db, err := sql.Open("sqlite", e.db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`update records set removed_at='2026-01-03'`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	res = e.runWith(nil, "--db", e.db, "drafts", "show", item["id"].(string))
	if res.code == 0 || !strings.Contains(res.stderr, "teams_draft_removed") {
		t.Fatal(res)
	}
	res = e.runWith(nil, "--db", e.db, "drafts", "list", "--include-removed")
	if res.code != 0 || !strings.Contains(res.stdout, `"archive_presence":"removed"`) || !strings.Contains(res.stdout, `"draft_coverage"`) {
		t.Fatal(res)
	}
	res = e.runWith(nil, "--db", e.db, "--account", "other/user", "drafts", "list")
	if res.code != 0 || !strings.Contains(res.stdout, `"count":0`) {
		t.Fatal(res)
	}
	res = e.runWith(nil, "--db", e.db, "--teams-root", "", "drafts", "list")
	if res.code == 0 {
		t.Fatal("explicit acquisition flag accepted")
	}
}
