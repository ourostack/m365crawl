package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/outlookmail"
	"github.com/ourostack/m365crawl/internal/workiq"
)

func seedWorkIQMailAttempt(t *testing.T, s *Store) int64 {
	t.Helper()
	ctx := context.Background()
	// A named archived folder makes the profile real without inventing a native identity.
	mustCommitMail(t, s, mailBatch(mailT0))
	must0(s.SetWorkIQEnabled(ctx, true))
	attempt := must(s.BeginWorkIQAttempt(ctx, WorkIQScope{Family: "mail", Account: mailAcct}, mailT0))
	verifyWorkIQTestAttempt(t, s, attempt)
	return attempt
}

func graphMailFixture(id, folder, header string) json.RawMessage {
	raw, _ := json.Marshal(map[string]any{
		"id": id, "parentFolderId": folder, "internetMessageId": header, "subject": "Subject",
		"sentDateTime": "2026-09-10T07:59:00Z", "receivedDateTime": "2026-09-10T08:00:00Z",
		"lastModifiedDateTime":         "2026-09-10T08:01:00Z",
		"from":                         map[string]any{"emailAddress": map[string]string{"name": "Ann Sender", "address": "ann@example.test"}},
		"body":                         map[string]string{"contentType": "html", "content": "<p>Server body</p>"},
		"@microsoft.graph.downloadUrl": "private-link",
	})
	return raw
}

func TestWorkIQMailStagingRetainsWholeRunCopiesBeforeAnyMerge(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	attempt := seedWorkIQMailAttempt(t, s)
	must0(s.StageWorkIQMailFolder(ctx, attempt, workiq.Folder{ID: "inbox-id", Name: "Inbox", WellKnown: "inbox"}))
	must0(s.StageWorkIQMailFolder(ctx, attempt, workiq.Folder{ID: "sent-id", Name: "Sent", WellKnown: "sent"}))
	must0(s.StageWorkIQMailItems(ctx, attempt, "inbox-id", []json.RawMessage{graphMailFixture("copy-a", "inbox-id", " <SAME@EXAMPLE.TEST> ")}, mailT0))
	must0(s.StageWorkIQMailItems(ctx, attempt, "sent-id", []json.RawMessage{graphMailFixture("copy-b", "sent-id", "same@example.test")}, mailT0))
	if rowCount(t, s, `select count(*) from workiq_census_members where normalized_id='same@example.test'`) != 2 ||
		rowCount(t, s, `select count(*) from mail_messages`) != 0 ||
		rowCount(t, s, `select count(*) from workiq_censuses where complete=0`) != 1 {
		t.Fatal("late-page duplicate evidence was lost, admitted early or falsely called complete")
	}
	if strings.Contains(dump(t, s), "private-link") {
		t.Fatal("mail staging retained an unscrubbed download URL")
	}
	before := dump(t, s)
	must0(s.StageWorkIQMailItems(ctx, attempt, "sent-id", []json.RawMessage{graphMailFixture("copy-b", "sent-id", "same@example.test")}, mailT0))
	if dump(t, s) != before {
		t.Fatal("replayed staging changed source observations")
	}
	before = dump(t, s)
	if err := s.StageWorkIQMailItems(ctx, attempt, "inbox-id", []json.RawMessage{graphMailFixture("wrong", "sent-id", "other")}, mailT0); err == nil ||
		dump(t, s) != before {
		t.Fatal("cross-folder data was retained or partially changed staging")
	}
}

func settleWorkIQMailFixture(t *testing.T, s *Store, attempt int64) {
	t.Helper()
	var after int64
	for {
		next, complete, err := s.IndexWorkIQMailCandidates(context.Background(), attempt, after, 128)
		must0(err)
		after = next
		if complete {
			break
		}
	}
	// Synthetic durable proof for canonical-unit tests. The production traversal
	// must independently prove its complete inventory before sealing a census.
	_, err := s.db.ExecContext(context.Background(), `update workiq_censuses set complete=1,completed_at=? where attempt_id=?`, fmtTime(mailT0), attempt)
	must0(err)
}

