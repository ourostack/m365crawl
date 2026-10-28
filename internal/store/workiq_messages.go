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

// Native clock qualification is code-owned and initially absent for every source class.
var workIQVersionsComparable = func(string) bool { return false }

var scrubWorkIQProjection = teamsdesktop.Scrub

type workIQCacheMessageResult struct {
	Message                     teamsdesktop.Message
	Bound, Changed, PublishApps bool
}

func workIQHasRows(ctx context.Context, tx *sql.Tx, kind string) (bool, error) {
	var version int
	if err := tx.QueryRowContext(ctx, `select coalesce(max(version),0) from schema_migrations`).Scan(&version); err != nil {
		return false, err
	}
	if version < 21 {
		return false, nil
	}
	var exists bool
	err := tx.QueryRowContext(ctx, `select exists(select 1 from workiq_rows where kind=?)`, kind).Scan(&exists)
	return exists, err
}

func safeWorkIQCacheMessage(in teamsdesktop.Message) (teamsdesktop.Message, string, error) {
	if len(in.Raw) > 0 {
		in.Raw, _ = scrubWorkIQProjection(in.Raw)
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return teamsdesktop.Message{}, "", errors.New("invalid cache source projection")
	}
	safe, _ := scrubWorkIQProjection(raw)
	var out teamsdesktop.Message
	if json.Unmarshal(safe, &out) != nil || out.TenantID != in.TenantID || out.UserID != in.UserID ||
		out.ConversationID != in.ConversationID || out.ID != in.ID {
		return teamsdesktop.Message{}, "", errors.New("cache source identity could not be retained")
	}
	return out, string(safe), nil
}

func workIQStoredTime(value sql.NullString) (*time.Time, error) {
	if !value.Valid {
		return nil, nil
	}
	at, err := time.Parse(time.RFC3339Nano, value.String)
	if err != nil {
		return nil, errors.New("invalid stored source clock")
	}
	return &at, nil
}

