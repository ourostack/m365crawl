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

type WorkIQMessageBatch struct {
	AttemptID  int64
	Chat       json.RawMessage
	Messages   []json.RawMessage
	ObservedAt time.Time
}

type WorkIQMessageResult struct {
	Counts Counts
	Gaps   map[string]int
}

var workIQMessageIDsQualified = workiq.IDQualified

func (s *Store) CommitWorkIQMessages(ctx context.Context, batch WorkIQMessageBatch) (WorkIQMessageResult, error) {
	binding, err := s.validateWorkIQAttemptOwner(ctx, batch.AttemptID)
	if err != nil {
		return WorkIQMessageResult{}, err
	}
	result, qualified, err := prepareWorkIQMessageBatch(ctx, binding, batch)
	if err != nil {
		return WorkIQMessageResult{}, err
	}
	if len(qualified) == 0 {
		return result, nil
	}
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		return commitWorkIQMessageRecords(ctx, tx, binding.Fact.ID, qualified, batch.ObservedAt, &result)
	})
	if err != nil {
		return WorkIQMessageResult{}, err
	}
	return result, nil
}

func prepareWorkIQMessageBatch(ctx context.Context, binding workiq.AccountBinding, batch WorkIQMessageBatch) (WorkIQMessageResult, []workiq.ChatMessage, error) {
	chat, err := workiq.DecodeChat(ctx, batch.Chat)
	if err != nil {
		return WorkIQMessageResult{}, nil, err
	}
	if binding.Fact.Family != "chats" || binding.Fact.TenantID != chat.TenantID || chat.TenantID == "" || batch.ObservedAt.IsZero() {
		return WorkIQMessageResult{}, nil, errors.New("WorkIQ messages do not match the verified archive tenant")
	}
	result := WorkIQMessageResult{Gaps: map[string]int{}}
	seen := map[string]bool{}
	size := len(batch.Chat)
	var qualified []workiq.ChatMessage
	for _, raw := range batch.Messages {
		size += len(raw)
		if size > 16<<20 {
			return WorkIQMessageResult{}, nil, errors.New("WorkIQ message batch exceeds the page bound")
		}
		header, err := workiq.DecodeMessageHeader(ctx, raw)
		if err != nil {
			return WorkIQMessageResult{}, nil, err
		}
		if seen[header.ID] {
			return WorkIQMessageResult{}, nil, errors.New("WorkIQ page repeats a message identity")
		}
		seen[header.ID] = true
		result.Counts.Seen++
		class := workiq.ClassChat
		if chat.Type == "channel" {
			class = workiq.ClassChannelRoot
			if header.ReplyToID != "" {
				class = workiq.ClassChannelReply
			}
		}
		if !workIQMessageIDsQualified(class) {
			result.Gaps["workiq_message_identity_unverified"]++
			continue
		}
		if header.Removed {
			result.Counts.Unchanged++
			continue // @removed is not an explicit deletedDateTime.
		}
		message, err := workiq.DecodeChatMessage(ctx, binding.Fact, chat, raw)
		if err != nil {
			return WorkIQMessageResult{}, nil, err
		}
		qualified = append(qualified, message)
	}
	return result, qualified, nil
}

func commitWorkIQMessageRecords(ctx context.Context, tx *sql.Tx, account string, qualified []workiq.ChatMessage, observed time.Time, result *WorkIQMessageResult) error {
	records := make([]workIQServerMessageRecord, 0, len(qualified))
	native := make([]teamsdesktop.Message, 0, len(qualified))
	reduced := map[string]workIQCacheMessageResult{}
	for _, message := range qualified {
		record, canonical, firstBinding, unverified, err := reduceWorkIQServerMessage(ctx, tx, account, message, observed)
		if err != nil {
			return err
		}
		if unverified {
			result.Gaps["workiq_version_unverified"]++
		}
		records, native = append(records, record), append(native, canonical)
		reduced[canonical.ID] = workIQCacheMessageResult{Message: canonical, Bound: true, Changed: firstBinding}
	}
	counts, _, rows, err := applyMessageSources(ctx, tx, native, reduced)
	if err != nil {
		return err
	}
	for i, record := range records {
		if err := record.persist(ctx, tx, account, rows[i].Rowid, observed); err != nil {
			return err
		}
	}
	result.Counts.Inserted, result.Counts.Updated = counts.Inserted, counts.Updated
	result.Counts.Unchanged += counts.Unchanged
	return nil
}