func TestWorkIQMailCanonicalAdmissionPreservesCacheFirstMetadataAndFirstID(t *testing.T) {
	for _, cacheFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "server-first", true: "cache-first"}[cacheFirst], func(t *testing.T) {
			s := newStore(t)
			ctx := context.Background()
			attempt := seedWorkIQMailAttempt(t, s)
			if cacheFirst {
				mustCommitMail(t, s, mailBatch(mailT0, mailMsg(100, fInbox, "<one@example.test>", "Subject", 0)))
			}
			must0(s.StageWorkIQMailFolder(ctx, attempt, workiq.Folder{ID: "inbox-id", Name: "Inbox", WellKnown: "inbox"}))
			must0(s.StageWorkIQMailItems(ctx, attempt, "inbox-id", []json.RawMessage{graphMailFixture("graph-one", "inbox-id", "one@example.test")}, mailT0))
			early := must(s.CommitWorkIQMailGroup(ctx, attempt, "one@example.test"))
			if !early.Pending || rowCount(t, s, `select count(*) from mail_messages`) != map[bool]int{false: 0, true: 1}[cacheFirst] {
				t.Fatal("uncertain group admitted before the whole-run identity census")
			}
			settleWorkIQMailFixture(t, s, attempt)
			result := must(s.CommitWorkIQMailGroup(ctx, attempt, "one@example.test"))
			if result.Added+result.Updated != 1 || rowCount(t, s, `select count(*) from mail_messages`) != 1 ||
				rowCount(t, s, `select count(*) from workiq_rows where kind='mail'`) != 1 {
				t.Fatal("server mail did not become one canonical source-aware row")
			}
			var key uint32
			must0(s.db.QueryRowContext(ctx, `select detail_key from mail_messages`).Scan(&key))
			item := must(s.MailGet(ctx, mailAcct, key))
			if cacheFirst && (key != 100 || item.SenderName != "Ann Sender" || item.BodyText != "body of Subject" ||
				item.InternetMessageID != "<one@example.test>" || len(item.Recipients) != 1) {
				t.Fatal("server arrival changed a cache-first public ID, original header, immutable metadata or conflicting text")
			}
			if !cacheFirst && (item.BodyText != "Server body" || item.InternetMessageID != "one@example.test") {
				t.Fatal("server-only mail projection was not available through existing archive reads")
			}
			before := dump(t, s)
			_ = must(s.CommitWorkIQMailGroup(ctx, attempt, "one@example.test"))
			if dump(t, s) != before {
				t.Fatal("already admitted mail group was not idempotent")
			}
		})
	}
}

func TestWorkIQMailCacheArrivalAndFutureNumericCollisionKeepPublishedIDs(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	attempt := seedWorkIQMailAttempt(t, s)
	must0(s.StageWorkIQMailFolder(ctx, attempt, workiq.Folder{ID: "inbox-id", Name: "Inbox", WellKnown: "inbox"}))
	must0(s.StageWorkIQMailItems(ctx, attempt, "inbox-id", []json.RawMessage{graphMailFixture("graph-one", "inbox-id", "one@example.test")}, mailT0))
	settleWorkIQMailFixture(t, s, attempt)
	_ = must(s.CommitWorkIQMailGroup(ctx, attempt, "one@example.test"))
	var public uint32
	must0(s.db.QueryRowContext(ctx, `select detail_key from mail_messages`).Scan(&public))
	mustCommitMail(t, s, mailBatch(mailT0, mailMsg(100, fInbox, "<one@example.test>", "Subject", 0)))
	if rowCount(t, s, `select count(*) from mail_messages`) != 1 ||
		rowCount(t, s, `select count(*) from workiq_rows where cache_seen=1`) != 1 {
		t.Fatal("server-first then cache arrival duplicated the canonical message or lost provenance")
	}
	alias := must(s.MailGet(ctx, mailAcct, 100))
	first := must(s.MailGet(ctx, mailAcct, public))
	if alias.Rowid != first.Rowid || alias.DetailKey != public {
		t.Fatal("cache counterpart ID stopped resolving to the first public ID")
	}
	// A later native key equal to the first server public ID belongs to another
	// item. It must allocate a different canonical key, never replace that ID.
	mustCommitMail(t, s, mailBatch(mailT0, mailMsg(100, fInbox, "<one@example.test>", "Subject", 0),
		mailMsg(public, fInbox, "<other@example.test>", "Different", 0)))
	first = must(s.MailGet(ctx, mailAcct, public))
	if first.InternetMessageID != "<one@example.test>" && first.InternetMessageID != "one@example.test" ||
		rowCount(t, s, `select count(*) from mail_messages`) != 2 {
		t.Fatal("future native numeric collision stole an already published server ID")
	}
	before := rowCount(t, s, `select count(*) from mail_messages`)
	mustCommitMail(t, s, mailBatch(mailT0, mailMsg(100, fInbox, "<one@example.test>", "Subject", 0),
		mailMsg(public, fInbox, "<other@example.test>", "Different", 0)))
	if rowCount(t, s, `select count(*) from mail_messages`) != before {
		t.Fatal("replayed native key reuse allocated another canonical item")
	}
}

