package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/teamsdesktop"
	"github.com/ourostack/m365crawl/internal/workiq"
)

func TestWorkIQServerSurvivesCacheRederiveAndApplicationBackfill(t *testing.T) {
	for _, mixed := range []bool{false, true} {
		t.Run(map[bool]string{false: "server-only", true: "mixed-server-winner"}[mixed], func(t *testing.T) {
			s := newStore(t)
			ctx := context.Background()
			must0(s.ApplyAccount(ctx, acctA))
			message := msg(acctA, "chat", "one", "server retained", base)
			message.Raw = nil
			conversation := conv(acctA, "chat", "Chat", "server room")
			conversation.Raw = nil
			if mixed {
				message.Raw = []byte(`{"id":"one","messageType":"RichText/Html","content":"<p>cache reparse</p>","imDisplayName":"Cache Name"}`)
				conversation.Raw = []byte(`{}`)
			}
			_ = must(s.ApplyConversations(ctx, []teamsdesktop.Conversation{conversation}))
			_ = must(s.ApplyMessages(ctx, []teamsdesktop.Message{message}))
			messageRowid := rowCount(t, s, `select rowid from messages`)
			conversationRowid := rowCount(t, s, `select rowid from conversations`)
			account := acctA.TenantID + "/" + acctA.UserID
			for _, row := range []struct {
				kind, graphID, raw string
				id                 int
			}{
				{"message", "one", `{"id":"one","messageType":"message","createdDateTime":"2026-09-01T12:00:00Z","lastModifiedDateTime":"2026-09-01T12:01:00Z","body":{"contentType":"text","content":"server retained"}}`, messageRowid},
				{"conversation", "chat", `{"id":"chat","tenantId":"tenant-1","chatType":"group","topic":"server room"}`, conversationRowid},
			} {
				// This is an existing synthetic source binding, not permission to qualify a
				// production native identity class or ingest a new server message.
				_, err := s.db.ExecContext(ctx, `insert into workiq_rows(kind,account_id,item_rowid,graph_id,server_json,cache_first,cache_seen,server_seen_at,winner,modified_at)
values(?,?,?,?,?,?,?,?,'workiq_server',?)`, row.kind, account, row.id, row.graphID, row.raw, b2i(mixed), b2i(mixed), fmtTime(base), fmtTime(base))
				must0(err)
			}
			beforeApp := rowCount(t, s, `select count(*) from message_app_observations`)
			_, err := s.db.ExecContext(ctx, `insert into meta(key,value) values(?, '1'),(?, '0')
on conflict(key) do update set value=excluded.value`, derivationKey, messageAppKey)
			must0(err)
			_ = must(s.Rederive(ctx))
			backfill := must(s.BackfillMessageApplications(ctx))
			var text, name string
			must0(s.db.QueryRowContext(ctx, `select content_text from messages where rowid=?`, messageRowid).Scan(&text))
			must0(s.db.QueryRowContext(ctx, `select display_name from conversations where rowid=?`, conversationRowid).Scan(&name))
			if text != "server retained" || name != "server room" || backfill.Processed != 0 ||
				rowCount(t, s, `select count(*) from message_app_observations`) != beforeApp {
				t.Fatal("cache-shaped rederive/backfill changed a server winner or claimed source applicability")
			}
			if rowCount(t, s, `select count(*) from message_fts where rowid=? and message_fts match 'server'`, messageRowid) != 1 ||
				rowCount(t, s, `select count(*) from workiq_rows where server_seen_at=?`, fmtTime(base)) != 2 {
				t.Fatal("maintenance lost FTS ownership or fabricated source evidence")
			}
			var retained sql.NullString
			must0(s.db.QueryRowContext(ctx, `select raw_json from messages where rowid=?`, messageRowid).Scan(&retained))
			if retained.String != string(message.Raw) || retained.Valid != (len(message.Raw) > 0) {
				t.Fatal("maintenance changed the cache raw payload of a server winner")
			}
		})
	}
}

