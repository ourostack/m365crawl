package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/teamsdesktop"
	"github.com/ourostack/m365crawl/internal/workiq"
)

func TestWorkIQMessageAdmissionUnverifiedClassesOnlyCountWithoutPayloadRows(t *testing.T) {
	for _, kind := range []string{"group", "channel"} {
		t.Run(kind, func(t *testing.T) {
			s := newStore(t)
			ctx := context.Background()
			attempt := seedWorkIQConversation(t, s)
			batch := WorkIQMessageBatch{AttemptID: attempt, ObservedAt: base,
				Chat: []byte(`{"id":"chat","tenantId":"tenant-1","chatType":"` + kind + `"}`),
				Messages: []json.RawMessage{
					[]byte(`{"id":"one","messageType":"message","createdDateTime":"2026-09-01T12:00:00Z","body":{"content":"withheld body"}}`),
					[]byte(`{"id":"two","messageType":"message","replyToId":"root","createdDateTime":"2026-09-01T12:00:00Z"}`),
				}}
			for _, class := range []workiq.MessageClass{workiq.ClassChat, workiq.ClassChannelRoot, workiq.ClassChannelReply} {
				if workiq.IDQualified(class) {
					t.Fatal("an unverified native message class became production qualified")
				}
			}

			before := dump(t, s)
			result := must(s.CommitWorkIQMessages(ctx, batch))
			if result.Counts.Seen != 2 || result.Gaps["workiq_message_identity_unverified"] != 2 || dump(t, s) != before {
				t.Fatal("unverified messages were not count-and-gap only")
			}
			batch.Chat = []byte(`{"id":"chat","tenantId":"other","chatType":"group"}`)
			if _, err := s.CommitWorkIQMessages(ctx, batch); err == nil || dump(t, s) != before {
				t.Fatal("message admission guessed the actual conversation tenant")
			}
		})
	}
}

func qualifyWorkIQMessageTest(t *testing.T) {
	t.Helper()
	original := workIQMessageIDsQualified
	workIQMessageIDsQualified = func(workiq.MessageClass) bool { return true }
	t.Cleanup(func() { workIQMessageIDsQualified = original })
}

func qualifiedWorkIQMessageBatch(attempt int64) WorkIQMessageBatch {
	return WorkIQMessageBatch{AttemptID: attempt, ObservedAt: base,
		Chat:     []byte(`{"id":"chat","tenantId":"tenant-1","chatType":"group"}`),
		Messages: []json.RawMessage{[]byte(`{"id":"one","messageType":"message","createdDateTime":"2026-09-01T12:00:00Z","lastModifiedDateTime":"2026-09-01T12:01:00Z","body":{"contentType":"text","content":"Server retained"},"@microsoft.graph.downloadUrl":"private-link"}`)}}
}

func TestWorkIQMessageAdmissionQualifiedSourceRetainsNativeIdentityAndRawMeaning(t *testing.T) {
	qualifyWorkIQMessageTest(t)
	for _, cacheFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "server-first", true: "cache-first"}[cacheFirst], func(t *testing.T) {
			s := newStore(t)
			ctx := context.Background()
			attempt := seedWorkIQConversation(t, s)
			cache := msg(acctA, "chat", "one", "Cache retained", base)
			oldRowid := 0
			if cacheFirst {
				_ = must(s.ApplyMessages(ctx, []teamsdesktop.Message{cache}))
				oldRowid = rowCount(t, s, `select rowid from messages`)
			}
			apps := rowCount(t, s, `select count(*) from message_app_observations`)
			batch := qualifiedWorkIQMessageBatch(attempt)
			result := must(s.CommitWorkIQMessages(ctx, batch))
			if result.Counts.Seen != 1 || result.Counts.Inserted+result.Counts.Updated != 1 ||
				rowCount(t, s, `select count(*) from messages`) != 1 ||
				rowCount(t, s, `select count(*) from message_app_observations`) != apps {
				t.Fatal("qualified server admission did not create one source-aware canonical item without cache app claims")
			}
			if cacheFirst && rowCount(t, s, `select rowid from messages`) != oldRowid {
				t.Fatal("server arrival changed an already published native rowid")
			}
			var text, winner string
			var rawNull bool
			must0(s.db.QueryRowContext(ctx, `select content_text,raw_json is null from messages`).Scan(&text, &rawNull))
			must0(s.db.QueryRowContext(ctx, `select winner from workiq_rows where kind='message'`).Scan(&winner))
			if cacheFirst && (text != "Cache retained" || winner != "desktop_cache" || rawNull ||
				result.Gaps["workiq_version_unverified"] != 1) ||
				!cacheFirst && (text != "Server retained" || winner != "workiq_server" || !rawNull) {
				t.Fatal("server source confused cache raw or bypassed conservative version precedence")
			}
			_ = must(s.ApplyMessages(ctx, []teamsdesktop.Message{cache}))
			if rowCount(t, s, `select count(*) from messages`) != 1 ||
				rowCount(t, s, `select count(*) from workiq_rows where cache_seen=1`) != 1 {
				t.Fatal("later cache appearance duplicated a server-first item")
			}
			before := dump(t, s)
			_ = must(s.CommitWorkIQMessages(ctx, batch))
			if dump(t, s) != before {
				t.Fatal("replaying the same server observation changed the archive")
			}
		})
	}
}

