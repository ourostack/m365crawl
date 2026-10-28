package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/store"
)

func TestWorkIQPublicSourceFieldsSurviveActualCommands(t *testing.T) {
	e := mailEnv(t)
	e.sync()
	var snapshots []map[string]any
	for _, origin := range []string{"desktop_cache", "both"} {
		if origin == "both" {
			// Existing synthetic source bindings exercise output, not admission or
			// qualification of a production native identity class.
			e.exec(`insert into workiq_rows(kind,account_id,item_rowid,graph_id,server_json,cache_first,cache_seen,winner,modified_at)
select 'message',tenant_id||'/'||user_id,rowid,id,'{}',1,1,'desktop_cache','2026-09-10T08:00:00Z' from messages;
insert into workiq_rows(kind,account_id,item_rowid,graph_id,server_json,cache_first,cache_seen,winner,modified_at)
select 'conversation',tenant_id||'/'||user_id,rowid,id,'{}',1,1,'desktop_cache','2026-09-10T08:00:00Z' from conversations;
insert into workiq_rows(kind,account_id,item_rowid,graph_id,server_json,cache_first,cache_seen,winner,modified_at)
select 'mail',account,rowid,cast(detail_key as text),'{}',1,1,'desktop_cache','2026-09-10T08:00:00Z' from mail_messages`)
		}
		for _, args := range [][]string{
			{"messages"}, {"unread"}, {"conversations"},
			{"search", "Fixture", "--source", "chats"},
			{"search", "Quarterly", "--source", "mail"},
			{"mail", "list"}, {"mail", "unread"},
			{"mail", "thread", mailAccount + ":101"},
		} {
			code, out, stderr := e.mail(append([]string{"--json", "--fields", "provenance"}, args...)...)
			if code != 0 {
				t.Fatalf("source field %v exit %d: %s", args, code, stderr)
			}
			rows := items(t, decode(t, out))
			if len(rows) == 0 {
				t.Fatalf("source field %v lost all fixture items", args)
			}
			for _, row := range rows {
				if len(row) != 1 || row["provenance"] != origin {
					t.Fatalf("source origin lost or search.source reinterpreted: %v", row)
				}
			}
			snapshots = append(snapshots, map[string]any{"case": origin, "command": strings.Join(args, " "), "item": rows[0]})
		}
		doc := mailJSON(t, e, "mail", "show", mailAccount+":101")
		if doc["provenance"] != origin {
			t.Fatal("mail show lost source provenance")
		}
		snapshots = append(snapshots, map[string]any{"case": origin, "command": "mail show", "provenance": doc["provenance"], "deleted_at": doc["deleted_at"]})
	}
	raw, err := json.MarshalIndent(snapshots, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	checkGolden(t, "workiq-provenance.json", string(raw)+"\n")
}

func TestWorkIQNestedAndSharedMailKeepEffectiveSourceFields(t *testing.T) {
	at := mailT0
	row := store.MailRow{Account: mailAccount, DetailKey: 101, Provenance: "workiq_server", DeletedAt: at}
	values := []any{listItemOf(row, 0), showItemOf(row, 0), searchMailItemOf(row, 0),
		relatedMailOf([]store.MailRelated{{MailRow: row, Match: "invite"}})[0]}
	for _, value := range values {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		item := decode(t, string(raw))
		if item["provenance"] != "workiq_server" || item["deleted_at"] != "2026-09-10T08:00:00Z" {
			t.Fatal("shared/nested mail projection lost its source or effective deletion")
		}
		if source, ok := item["source"]; ok && source != "mail" {
			t.Fatal("shared-search source was replaced by acquisition provenance")
		}
	}
}
