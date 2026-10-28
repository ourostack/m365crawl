package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/ourostack/m365crawl/internal/teamsdesktop"
	"github.com/ourostack/m365crawl/internal/workiq"
)

func (s *Store) CommitWorkIQConversation(ctx context.Context, attempt int64, raw json.RawMessage, observed time.Time) (Counts, error) {
	binding, err := s.validateWorkIQAttemptOwner(ctx, attempt)
	if err != nil {
		return Counts{}, err
	}
	chat, err := workiq.DecodeChat(ctx, raw)
	if err != nil {
		return Counts{}, err
	}
	if binding.Fact.Family != "chats" || chat.TenantID == "" || chat.TenantID != binding.Fact.TenantID || observed.IsZero() {
		return Counts{}, errors.New("WorkIQ conversation does not match the verified archive tenant")
	}
	var counts Counts
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		var err error
		counts, err = commitWorkIQConversation(ctx, tx, binding.Fact, chat, observed)
		return err
	})
	if err != nil {
		return Counts{}, err
	}
	return counts, nil
}

func loadWorkIQConversation(ctx context.Context, tx *sql.Tx, account workiq.AccountFact, id string) (teamsdesktop.Conversation, int64, error) {
	c := teamsdesktop.Conversation{TenantID: account.TenantID, UserID: account.UserID, ID: id}
	var rowid int64
	var members, last, horizon, raw sql.NullString
	err := tx.QueryRowContext(ctx, `select rowid,kind,title,topic,display_name,team_id,parent_id,members_json,last_message_at,
read_horizon_at,read_horizon_client_message_id,favorite,raw_json from conversations where tenant_id=? and user_id=? and id=?`,
		account.TenantID, account.UserID, id).Scan(&rowid, &c.Kind, &c.Title, &c.Topic, &c.DisplayName, &c.TeamID, &c.ParentID,
		&members, &last, &horizon, &c.ReadHorizonClientMessageID, &c.Favorite, &raw)
	if err != nil {
		return c, 0, err
	}
	if members.Valid && json.Unmarshal([]byte(members.String), &c.Members) != nil {
		return c, 0, errors.New("invalid archived conversation members")
	}
	if value, err := workIQStoredTime(last); err != nil {
		return c, 0, err
	} else if value != nil {
		c.LastMessageAt = *value
	}
	if value, err := workIQStoredTime(horizon); err != nil {
		return c, 0, err
	} else if value != nil {
		c.ReadHorizonAt = *value
	}
	if raw.Valid {
		c.Raw = []byte(raw.String)
	}
	return c, rowid, nil
}

func safeWorkIQCacheConversation(in teamsdesktop.Conversation) (teamsdesktop.Conversation, string, error) {
	if len(in.Raw) > 0 {
		in.Raw, _ = scrubWorkIQProjection(in.Raw)
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return teamsdesktop.Conversation{}, "", errors.New("invalid cache conversation projection")
	}
	safe, _ := scrubWorkIQProjection(raw)
	var out teamsdesktop.Conversation
	if json.Unmarshal(safe, &out) != nil || out.TenantID != in.TenantID || out.UserID != in.UserID || out.ID != in.ID {
		return teamsdesktop.Conversation{}, "", errors.New("cache conversation identity could not be retained")
	}
	return out, string(safe), nil
}

func decodeWorkIQConversationProjection(raw string, identity teamsdesktop.Conversation, server bool) (teamsdesktop.Conversation, error) {
	var out teamsdesktop.Conversation
	if json.Unmarshal([]byte(raw), &out) != nil || out.TenantID != identity.TenantID ||
		out.UserID != identity.UserID || out.ID != identity.ID || server && len(out.Raw) != 0 {
		return out, errors.New("invalid stored conversation source identity")
	}
	return out, nil
}

// applyWorkIQCacheConversation records the independent native copy before normal cache upsert.
// Conversation metadata has no qualified modification domain: nonempty cache names win.
func applyWorkIQCacheConversation(ctx context.Context, tx *sql.Tx, incoming teamsdesktop.Conversation, observed time.Time) (teamsdesktop.Conversation, bool, error) {
	account := incoming.TenantID + "/" + incoming.UserID
	var rowid int64
	var cacheSeen bool
	var cacheJSON sql.NullString
	var serverJSON, oldWinner string
	err := tx.QueryRowContext(ctx, `select w.item_rowid,w.cache_seen,w.cache_projection,w.server_projection,w.winner
from conversations c join workiq_rows w on w.kind='conversation' and w.account_id=c.tenant_id||'/'||c.user_id and w.item_rowid=c.rowid
where c.tenant_id=? and c.user_id=? and c.id=?`, incoming.TenantID, incoming.UserID, incoming.ID).
		Scan(&rowid, &cacheSeen, &cacheJSON, &serverJSON, &oldWinner)
	if errors.Is(err, sql.ErrNoRows) {
		return incoming, false, nil
	}
	if err != nil {
		return incoming, false, err
	}
	server, err := decodeWorkIQConversationProjection(serverJSON, incoming, true)
	if err != nil {
		return incoming, false, err
	}
	if cacheJSON.Valid {
		if _, err := decodeWorkIQConversationProjection(cacheJSON.String, incoming, false); err != nil {
			return incoming, false, err
		}
	}
	cache, encoded, err := safeWorkIQCacheConversation(incoming)
	if err != nil {
		return incoming, false, err
	}
	effective := cache
	winner := "desktop_cache"
	if firstNonEmpty(cache.DisplayName, cache.Topic, cache.Title) == "" {
		effective.DisplayName, effective.Topic, effective.Title = server.DisplayName, server.Topic, server.Title
		winner = "workiq_server"
	}
	changed := !cacheSeen || !cacheJSON.Valid || cacheJSON.String != encoded || oldWinner != winner
	if changed {
		if _, err := tx.ExecContext(ctx, `update workiq_rows set cache_seen=1,cache_projection=?,cache_raw_json=?,cache_seen_at=?,winner=?
where kind='conversation' and account_id=? and item_rowid=?`,
			encoded, rawOrNil(cache.Raw), workIQTimeValue(&observed), winner, account, rowid); err != nil {
			return incoming, false, err
		}
	}
	return effective, changed, nil
}

