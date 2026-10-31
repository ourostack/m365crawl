package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	crawlstore "github.com/openclaw/crawlkit/store"
	"github.com/ourostack/m365crawl/internal/workiq"
)

func setupWorkIQMailControl(t *testing.T, s *Store, cache bool) int64 {
	t.Helper()
	attempt := seedWorkIQMailAttempt(t, s)
	if cache {
		mustCommitMail(t, s, mailBatch(mailT0, mailMsg(100, fInbox, "<one@example.test>", "Subject", 0)))
	}
	must0(s.StageWorkIQMailFolder(context.Background(), attempt, workiq.Folder{ID: "inbox-id", WellKnown: "inbox"}))
	must0(s.StageWorkIQMailItems(context.Background(), attempt, "inbox-id", []json.RawMessage{graphMailFixture("one", "inbox-id", "one@example.test")}, mailT0))
	settleWorkIQMailFixture(t, s, attempt)
	return attempt
}

func TestWorkIQMailControlsRichGraphMappingKeepsUnknownsAndChildMetadata(t *testing.T) {
	for _, read := range []bool{false, true} {
		t.Run(map[bool]string{false: "unread", true: "read"}[read], func(t *testing.T) {
			s := newStore(t)
			ctx := context.Background()
			attempt := seedWorkIQMailAttempt(t, s)
			must0(s.StageWorkIQMailFolder(ctx, attempt, workiq.Folder{ID: "custom", ParentID: "root", Name: "Custom"}))
			raw := []byte(`{"id":"rich","parentFolderId":"custom","internetMessageId":"rich@test","conversationId":"thread",
"subject":"Subject","from":{"emailAddress":{"name":"Sender","address":"sender@test"}},"body":{"contentType":"text","content":"Plain <safe>"},
"hasAttachments":true,"toRecipients":[{"emailAddress":{"name":"To","address":"to@test"}}],
"ccRecipients":[{"emailAddress":{"address":"cc@test"}}],"bccRecipients":[{"emailAddress":{"address":"bcc@test"}}],
"attachments":[{"id":"attachment","name":"file.txt","contentType":"text/plain","size":12,"isInline":false}]}`)
			var payload map[string]any
			must0(json.Unmarshal(raw, &payload))
			payload["isRead"] = read
			raw = must(json.Marshal(payload))
			must0(s.StageWorkIQMailItems(ctx, attempt, "custom", []json.RawMessage{raw}, mailT0))
			settleWorkIQMailFixture(t, s, attempt)
			result := must(s.CommitWorkIQMailGroup(ctx, attempt, "rich@test"))
			var key uint32
			must0(s.db.QueryRowContext(ctx, `select detail_key from mail_messages`).Scan(&key))
			item := must(s.MailGet(ctx, mailAcct, key))
			if item.BodyText != "Plain <safe>" || item.IsRead == nil || *item.IsRead != read || item.ConversationID != "thread" ||
				len(item.Recipients) != 3 || len(item.Attachments) != 1 || item.Attachments[0].Name != "file.txt" ||
				!item.HasAttachments || !item.SentAt.IsZero() || item.FolderKind != "other" || result.Gaps["workiq_clock_unknown"] != 1 {
				t.Fatal("rich server projection lost typed child metadata or fabricated unknown clocks")
			}
		})
	}
}