func TestWorkIQServerMailSurvivesCacheTextRebuild(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	mustCommitMail(t, s, mailBatch(mailT0, mailMsg(21, fInbox, "<one@test.invalid>", "Server text", 0)))
	_, err := s.db.ExecContext(ctx, `update mail_messages set body_html_gz=null,body_text='server plain  body',body_text_version=0,modified_at=?`, fmtTime(base))
	must0(err)
	_, err = s.db.ExecContext(ctx, `insert into workiq_rows(kind,account_id,item_rowid,graph_id,server_json,cache_first,cache_seen,server_seen_at,winner,modified_at)
select 'mail',account,rowid,'server-mail','{"id":"server-mail","body":{"contentType":"text","content":"server plain  body"}}',0,0,?,'workiq_server',? from mail_messages`,
		fmtTime(base), fmtTime(base))
	must0(err)
	_, err = s.db.ExecContext(ctx, `update meta set value='0' where key=?`, mailTextKey)
	must0(err)
	_ = must(s.Rederive(ctx))
	var text, modified string
	var nativeVersion int
	must0(s.db.QueryRowContext(ctx, `select body_text,body_text_version,modified_at from mail_messages`).Scan(&text, &nativeVersion, &modified))
	if text != "server plain  body" || nativeVersion != 0 || modified != fmtTime(base) {
		t.Fatal("cache-only mail reconstruction claimed server projection versioning or changed source data")
	}
}

func seedWorkIQBoundMessage(t *testing.T, s *Store, mixed bool) (teamsdesktop.Message, int) {
	t.Helper()
	ctx := context.Background()
	must0(s.ApplyAccount(ctx, acctA))
	account := acctA.TenantID + "/" + acctA.UserID
	projected := must(workiq.DecodeChatMessage(ctx,
		workiq.AccountFact{Family: "chats", TenantID: acctA.TenantID, UserID: acctA.UserID},
		workiq.Chat{ID: "chat", TenantID: acctA.TenantID},
		[]byte(`{"id":"one","messageType":"message","createdDateTime":"2026-09-01T12:00:00Z","lastModifiedDateTime":"2026-09-01T12:01:00Z","body":{"contentType":"text","content":"server retained"}}`)))
	cached := msg(acctA, "chat", "one", "cache retained", base)
	cached.Version = projected.Native.Version - 1000
	cached.Raw = []byte(`{"id":"one","messageType":"RichText/Html","content":"<p>cache retained</p>"}`)
	canonical := projected.Native
	var cacheProjection, cacheVersion any
	if mixed {
		canonical.Raw = cached.Raw
		cacheProjection = string(must(json.Marshal(cached)))
		cacheVersion = cached.Version
	}
	_ = must(s.ApplyMessages(ctx, []teamsdesktop.Message{canonical}))
	rowid := rowCount(t, s, `select rowid from messages`)
	_, err := s.db.ExecContext(ctx, `insert into workiq_rows(kind,account_id,item_rowid,graph_id,server_json,server_projection,cache_projection,
cache_first,cache_seen,cache_version,server_version,server_seen_at,winner,modified_at)
values('message',?,?,'one',?,?,?,?,?,?,?,?, 'workiq_server',?)`,
		account, rowid, string(projected.Raw), string(must(json.Marshal(projected))), cacheProjection,
		b2i(mixed), b2i(mixed), cacheVersion, projected.ModifiedAt.Format(time.RFC3339Nano), fmtTime(base), fmtTime(base))
	must0(err)
	return cached, rowid
}

