package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/teamsdesktop"
)

func TestWorkIQConversationAdmissionAndCacheAppearanceKeepOneCanonicalRow(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	must0(s.ApplyAccount(ctx, acctA))
	must0(s.SetWorkIQEnabled(ctx, true))
	account := acctA.TenantID + "/" + acctA.UserID
	attempt := must(s.BeginWorkIQAttempt(ctx, WorkIQScope{Family: "chats", Account: account}, base))
	verifyWorkIQTestAttempt(t, s, attempt)
	raw := []byte(`{"id":"server-chat","tenantId":"tenant-1","chatType":"group","topic":"Server room"}`)
	counts, err := s.CommitWorkIQConversation(ctx, attempt, raw, base)
	if err != nil || counts.Inserted != 1 || rowCount(t, s, `select count(*) from conversations`) != 1 {
		t.Fatal("server-known conversation was not admitted for its actual existing tenant owner")
	}
	rowid := rowCount(t, s, `select rowid from conversations`)
	var cacheRawNull bool
	must0(s.db.QueryRowContext(ctx, `select raw_json is null from conversations`).Scan(&cacheRawNull))
	if !cacheRawNull || rowCount(t, s, `select count(*) from workiq_rows where kind='conversation' and cache_seen=0 and winner='workiq_server'`) != 1 {
		t.Fatal("server conversation fabricated cache JSON or cache provenance")
	}
	native := conv(acctA, "server-chat", "Chat", "Cache room")
	_ = must(s.ApplyConversations(ctx, []teamsdesktop.Conversation{native}))
	if rowCount(t, s, `select rowid from conversations`) != rowid ||
		rowCount(t, s, `select count(*) from workiq_rows where kind='conversation' and cache_seen=1`) != 1 {
		t.Fatal("later cache appearance duplicated or reidentified the server conversation")
	}
	if rowCount(t, s, `select count(*) from accounts`) != 1 || rowCount(t, s, `select count(*) from sync_runs`) != 0 {
		t.Fatal("conversation admission fabricated an account or ordinary freshness")
	}
}

func TestWorkIQConversationAdmissionDoesNotOverrideCacheOrGuessTenant(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	must0(s.ApplyAccount(ctx, acctA))
	_ = must(s.ApplyConversations(ctx, []teamsdesktop.Conversation{conv(acctA, "chat", "Chat", "Cache room")}))
	must0(s.SetWorkIQEnabled(ctx, true))
	attempt := must(s.BeginWorkIQAttempt(ctx, WorkIQScope{Family: "chats", Account: acctA.TenantID + "/" + acctA.UserID}, base))
	verifyWorkIQTestAttempt(t, s, attempt)
	_, err := s.CommitWorkIQConversation(ctx, attempt, []byte(`{"id":"chat","tenantId":"tenant-1","chatType":"group","topic":"Server room"}`), base)
	must0(err)
	var name string
	must0(s.db.QueryRowContext(ctx, `select display_name from conversations`).Scan(&name))
	if name != "Cache room" || rowCount(t, s, `select count(*) from workiq_rows where cache_seen=1`) != 1 {
		t.Fatal("unqualified server metadata replaced known cache content or lost provenance")
	}
	for _, raw := range []string{
		`{"id":"wrong","tenantId":"unmatched","chatType":"group"}`,
		`{"id":"wrong","chatType":"group"}`,
	} {
		if _, err := s.CommitWorkIQConversation(ctx, attempt, []byte(raw), base); err == nil {
			t.Fatal("conversation guessed a home tenant or registered an unknown account")
		}
	}

}

func seedWorkIQConversation(t *testing.T, s *Store) int64 {
	t.Helper()
	ctx := context.Background()
	must0(s.ApplyAccount(ctx, acctA))
	must0(s.SetWorkIQEnabled(ctx, true))
	attempt := must(s.BeginWorkIQAttempt(ctx, WorkIQScope{Family: "chats", Account: acctA.TenantID + "/" + acctA.UserID}, base))
	verifyWorkIQTestAttempt(t, s, attempt)
	return attempt
}