func TestWorkIQMessageAdmissionQualifiedDeletionKeepsIndependentSourceMarks(t *testing.T) {
	qualifyWorkIQMessageTest(t)
	s := newStore(t)
	ctx := context.Background()
	attempt := seedWorkIQConversation(t, s)
	batch := qualifiedWorkIQMessageBatch(attempt)
	_ = must(s.CommitWorkIQMessages(ctx, batch))
	cache := msg(acctA, "chat", "one", "Cache retained", base)
	cache.DeletedAt, cache.Version = base.Add(time.Hour), 100
	_ = must(s.ApplyMessages(ctx, []teamsdesktop.Message{cache}))
	batch.ObservedAt = base.Add(2 * time.Hour)
	batch.Messages[0] = []byte(`{"id":"one","messageType":"message","createdDateTime":"2026-09-01T12:00:00Z","lastModifiedDateTime":"2026-09-02T12:00:00Z","body":{"contentType":"text","content":"New server"}}`)
	_ = must(s.CommitWorkIQMessages(ctx, batch))
	if rowCount(t, s, `select count(*) from messages where deleted_at is not null`) != 1 {
		t.Fatal("an unqualified server clock cleared a cache tombstone")
	}
	cache.DeletedAt, cache.Version = time.Time{}, 101
	_ = must(s.ApplyMessages(ctx, []teamsdesktop.Message{cache}))
	if rowCount(t, s, `select count(*) from messages where deleted_at is null`) != 1 {
		t.Fatal("a strictly newer same-source live copy failed to restore")
	}
	batch.Messages[0] = []byte(`{"id":"one","messageType":"message","deletedDateTime":"2026-09-03T12:00:00Z","lastModifiedDateTime":"2026-09-03T12:00:00Z"}`)
	_ = must(s.CommitWorkIQMessages(ctx, batch))
	if rowCount(t, s, `select count(*) from messages where deleted_at is not null`) != 1 ||
		rowCount(t, s, `select count(*) from workiq_rows where server_deleted_at is not null and cache_deleted_at is not null`) != 1 {
		t.Fatal("server tombstone was lost or destroyed the retained cache deletion history")
	}
	_ = must(s.Rederive(ctx))
	if rowCount(t, s, `select count(*) from messages where content_text<>''`) != 1 {
		t.Fatal("a bodyless server deletion blanked retained message text")
	}
	var tombstoneRaw, retainedRaw string
	must0(s.db.QueryRowContext(ctx, `select server_json from workiq_rows`).Scan(&tombstoneRaw))
	stale := qualifiedWorkIQMessageBatch(attempt)
	_ = must(s.CommitWorkIQMessages(ctx, stale))
	must0(s.db.QueryRowContext(ctx, `select server_json from workiq_rows`).Scan(&retainedRaw))
	if tombstoneRaw != retainedRaw || rowCount(t, s, `select count(*) from messages where deleted_at is not null`) != 1 {
		t.Fatal("stale live server copy replaced the newer tombstone shadow or resurrected it")
	}
}