type workIQServerMessageRecord struct {
	Server     workiq.ChatMessage
	LatestRaw  json.RawMessage
	Cache      teamsdesktop.Message
	CacheJSON  sql.NullString
	CacheRaw   sql.NullString
	CacheFirst bool
	State      WorkIQReduceInput
	Winner     string
}

func loadWorkIQNativeMessage(ctx context.Context, tx *sql.Tx, id teamsdesktop.Message) (teamsdesktop.Message, int64, error) {
	m := teamsdesktop.Message{TenantID: id.TenantID, UserID: id.UserID, ConversationID: id.ConversationID, ID: id.ID}
	var rowid int64
	var sent string
	var edited, deleted, mentions, reactions, files, links, raw sql.NullString
	err := tx.QueryRowContext(ctx, `select rowid,reply_chain_id,parent_message_id,client_message_id,sender_id,sender_name,sent_at,edited_at,deleted_at,
message_type,content_type,content_html,content_text,version,mentions_json,mentions_me,reactions_json,files_json,links_json,subject,importance,pinned,link,raw_json
from messages where tenant_id=? and user_id=? and conversation_id=? and id=?`,
		id.TenantID, id.UserID, id.ConversationID, id.ID).
		Scan(&rowid, &m.ReplyChainID, &m.ParentMessageID, &m.ClientMessageID, &m.SenderID, &m.SenderName, &sent, &edited, &deleted,
			&m.MessageType, &m.ContentType, &m.ContentHTML, &m.ContentText, &m.Version, &mentions, &m.MentionsMe, &reactions, &files, &links,
			&m.Subject, &m.Importance, &m.Pinned, &m.Link, &raw)
	if err != nil {
		return m, 0, err
	}
	for _, clock := range []struct {
		raw  sql.NullString
		into *time.Time
	}{
		{sql.NullString{String: sent, Valid: sent != ""}, &m.SentAt}, {edited, &m.EditedAt}, {deleted, &m.DeletedAt},
	} {
		at, err := workIQStoredTime(clock.raw)
		if err != nil {
			return m, 0, err
		}
		if at != nil {
			*clock.into = *at
		}
	}
	for _, value := range []struct {
		raw  sql.NullString
		into any
	}{{mentions, &m.Mentions}, {reactions, &m.Reactions}, {files, &m.Files}, {links, &m.Links}} {
		if value.raw.Valid && json.Unmarshal([]byte(value.raw.String), value.into) != nil {
			return m, 0, errors.New("invalid archived native message projection")
		}
	}
	if raw.Valid {
		m.Raw = []byte(raw.String)
	}
	return m, rowid, nil
}