func TestWorkIQConversationEmptyCopiesDoNotEraseKnownServerName(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	attempt := seedWorkIQConversation(t, s)
	first := []byte(`{"id":"chat","tenantId":"tenant-1","chatType":"group","topic":"Preserved name","@microsoft.graph.downloadUrl":"private-link"}`)
	_ = must(s.CommitWorkIQConversation(ctx, attempt, first, base))
	before := dump(t, s)
	_ = must(s.CommitWorkIQConversation(ctx, attempt, first, base))
	if dump(t, s) != before {
		t.Fatal("the same source copy was not idempotent")
	}
	_ = must(s.CommitWorkIQConversation(ctx, attempt, []byte(`{"id":"chat","tenantId":"tenant-1","chatType":"group","topic":""}`), base.Add(time.Minute)))
	empty := conv(acctA, "chat", "Chat", "")
	_ = must(s.ApplyConversations(ctx, []teamsdesktop.Conversation{empty}))
	if rowCount(t, s, `select count(*) from conversations where display_name='Preserved name'`) != 1 {
		t.Fatal("an empty server projection followed by an empty cache copy erased the known name")
	}
	var projection teamsdesktop.Conversation
	var projected string
	must0(s.db.QueryRowContext(ctx, `select server_projection from workiq_rows`).Scan(&projected))
	must0(json.Unmarshal([]byte(projected), &projection))
	if projection.DisplayName != "Preserved name" || len(projection.Raw) != 0 ||
		strings.Contains(dump(t, s), "private-link") {
		t.Fatal("server projection/raw were not independently retained and scrubbed")
	}
	before = dump(t, s)
	_ = must(s.ApplyConversations(ctx, []teamsdesktop.Conversation{empty}))
	if dump(t, s) != before {
		t.Fatal("unchanged cache copy fabricated another sighting")
	}
}

func TestWorkIQConversationRejectsCorruptCacheIdentityBeforeMutation(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	attempt := seedWorkIQConversation(t, s)
	_ = must(s.ApplyConversations(ctx, []teamsdesktop.Conversation{conv(acctA, "chat", "Chat", "Cache room")}))
	raw := []byte(`{"id":"chat","tenantId":"tenant-1","chatType":"group","topic":"Server room"}`)
	_ = must(s.CommitWorkIQConversation(ctx, attempt, raw, base))
	_, err := s.db.ExecContext(ctx, `update workiq_rows set cache_projection='{"ID":"other","TenantID":"tenant-1","UserID":"user-A"}'`)
	must0(err)
	before := dump(t, s)
	if _, err := s.CommitWorkIQConversation(ctx, attempt, raw, base); err == nil || dump(t, s) != before {
		t.Fatal("server admission accepted an inconsistent cached identity")
	}
	if _, err := s.ApplyConversations(ctx, []teamsdesktop.Conversation{conv(acctA, "chat", "Chat", "Changed")}); err == nil ||
		dump(t, s) != before {
		t.Fatal("cache update silently repaired an inconsistent cached identity")
	}
}

func TestWorkIQConversationFaultsRollbackSourceAndCanonicalRows(t *testing.T) {
	raw := []byte(`{"id":"chat","tenantId":"tenant-1","chatType":"group","topic":"Server room"}`)
	for _, mode := range []string{"new-server", "existing-cache", "existing-server", "native-bound"} {
		t.Run(mode, func(t *testing.T) {
			var attempt int64
			setup := func(t *testing.T, s *Store) {
				attempt = seedWorkIQConversation(t, s)
				if mode == "existing-cache" {
					native := conv(acctA, "chat", "Chat", "Cache room")
					native.LastMessageAt, native.ReadHorizonAt = base, base
					native.Members = []string{"member"}
					native.Raw = []byte(`{}`)
					_ = must(s.ApplyConversations(context.Background(), []teamsdesktop.Conversation{native}))
				}
				if mode == "existing-server" || mode == "native-bound" {
					_ = must(s.CommitWorkIQConversation(context.Background(), attempt,
						[]byte(`{"id":"chat","tenantId":"tenant-1","chatType":"group","topic":"Old server room"}`), base))
				}
			}
			sweepFaults(t, setup, func(ctx context.Context, s *Store) error {
				if mode == "native-bound" {
					_, err := s.ApplyConversations(ctx, []teamsdesktop.Conversation{conv(acctA, "chat", "Chat", "Cache room")})
					return err
				}
				_, err := s.CommitWorkIQConversation(ctx, attempt, raw, base.Add(time.Minute))
				return err
			})
		})
	}
}

