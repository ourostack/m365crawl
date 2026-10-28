package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/ourostack/m365crawl/internal/outlookmail"
	"github.com/ourostack/m365crawl/internal/workiq"
)

func TestWorkIQMailFutureNativeFolderKeyCannotReplaceGraphFolder(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	attempt := seedWorkIQMailAttempt(t, s)
	_, _, err := s.IndexWorkIQMailCandidates(ctx, attempt, 0, 128)
	must0(err)
	root := stageWorkIQInventoryPage(t, s, attempt, "/me/mailFolders?includeHiddenFolders=true", map[string]any{"value": []any{map[string]any{"id": "server-folder", "displayName": "Server folder", "childFolderCount": 0}}})
	_, err = s.PrepareWorkIQMailPage(ctx, attempt, root, 128)
	must0(err)
	resource := "/me/mailFolders/server-folder/messages/delta()"
	page := stageWorkIQInventoryPage(t, s, attempt, resource, map[string]any{"value": []json.RawMessage{graphMailFixture("one", "server-folder", "one@example.test")}, "@odata.deltaLink": resource + "?$deltatoken=end"})
	_, err = s.PrepareWorkIQMailPage(ctx, attempt, page, 128)
	must0(err)
	must0(s.SealWorkIQMailInventory(ctx, attempt, mailT0))
	_ = must(s.CommitWorkIQMailGroup(ctx, attempt, "one@example.test"))
	var key, sourceFolder uint32
	must0(s.db.QueryRowContext(ctx, `select detail_key,folder_key from mail_messages`).Scan(&key, &sourceFolder))
	local := mailMsg(1000, sourceFolder, "other@example.test", "Other", 0)
	local.Folder = outlookmail.Folder{Key: sourceFolder, Kind: "other", Name: "Native unrelated"}
	b := mailBatch(mailT0, local)
	b.Result.Folders = append(b.Result.Folders, local.Folder)
	mustCommitMail(t, s, b)
	item := must(s.MailGet(ctx, mailAcct, key))
	if item.Folder != "Server folder" || item.FolderKey != sourceFolder {
		t.Fatal("future native numeric folder key replaced the already published Graph folder")
	}
	var nativeFolder uint32
	must0(s.db.QueryRowContext(ctx, `select folder_key from mail_messages where internet_message_id='other@example.test'`).Scan(&nativeFolder))
	if nativeFolder == sourceFolder {
		t.Fatal("unrelated native folder identity merged with a Graph folder")
	}
	before := dump(t, s)
	mustCommitMail(t, s, b)
	if dump(t, s) != before {
		t.Fatal("native folder-key alias replay changed stable IDs or clocks")
	}
}

func TestWorkIQMailFutureNativeAttachmentKeyCannotStealGraphMetadata(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	attempt := seedWorkIQMailAttempt(t, s)
	raw := graphMailFixture("one", "inbox-id", "one@example.test")
	var data map[string]any
	must0(json.Unmarshal(raw, &data))
	data["hasAttachments"] = true
	data["attachments"] = []any{map[string]any{"id": "graph-attachment", "name": "Server attachment", "size": 10}}
	prepareRealMailInventory(t, s, attempt, []json.RawMessage{must(json.Marshal(data))})
	_ = must(s.CommitWorkIQMailGroup(ctx, attempt, "one@example.test"))
	var sourceKey uint32
	must0(s.db.QueryRowContext(ctx, `select attachment_key from mail_attachments`).Scan(&sourceKey))
	local := mailMsg(100, fInbox, "<one@example.test>", "Subject", 0)
	local.Attachments = []outlookmail.Attachment{{Key: sourceKey, MessageKey: 100, Name: "Native attachment", Size: 20}}
	mustCommitMail(t, s, mailBatch(mailT0, local))
	var key uint32
	must0(s.db.QueryRowContext(ctx, `select detail_key from mail_messages`).Scan(&key))
	item := must(s.MailGet(ctx, mailAcct, key))
	if len(item.Attachments) != 2 || rowCount(t, s, `select count(*) from mail_attachments where attachment_key=? and name='Server attachment'`, sourceKey) != 1 {
		t.Fatal("future native attachment numeric key stole or silently dropped independent source metadata")
	}
	before := dump(t, s)
	mustCommitMail(t, s, mailBatch(mailT0, local))
	if dump(t, s) != before {
		t.Fatal("native attachment alias replay changed canonical identities")
	}
}