func TestWorkIQMailAmbiguousAndForgedGroupsNeverMerge(t *testing.T) {
	for _, kind := range []string{"two-cache", "two-server", "forged-sender", "missing-id", "missing-sent"} {
		t.Run(kind, func(t *testing.T) {
			s := newStore(t)
			ctx := context.Background()
			attempt := seedWorkIQMailAttempt(t, s)
			native := []outlookmail.Message{mailMsg(100, fInbox, "<one@example.test>", "Subject", 0)}
			if kind == "two-cache" {
				native = append(native, mailMsg(101, fSent, "<one@example.test>", "Subject", 0))
			}
			mustCommitMail(t, s, mailBatch(mailT0, native...))
			must0(s.StageWorkIQMailFolder(ctx, attempt, workiq.Folder{ID: "inbox-id", WellKnown: "inbox"}))
			raw := graphMailFixture("graph-one", "inbox-id", "one@example.test")
			group := "one@example.test"
			switch kind {
			case "forged-sender":
				raw = []byte(strings.ReplaceAll(string(raw), "ann@example.test", "forged@example.test"))
			case "missing-sent":
				raw = []byte(strings.ReplaceAll(string(raw), `"sentDateTime":"2026-09-10T07:59:00Z",`, ""))
			case "missing-id":
				raw = graphMailFixture("graph-one", "inbox-id", "")
				group = ""
			}
			must0(s.StageWorkIQMailItems(ctx, attempt, "inbox-id", []json.RawMessage{raw}, mailT0))
			if kind == "two-server" {
				must0(s.StageWorkIQMailItems(ctx, attempt, "inbox-id", []json.RawMessage{graphMailFixture("graph-two", "inbox-id", "one@example.test")}, mailT0))
			}
			settleWorkIQMailFixture(t, s, attempt)
			result := must(s.CommitWorkIQMailGroup(ctx, attempt, group))
			want := 2
			if kind == "two-cache" || kind == "two-server" {
				want = 3
			}
			if rowCount(t, s, `select count(*) from mail_messages`) != want || len(result.Gaps) == 0 ||
				rowCount(t, s, `select count(*) from workiq_rows where cache_seen=1`) != 0 ||
				rowCount(t, s, `select count(*) from workiq_gaps where attempt_id=? and resolved=0`, attempt) == 0 {
				t.Fatal("ambiguous, absent-ID or uncorroborated mail merged into a native row")
			}
		})
	}
}