func TestWorkIQMessageAdmissionAndShadowRederiveFaultsRollbackAllState(t *testing.T) {
	qualifyWorkIQMessageTest(t)
	for _, mode := range []string{"new-server", "cache-first", "server-bound", "shadow-rederive"} {
		t.Run(mode, func(t *testing.T) {
			var batch WorkIQMessageBatch
			setup := func(t *testing.T, s *Store) {
				attempt := seedWorkIQConversation(t, s)
				batch = qualifiedWorkIQMessageBatch(attempt)
				if mode == "cache-first" || mode == "shadow-rederive" {
					cache := msg(acctA, "chat", "one", "Cache retained", base)
					cache.EditedAt, cache.DeletedAt = base, base
					cache.Mentions = []teamsdesktop.Mention{{ID: "member"}}
					_ = must(s.ApplyMessages(context.Background(), []teamsdesktop.Message{cache}))
				}
				if mode == "server-bound" || mode == "shadow-rederive" {
					_ = must(s.CommitWorkIQMessages(context.Background(), batch))
					batch.ObservedAt = base.Add(time.Minute)
				}
			}
			sweepFaults(t, setup, func(ctx context.Context, s *Store) error {
				if mode == "shadow-rederive" {
					_, err := s.Rederive(ctx)
					return err
				}
				_, err := s.CommitWorkIQMessages(ctx, batch)
				return err
			})
		})
	}
}

func TestWorkIQMessageAdmissionRejectsInvalidOrContradictoryEvidence(t *testing.T) {
	qualifyWorkIQMessageTest(t)
	s := newStore(t)
	ctx := context.Background()
	attempt := seedWorkIQConversation(t, s)
	normal := qualifiedWorkIQMessageBatch(attempt)
	for _, input := range []string{`{`, `{"id":"one"}`, `{"id":"one","createdDateTime":"bad"}`} {
		batch := normal
		batch.Messages = []json.RawMessage{[]byte(input)}
		if _, err := s.CommitWorkIQMessages(ctx, batch); err == nil {
			t.Fatal("invalid qualified message was accepted")
		}
	}
	duplicate := normal
	duplicate.Messages = append(duplicate.Messages, duplicate.Messages[0])
	if _, err := s.CommitWorkIQMessages(ctx, duplicate); err == nil {
		t.Fatal("duplicate page identities were accepted")
	}
	for _, edit := range []string{
		`update messages set sent_at='bad'`,
		`update messages set mentions_json='bad'`,
		`update workiq_rows set server_projection='{}'`,
		`update workiq_rows set cache_projection='{}',cache_seen=1`,
		`update workiq_rows set cache_projection=null,cache_version=42,cache_seen=1`,
		`update workiq_rows set cache_seen_at='bad'`,
	} {
		t.Run(edit, func(t *testing.T) {
			archive := newStore(t)
			run := seedWorkIQConversation(t, archive)
			batch := qualifiedWorkIQMessageBatch(run)
			_ = must(archive.CommitWorkIQMessages(ctx, batch))
			_, err := archive.db.ExecContext(ctx, edit)
			must0(err)
			before := dump(t, archive)
			if _, err := archive.CommitWorkIQMessages(ctx, batch); err == nil || dump(t, archive) != before {
				t.Fatal("corrupt source input was admitted or partially changed")
			}
		})
	}
	normal.Chat = []byte(`{`)
	if _, err := s.CommitWorkIQMessages(ctx, normal); err == nil {
		t.Fatal("invalid conversation was admitted")
	}
	normal = qualifiedWorkIQMessageBatch(-1)
	if _, err := s.CommitWorkIQMessages(ctx, normal); err == nil {
		t.Fatal("unknown attempt was admitted")
	}
	normal = qualifiedWorkIQMessageBatch(attempt)
	normal.ObservedAt = time.Time{}
	if _, err := s.CommitWorkIQMessages(ctx, normal); err == nil {
		t.Fatal("missing observation clock was admitted")
	}
	normal = qualifiedWorkIQMessageBatch(attempt)
	normal.Messages = []json.RawMessage{[]byte(`{"id":"one","@removed":{}}`)}
	result := must(s.CommitWorkIQMessages(ctx, normal))
	if result.Counts.Unchanged != 1 || rowCount(t, s, `select count(*) from messages`) != 0 {
		t.Fatal("@removed fabricated a message or deletion")
	}
}