func reduceWorkIQServerMessage(ctx context.Context, tx *sql.Tx, account string, incoming workiq.ChatMessage, observed time.Time) (workIQServerMessageRecord, teamsdesktop.Message, bool, bool, error) {
	record := workIQServerMessageRecord{LatestRaw: incoming.Raw}
	native, rowid, err := loadWorkIQNativeMessage(ctx, tx, incoming.Native)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return record, native, false, false, err
	}
	exists := err == nil
	record.CacheFirst, record.State.Cache.Seen = exists, exists
	var serverJSON, latestRaw string
	var cacheSeen, serverSeen bool
	var cacheAt, serverAt, cacheDeleted, serverDeleted, serverDeletedVersion sql.NullString
	var cacheVersion, cacheDeletedVersion sql.NullInt64
	bound := false
	if exists {
		err = tx.QueryRowContext(ctx, `select cache_first,cache_seen,server_seen,cache_projection,server_projection,cache_raw_json,server_json,
cache_seen_at,server_seen_at,cache_deleted_at,server_deleted_at,cache_version,cache_deleted_version,server_deleted_version
from workiq_rows where kind='message' and account_id=? and item_rowid=?`, account, rowid).
			Scan(&record.CacheFirst, &cacheSeen, &serverSeen, &record.CacheJSON, &serverJSON, &record.CacheRaw, &latestRaw,
				&cacheAt, &serverAt, &cacheDeleted, &serverDeleted, &cacheVersion, &cacheDeletedVersion, &serverDeletedVersion)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return record, native, false, false, err
		}
		bound = err == nil
	}
	if bound {
		if json.Unmarshal([]byte(serverJSON), &record.Server) != nil || record.Server.Native.TenantID != incoming.Native.TenantID ||
			record.Server.Native.UserID != incoming.Native.UserID || record.Server.Native.ConversationID != incoming.Native.ConversationID ||
			record.Server.Native.ID != incoming.Native.ID || record.Server.Class != incoming.Class || len(record.Server.Native.Raw) != 0 {
			return record, native, false, false, errors.New("invalid stored server message source identity")
		}
		record.State.Cache.Seen, record.State.Server.Seen = cacheSeen, serverSeen
		if !record.CacheJSON.Valid && cacheVersion.Valid && cacheVersion.Int64 != 0 {
			return record, native, false, false, errors.New("inconsistent retained cache source version")
		}
		if record.CacheJSON.Valid {
			if json.Unmarshal([]byte(record.CacheJSON.String), &record.Cache) != nil || record.Cache.TenantID != incoming.Native.TenantID ||
				record.Cache.UserID != incoming.Native.UserID || record.Cache.ConversationID != incoming.Native.ConversationID ||
				record.Cache.ID != incoming.Native.ID || !cacheSeen || !cacheVersion.Valid || record.Cache.Version != cacheVersion.Int64 {
				return record, native, false, false, errors.New("invalid stored native message source identity")
			}
		}
		for _, clock := range []struct {
			value sql.NullString
			into  **time.Time
		}{
			{cacheAt, &record.State.Cache.SeenAt}, {serverAt, &record.State.Server.SeenAt},
			{cacheDeleted, &record.State.Cache.DeletedAt}, {serverDeleted, &record.State.Server.DeletedAt},
			{serverDeletedVersion, &record.State.Server.DeletedVersionAt},
		} {
			*clock.into, err = workIQStoredTime(clock.value)
			if err != nil {
				return record, native, false, false, err
			}
		}
		record.State.Cache.DeletedVersion = cacheDeletedVersion.Int64
	} else if exists {
		var encoded string
		record.Cache, encoded, err = safeWorkIQCacheMessage(native)
		if err != nil {
			return record, native, false, false, err
		}
		record.CacheJSON = sql.NullString{String: encoded, Valid: true}
		record.CacheRaw = sql.NullString{String: string(record.Cache.Raw), Valid: len(record.Cache.Raw) > 0}
		if !native.DeletedAt.IsZero() {
			record.State.Cache.DeletedAt = &record.Cache.DeletedAt
			record.State.Cache.DeletedVersion = native.Version
		}
	}
	state := &record.State
	state.CrossVersionComparable = workIQVersionsComparable(string(incoming.Class))
	state.Cache.Version, state.Cache.Text = record.Cache.Version, record.Cache.ContentText
	previous := record.Server
	if bound {
		newest := previous.ModifiedAt
		if state.Server.DeletedVersionAt != nil && state.Server.DeletedVersionAt.After(newest) {
			newest = *state.Server.DeletedVersionAt
		}
		if incoming.ModifiedAt.Before(newest) {
			record.LatestRaw = json.RawMessage(latestRaw)
		}
	}
	if !incoming.Native.DeletedAt.IsZero() {
		if state.Server.DeletedVersionAt == nil || incoming.ModifiedAt.After(*state.Server.DeletedVersionAt) {
			state.Server.DeletedAt = &incoming.Native.DeletedAt
			state.Server.DeletedVersionAt = &incoming.ModifiedAt
		}
		if !bound {
			record.Server = incoming
		}
	} else if !bound || incoming.ModifiedAt.Compare(record.Server.ModifiedAt) >= 0 {
		if incoming.Native.ContentText == "" && previous.Native.ContentText != "" {
			incoming.Native.ContentText, incoming.Native.ContentHTML = previous.Native.ContentText, previous.Native.ContentHTML
		}
		record.Server = incoming
		state.Server.SeenAt = &observed
	}
	state.Server.Seen = true
	state.Server.Version, state.Server.Text = record.Server.Native.Version, record.Server.Native.ContentText
	if !record.Server.ModifiedAt.IsZero() {
		state.Server.VersionAt = &record.Server.ModifiedAt
	}
	if state.Server.DeletedVersionAt != nil {
		state.Server.DeletedVersion = state.Server.DeletedVersionAt.UnixMilli()
	}
	effective := reduceWorkIQState(*state)
	if !record.CacheJSON.Valid {
		effective.Winner = "workiq_server"
	}
	record.Winner = effective.Winner
	canonical := record.Cache
	if effective.Winner == "workiq_server" {
		canonical = record.Server.Native
	}
	canonical.Raw = nil
	if record.CacheRaw.Valid {
		canonical.Raw = []byte(record.CacheRaw.String)
	}
	canonical.DeletedAt = time.Time{}
	if effective.DeletedAt != nil {
		canonical.DeletedAt = *effective.DeletedAt
	}
	return record, canonical, !bound, effective.VersionUnverified, nil
}