func TestWorkIQMailBoundUpdatesAndMovesKeepFirstCanonicalIdentity(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	attempt := seedWorkIQMailAttempt(t, s)
	must0(s.StageWorkIQMailFolder(ctx, attempt, workiq.Folder{ID: "inbox-id", WellKnown: "inbox"}))
	must0(s.StageWorkIQMailItems(ctx, attempt, "inbox-id", []json.RawMessage{graphMailFixture("graph-one", "inbox-id", "one@example.test")}, mailT0))
	settleWorkIQMailFixture(t, s, attempt)
	_ = must(s.CommitWorkIQMailGroup(ctx, attempt, "one@example.test"))
	var key uint32
	must0(s.db.QueryRowContext(ctx, `select detail_key from mail_messages`).Scan(&key))
	row := must(s.MailGet(ctx, mailAcct, key))
	for _, graphID := range []string{"graph-one", "moved-graph"} {
		run := must(s.BeginWorkIQAttempt(ctx, WorkIQScope{Family: "mail", Account: mailAcct}, mailT0))
		verifyWorkIQTestAttempt(t, s, run)
		must0(s.StageWorkIQMailFolder(ctx, run, workiq.Folder{ID: "archive-id", WellKnown: "archive"}))
		raw := graphMailFixture(graphID, "archive-id", "one@example.test")
		raw = []byte(strings.ReplaceAll(strings.ReplaceAll(string(raw), "Server body", "Updated body"), "2026-09-10T08:01:00Z", "2026-09-11T08:01:00Z"))
		must0(s.StageWorkIQMailItems(ctx, run, "archive-id", []json.RawMessage{raw}, mailT0.Add(time.Hour)))
		settleWorkIQMailFixture(t, s, run)
		_ = must(s.CommitWorkIQMailGroup(ctx, run, "one@example.test"))
		now := must(s.MailGet(ctx, mailAcct, key))
		if now.Rowid != row.Rowid || now.BodyText != "Updated body" || now.FolderKind != "archive" || !now.StateSeenAt.Equal(mailT0.Add(time.Hour)) ||
			rowCount(t, s, `select count(*) from mail_messages`) != 1 {
			t.Fatal("bound source update/move lost canonical identity, body or actual folder")
		}
	}
}

func TestWorkIQMailCacheOwnerContradictionAndUnchangedReplay(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	attempt := seedWorkIQMailAttempt(t, s)
	must0(s.StageWorkIQMailFolder(ctx, attempt, workiq.Folder{ID: "inbox-id", WellKnown: "inbox"}))
	must0(s.StageWorkIQMailItems(ctx, attempt, "inbox-id", []json.RawMessage{graphMailFixture("graph-one", "inbox-id", "one@example.test")}, mailT0))
	settleWorkIQMailFixture(t, s, attempt)
	_ = must(s.CommitWorkIQMailGroup(ctx, attempt, "one@example.test"))
	batch := mailBatch(mailT0, mailMsg(100, fInbox, "<one@example.test>", "Subject", 0))
	mustCommitMail(t, s, batch)
	before := dump(t, s)
	batch.ReadAt = mailT0.Add(time.Hour) // Same trusted store copy, no new native evidence.
	mustCommitMail(t, s, batch)
	if dump(t, s) != before {
		t.Fatal("an unchanged native replay fabricated new source/canonical clocks")
	}
	_, err := s.db.ExecContext(ctx, `insert into meta(key,value) values(?,?) on conflict(key) do update set value=excluded.value`,
		outlookIdentityKey+mailAcct, "contradictory@example.invalid")
	must0(err)
	before = dump(t, s)
	if _, err := s.CommitMail(ctx, batch); err == nil || dump(t, s) != before {
		t.Fatal("positive contradictory native owner facts were accepted or partially changed data")
	}
}

func TestWorkIQMailNativeBatchMultipleKeyCollisionsAreDistinct(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	attempt := seedWorkIQMailAttempt(t, s)
	must0(s.StageWorkIQMailFolder(ctx, attempt, workiq.Folder{ID: "inbox-id", WellKnown: "inbox"}))
	must0(s.StageWorkIQMailItems(ctx, attempt, "inbox-id", []json.RawMessage{graphMailFixture("a", "inbox-id", "a@test"), graphMailFixture("b", "inbox-id", "b@test")}, mailT0))
	settleWorkIQMailFixture(t, s, attempt)
	_ = must(s.CommitWorkIQMailGroup(ctx, attempt, "a@test"))
	_ = must(s.CommitWorkIQMailGroup(ctx, attempt, "b@test"))
	var first, second uint32
	must0(s.db.QueryRowContext(ctx, `select min(detail_key),max(detail_key) from mail_messages`).Scan(&first, &second))
	mustCommitMail(t, s, mailBatch(mailT0, mailMsg(first, fInbox, "<other-a@test>", "Other A", 0), mailMsg(second, fInbox, "<other-b@test>", "Other B", 0)))
	if rowCount(t, s, `select count(*) from mail_messages`) != 4 {
		t.Fatal("two native collisions reserved the same new canonical key")
	}
}