func TestWorkIQMessageAdmissionRederiveChangesShadowWithoutSourceFreshness(t *testing.T) {
	qualifyWorkIQMessageTest(t)
	s := newStore(t)
	ctx := context.Background()
	attempt := seedWorkIQConversation(t, s)
	cache := msg(acctA, "chat", "one", "Old projected text", base)
	cache.Raw = []byte(`{"id":"one","messageType":"RichText/Html","content":"<p>New derived text</p>","imDisplayName":"Derived sender"}`)
	cache.DeletedAt = base
	_ = must(s.ApplyMessages(ctx, []teamsdesktop.Message{cache}))
	_ = must(s.CommitWorkIQMessages(ctx, qualifiedWorkIQMessageBatch(attempt)))
	var clocksBefore, updated string
	must0(s.db.QueryRowContext(ctx, `select coalesce(cache_seen_at,'')||'/'||coalesce(server_seen_at,'') from workiq_rows`).Scan(&clocksBefore))
	must0(s.db.QueryRowContext(ctx, `select updated_at from messages`).Scan(&updated))
	_ = must(s.Rederive(ctx))
	var projection teamsdesktop.Message
	var projected, clocksAfter, afterUpdate string
	must0(s.db.QueryRowContext(ctx, `select cache_projection,coalesce(cache_seen_at,'')||'/'||coalesce(server_seen_at,'') from workiq_rows`).Scan(&projected, &clocksAfter))
	must0(json.Unmarshal([]byte(projected), &projection))
	must0(s.db.QueryRowContext(ctx, `select updated_at from messages`).Scan(&afterUpdate))
	if projection.ContentText != "New derived text" || clocksBefore != clocksAfter || updated != afterUpdate ||
		rowCount(t, s, `select count(*) from messages where deleted_at is not null`) != 1 {
		t.Fatal("source shadow rederive failed to update its projection or changed source clocks/deletion")
	}
}

func TestWorkIQMessageAdmissionBoundedCodecAndMaintenanceOutcomes(t *testing.T) {
	qualifyWorkIQMessageTest(t)
	ctx := context.Background()
	s := newStore(t)
	attempt := seedWorkIQConversation(t, s)
	batch := qualifiedWorkIQMessageBatch(attempt)
	batch.Messages = []json.RawMessage{[]byte(strings.Repeat(" ", 16<<20))}
	if _, err := s.CommitWorkIQMessages(ctx, batch); err == nil {
		t.Fatal("oversized message batch was admitted")
	}
	for _, mode := range []string{"unchanged-shadow", "unreadable-shadow", "corrupt-shadow"} {
		t.Run(mode, func(t *testing.T) {
			archive := newStore(t)
			run := seedWorkIQConversation(t, archive)
			cache := msg(acctA, "chat", "one", "", base)
			cache.SenderName = ""
			if mode == "unreadable-shadow" {
				cache.Raw = []byte(`{`)
			}
			_ = must(archive.ApplyMessages(ctx, []teamsdesktop.Message{cache}))
			_ = must(archive.CommitWorkIQMessages(ctx, qualifiedWorkIQMessageBatch(run)))
			if mode == "corrupt-shadow" {
				_, err := archive.db.ExecContext(ctx, `update workiq_rows set cache_projection='broken'`)
				must0(err)
				if _, err := archive.Rederive(ctx); err == nil {
					t.Fatal("corrupt cache projection silently skipped")
				}
			} else {
				_ = must(archive.Rederive(ctx))
			}
		})
	}
	tombstone := qualifiedWorkIQMessageBatch(attempt)
	tombstone.Messages = []json.RawMessage{[]byte(`{"id":"one","lastModifiedDateTime":"2026-09-01T12:01:00Z","deletedDateTime":"2026-09-01T12:01:00Z"}`)}
	_ = must(s.CommitWorkIQMessages(ctx, tombstone))
	if rowCount(t, s, `select count(*) from workiq_rows where server_seen_at is null and server_deleted_at is not null`) != 1 {
		t.Fatal("first server tombstone invented a live sighting")
	}
	invalid := workIQServerMessageRecord{Server: workiq.ChatMessage{ModifiedAt: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)}}
	err := s.inTx(ctx, func(tx *sql.Tx) error { return invalid.persist(ctx, tx, "unused", 1, base) })
	if err == nil {
		t.Fatal("invalid source projection clock was accepted")
	}
	other := msg(acctA, "chat", "two", "Cache", base)
	_ = must(s.ApplyMessages(ctx, []teamsdesktop.Message{other}))
	original := scrubWorkIQProjection
	scrubWorkIQProjection = func([]byte) ([]byte, int) { return []byte(`{`), 0 }
	t.Cleanup(func() { scrubWorkIQProjection = original })
	target := qualifiedWorkIQMessageBatch(attempt)
	target.Messages[0] = []byte(strings.ReplaceAll(string(target.Messages[0]), `"one"`, `"two"`))
	if _, err := s.CommitWorkIQMessages(ctx, target); err == nil {
		t.Fatal("invalid scrubbed cache source was admitted")
	}
}