func TestWorkIQMailNativeMetadataFaultsKeepPublishedSourceIdentities(t *testing.T) {
	for _, attachment := range []bool{false, true} {
		t.Run(fmt.Sprint(attachment), func(t *testing.T) {
			var b MailBatch
			setup := func(t *testing.T, s *Store) {
				ctx := context.Background()
				attempt := seedWorkIQMailAttempt(t, s)
				if attachment {
					var data map[string]any
					must0(json.Unmarshal(graphMailFixture("one", "inbox-id", "one@example.test"), &data))
					data["attachments"] = []any{map[string]any{"id": "graph-attachment", "name": "Server attachment", "size": 10}}
					prepareRealMailInventory(t, s, attempt, []json.RawMessage{must(json.Marshal(data))})
					_ = must(s.CommitWorkIQMailGroup(ctx, attempt, "one@example.test"))
					var key uint32
					must0(s.db.QueryRowContext(ctx, `select attachment_key from mail_attachments`).Scan(&key))
					local := mailMsg(100, fInbox, "one@example.test", "Subject", 0)
					local.Attachments = []outlookmail.Attachment{{Key: key, MessageKey: 100, Name: "Native attachment"}}
					b = mailBatch(mailT0, local)
				} else {
					must0(s.StageWorkIQMailFolder(ctx, attempt, workiq.Folder{ID: "server-folder", Name: "Server folder"}))
					var key uint32
					must0(s.db.QueryRowContext(ctx, `select folder_key from workiq_folders`).Scan(&key))
					local := mailMsg(100, key, "other@example.test", "Other", 0)
					local.Folder = outlookmail.Folder{Key: key, Name: "Native folder", Kind: "other"}
					b = mailBatch(mailT0, local)
					b.Result.Folders = append(b.Result.Folders, local.Folder)
				}
			}
			sweepFaults(t, setup, func(ctx context.Context, s *Store) error { _, err := s.CommitMail(ctx, b); return err })
		})
	}
}

func TestWorkIQMailNativeKeyDomainsReserveWholeBatchAndOriginalRefs(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	must0(s.inTx(ctx, func(tx *sql.Tx) error {
		graph := must(mailSourceKey(ctx, tx, mailAcct, "workiq:folder", "1"))
		native := must(workIQNativeMailKey(ctx, tx, mailAcct, "folder", graph, 1000))
		if native <= 1000 || native == graph {
			t.Fatal("native allocation ignored batch high-water reservation")
		}
		if again := must(mailSourceKey(ctx, tx, mailAcct, "workiq:folder", "1")); again != graph {
			t.Fatal("same opaque Graph source ID crossed into native namespace")
		}
		if again := must(workIQNativeMailKey(ctx, tx, mailAcct, "folder", graph, 0)); again != native {
			t.Fatal("native folder reference did not retain its canonical key")
		}
		return nil
	}))
}

func TestWorkIQMailNativeParentReferenceAndBatchReservationStayIndependent(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	attempt := seedWorkIQMailAttempt(t, s)
	must0(s.StageWorkIQMailFolder(ctx, attempt, workiq.Folder{ID: "server-folder", Name: "Server folder"}))
	var graph uint32
	must0(s.db.QueryRowContext(ctx, `select folder_key from workiq_folders`).Scan(&graph))
	child := graph + 100
	b := mailBatch(mailT0)
	b.Result.RootKey = graph
	b.Result.Folders = append(b.Result.Folders, outlookmail.Folder{Key: child, Parent: graph, Name: "Native child", Kind: "other"})
	b.Result.Messages = []outlookmail.Message{mailMsg(1000, child, "child@example.test", "Child", 0)}
	mustCommitMail(t, s, b)
	var parent uint32
	must0(s.db.QueryRowContext(ctx, `select parent_key from mail_folders where account=? and name='Native child'`, mailAcct).Scan(&parent))
	if parent <= child || parent == graph {
		t.Fatal("unobserved native parent reference attached to the unrelated Graph folder or stole an input key")
	}
	b.Result.Folders = append(b.Result.Folders, outlookmail.Folder{Key: graph, Name: "Native parent", Kind: "other"})
	mustCommitMail(t, s, b)
	if rowCount(t, s, `select count(*) from mail_folders where account=? and folder_key=? and name='Native parent'`, mailAcct, parent) != 1 ||
		rowCount(t, s, `select count(*) from mail_folders where account=? and folder_key=? and name='Server folder'`, mailAcct, graph) != 1 {
		t.Fatal("later native parent materialization lost either namespace's original identity")
	}
}

func TestWorkIQMailNativeKeyCorruptLookupAndExhaustionFailClosed(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	must0(s.inTx(ctx, func(tx *sql.Tx) error { _, err := workIQNativeMailKey(ctx, tx, mailAcct, "folder", 7, 0); return err }))
	inj := injectFaults(t, s)
	before := dump(t, s)
	inj.arm("scan", 1)
	err := s.inTx(ctx, func(tx *sql.Tx) error { _, err := workIQNativeMailKey(ctx, tx, mailAcct, "folder", 7, 0); return err })
	if !inj.disarm() || err == nil || dump(t, s) != before {
		t.Fatal("NULL/corrupt native key lookup became a new identity or changed the archive")
	}
	must0(s.inTx(ctx, func(tx *sql.Tx) error {
		_, err := mailSourceKey(ctx, tx, mailAcct, "workiq:attachment", "reserved")
		return err
	}))
	before = dump(t, s)
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		var reserved uint32
		must0(tx.QueryRowContext(ctx, `select key from workiq_source_keys where kind='attachment' and namespace='graph'`).Scan(&reserved))
		_, err := workIQNativeMailKey(ctx, tx, mailAcct, "attachment", reserved, ^uint32(0))
		return err
	})
	if err == nil || dump(t, s) != before {
		t.Fatal("exhausted native collision allocation stole a published key")
	}
}