func TestWorkIQConversationAdmissionRequiresVerifiedOwnerAndValidClocks(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	attempt := seedWorkIQConversation(t, s)
	raw := []byte(`{"id":"chat","tenantId":"tenant-1","chatType":"group","topic":"Server room"}`)
	for _, value := range []struct {
		attempt int64
		raw     []byte
		at      time.Time
	}{{-1, raw, base}, {attempt, []byte(`{`), base}, {attempt, raw, time.Time{}}} {
		if _, err := s.CommitWorkIQConversation(ctx, value.attempt, value.raw, value.at); err == nil {
			t.Fatal("invalid source input was admitted")
		}
	}
	for _, edit := range []string{
		`update conversations set members_json='broken'`,
		`update conversations set last_message_at='broken'`,
		`update conversations set read_horizon_at='broken'`,
		`update workiq_rows set server_projection='broken'`,
		`update workiq_rows set cache_projection='broken'`,
	} {
		t.Run(edit, func(t *testing.T) {
			archive := newStore(t)
			run := seedWorkIQConversation(t, archive)
			_ = must(archive.CommitWorkIQConversation(ctx, run, raw, base))
			_, err := archive.db.ExecContext(ctx, edit)
			must0(err)
			before := dump(t, archive)
			if _, err := archive.CommitWorkIQConversation(ctx, run, raw, base); err == nil || dump(t, archive) != before {
				t.Fatal("corrupt source/archive input was accepted or partially changed")
			}
		})
	}
	original := scrubWorkIQProjection
	scrubWorkIQProjection = func([]byte) ([]byte, int) { return []byte(`{`), 0 }
	t.Cleanup(func() { scrubWorkIQProjection = original })
	native := conv(acctA, "chat", "Chat", "Cache room")
	if _, _, err := safeWorkIQCacheConversation(native); err == nil {
		t.Fatal("malformed scrubbed conversation projection was accepted")
	}
	native.LastMessageAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, _, err := safeWorkIQCacheConversation(native); err == nil {
		t.Fatal("unrepresentable conversation clock was accepted")
	}
}

func TestWorkIQConversationUpdatesKeepCanonicalIdentityAndCacheReadState(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	attempt := seedWorkIQConversation(t, s)
	first := []byte(`{"id":"chat","tenantId":"tenant-1","chatType":"group","topic":"Old room"}`)
	_ = must(s.CommitWorkIQConversation(ctx, attempt, first, base))
	id := rowCount(t, s, `select rowid from conversations`)
	renamed := []byte(`{"id":"chat","tenantId":"tenant-1","chatType":"group","topic":"New room"}`)
	n := must(s.CommitWorkIQConversation(ctx, attempt, renamed, base.Add(time.Minute)))
	if n.Updated != 1 || rowCount(t, s, `select rowid from conversations`) != id ||
		rowCount(t, s, `select count(*) from conversation_fts where conversation_fts match 'New'`) != 1 {
		t.Fatal("server rename lost canonical identity, change count or title index")
	}
	native := conv(acctA, "chat", "Chat", "")
	native.Raw, native.ReadHorizonAt = []byte(`{}`), base
	native.ReadHorizonClientMessageID = "known-read"
	_ = must(s.ApplyConversations(ctx, []teamsdesktop.Conversation{native, conv(acctA, "unbound", "Chat", "Other")}))
	native.ReadHorizonAt, native.ReadHorizonClientMessageID = time.Time{}, ""
	_ = must(s.ApplyConversations(ctx, []teamsdesktop.Conversation{native}))
	if rowCount(t, s, `select count(*) from conversations where id='chat' and display_name='New room' and read_horizon_client_message_id='known-read'`) != 1 {
		t.Fatal("server winner lost known native read metadata")
	}
	// Malformed projections and scrub failures must refuse the native transaction too.
	for _, column := range []string{"server_projection", "cache_projection"} {
		previous := ""
		must0(s.db.QueryRowContext(ctx, `select `+column+` from workiq_rows`).Scan(&previous))
		_, err := s.db.ExecContext(ctx, `update workiq_rows set `+column+`='broken'`)
		must0(err)
		before := dump(t, s)
		if _, err := s.ApplyConversations(ctx, []teamsdesktop.Conversation{native}); err == nil || dump(t, s) != before {
			t.Fatal("native source transaction accepted malformed projection or partially changed data")
		}
		_, err = s.db.ExecContext(ctx, `update workiq_rows set `+column+`=?`, previous)
		must0(err)
	}
	original := scrubWorkIQProjection
	scrubWorkIQProjection = func([]byte) ([]byte, int) { return []byte(`{`), 0 }
	t.Cleanup(func() { scrubWorkIQProjection = original })
	if _, err := s.ApplyConversations(ctx, []teamsdesktop.Conversation{native}); err == nil {
		t.Fatal("native source transaction accepted malformed scrubbed projection")
	}
	_ = must(s.ApplyConversations(ctx, []teamsdesktop.Conversation{conv(acctA, "fresh-cache", "Chat", "Cache")}))
	if _, err := s.CommitWorkIQConversation(ctx, attempt,
		[]byte(`{"id":"fresh-cache","tenantId":"tenant-1","chatType":"group","topic":"Server"}`), base); err == nil {
		t.Fatal("server admission accepted malformed scrubbed cache snapshot")
	}
}