func TestWorkIQMessageAdmissionNeverBlanksOrDowngradesAcceptedServerRaw(t *testing.T) {
	qualifyWorkIQMessageTest(t)
	ctx := context.Background()
	s := newStore(t)
	attempt := seedWorkIQConversation(t, s)
	batch := qualifiedWorkIQMessageBatch(attempt)
	_ = must(s.CommitWorkIQMessages(ctx, batch))
	batch.Messages[0] = []byte(`{"id":"one","messageType":"message","createdDateTime":"2026-09-01T12:00:00Z","lastModifiedDateTime":"2026-09-03T12:00:00Z","body":{"contentType":"text","content":""}}`)
	_ = must(s.CommitWorkIQMessages(ctx, batch))
	var retained string
	must0(s.db.QueryRowContext(ctx, `select server_json from workiq_rows`).Scan(&retained))
	if rowCount(t, s, `select count(*) from messages where content_text='Server retained'`) != 1 {
		t.Fatal("empty server update erased retained text")
	}
	old := qualifiedWorkIQMessageBatch(attempt)
	_ = must(s.CommitWorkIQMessages(ctx, old))
	var raw string
	must0(s.db.QueryRowContext(ctx, `select server_json from workiq_rows`).Scan(&raw))
	if raw != retained {
		t.Fatal("a stale source copy replaced the accepted newer raw shadow")
	}
}

func TestWorkIQMessageAdmissionQualifiedClockWinnerAndCacheTie(t *testing.T) {
	qualifyWorkIQMessageTest(t)
	original := workIQVersionsComparable
	workIQVersionsComparable = func(string) bool { return true }
	t.Cleanup(func() { workIQVersionsComparable = original })
	s := newStore(t)
	ctx := context.Background()
	attempt := seedWorkIQConversation(t, s)
	cache := msg(acctA, "chat", "one", "Cache older", base)
	cache.Version = base.UnixMilli()
	_ = must(s.ApplyMessages(ctx, []teamsdesktop.Message{cache}))
	result := must(s.CommitWorkIQMessages(ctx, qualifiedWorkIQMessageBatch(attempt)))
	if result.Gaps["workiq_version_unverified"] != 0 ||
		rowCount(t, s, `select count(*) from messages where content_text='Server retained'`) != 1 {
		t.Fatal("qualified newer server content did not win")
	}
	cache.Version = base.Add(time.Minute).UnixMilli()
	cache.ContentText = "Cache tie"
	_ = must(s.ApplyMessages(ctx, []teamsdesktop.Message{cache}))
	_ = must(s.CommitWorkIQMessages(ctx, qualifiedWorkIQMessageBatch(attempt)))
	if rowCount(t, s, `select count(*) from messages where content_text='Cache tie'`) != 1 {
		t.Fatal("equal comparable clocks did not prefer the native cache")
	}
}