func TestWorkIQMailControlsRejectInvalidStagingAndSourceScope(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	attempt := seedWorkIQMailAttempt(t, s)
	for _, f := range []workiq.Folder{
		{}, {ID: "same", ParentID: "same"}, {ID: "bad", WellKnown: "invented"},
		{ID: "bad", Raw: json.RawMessage(`{`)},
	} {
		before := dump(t, s)
		if err := s.StageWorkIQMailFolder(ctx, attempt, f); err == nil || dump(t, s) != before {
			t.Fatal("invalid folder projection was retained")
		}
	}
	must0(s.StageWorkIQMailFolder(ctx, attempt, workiq.Folder{ID: "folder"}))
	for _, value := range []struct {
		folder string
		raw    []json.RawMessage
		at     time.Time
	}{
		{"", nil, mailT0}, {"folder", nil, time.Time{}}, {"folder", []json.RawMessage{[]byte(`{`)}, mailT0},
		{"folder", []json.RawMessage{[]byte(strings.Repeat(" ", 16<<20+1))}, mailT0},
	} {
		before := dump(t, s)
		if err := s.StageWorkIQMailItems(ctx, attempt, value.folder, value.raw, value.at); err == nil || dump(t, s) != before {
			t.Fatal("invalid mail page mutated staging")
		}
	}
	must0(s.StageWorkIQMailItems(ctx, attempt, "folder", []json.RawMessage{[]byte(`{"id":"removed","@removed":{}}`)}, mailT0))
	settleWorkIQMailFixture(t, s, attempt)
	_ = must(s.CommitWorkIQMailGroup(ctx, attempt, ""))
	if rowCount(t, s, `select count(*) from mail_messages`) != 0 {
		t.Fatal("removed-only observation fabricated a canonical message")
	}
	if err := s.StageWorkIQMailItems(ctx, attempt, "folder", nil, mailT0); err == nil {
		t.Fatal("settled inventory accepted later pages")
	}
	if _, err := s.CommitWorkIQMailGroup(ctx, attempt, " <NOT-NORMALIZED> "); err == nil {
		t.Fatal("non-normalized identity group accepted")
	}
	chat := seedWorkIQConversation(t, s)
	if _, err := s.CommitWorkIQMailGroup(ctx, chat, ""); err == nil {
		t.Fatal("chat owner admitted mail")
	}
	if err := s.StageWorkIQMailFolder(ctx, chat, workiq.Folder{ID: "folder"}); err == nil {
		t.Fatal("chat attempt staged mail folders")
	}
	if err := s.StageWorkIQMailItems(ctx, chat, "folder", nil, mailT0); err == nil {
		t.Fatal("chat attempt staged mail")
	}
	if err := s.ConfirmWorkIQMailCensus(ctx, chat); err == nil {
		t.Fatal("chat attempt confirmed mail absence")
	}
	if _, err := s.CommitWorkIQMailGroup(ctx, -1, ""); err == nil {
		t.Fatal("unknown attempt admitted mail")
	}
	if err := s.ConfirmWorkIQMailCensus(ctx, -1); err == nil {
		t.Fatal("unknown attempt confirmed absence")
	}
	original := scrubWorkIQProjection
	scrubWorkIQProjection = func([]byte) ([]byte, int) { return []byte(`{`), 0 }
	t.Cleanup(func() { scrubWorkIQProjection = original })
	if err := s.StageWorkIQMailFolder(ctx, attempt, workiq.Folder{ID: "scrub"}); err == nil {
		t.Fatal("malformed scrubbed folder was retained")
	}
}

func TestWorkIQMailControlsMalformedCohortAndProjectionNeverPartiallyCommit(t *testing.T) {
	for _, edit := range []string{
		`update workiq_census_members set observed_at='bad'`,
		`update workiq_census_members set data_json='{'`,
		`update mail_messages set sent_at='bad'`,
	} {
		t.Run(edit, func(t *testing.T) {
			s := newStore(t)
			ctx := context.Background()
			attempt := setupWorkIQMailControl(t, s, true)
			_, err := s.db.ExecContext(ctx, edit)
			must0(err)
			before := dump(t, s)
			if _, err := s.CommitWorkIQMailGroup(ctx, attempt, "one@example.test"); err == nil || dump(t, s) != before {
				t.Fatal("corrupt cohort was accepted or partially committed")
			}
		})
	}
}