func TestWorkIQBoundMessageUsesItsOwnCacheVersionAndDoesNotPublishServerApps(t *testing.T) {
	if workIQVersionsComparable(string(workiq.ClassChat)) {
		t.Fatal("unverified native clocks became comparable in production")
	}
	for _, tc := range []struct {
		name                    string
		mixed, qualified, blank bool
		wantWinner              string
	}{
		{"first empty cache copy", false, false, true, "workiq_server"},
		{"qualified older cache", true, true, false, "workiq_server"},
		{"unqualified cache precedence", true, false, false, "desktop_cache"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			originalPolicy := workIQVersionsComparable
			workIQVersionsComparable = func(class string) bool { return tc.qualified && class == string(workiq.ClassChat) }
			t.Cleanup(func() { workIQVersionsComparable = originalPolicy })
			s := newStore(t)
			ctx := context.Background()
			account := acctA.TenantID + "/" + acctA.UserID
			cached, rowid := seedWorkIQBoundMessage(t, s, tc.mixed)
			beforeApps := rowCount(t, s, `select count(*) from message_app_observations`)
			incoming := cached
			incoming.Version++
			if tc.blank {
				incoming.ContentText, incoming.ContentHTML = "", ""
				incoming.Raw = []byte(`{"id":"one","messageType":"RichText/Html","content":""}`)
			}
			_ = must(s.ApplyMessages(ctx, []teamsdesktop.Message{incoming}))
			var text, winner string
			var cacheSeen int64
			var observedVersion sql.NullInt64
			must0(s.db.QueryRowContext(ctx, `select content_text from messages where rowid=?`, rowid).Scan(&text))
			must0(s.db.QueryRowContext(ctx, `select cache_seen,cache_version,winner from workiq_rows where kind='message' and account_id=? and item_rowid=?`,
				account, rowid).Scan(&cacheSeen, &observedVersion, &winner))
			wantText := "server retained"
			if tc.wantWinner == "desktop_cache" {
				wantText = "cache retained"
			}
			if cacheSeen != 1 || !observedVersion.Valid || observedVersion.Int64 != incoming.Version || winner != tc.wantWinner || text != wantText ||
				rowCount(t, s, `select count(*) from messages`) != 1 {
				t.Fatal("cache appearance used a server clock, duplicated identity or lost source-aware content precedence")
			}
			if tc.wantWinner == "workiq_server" && rowCount(t, s, `select count(*) from message_app_observations`) != beforeApps {
				t.Fatal("a server-winning cache update published cache application applicability")
			}
			beforeRepeat := dump(t, s)
			_ = must(s.ApplyMessages(ctx, []teamsdesktop.Message{incoming}))
			if dump(t, s) != beforeRepeat {
				t.Fatal("an unchanged full-read changed source data or fabricated another sighting")
			}
		})
	}
}

func TestWorkIQBoundMessageFaultsRollbackBothSourceAndCanonicalData(t *testing.T) {
	var incoming teamsdesktop.Message
	setup := func(t *testing.T, s *Store) {
		incoming, _ = seedWorkIQBoundMessage(t, s, true)
		incoming.Version++
	}
	sweepFaults(t, setup, func(ctx context.Context, s *Store) error {
		_, err := s.ApplyMessages(ctx, []teamsdesktop.Message{incoming})
		return err
	})
}

func TestWorkIQBoundMessageOwnDeletionAndUnqualifiedServerDeletion(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	incoming, rowid := seedWorkIQBoundMessage(t, s, true)
	incoming.Version++
	incoming.DeletedAt = base.Add(time.Hour)
	_ = must(s.ApplyMessages(ctx, []teamsdesktop.Message{incoming}))
	if rowCount(t, s, `select count(*) from messages where rowid=? and deleted_at is not null`, rowid) != 1 {
		t.Fatal("a newer server copy cleared an unqualified cache tombstone")
	}
	// A tied live version cannot restore its own source.
	incoming.DeletedAt = time.Time{}
	_ = must(s.ApplyMessages(ctx, []teamsdesktop.Message{incoming}))
	if rowCount(t, s, `select count(*) from messages where rowid=? and deleted_at is not null`, rowid) != 1 {
		t.Fatal("equal cache version restored a tombstone")
	}
	incoming.Version++
	_ = must(s.ApplyMessages(ctx, []teamsdesktop.Message{incoming}))
	if rowCount(t, s, `select count(*) from messages where rowid=? and deleted_at is null`, rowid) != 1 ||
		rowCount(t, s, `select count(*) from workiq_rows where cache_deleted_at is not null`) != 1 {
		t.Fatal("same-source restoration lost historical deletion or failed to restore")
	}
	serverDelete := base.Add(2 * time.Minute)
	_, err := s.db.ExecContext(ctx, `update workiq_rows set server_deleted_at=?,server_deleted_version=?`, fmtTime(serverDelete), serverDelete.Format(time.RFC3339Nano))
	must0(err)
	incoming.Version += 100000
	_ = must(s.ApplyMessages(ctx, []teamsdesktop.Message{incoming}))
	if rowCount(t, s, `select count(*) from messages where deleted_at is not null`) != 1 {
		t.Fatal("an unqualified cache clock cleared server deletion")
	}
	// A first cache sighting can itself be a tombstone, without inventing a cache live copy.
	only := newStore(t)
	deleted, _ := seedWorkIQBoundMessage(t, only, false)
	deleted.DeletedAt = base
	deleted.Raw = nil
	_ = must(only.ApplyMessages(ctx, []teamsdesktop.Message{deleted}))
	if rowCount(t, only, `select count(*) from workiq_rows where cache_projection is null and cache_deleted_at is not null`) != 1 {
		t.Fatal("first tombstone fabricated a cache live projection")
	}
}

