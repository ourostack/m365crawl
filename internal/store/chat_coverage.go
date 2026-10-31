package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"time"
)

// The Teams conversation scopes a coverage answer judges on its own.
const (
	ChatScopeChats    = "chats"    // 1:1 and group chats, and the account's own notes
	ChatScopeMeetings = "meetings" // meeting chats
	ChatScopeChannels = "channels" // channels and teams
)

// chatScopeCond is the condition on the conversation alias c of a scope. Engage communities and
// Teams' 48: pseudo-conversations (notifications, call logs, drafts, mentions and the like) are
// in no scope, except the account's own notes.
func chatScopeCond(scope string) string {
	switch scope {
	case ChatScopeMeetings:
		return `(lower(c.kind)='meeting' and not ` + isChannelCond + `)`
	case ChatScopeChannels:
		return isChannelCond
	}
	return `(not ` + isChannelCond + ` and lower(c.kind) not in ('meeting','engagecommunity') and (c.id not like '48:%' or c.id='48:notes'))`
}

// authoredCond holds for a message someone posted: not a call event, thread activity, control
// message or call recording or transcript notice, which Teams writes itself.
const authoredCond = `(m.message_type not like 'Event/%' and m.message_type not like 'ThreadActivity/%' and m.message_type not like 'Control/%'
 and m.message_type not in ('RichText/Media_CallRecording','RichText/Media_CallTranscript'))`

// TeamsReadLog is when the archive read one Teams account's cache.
type TeamsReadLog struct {
	// Reads are the start times of successful reads (ok or ok_with_omissions) of a Teams source
	// whose account scope includes the account or is unknown, oldest first.
	Reads []time.Time
	// LastUnchanged is the start of the newest read that found the Teams cache unchanged.
	LastUnchanged time.Time
	// Legacy says the archive has no such runs, so Reads are the account's first_seen_at and
	// last_synced_at.
	Legacy bool
}

// TeamsReads reads the Teams read log of the account (<tenantId>/<userId>).
func (s *Store) TeamsReads(ctx context.Context, account string) (TeamsReadLog, error) {
	var out TeamsReadLog
	var scoped int
	if err := s.db.QueryRowContext(ctx, `select count(*) from pragma_table_info('sync_runs') where name='source_accounts_json'`).Scan(&scoped); err != nil {
		return out, err
	}
	column := "null"
	if scoped > 0 {
		column = "source_accounts_json"
	}
	err := s.each(ctx, `select source, status, started_at, `+column+` from sync_runs where source<>'' and status in ('ok','ok_with_omissions','unchanged') order by id`, func(rs *sql.Rows) error {
		var source, status, at, accounts sql.NullString
		_ = rs.Scan(&source, &status, &at, &accounts) // every column scans into a NullString
		var scope []string
		if family, _ := SourceFamily(source.String); family != "teams" || accounts.Valid && json.Unmarshal([]byte(accounts.String), &scope) == nil && len(scope) > 0 && !slices.Contains(scope, account) {
			return nil
		}
		t := parseTime(at)
		switch {
		case t.IsZero():
		case status.String == "unchanged":
			out.LastUnchanged = t
		default:
			out.Reads = append(out.Reads, t)
		}
		return nil
	})
	if err != nil || len(out.Reads) > 0 {
		slices.SortFunc(out.Reads, time.Time.Compare)
		return out, err
	}
	var first, last sql.NullString
	err = s.db.QueryRowContext(ctx, `select first_seen_at, last_synced_at from accounts where tenant_id||'/'||user_id=?`, account).Scan(&first, &last)
	if errors.Is(err, sql.ErrNoRows) {
		return out, nil
	}
	for _, t := range []time.Time{parseTime(first), parseTime(last)} {
		if !t.IsZero() && !slices.Contains(out.Reads, t) {
			out.Reads = append(out.Reads, t)
		}
	}
	out.Legacy = len(out.Reads) > 0
	return out, err
}