func TestWorkIQMailControlsPopulatedAndBoundFaultsRollback(t *testing.T) {
	for _, mode := range []string{"rich-source", "cache-first", "source-move", "bound-update", "native-collision", "header-presence", "populated-index", "gap-group", "census-first", "census-second"} {
		t.Run(mode, func(t *testing.T) {
			var attempt int64
			setup := func(t *testing.T, s *Store) {
				ctx := context.Background()
				attempt = setupWorkIQMailControl(t, s, mode == "cache-first")
				if mode == "populated-index" {
					mustCommitMail(t, s, mailBatch(mailT0, mailMsg(100, fInbox, "<native@test>", "Native", 0)))
				}
				if mode == "gap-group" {
					_, err := s.db.ExecContext(ctx, `update workiq_census_members set data_json=replace(data_json,'one@example.test','')`)
					must0(err)
				}
				if mode == "rich-source" {
					_, err := s.db.ExecContext(ctx, `update workiq_census_members set data_json=json_set(data_json,'$.attachments',json('[{"id":"att","name":"x.txt","size":1}]'))`)
					must0(err)
				}
				if mode == "bound-update" || mode == "source-move" || mode == "native-collision" || mode == "header-presence" || strings.HasPrefix(mode, "census-") {
					_ = must(s.CommitWorkIQMailGroup(ctx, attempt, "one@example.test"))
				}
				if mode == "header-presence" {
					mustCommitMail(t, s, mailBatch(mailT0, mailMsg(100, fInbox, "<one@example.test>", "Subject", 0)))
				}
				if mode == "bound-update" || mode == "source-move" {
					attempt = must(s.BeginWorkIQAttempt(ctx, WorkIQScope{Family: "mail", Account: mailAcct}, mailT0.Add(time.Hour)))
					verifyWorkIQTestAttempt(t, s, attempt)
					must0(s.StageWorkIQMailFolder(ctx, attempt, workiq.Folder{ID: "inbox-id", WellKnown: "inbox"}))
					id := "one"
					if mode == "source-move" {
						id = "moved"
					}
					must0(s.StageWorkIQMailItems(ctx, attempt, "inbox-id", []json.RawMessage{graphMailFixture(id, "inbox-id", "one@example.test")}, mailT0.Add(time.Hour)))
					settleWorkIQMailFixture(t, s, attempt)
				}
				if strings.HasPrefix(mode, "census-") {
					passes := 1
					if mode == "census-second" {
						passes = 2
					}
					for pass := 1; pass <= passes; pass++ {
						attempt = must(s.BeginWorkIQAttempt(ctx, WorkIQScope{Family: "mail", Account: mailAcct}, mailT0.Add(time.Duration(pass)*time.Hour)))
						verifyWorkIQTestAttempt(t, s, attempt)
						must0(s.StageWorkIQMailFolder(ctx, attempt, workiq.Folder{ID: "inbox-id", WellKnown: "inbox"}))
						must0(s.StageWorkIQMailItems(ctx, attempt, "inbox-id", nil, mailT0))
						settleWorkIQMailFixture(t, s, attempt)
						_, err := s.db.ExecContext(ctx, `update workiq_censuses set full_enumeration=1,completed_at=? where attempt_id=?`, fmtTime(mailT0.Add(time.Duration(pass)*time.Hour)), attempt)
						must0(err)
						if pass < passes {
							must0(s.ConfirmWorkIQMailCensus(ctx, attempt))
						}
					}
				}
			}
			sweepFaults(t, setup, func(ctx context.Context, s *Store) error {
				switch mode {
				case "populated-index":
					_, _, err := s.IndexWorkIQMailCandidates(ctx, attempt, 0, 128)
					return err
				case "native-collision":
					_, err := s.CommitMail(ctx, mailBatch(mailT0, mailMsg(1, fInbox, "<unrelated@test>", "Unrelated", 0)))
					return err
				case "header-presence":
					b := mailBatch(mailT0)
					b.Result.SeenDetailKeys = []uint32{100}
					b.RekeyAcrossFolders = true
					_, err := s.CommitMail(ctx, b)
					return err
				case "census-first", "census-second":
					return s.ConfirmWorkIQMailCensus(ctx, attempt)
				default:
					_, err := s.CommitWorkIQMailGroup(ctx, attempt, "one@example.test")
					return err
				}
			})
		})
	}
}

func TestWorkIQMailControlsMigratesActualMissingDeletedColumn(t *testing.T) {
	ctx := context.Background()
	legacyDDL := strings.Replace(schemaDDL, workiqSchemaDDL, "", 1)
	legacyDDL = strings.Replace(legacyDDL, "  deleted_at text,\n  unique(account, detail_key)", "  unique(account, detail_key)", 1)
	path := writableArchivePath(t)
	cs := must(crawlstore.Open(ctx, crawlstore.Options{Path: path, Schema: legacyDDL, SchemaVersion: 20}))
	var absent int
	must0(cs.DB().QueryRowContext(ctx, `select count(*) from pragma_table_info('mail_messages') where name='deleted_at'`).Scan(&absent))
	if absent != 0 {
		t.Fatal("legacy fixture accidentally included the new source deletion column")
	}
	must0(cs.Close())
	s := must(Open(ctx, path))
	defer func() { _ = s.Close() }()
	if rowCount(t, s, `select count(*) from pragma_table_info('mail_messages') where name='deleted_at'`) != 1 {
		t.Fatal("actual old schema did not gain nullable explicit deletion")
	}
}