func TestWorkIQBoundMessageCorruptProjectionsAndClocksFailClosed(t *testing.T) {
	for _, statement := range []string{
		`update workiq_rows set server_projection='{'`,
		`update workiq_rows set server_projection='{}'`,
		`update workiq_rows set cache_projection='{'`,
		`update workiq_rows set cache_projection='{}'`,
		`update workiq_rows set cache_seen_at='private-clock-text'`,
		`update workiq_rows set server_deleted_version='private-clock-text'`,
		`update workiq_rows set server_projection=json_set(server_projection,'$.Class','unknown')`,
		`update workiq_rows set cache_version=cache_version-1`,
	} {
		s := newStore(t)
		incoming, _ := seedWorkIQBoundMessage(t, s, true)
		_, err := s.db.Exec(statement)
		must0(err)
		before := dump(t, s)
		if _, err := s.ApplyMessages(context.Background(), []teamsdesktop.Message{incoming}); err == nil {
			t.Fatal("corrupt source state became an empty or successful cache observation")
		}
		if dump(t, s) != before {
			t.Fatal("a corrupt source projection partially changed the canonical archive")
		}
	}
}

func TestWorkIQBoundMessageLeavesUnboundIdentitiesOnLegacyPath(t *testing.T) {
	s := newStore(t)
	_, _ = seedWorkIQBoundMessage(t, s, true)
	other := msg(acctA, "chat", "other", "unbound", base)
	_ = must(s.ApplyMessages(context.Background(), []teamsdesktop.Message{other}))
	if rowCount(t, s, `select count(*) from messages`) != 2 || rowCount(t, s, `select count(*) from workiq_rows`) != 1 {
		t.Fatal("an unbound cache message acquired server provenance")
	}
}

func TestWorkIQCacheProjectionFailureDoesNotMutateSource(t *testing.T) {
	s := newStore(t)
	incoming, _ := seedWorkIQBoundMessage(t, s, true)
	incoming.SentAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	before := dump(t, s)
	if _, err := s.ApplyMessages(context.Background(), []teamsdesktop.Message{incoming}); err == nil || dump(t, s) != before {
		t.Fatal("an unencodable cache projection changed source state")
	}
	original := scrubWorkIQProjection
	scrubWorkIQProjection = func([]byte) ([]byte, int) { return []byte(`{`), 0 }
	t.Cleanup(func() { scrubWorkIQProjection = original })
	incoming.SentAt = base
	if _, err := s.ApplyMessages(context.Background(), []teamsdesktop.Message{incoming}); err == nil || dump(t, s) != before {
		t.Fatal("a malformed scrub result was admitted as a cache source projection")
	}
}

func TestWorkIQSourceClockPersistenceRetainsExactPrecision(t *testing.T) {
	at := base.Add(123456700 * time.Nanosecond)
	value, ok := workIQTimeValue(&at).(string)
	if !ok {
		t.Fatal("source clock was not stored as UTC text")
	}
	restored, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || !restored.Equal(at) {
		t.Fatal("new source-state persistence rounded a retained clock")
	}
}