func TestWorkIQMailServerFillsEmptyCacheBodyWithoutChangingCacheMetadata(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	attempt := seedWorkIQMailAttempt(t, s)
	native := mailMsg(100, fInbox, "<one@example.test>", "Subject", 0)
	native.SenderName = "Retained local sender"
	native.Body = outlookmail.Body{State: outlookmail.BodyNone}
	mustCommitMail(t, s, mailBatch(mailT0, native))
	must0(s.StageWorkIQMailFolder(ctx, attempt, workiq.Folder{ID: "inbox-id", WellKnown: "inbox"}))
	must0(s.StageWorkIQMailItems(ctx, attempt, "inbox-id", []json.RawMessage{graphMailFixture("one", "inbox-id", "one@example.test")}, mailT0))
	settleWorkIQMailFixture(t, s, attempt)
	_ = must(s.CommitWorkIQMailGroup(ctx, attempt, "one@example.test"))
	row := must(s.MailGet(ctx, mailAcct, 100))
	if row.BodyText != "Server body" || row.SenderName != native.SenderName || row.IsRead == nil || *row.IsRead || len(row.Recipients) != 1 {
		t.Fatal("server failed to fill unknown body or overwrote cache-first immutable/read metadata")
	}
}

func TestWorkIQMailNativeAbsenceNeverClaimsServerOnlyRows(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	attempt := seedWorkIQMailAttempt(t, s)
	must0(s.StageWorkIQMailFolder(ctx, attempt, workiq.Folder{ID: "inbox-id", WellKnown: "inbox"}))
	must0(s.StageWorkIQMailItems(ctx, attempt, "inbox-id", []json.RawMessage{graphMailFixture("one", "inbox-id", "one@example.test")}, mailT0))
	settleWorkIQMailFixture(t, s, attempt)
	_ = must(s.CommitWorkIQMailGroup(ctx, attempt, "one@example.test"))
	other := mailMsg(200, fInbox, "<other@example.test>", "Older native mail", 2)
	mustCommitMail(t, s, mailBatch(mailT0.Add(time.Hour), other))
	mustCommitMail(t, s, mailBatch(mailT0.Add(2*time.Hour), other))
	if rowCount(t, s, `select count(*) from mail_messages where internet_message_id='one@example.test' and gone_at is null and evicted_at is null`) != 1 ||
		rowCount(t, s, `select count(*) from mail_absent where detail_key=(select detail_key from mail_messages where internet_message_id='one@example.test')`) != 0 {
		t.Fatal("native absence inferred a cache sighting or missing mark for a server-only row")
	}
}