func (record workIQServerMessageRecord) persist(ctx context.Context, tx *sql.Tx, account string, rowid int64, observed time.Time) error {
	serverJSON, err := json.Marshal(record.Server)
	if err != nil {
		return errors.New("invalid server message projection clock")
	}
	var cacheJSON, cacheRaw any
	if record.CacheJSON.Valid {
		cacheJSON = record.CacheJSON.String
	}
	if record.CacheRaw.Valid {
		cacheRaw = record.CacheRaw.String
	}
	_, err = tx.ExecContext(ctx, `insert into workiq_rows(kind,account_id,item_rowid,graph_id,server_json,server_projection,cache_projection,
cache_first,cache_seen,server_seen,cache_raw_json,cache_version,server_version,cache_seen_at,server_seen_at,cache_deleted_at,server_deleted_at,
cache_deleted_version,server_deleted_version,winner,modified_at) values('message',?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
on conflict(kind,account_id,item_rowid) do update set server_json=excluded.server_json,server_projection=excluded.server_projection,
server_version=excluded.server_version,server_seen=excluded.server_seen,server_seen_at=excluded.server_seen_at,
server_deleted_at=excluded.server_deleted_at,server_deleted_version=excluded.server_deleted_version,winner=excluded.winner,modified_at=excluded.modified_at`,
		account, rowid, record.Server.Native.ID, string(record.LatestRaw), string(serverJSON), cacheJSON, b2i(record.CacheFirst),
		b2i(record.State.Cache.Seen), b2i(record.State.Server.Seen), cacheRaw, record.Cache.Version, workIQTimeValue(record.State.Server.VersionAt),
		workIQTimeValue(record.State.Cache.SeenAt), workIQTimeValue(record.State.Server.SeenAt), workIQTimeValue(record.State.Cache.DeletedAt),
		workIQTimeValue(record.State.Server.DeletedAt), record.State.Cache.DeletedVersion, workIQTimeValue(record.State.Server.DeletedVersionAt),
		record.Winner, workIQTimeValue(&observed))
	return err
}