func commitWorkIQConversation(ctx context.Context, tx *sql.Tx, account workiq.AccountFact, chat workiq.Chat, observed time.Time) (Counts, error) {
	counts := Counts{Seen: 1}
	canonical, rowid, err := loadWorkIQConversation(ctx, tx, account, chat.ID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Counts{}, err
	}
	exists := err == nil
	cacheSeen := exists
	var cacheJSON sql.NullString
	var oldWinner, previousServer string
	if exists {
		err = tx.QueryRowContext(ctx, `select cache_seen,cache_projection,winner,server_projection from workiq_rows
where kind='conversation' and account_id=? and item_rowid=?`, account.ID, rowid).Scan(&cacheSeen, &cacheJSON, &oldWinner, &previousServer)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return Counts{}, err
		}
	}
	cache := canonical
	if cacheJSON.Valid {
		cache, err = decodeWorkIQConversationProjection(cacheJSON.String, canonical, false)
		if err != nil {
			return Counts{}, err
		}
	}
	if exists && cacheSeen && !cacheJSON.Valid {
		var encoded string
		cache, encoded, err = safeWorkIQCacheConversation(cache)
		if err != nil {
			return Counts{}, err
		}
		cacheJSON = sql.NullString{String: encoded, Valid: true}
	}
	server := teamsdesktop.Conversation{TenantID: account.TenantID, UserID: account.UserID, ID: chat.ID,
		Kind: "Chat", Title: chat.Topic, Topic: chat.Topic, DisplayName: chat.Topic}
	if previousServer != "" {
		previous, err := decodeWorkIQConversationProjection(previousServer, server, true)
		if err != nil {
			return Counts{}, err
		}
		if chat.Topic == "" {
			server.Title, server.Topic, server.DisplayName = previous.Title, previous.Topic, previous.DisplayName
		}
	}
	winner := "workiq_server"
	if cacheSeen && firstNonEmpty(cache.DisplayName, cache.Topic, cache.Title) != "" {
		winner = "desktop_cache"
	}
	next := canonical
	if !exists {
		next = server
	} else if winner == "workiq_server" {
		next.Title, next.Topic, next.DisplayName = server.Title, server.Topic, server.DisplayName
	}
	members, last, horizon, cacheRaw := jsonOrNil(next.Members), fmtTime(next.LastMessageAt), fmtTime(next.ReadHorizonAt), rawOrNil(next.Raw)
	hash := hashOf(next.Kind, next.Title, next.Topic, next.DisplayName, next.TeamID, next.ParentID, members, last, horizon, next.ReadHorizonClientMessageID, next.Favorite, cacheRaw)
	changed := !exists || next.Title != canonical.Title || next.Topic != canonical.Topic || next.DisplayName != canonical.DisplayName
	if !exists {
		result, err := tx.ExecContext(ctx, `insert into conversations(tenant_id,user_id,id,kind,title,topic,display_name,content_hash,updated_at)
values(?,?,?,?,?,?,?,?,?)`, next.TenantID, next.UserID, next.ID, next.Kind, next.Title, next.Topic, next.DisplayName, hash, nowText())
		if err != nil {
			return Counts{}, err
		}
		rowid, err = result.LastInsertId()
		if err != nil {
			return Counts{}, err
		}
		counts.Inserted++
	} else if changed {
		if _, err := tx.ExecContext(ctx, `update conversations set title=?,topic=?,display_name=?,content_hash=?,updated_at=? where rowid=?`,
			next.Title, next.Topic, next.DisplayName, hash, nowText(), rowid); err != nil {
			return Counts{}, err
		}
		counts.Updated++
	} else if oldWinner == "" {
		counts.Updated++
	} else {
		counts.Unchanged++
	}
	var cacheProjection any
	if cacheJSON.Valid {
		cacheProjection = cacheJSON.String
	}
	serverJSON, _ := json.Marshal(server) // source strings and zero native clocks always encode
	if _, err := tx.ExecContext(ctx, `insert into workiq_rows(kind,account_id,item_rowid,graph_id,server_json,server_projection,cache_projection,
cache_first,cache_seen,server_seen_at,winner,modified_at) values('conversation',?,?,?,?,?,?,?,?,?,?,?)
on conflict(kind,account_id,item_rowid) do update set graph_id=excluded.graph_id,server_json=excluded.server_json,
server_projection=excluded.server_projection,server_seen_at=excluded.server_seen_at,winner=excluded.winner,modified_at=excluded.modified_at`,
		account.ID, rowid, chat.ID, string(chat.Raw), string(serverJSON), cacheProjection, b2i(cacheSeen), b2i(cacheSeen),
		workIQTimeValue(&observed), winner, workIQTimeValue(&observed)); err != nil {
		return Counts{}, err
	}
	if changed {
		if err := reindexTitles(ctx, tx, []teamsdesktop.Conversation{next}); err != nil {
			return Counts{}, err
		}
	}
	return counts, nil
}