func TestWorkIQMailPersistedFirstMissControlsLaterServerSighting(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	attempt := seedWorkIQMailAttempt(t, s)
	native := mailMsg(100, fInbox, "<one@example.test>", "Subject", 0)
	other := mailMsg(200, fInbox, "<other@example.test>", "Older", 2)
	mustCommitMail(t, s, mailBatch(mailT0, native, other))
	must0(s.StageWorkIQMailFolder(ctx, attempt, workiq.Folder{ID: "inbox-id", WellKnown: "inbox"}))
	must0(s.StageWorkIQMailItems(ctx, attempt, "inbox-id", []json.RawMessage{graphMailFixture("one", "inbox-id", "one@example.test")}, mailT0))
	settleWorkIQMailFixture(t, s, attempt)
	_ = must(s.CommitWorkIQMailGroup(ctx, attempt, "one@example.test"))
	miss := mailT0.Add(time.Hour)
	mustCommitMail(t, s, mailBatch(miss, other))
	mustCommitMail(t, s, mailBatch(miss.Add(time.Hour), other))
	if must(s.MailGet(ctx, mailAcct, 100)).GoneAt.IsZero() {
		var detail string
		must0(s.db.QueryRowContext(ctx, `select json_array(w.cache_seen,w.cache_first_miss_at,w.cache_gone_at,w.server_seen_at,m.gone_at,
(select misses from mail_absent a where a.account=m.account and a.detail_key=m.detail_key))
from workiq_rows w join mail_messages m on m.rowid=w.item_rowid where w.kind='mail'`).Scan(&detail))
		t.Fatal("an earlier server sighting suppressed a later native first miss; synthetic clocks/flags:", detail)
	}
	run := must(s.BeginWorkIQAttempt(ctx, WorkIQScope{Family: "mail", Account: mailAcct}, miss.Add(2*time.Hour)))
	verifyWorkIQTestAttempt(t, s, run)
	must0(s.StageWorkIQMailFolder(ctx, run, workiq.Folder{ID: "inbox-id", WellKnown: "inbox"}))
	must0(s.StageWorkIQMailItems(ctx, run, "inbox-id", []json.RawMessage{graphMailFixture("one", "inbox-id", "one@example.test")}, miss.Add(2*time.Hour)))
	settleWorkIQMailFixture(t, s, run)
	_ = must(s.CommitWorkIQMailGroup(ctx, run, "one@example.test"))
	if !must(s.MailGet(ctx, mailAcct, 100)).GoneAt.IsZero() ||
		rowCount(t, s, `select count(*) from workiq_rows where julianday(cache_first_miss_at)=julianday(?) and cache_gone_at is not null`, workIQTimeValue(&miss)) != 1 {
		t.Fatal("later live server receipt failed to suppress effective cache absence while retaining original evidence")
	}
}

func TestWorkIQMailCandidateIndexIsBoundedAndExact(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	mustCommitMail(t, s, mailBatch(mailT0, mailMsg(100, fInbox, "\t < ONE@Example.Test >\n", "Subject", 0),
		mailMsg(101, fInbox, "<other@test>", "Other", 0), mailMsg(102, fInbox, "<third@test>", "Third", 0)))
	attempt := seedWorkIQMailAttempt(t, s)
	after, done, err := s.IndexWorkIQMailCandidates(ctx, attempt, 0, 2)
	must0(err)
	if done || after != 2 || rowCount(t, s, `select count(*) from workiq_aliases where kind='mail_identity_index'`) != 2 {
		t.Fatal("candidate preparation ignored its bounded page or falsely finished")
	}
	after, done, err = s.IndexWorkIQMailCandidates(ctx, attempt, after, 2)
	must0(err)
	if !done || after != 3 {
		t.Fatal("bounded native identity preparation failed to resume to completion")
	}
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		ids, err := workIQNativeMailCandidates(ctx, tx, mailAcct, "one@example.test")
		if err == nil && (len(ids) != 1 || ids[0] != 1) {
			t.Fatal("candidate lookup approximated exact native header normalization")
		}
		return err
	})
	must0(err)
	before := dump(t, s)
	_, _, err = s.IndexWorkIQMailCandidates(ctx, attempt, 0, 0)
	if err == nil || dump(t, s) != before {
		t.Fatal("invalid index bounds mutated preparation state")
	}
}