func applyWorkIQCacheMessage(ctx context.Context, tx *sql.Tx, incoming teamsdesktop.Message, observed time.Time) (workIQCacheMessageResult, error) {
	result := workIQCacheMessageResult{Message: incoming, PublishApps: true}
	account := incoming.TenantID + "/" + incoming.UserID
	var rowid int64
	var cacheSeen bool
	var cacheJSON, cacheRaw, canonicalRaw, cacheAt, serverAt, cacheDeleted, serverDeleted, serverDeletedVersion sql.NullString
	var cacheVersion, cacheDeletedVersion sql.NullInt64
	var serverJSON, oldWinner string
	err := tx.QueryRowContext(ctx, `select w.item_rowid,w.cache_seen,w.cache_projection,w.server_projection,
w.cache_raw_json,m.raw_json,w.cache_seen_at,w.server_seen_at,w.cache_deleted_at,w.server_deleted_at,w.cache_version,w.cache_deleted_version,w.server_deleted_version,w.winner
from messages m join workiq_rows w on w.kind='message' and w.account_id=m.tenant_id||'/'||m.user_id and w.item_rowid=m.rowid
where m.tenant_id=? and m.user_id=? and m.conversation_id=? and m.id=?`,
		incoming.TenantID, incoming.UserID, incoming.ConversationID, incoming.ID).
		Scan(&rowid, &cacheSeen, &cacheJSON, &serverJSON, &cacheRaw, &canonicalRaw, &cacheAt, &serverAt,
			&cacheDeleted, &serverDeleted, &cacheVersion, &cacheDeletedVersion, &serverDeletedVersion, &oldWinner)
	if errors.Is(err, sql.ErrNoRows) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	var server workiq.ChatMessage
	if json.Unmarshal([]byte(serverJSON), &server) != nil || server.Native.TenantID != incoming.TenantID ||
		server.Native.UserID != incoming.UserID || server.Native.ConversationID != incoming.ConversationID || server.Native.ID != incoming.ID ||
		len(server.Native.Raw) != 0 {
		return result, errors.New("invalid stored server message projection")
	}
	switch server.Class {
	case workiq.ClassChat, workiq.ClassChannelRoot, workiq.ClassChannelReply:
	default:
		return result, errors.New("invalid stored server identity class")
	}
	var cache teamsdesktop.Message
	hasCache := cacheJSON.Valid && cacheJSON.String != "null"
	if hasCache && (json.Unmarshal([]byte(cacheJSON.String), &cache) != nil || cache.TenantID != incoming.TenantID ||
		cache.UserID != incoming.UserID || cache.ConversationID != incoming.ConversationID || cache.ID != incoming.ID) {
		return result, errors.New("invalid stored cache message projection")
	}
	if hasCache && (!cacheSeen || !cacheVersion.Valid || cacheVersion.Int64 != cache.Version) ||
		!hasCache && cacheVersion.Valid && cacheVersion.Int64 != 0 {
		return result, errors.New("inconsistent stored cache source version")
	}
	state := WorkIQReduceInput{CrossVersionComparable: workIQVersionsComparable(string(server.Class))}
	state.Cache.Seen, state.Server.Seen = cacheSeen, true
	for _, clock := range []struct {
		value sql.NullString
		into  **time.Time
	}{{cacheAt, &state.Cache.SeenAt}, {serverAt, &state.Server.SeenAt},
		{cacheDeleted, &state.Cache.DeletedAt}, {serverDeleted, &state.Server.DeletedAt},
		{serverDeletedVersion, &state.Server.DeletedVersionAt}} {
		*clock.into, err = workIQStoredTime(clock.value)
		if err != nil {
			return result, err
		}
	}
	state.Cache.DeletedVersion = cacheDeletedVersion.Int64
	state.Server.Version, state.Server.Text = server.Native.Version, server.Native.ContentText
	if !server.ModifiedAt.IsZero() {
		state.Server.VersionAt = &server.ModifiedAt
	}
	if state.Server.DeletedVersionAt != nil {
		state.Server.DeletedVersion = state.Server.DeletedVersionAt.UnixMilli()
	}
	safeIncoming, encoded, err := safeWorkIQCacheMessage(incoming)
	if err != nil {
		return result, err
	}
	if !cacheRaw.Valid {
		cacheRaw = canonicalRaw
	}
	priorVersion := max(cache.Version, cacheDeletedVersion.Int64)
	changed := !cacheSeen
	if incoming.Version >= priorVersion || !cacheSeen {
		if !cacheRaw.Valid || cacheRaw.String != string(safeIncoming.Raw) {
			changed = true
		}
		cacheRaw = sql.NullString{String: string(safeIncoming.Raw), Valid: len(safeIncoming.Raw) > 0}
	}
	if !incoming.DeletedAt.IsZero() {
		if state.Cache.DeletedAt == nil || incoming.Version > cacheDeletedVersion.Int64 {
			state.Cache.DeletedAt = &safeIncoming.DeletedAt
			state.Cache.DeletedVersion = incoming.Version
			changed = true
		}
	} else if !hasCache || incoming.Version >= cache.Version {
		changed = changed || !cacheJSON.Valid || cacheJSON.String != encoded
		cache, hasCache, cacheJSON = safeIncoming, true, sql.NullString{String: encoded, Valid: true}
	}
	if changed && !observed.IsZero() {
		state.Cache.SeenAt = &observed
	}
	state.Cache.Seen, state.Cache.Version, state.Cache.Text = true, cache.Version, cache.ContentText
	effective := reduceWorkIQState(state)
	if !hasCache {
		effective.Winner = "workiq_server"
	}
	canonical := cache
	if effective.Winner == "workiq_server" {
		canonical = server.Native
	}
	canonical.Raw = nil
	if cacheRaw.Valid {
		canonical.Raw = []byte(cacheRaw.String)
	}
	canonical.DeletedAt = time.Time{}
	if effective.DeletedAt != nil {
		canonical.DeletedAt = *effective.DeletedAt
	}
	var cacheProjection, rawValue any
	if cacheJSON.Valid {
		cacheProjection = cacheJSON.String
	}
	if cacheRaw.Valid {
		rawValue = cacheRaw.String
	}
	_, err = tx.ExecContext(ctx, `update workiq_rows set cache_seen=1,cache_projection=?,cache_raw_json=?,cache_version=?,
cache_seen_at=?,cache_deleted_at=?,cache_deleted_version=?,winner=? where kind='message' and account_id=? and item_rowid=?`,
		cacheProjection, rawValue, cache.Version, workIQTimeValue(state.Cache.SeenAt), workIQTimeValue(state.Cache.DeletedAt),
		state.Cache.DeletedVersion, effective.Winner, account, rowid)
	if err != nil {
		return result, err
	}
	result.Message, result.Bound, result.Changed = canonical, true, changed || oldWinner != effective.Winner
	result.PublishApps = effective.Winner == "desktop_cache" && effective.DeletedAt == nil
	return result, nil
}

func workIQTimeValue(at *time.Time) any {
	if at == nil {
		return nil
	}
	return at.UTC().Format(time.RFC3339Nano)
}