// ChatEvidence is what the archive holds of one account's conversations of a scope over a range,
// and the signs that Teams' cache did not hold all of it.
type ChatEvidence struct {
	// Items are authored messages that are not deleted, MyItems those the account sent, Deleted
	// the authored messages marked deleted.
	Items, MyItems, Deleted int
	// Behind counts conversations whose last_message_at is at or after the range's start and later
	// than their newest archived message, which is before the range's end: Teams knows of
	// activity the archive does not hold.
	Behind int
	// GapConversations counts conversations whose archived messages around the range skip Teams
	// sequence numbers, and PossiblyMissing the numbers skipped. Teams also numbers events it
	// never caches, so these are possibly, not certainly, messages.
	GapConversations, PossiblyMissing int
}

// ChatEvidence reads the evidence of [since, until) for the account's conversations of scope.
func (s *Store) ChatEvidence(ctx context.Context, tenantID, userID, scope string, since, until time.Time) (ChatEvidence, error) {
	var out ChatEvidence
	cond := chatScopeCond(scope)
	from, to := since.UTC().Format(timeLayout), until.UTC().Format(timeLayout)
	// The unary + keeps SQLite off the (tenant, user) index, which would scan every message of the
	// account; the sent_at index reads only the range.
	err := s.db.QueryRowContext(ctx, `select
 coalesce(sum(m.deleted_at is null),0),
 coalesce(sum(m.deleted_at is null and lower(m.sender_id)=lower('8:orgid:'||m.user_id)),0),
 coalesce(sum(m.deleted_at is not null),0)
from messages m join conversations c on c.tenant_id=m.tenant_id and c.user_id=m.user_id and c.id=m.conversation_id
where +m.tenant_id=? and +m.user_id=? and m.sent_at>=? and m.sent_at<? and `+authoredCond+` and `+cond, //nolint:gosec // G202: fixed package conditions
		tenantID, userID, from, to).Scan(&out.Items, &out.MyItems, &out.Deleted)
	if err != nil {
		return out, err
	}
	type conv struct{ id, last, newest sql.NullString }
	var convs []conv
	err = s.each(ctx, `select c.id, c.last_message_at, (select max(m.sent_at) from messages m where m.tenant_id=c.tenant_id and m.user_id=c.user_id and m.conversation_id=c.id)
from conversations c where c.tenant_id=? and c.user_id=? and c.last_message_at>=? and `+cond+` order by c.id`, func(rs *sql.Rows) error { //nolint:gosec // G202: fixed package conditions
		var c conv
		_ = rs.Scan(&c.id, &c.last, &c.newest) // every column scans into a NullString
		convs = append(convs, c)
		return nil
	}, tenantID, userID, from)
	if err != nil {
		return out, err
	}
	for _, c := range convs {
		// A conversation with no archived message has a NULL newest, which compares as "".
		if c.newest.String < c.last.String && c.newest.String < to {
			out.Behind++
		}
		missing, err := s.sequenceGap(ctx, tenantID, userID, c.id.String, from, to)
		if err != nil {
			return out, err
		}
		if missing > 0 {
			out.GapConversations++
			out.PossiblyMissing += missing
		}
	}
	return out, nil
}

// sequenceGap counts the Teams sequence numbers missing between the conversation's archived
// messages in [from, to), the nearest one before and the nearest one after. Every such pair
// spans part of the range, so whatever is missing between them may lie in it. A message without
// an integer sequenceId is left out, and the nearest numbered message stands in for it.
func (s *Store) sequenceGap(ctx context.Context, tenantID, userID, conversation, from, to string) (int, error) {
	const seq = `case when json_valid(raw_json) and json_type(raw_json,'$.sequenceId')='integer' then json_extract(raw_json,'$.sequenceId') end`
	const where = `tenant_id=? and user_id=? and conversation_id=?`
	var missing int
	err := s.db.QueryRowContext(ctx, `select coalesce(sum(case when q-p>1 then q-p-1 else 0 end),0) from (select q, lag(q) over (order by q) p from (
 select `+seq+` q from messages where `+where+` and sent_at>=? and sent_at<?
 union all select * from (select `+seq+` q from messages where `+where+` and sent_at<? and `+seq+` is not null order by sent_at desc limit 1)
 union all select * from (select `+seq+` q from messages where `+where+` and sent_at>=? and `+seq+` is not null order by sent_at limit 1)
) where q is not null)`, tenantID, userID, conversation, from, to, tenantID, userID, conversation, from, tenantID, userID, conversation, to).Scan(&missing)
	return missing, err
}