func TestWorkIQMailMappedNativeHeaderStillPreventsAbsence(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	attempt := seedWorkIQMailAttempt(t, s)
	must0(s.StageWorkIQMailFolder(ctx, attempt, workiq.Folder{ID: "inbox-id", WellKnown: "inbox"}))
	must0(s.StageWorkIQMailItems(ctx, attempt, "inbox-id", []json.RawMessage{graphMailFixture("one", "inbox-id", "one@example.test")}, mailT0))
	settleWorkIQMailFixture(t, s, attempt)
	_ = must(s.CommitWorkIQMailGroup(ctx, attempt, "one@example.test"))
	other := mailMsg(200, fInbox, "<other@test>", "Older", 2)
	mustCommitMail(t, s, mailBatch(mailT0, mailMsg(100, fInbox, "<one@example.test>", "Subject", 0), other))
	for _, offset := range []time.Duration{time.Hour, 2 * time.Hour} {
		batch := mailBatch(mailT0.Add(offset), other)
		batch.RekeyAcrossFolders = true
		batch.Result.SeenDetailKeys = []uint32{100} // Header seen; detail temporarily unavailable.
		mustCommitMail(t, s, batch)
	}
	if !must(s.MailGet(ctx, mailAcct, 100)).GoneAt.IsZero() ||
		rowCount(t, s, `select count(*) from mail_absent a join mail_messages m on m.account=a.account and m.detail_key=a.detail_key
join workiq_aliases x on x.account_id=m.account and x.item_rowid=m.rowid where x.kind='mail' and x.namespace='public' and x.alias='100'`) != 0 {
		t.Fatal("a mapped native header was treated as absent because its canonical numeric key differs")
	}
}

func TestWorkIQMailIntegrationFaultsRollbackAllCanonicalAndSourceData(t *testing.T) {
	for _, mode := range []string{"folder", "items", "index", "canonical", "cache-arrival"} {
		t.Run(mode, func(t *testing.T) {
			var attempt int64
			setup := func(t *testing.T, s *Store) {
				ctx := context.Background()
				attempt = seedWorkIQMailAttempt(t, s)
				if mode != "folder" {
					must0(s.StageWorkIQMailFolder(ctx, attempt, workiq.Folder{ID: "inbox-id", ParentID: "root", WellKnown: "inbox"}))
				}
				if mode == "canonical" || mode == "cache-arrival" {
					must0(s.StageWorkIQMailItems(ctx, attempt, "inbox-id", []json.RawMessage{graphMailFixture("one", "inbox-id", "one@example.test")}, mailT0))
					settleWorkIQMailFixture(t, s, attempt)
					if mode == "cache-arrival" {
						_ = must(s.CommitWorkIQMailGroup(ctx, attempt, "one@example.test"))
					}
				}
			}
			sweepFaults(t, setup, func(ctx context.Context, s *Store) error {
				switch mode {
				case "folder":
					return s.StageWorkIQMailFolder(ctx, attempt, workiq.Folder{ID: "child", ParentID: "root", WellKnown: "inbox"})
				case "items":
					return s.StageWorkIQMailItems(ctx, attempt, "inbox-id", []json.RawMessage{graphMailFixture("one", "inbox-id", "one@example.test")}, mailT0)
				case "index":
					_, _, err := s.IndexWorkIQMailCandidates(ctx, attempt, 0, 128)
					return err
				case "canonical":
					_, err := s.CommitWorkIQMailGroup(ctx, attempt, "one@example.test")
					return err
				default:
					_, err := s.CommitMail(ctx, mailBatch(mailT0, mailMsg(100, fInbox, "<one@example.test>", "Subject", 0)))
					return err
				}
			})
		})
	}
}

func TestWorkIQMailGoneNeedsTwoDistinctGapFreeFullCensuses(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	attempt := seedWorkIQMailAttempt(t, s)
	must0(s.StageWorkIQMailFolder(ctx, attempt, workiq.Folder{ID: "inbox-id", WellKnown: "inbox"}))
	must0(s.StageWorkIQMailItems(ctx, attempt, "inbox-id", []json.RawMessage{graphMailFixture("one", "inbox-id", "one@example.test")}, mailT0))
	settleWorkIQMailFixture(t, s, attempt)
	_ = must(s.CommitWorkIQMailGroup(ctx, attempt, "one@example.test"))
	var key uint32
	must0(s.db.QueryRowContext(ctx, `select detail_key from mail_messages`).Scan(&key))
	for pass := 1; pass <= 2; pass++ {
		run := must(s.BeginWorkIQAttempt(ctx, WorkIQScope{Family: "mail", Account: mailAcct}, mailT0.Add(time.Duration(pass)*time.Hour)))
		verifyWorkIQTestAttempt(t, s, run)
		must0(s.StageWorkIQMailFolder(ctx, run, workiq.Folder{ID: "inbox-id", WellKnown: "inbox"}))
		must0(s.StageWorkIQMailItems(ctx, run, "inbox-id", nil, mailT0.Add(time.Duration(pass)*time.Hour)))
		settleWorkIQMailFixture(t, s, run)
		// Completing an incremental delta is NOT a fresh whole inventory.
		if err := s.ConfirmWorkIQMailCensus(ctx, run); err == nil {
			t.Fatal("incremental cursor completion was accepted as full absence proof")
		}
		_, err := s.db.ExecContext(ctx, `update workiq_censuses set full_enumeration=1,completed_at=? where attempt_id=?`,
			fmtTime(mailT0.Add(time.Duration(pass)*time.Hour)), run)
		must0(err)
		must0(s.ConfirmWorkIQMailCensus(ctx, run))
		before := dump(t, s)
		must0(s.ConfirmWorkIQMailCensus(ctx, run))
		if dump(t, s) != before {
			t.Fatal("same census replay created new absence evidence")
		}
		if gone := !must(s.MailGet(ctx, mailAcct, key)).GoneAt.IsZero(); gone != (pass == 2) {
			t.Fatal("server absence did not require two distinct full census IDs")
		}
	}

}

func TestWorkIQMailExplicitDeletionRetainsContentAndNeedsNewerOwnLiveVersion(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	attempt := seedWorkIQMailAttempt(t, s)
	must0(s.StageWorkIQMailFolder(ctx, attempt, workiq.Folder{ID: "inbox-id", WellKnown: "inbox"}))
	must0(s.StageWorkIQMailItems(ctx, attempt, "inbox-id", []json.RawMessage{graphMailFixture("one", "inbox-id", "one@example.test")}, mailT0))
	settleWorkIQMailFixture(t, s, attempt)
	_ = must(s.CommitWorkIQMailGroup(ctx, attempt, "one@example.test"))
	if rowCount(t, s, `select count(*) from pragma_table_info('mail_messages') where name='deleted_at'`) != 1 {
		t.Fatal("explicit server mail deletion has no effective public mark distinct from absence")
	}
	copy := func(raw json.RawMessage, at time.Time) {
		run := must(s.BeginWorkIQAttempt(ctx, WorkIQScope{Family: "mail", Account: mailAcct}, at))
		verifyWorkIQTestAttempt(t, s, run)
		must0(s.StageWorkIQMailFolder(ctx, run, workiq.Folder{ID: "inbox-id", WellKnown: "inbox"}))
		must0(s.StageWorkIQMailItems(ctx, run, "inbox-id", []json.RawMessage{raw}, at))
		settleWorkIQMailFixture(t, s, run)
		_ = must(s.CommitWorkIQMailGroup(ctx, run, "one@example.test"))
	}
	deleted := []byte(`{"id":"one","parentFolderId":"inbox-id","internetMessageId":"one@example.test","lastModifiedDateTime":"2026-09-11T08:00:00Z","deletedDateTime":"2026-09-11T08:00:00Z"}`)
	copy(deleted, mailT0.Add(24*time.Hour))
	if rowCount(t, s, `select count(*) from mail_messages where deleted_at is not null and gone_at is null and body_text='Server body'`) != 1 {
		t.Fatal("explicit server tombstone was confused with absence or blanked known content")
	}
	old := graphMailFixture("one", "inbox-id", "one@example.test")
	copy(old, mailT0.Add(25*time.Hour))
	if rowCount(t, s, `select count(*) from mail_messages where deleted_at is not null`) != 1 {
		t.Fatal("stale live server copy resurrected its own tombstone")
	}
	newer := []byte(strings.ReplaceAll(string(old), "2026-09-10T08:01:00Z", "2026-09-12T08:01:00Z"))
	copy(newer, mailT0.Add(48*time.Hour))
	if rowCount(t, s, `select count(*) from mail_messages where deleted_at is null`) != 1 ||
		rowCount(t, s, `select count(*) from workiq_rows where server_deleted_at is not null`) != 1 {
		t.Fatal("newer same-source live version failed to restore or erased retained deletion history")
	}
}
