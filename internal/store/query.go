package store

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	crawlstore "github.com/openclaw/crawlkit/store"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/teamsdesktop"
)

// Filter narrows message, search, unread and listing queries.
type Filter struct {
	Account      *teamsdesktop.Account // nil: every account
	Conversation string                // conversation id, or an exact title / display name
	From         string                // person id exactly, else case-insensitive substring of sender_name
	Since, Until time.Time
	Limit        int // 0 means DefaultLimit
	// IncludeDeleted lists deleted messages too (they are never counted as unread).
	IncludeDeleted bool
	MentionsMe     bool // only messages that mention the account's own user
	// DirectMentions keeps only messages that mention the account's own user by name: a person
	// mention, not a channel, team, tag or @everyone broadcast (see MessageRow.MentionKind).
	DirectMentions bool
	Unread         bool // only unread messages (see unreadCond)
	// IncludeChannels counts channel and team conversations (kinds Topic, Space) as unread
	// sources too; by default unread covers chats and meetings only.
	IncludeChannels bool
	// IncludeSystem keeps Teams' system pseudo-conversations (see SystemConversationIDs), which
	// are left out by default.
	IncludeSystem bool
	// Team narrows to one team: its id, or its name compared case-insensitively and exactly. It
	// matches the team's own conversation and every conversation whose team_id is the team. An
	// unknown or ambiguous name is a usage error.
	Team string
	// Total, when non-nil, receives the exact number of matches ignoring Limit, but only when the
	// result is truncated (it costs one extra COUNT query then); otherwise it is left unchanged.
	Total *int
}

// systemConversationIDs are the pseudo-conversations Teams keeps for its notification feed, call
// log and annotations. Their messages mirror real ones, so reads leave them out unless asked.
// 48:notes (the user's own notes) is a real conversation and stays in.
var systemConversationIDs = []string{"48:notifications", "48:calllogs", "48:annotations"}

// SystemConversationIDs returns a copy of the system pseudo-conversation ids.
func SystemConversationIDs() []string { return append([]string(nil), systemConversationIDs...) }

// notSystemCond is a condition that holds when the conversation id in col is not a system one.
// The ids are package constants, so they are safe to inline.
func notSystemCond(col string) string {
	q := make([]string, len(systemConversationIDs))
	for i, id := range systemConversationIDs {
		q[i] = "'" + id + "'"
	}
	return col + ` not in (` + strings.Join(q, ",") + `)`
}

func (f Filter) limit() int {
	if f.Limit <= 0 {
		return DefaultLimit
	}
	return f.Limit
}

// MessageRow is a message as read back, with its conversation's composed display name.
type MessageRow struct {
	TenantID                string                 `json:"tenant_id"`
	UserID                  string                 `json:"user_id"`
	ConversationID          string                 `json:"conversation_id"`
	ConversationDisplayName string                 `json:"conversation_display_name"`
	ID                      string                 `json:"id"`
	ReplyChainID            string                 `json:"reply_chain_id,omitempty"`
	ParentMessageID         string                 `json:"parent_message_id,omitempty"`
	ClientMessageID         string                 `json:"client_message_id,omitempty"`
	SenderID                string                 `json:"sender_id"`
	SenderName              string                 `json:"sender_name"`
	SentAt                  time.Time              `json:"sent_at"`
	EditedAt                time.Time              `json:"edited_at,omitzero"`
	DeletedAt               time.Time              `json:"deleted_at,omitzero"`
	MessageType             string                 `json:"message_type"`
	ContentType             string                 `json:"content_type"`
	ContentText             string                 `json:"content_text"`
	Version                 int64                  `json:"version"`
	Mentions                []teamsdesktop.Mention `json:"mentions"`
	MentionsMe              bool                   `json:"mentions_me"`
	// MentionKind says how the message mentions the account: person (by name), channel, team, tag,
	// everyone, or other when the feed gives a kind this version does not know. Empty unless
	// MentionsMe.
	MentionKind string                  `json:"mention_kind,omitempty"`
	Reactions   []teamsdesktop.Reaction `json:"reactions"`
	Files       []teamsdesktop.File     `json:"files"`
	Links       []string                `json:"links"`
	Subject     string                  `json:"subject,omitempty"`
	Importance  string                  `json:"importance,omitempty"`
	Pinned      bool                    `json:"pinned"`
	Link        string                  `json:"link,omitempty"`
	// ReplyCount and LastReplyAt describe a channel thread root: the number of live replies and
	// when the newest one was sent. ReplyCount is nil for every other message (a reply, a chat
	// message), so 0 means "a root nobody has answered".
	ReplyCount  *int      `json:"reply_count,omitempty"`
	LastReplyAt time.Time `json:"last_reply_at,omitzero"`

	replyRoot bool // a channel thread root: its replies are counted by fillReplies
}

// ConversationRow is a conversation with its composed display name.
type ConversationRow struct {
	TenantID      string    `json:"tenant_id"`
	UserID        string    `json:"user_id"`
	ID            string    `json:"id"`
	Kind          string    `json:"kind"`
	Title         string    `json:"title"`
	Topic         string    `json:"topic,omitempty"`
	DisplayName   string    `json:"display_name"`
	TeamID        string    `json:"team_id,omitempty"`
	ParentID      string    `json:"parent_id,omitempty"`
	Members       []string  `json:"members"`
	LastMessageAt time.Time `json:"last_message_at,omitzero"`
	ReadHorizonAt time.Time `json:"read_horizon_at,omitzero"`
	Favorite      bool      `json:"favorite"`
	// CalendarSeriesKey and CalendarEventCount place a Meeting conversation in the calendar: the
	// series its events share and how many live, non-cancelled occurrences the archive holds (a declined
	// one counts: the meeting happened). Both are empty for
	// a chat no event names. See Store.linkCalendar.
	CalendarSeriesKey  string `json:"calendar_series_key,omitempty"`
	CalendarEventCount int    `json:"calendar_event_count,omitempty"`
}

// PersonRow is someone seen as a Teams sender or member, or as a mail sender or recipient. A mail
// correspondent has the id "mail:<address>", its lower-case address as Email, no tenant and the
// source "mail"; a Teams person has the source "chats" and no email (Teams keeps none).
type PersonRow struct {
	TenantID    string    `json:"tenant_id"`
	ID          string    `json:"id"`
	DisplayName string    `json:"display_name"`
	Email       string    `json:"email,omitempty"`
	Sources     []string  `json:"sources"`
	FirstSeenAt time.Time `json:"first_seen_at,omitzero"`
	LastSeenAt  time.Time `json:"last_seen_at,omitzero"`
}

// ActivityFilter narrows the activity feed.
type ActivityFilter struct {
	Account *teamsdesktop.Account
	Unread  bool
	Type    string // case-insensitive exact match
	// DirectMentions keeps only mention items that name the account's own user (subtype person).
	DirectMentions bool
	Since          time.Time
	Limit          int // 0 means DefaultLimit
	// IncludeSystem also joins messages of the system pseudo-conversations.
	IncludeSystem bool
	Team          string // as Filter.Team
	Total         *int   // as Filter.Total
}

// ActivityRow is a feed item joined to its message (when archived) and conversation.
type ActivityRow struct {
	TenantID                string    `json:"tenant_id"`
	UserID                  string    `json:"user_id"`
	ID                      string    `json:"id"`
	Type                    string    `json:"type"`
	Subtype                 string    `json:"subtype,omitempty"`
	IsRead                  bool      `json:"is_read"`
	At                      time.Time `json:"at"`
	ConversationID          string    `json:"conversation_id,omitempty"`
	ConversationDisplayName string    `json:"conversation_display_name"`
	MessageID               string    `json:"message_id,omitempty"`
	ReplyChainID            string    `json:"reply_chain_id,omitempty"`
	AppID                   string    `json:"app_id,omitempty"`
	MessageText             string    `json:"message_text"`
	SenderID                string    `json:"sender_id,omitempty"`
	SenderName              string    `json:"sender_name"`
	MessageSentAt           time.Time `json:"message_sent_at,omitzero"`
	MessageLink             string    `json:"message_link,omitempty"`
	// ActorID and ActorName say who did the thing the item reports: who reacted, replied or
	// mentioned. They stay empty for a type whose data names no one (see actorOf).
	ActorID   string `json:"actor_id,omitempty"`
	ActorName string `json:"actor_name,omitempty"`
	// ActorInferred is true when the actor is a best-effort match (a reaction's reactor, picked by
	// time), false when the data names it exactly (the related message's sender).
	ActorInferred bool `json:"actor_inferred,omitempty"`
}

// WhoamiRow is one archived account.
type WhoamiRow struct {
	TenantID     string    `json:"tenant_id"`
	UserID       string    `json:"user_id"`
	SelfID       string    `json:"self_id"` // the account's own MRI, as it appears in sender_id
	DisplayName  string    `json:"display_name"`
	Locale       string    `json:"locale,omitempty"`
	FirstSeenAt  time.Time `json:"first_seen_at,omitzero"`
	LastSyncedAt time.Time `json:"last_synced_at,omitzero"`
}

const (
	// chanName / teamName pick a conversation's own name; the composed display name prefixes the
	// team's name (from the team's own conversation row, same account) to a channel's.
	chanName = `coalesce(nullif(c.display_name,''),nullif(c.topic,''),nullif(c.title,''),'')`
	teamName = `coalesce(nullif(t.display_name,''),nullif(t.topic,''),nullif(t.title,''),'')`
	cdnExpr  = `(case when t.id is not null and ` + teamName + `<>'' and ` + chanName + `<>'' and instr(` + chanName + `,' › ')=0 then ` + teamName + `||' › '||` + chanName + ` else ` + chanName + ` end)`
	// unreadCond: newer than the conversation's read horizon, not sent by the account's own user,
	// not deleted; messageWhere also excludes channels (isChannelCond) unless Filter.IncludeChannels.
	// A conversation with no known horizon has no unread messages.
	unreadCond = `(c.read_horizon_at is not null and m.sent_at > c.read_horizon_at and m.deleted_at is null and lower(m.sender_id) <> lower('8:orgid:' || m.user_id))`

	// isChannelCond is the store's single channel test: the conversation kind is Topic (channel) or
	// Space (team), compared case-insensitively, or its id is a channel thread id (the same
	// @thread.tacv2 / @thread.skype suffixes as teamsdesktop's isChannelID). Either signal alone
	// makes it a channel, so a kind/id disagreement cannot leak channel unread.
	isChannelCond = `(lower(c.kind) in ('topic','space') or c.id like '%@thread.tacv2' or c.id like '%@thread.skype')`
	// mentionsMeExpr: the mapper saw a person mention of the account's user, or the account's own
	// activity feed holds a mention row (mention, mentionInChat; covers team, channel, tag and
	// everyone mentions) for this conversation and message. Evaluated at query time so activity
	// that arrives in a later sync counts without rewriting the message.
	mentionsMeExpr = `(m.mentions_me=1 or exists(select 1 from activity a where a.tenant_id=m.tenant_id and a.user_id=m.user_id and a.conversation_id=m.conversation_id and a.message_id=m.id and a.type like 'mention%'))`

	// mentionKindExpr is how the message mentions the account, for a message that does (see
	// mentionsMeExpr). A person mention by id is "person"; otherwise the account's own feed says
	// why it was notified (the mention item's subtype), the person subtype first. A subtype this
	// version does not know is "other". Without any feed item the message is not a mention.
	mentionKindExpr = `(case when m.mentions_me=1 then 'person' else coalesce((select case lower(a.subtype) when 'person' then 'person' when 'channel' then 'channel' when 'team' then 'team' when 'tag' then 'tag' when 'everyone' then 'everyone' else 'other' end from activity a where a.tenant_id=m.tenant_id and a.user_id=m.user_id and a.conversation_id=m.conversation_id and a.message_id=m.id and a.type like 'mention%' order by lower(a.subtype)='person' desc, a.at desc limit 1),'') end)`
	// directMentionExpr holds for a message that mentions the account by name: a person mention by
	// id, or a mention item whose subtype is person.
	directMentionExpr = `(m.mentions_me=1 or exists(select 1 from activity a where a.tenant_id=m.tenant_id and a.user_id=m.user_id and a.conversation_id=m.conversation_id and a.message_id=m.id and a.type like 'mention%' and lower(a.subtype)='person'))`

	// senderNameExpr is the message's own sender name, else the name the people table holds for
	// the sender id (call events and many bot posts carry an id but no name).
	senderNameExpr = `coalesce(nullif(m.sender_name,''),p.display_name,'')`

	// replyRootCond holds for a channel thread root: a channel message that starts its own reply
	// chain. Only such rows get a reply count (see fillReplies), which is looked up after the
	// limit so a long list is not counted row by row before it is cut.
	replyRootCond = `(` + isChannelCond + ` and (m.reply_chain_id='' or m.reply_chain_id=m.id))`

	msgCols = `m.tenant_id,m.user_id,m.conversation_id,` + cdnExpr + `,m.id,m.reply_chain_id,m.parent_message_id,m.client_message_id,m.sender_id,` + senderNameExpr + `,m.sent_at,m.edited_at,m.deleted_at,m.message_type,m.content_type,m.content_text,m.version,m.mentions_json,` + mentionsMeExpr + `,` + mentionKindExpr + `,m.reactions_json,m.files_json,m.links_json,m.subject,m.importance,m.pinned,m.link,case when ` + replyRootCond + ` then 1 else 0 end`
	msgJoin = ` left join conversations c on c.tenant_id=m.tenant_id and c.user_id=m.user_id and c.id=m.conversation_id
 left join conversations t on t.tenant_id=c.tenant_id and t.user_id=c.user_id and c.team_id<>'' and c.team_id<>c.id and t.id=c.team_id
 left join people p on p.tenant_id=m.tenant_id and p.id=m.sender_id`

	// teamCond narrows to one resolved team id: the team's own conversation and its channels.
	teamCond = `(c.team_id=? or c.id=?)`
)

func escapeLike(s string) string { return crawlstore.EscapeLike(s) }

type where struct {
	conds []string
	args  []any
}

func (w *where) add(cond string, args ...any) {
	w.conds = append(w.conds, cond)
	w.args = append(w.args, args...)
}

func (w *where) sql() string {
	if len(w.conds) == 0 {
		return ""
	}
	return " where " + strings.Join(w.conds, " and ")
}

// messageWhere adds the Filter's conditions on the message alias m.
func (s *Store) messageWhere(ctx context.Context, w *where, f Filter) error {
	if f.Account != nil {
		w.add(`m.tenant_id=? and m.user_id=?`, f.Account.TenantID, f.Account.UserID)
	}
	if !f.IncludeDeleted || f.Unread {
		w.add(`m.deleted_at is null`)
	}
	if !f.IncludeSystem {
		w.add(notSystemCond(`m.conversation_id`))
	}
	if f.Conversation != "" {
		w.add(`(m.conversation_id=? or c.title=? collate nocase or c.display_name=? collate nocase or `+cdnExpr+`=? collate nocase)`, f.Conversation, f.Conversation, f.Conversation, f.Conversation)
	}
	if f.Team != "" {
		id, err := s.resolveTeam(ctx, f.Account, f.Team)
		if err != nil {
			return err
		}
		w.add(teamCond, id, id)
	}
	if f.From != "" {
		var exact int
		probeScope, probeArgs := "", []any{f.From}
		if f.Account != nil { // another account's sender ids must not decide how this account's filter matches
			probeScope, probeArgs = ` and tenant_id=? and user_id=?`, append(probeArgs, f.Account.TenantID, f.Account.UserID)
		}
		if err := s.db.QueryRowContext(ctx, `select exists(select 1 from messages where sender_id=?`+probeScope+`)`, probeArgs...).Scan(&exact); err != nil {
			return err
		}
		if exact == 1 {
			w.add(`m.sender_id=?`, f.From)
		} else {
			w.add(`lower(`+senderNameExpr+`) like ? escape '\'`, "%"+strings.ToLower(escapeLike(f.From))+"%")
		}
	}
	if !f.Since.IsZero() {
		w.add(`m.sent_at>=?`, fmtTime(f.Since))
	}
	if !f.Until.IsZero() {
		w.add(`m.sent_at<=?`, fmtTime(f.Until))
	}
	if f.MentionsMe {
		w.add(mentionsMeExpr)
	}
	if f.DirectMentions {
		w.add(directMentionExpr)
	}
	if f.Unread {
		w.add(unreadCond)
		if !f.IncludeChannels {
			w.add(`not ` + isChannelCond)
		}
	}
	return nil
}

func scanMessages(rows *sql.Rows) ([]MessageRow, error) {
	defer func() { _ = rows.Close() }()
	out := []MessageRow{}
	for rows.Next() {
		var r MessageRow
		var sent, edited, deleted, mentions, reactions, files, links sql.NullString
		var mentionsMe, pinned, root int
		if err := rows.Scan(&r.TenantID, &r.UserID, &r.ConversationID, &r.ConversationDisplayName, &r.ID, &r.ReplyChainID, &r.ParentMessageID, &r.ClientMessageID, &r.SenderID, &r.SenderName, &sent, &edited, &deleted, &r.MessageType, &r.ContentType, &r.ContentText, &r.Version, &mentions, &mentionsMe, &r.MentionKind, &reactions, &files, &links, &r.Subject, &r.Importance, &pinned, &r.Link, &root); err != nil {
			return nil, err
		}
		r.SentAt, r.EditedAt, r.DeletedAt, r.replyRoot = parseTime(sent), parseTime(edited), parseTime(deleted), root == 1
		r.Mentions, r.Reactions, r.Files, r.Links = unmarshalNull[teamsdesktop.Mention](mentions), unmarshalNull[teamsdesktop.Reaction](reactions), unmarshalNull[teamsdesktop.File](files), unmarshalNull[string](links)
		r.MentionsMe, r.Pinned = mentionsMe == 1, pinned == 1
		out = append(out, r)
	}
	return out, rows.Err()
}

// runMessages runs a message query ordered newest first and trims the extra row that detects
// truncation. When the result is truncated and total is not nil, total receives the number of
// matching messages.
func (s *Store) runMessages(ctx context.Context, from string, w *where, limit int, total *int) ([]MessageRow, bool, error) {
	q := `select ` + msgCols + from + w.sql() + ` order by m.sent_at desc, m.id desc limit ?` //nolint:gosec // G202: fragments are package constants; values are placeholders
	rows, err := s.db.QueryContext(ctx, q, append(w.args, limit+1)...)
	if err != nil {
		return nil, false, err
	}
	out, err := scanMessages(rows)
	if err != nil {
		return nil, false, err
	}
	trunc := len(out) > limit
	if trunc {
		out = out[:limit]
		if err := s.countTotal(ctx, total, from, w); err != nil {
			return nil, false, err
		}
	}
	if err := s.fillReplies(ctx, out); err != nil {
		return nil, false, err
	}
	if err := nameUntitled(ctx, s, out, func(r *MessageRow) (convRef, *string) {
		return convRef{r.TenantID, r.UserID, r.ConversationID}, &r.ConversationDisplayName
	}); err != nil {
		return nil, false, err
	}
	return out, trunc, nil
}

// fillReplies sets ReplyCount and LastReplyAt on the channel thread roots among rows: the live
// replies stored under the root's reply chain, and the newest one's send time.
func (s *Store) fillReplies(ctx context.Context, rows []MessageRow) error {
	var stmt *sql.Stmt
	defer func() {
		if stmt != nil {
			_ = stmt.Close()
		}
	}()
	for i := range rows {
		r := &rows[i]
		if !r.replyRoot {
			continue
		}
		if stmt == nil {
			var err error
			if stmt, err = s.db.PrepareContext(ctx, `select count(*),max(sent_at) from messages where tenant_id=? and user_id=? and conversation_id=? and reply_chain_id=? and id<>? and deleted_at is null`); err != nil {
				return err
			}
		}
		var n int
		var last sql.NullString
		if err := stmt.QueryRowContext(ctx, r.TenantID, r.UserID, r.ConversationID, r.ID, r.ID).Scan(&n, &last); err != nil {
			return err
		}
		r.ReplyCount, r.LastReplyAt = &n, parseTime(last)
	}
	return nil
}

// countTotal stores in total the number of rows the query "select ... <from> <where>" matches
// without its limit. A nil total means the caller does not want it.
func (s *Store) countTotal(ctx context.Context, total *int, from string, w *where) error {
	if total == nil {
		return nil
	}
	if pruneJoinsOff {
		return s.db.QueryRowContext(ctx, `select count(*)`+from+w.sql(), w.args...).Scan(total) //nolint:gosec // G202: fragments are package constants; values are placeholders
	}
	return s.db.QueryRowContext(ctx, `select count(*)`+pruneJoins(from, w.sql())+w.sql(), w.args...).Scan(total) //nolint:gosec // G202: fragments are package constants; values are placeholders
}

// pruneJoinsOff makes countTotal count over the full from clause; a test sets it to compare.
var pruneJoinsOff bool

// pruneJoins drops the left joins of a from clause that the where clause (and the joins kept)
// never refer to. It rewrites SQL text: a join is kept when its alias followed by a dot ("t.")
// occurs in the where clause or in a kept join, so the aliases (c, t, p, m, sp) must never appear
// as "<alias>." inside a literal or another token. Every join here is a left join on a unique key, so it adds no rows and a count
// is the same without it; skipping them makes the count over a large archive several times faster.
func pruneJoins(from, whereSQL string) string {
	parts := strings.Split(from, " left join ")
	base, joins := parts[0], parts[1:]
	keep := make([]bool, len(joins))
	text := whereSQL
	for changed := true; changed; {
		changed = false
		for i, j := range joins {
			if keep[i] {
				continue
			}
			alias := strings.Fields(j)[1]
			if aliasRef(alias).MatchString(text) {
				keep[i], changed = true, true
				text += " " + j
			}
		}
	}
	for i, j := range joins {
		if keep[i] {
			base += " left join " + j
		}
	}
	return base
}

var aliasRefs = map[string]*regexp.Regexp{}

// aliasRef is the pattern for a reference to a join alias, compiled once per alias.
func aliasRef(alias string) *regexp.Regexp {
	aliasRefsMu.Lock()
	defer aliasRefsMu.Unlock()
	re, ok := aliasRefs[alias]
	if !ok {
		re = regexp.MustCompile(`\b` + regexp.QuoteMeta(alias) + `\.`)
		aliasRefs[alias] = re
	}
	return re
}

var aliasRefsMu sync.Mutex

// Messages lists messages in chronological order. With a Limit it returns the newest Limit
// messages matching the filter (still oldest first), and truncated says older ones exist.
func (s *Store) Messages(ctx context.Context, f Filter) ([]MessageRow, bool, error) {
	var w where
	if err := s.messageWhere(ctx, &w, f); err != nil {
		return nil, false, err
	}
	rows, trunc, err := s.runMessages(ctx, ` from messages m`+msgJoin, &w, f.limit(), f.Total)
	if err != nil {
		return nil, false, err
	}
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
	return rows, trunc, nil
}

// hasFilter reports whether f narrows messages by anything but the account and the listing
// switches: the filters that make a search with no words meaningful.
func (f Filter) hasFilter() bool {
	return f.MentionsMe || f.DirectMentions || f.From != "" || f.Conversation != "" || f.Team != "" || !f.Since.IsZero() || !f.Until.IsZero()
}

// Search matches message text with FTS5, newest first. Terms are ANDed; "quoted" segments are
// phrases and a trailing * makes a prefix term. With no words at all (blank query) it lists the
// messages the filters select, newest first; a blank query with no filter is a usage error.
func (s *Store) Search(ctx context.Context, query string, f Filter) ([]MessageRow, bool, error) {
	var w where
	from := ` from messages m` + msgJoin
	if strings.TrimSpace(query) != "" {
		match := buildFTSQuery(query)
		if match == "" {
			return nil, false, searchUsage("search query has no searchable terms")
		}
		w.add(`message_fts match ?`, match)
		from = ` from message_fts join messages m on m.rowid=message_fts.rowid` + msgJoin
	} else if !f.hasFilter() {
		return nil, false, searchUsage("search needs words to find or at least one filter (--mentions-me, --from, --conversation, --team, --since, --until)")
	}
	if err := s.messageWhere(ctx, &w, f); err != nil {
		return nil, false, err
	}
	// The match argument comes first in w.args; FROM has no placeholders of its own.
	return s.runMessages(ctx, from, &w, f.limit(), f.Total)
}

// Unread lists unread messages, newest first: sent after the conversation's read horizon, not by
// the account's own user, not deleted.
func (s *Store) Unread(ctx context.Context, f Filter) ([]MessageRow, bool, error) {
	f.Unread = true
	var w where
	if err := s.messageWhere(ctx, &w, f); err != nil {
		return nil, false, err
	}
	return s.runMessages(ctx, ` from messages m`+msgJoin, &w, f.limit(), f.Total)
}

// UnreadConversationRow is one conversation's unread summary.
type UnreadConversationRow struct {
	TenantID       string
	UserID         string
	ConversationID string
	DisplayName    string
	Kind           string
	UnreadCount    int
	OldestUnreadAt time.Time
	NewestUnreadAt time.Time
	Link           string // the newest unread message's link
}

// UnreadByConversation counts unread messages per conversation (same rules as Unread), most
// unread first, then newest activity first. Link is the newest unread message's link.
func (s *Store) UnreadByConversation(ctx context.Context, f Filter) ([]UnreadConversationRow, bool, error) {
	f.Unread = true
	var w where
	if err := s.messageWhere(ctx, &w, f); err != nil {
		return nil, false, err
	}
	limit := f.limit()
	//nolint:gosec // G202: fragments are package constants; values are placeholders
	q := `select tenant,user,conv_id,name,kind,cnt,oldest,newest,link from (select m.tenant_id as tenant,m.user_id as user,m.conversation_id as conv_id,` + cdnExpr + ` as name,coalesce(c.kind,'') as kind,m.sent_at as newest,m.link as link,
 count(*) over (partition by m.tenant_id,m.user_id,m.conversation_id) as cnt,
 min(m.sent_at) over (partition by m.tenant_id,m.user_id,m.conversation_id) as oldest,
 row_number() over (partition by m.tenant_id,m.user_id,m.conversation_id order by m.sent_at desc, m.id desc) as rn
 from messages m` + msgJoin + w.sql() + `) where rn=1 order by cnt desc, newest desc, conv_id limit ?`
	rows, err := s.db.QueryContext(ctx, q, append(w.args, limit+1)...)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = rows.Close() }()
	out := []UnreadConversationRow{}
	for rows.Next() {
		var r UnreadConversationRow
		var oldest, newest sql.NullString
		if err := rows.Scan(&r.TenantID, &r.UserID, &r.ConversationID, &r.DisplayName, &r.Kind, &r.UnreadCount, &oldest, &newest, &r.Link); err != nil {
			return nil, false, err
		}
		r.OldestUnreadAt, r.NewestUnreadAt = parseTime(oldest), parseTime(newest)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	trunc := len(out) > limit
	if trunc {
		out = out[:limit]
		if f.Total != nil {
			if err := s.db.QueryRowContext(ctx, `select count(*) from (select 1 from messages m`+pruneJoins(msgJoin, w.sql())+w.sql()+` group by m.tenant_id,m.user_id,m.conversation_id)`, w.args...).Scan(f.Total); err != nil { //nolint:gosec // G202: fragments are package constants; values are placeholders
				return nil, false, err
			}
		}
	}
	if err := nameUntitled(ctx, s, out, func(r *UnreadConversationRow) (convRef, *string) {
		return convRef{r.TenantID, r.UserID, r.ConversationID}, &r.DisplayName
	}); err != nil {
		return nil, false, err
	}
	return out, trunc, nil
}

// Thread returns a thread's root message and its replies, oldest first. f.Account narrows to one
// account and f.IncludeDeleted keeps deleted messages; its other fields are ignored.
func (s *Store) Thread(ctx context.Context, conversationID, rootID string, f Filter) ([]MessageRow, bool, error) {
	var w where
	w.add(`m.conversation_id=?`, conversationID)
	w.add(`(m.id=? or m.parent_message_id=? or m.reply_chain_id=?)`, rootID, rootID, rootID)
	if f.Account != nil {
		w.add(`m.tenant_id=? and m.user_id=?`, f.Account.TenantID, f.Account.UserID)
	}
	if !f.IncludeDeleted {
		w.add(`m.deleted_at is null`)
	}
	if !f.IncludeSystem {
		w.add(notSystemCond(`m.conversation_id`))
	}
	rows, err := s.db.QueryContext(ctx, `select `+msgCols+` from messages m`+msgJoin+w.sql()+` order by m.sent_at, m.id limit ?`, append(w.args, f.limit()+1)...) //nolint:gosec // G202: fragments are package constants; values are placeholders
	if err != nil {
		return nil, false, err
	}
	out, err := scanMessages(rows)
	if err != nil {
		return nil, false, err
	}
	trunc := len(out) > f.limit()
	if trunc {
		out = out[:f.limit()]
		if err := s.countTotal(ctx, f.Total, ` from messages m`+msgJoin, &w); err != nil {
			return nil, false, err
		}
	}
	if err := s.fillReplies(ctx, out); err != nil {
		return nil, false, err
	}
	if err := nameUntitled(ctx, s, out, func(r *MessageRow) (convRef, *string) {
		return convRef{r.TenantID, r.UserID, r.ConversationID}, &r.ConversationDisplayName
	}); err != nil {
		return nil, false, err
	}
	return out, trunc, nil
}

// Conversations lists conversations by latest activity. kind is a case-insensitive exact match
// on the Teams type. query is a fuzzy match on the conversation's name, ranked: an exact name
// first, then names that start with the query, then names that contain it, then conversations
// that hold all of the query's words in their title index (conversation_fts); each rank is
// ordered by latest activity. A channel is named both "Team › Channel" and by its own name, and
// either may match. f.Team limits the list to one team and its channels.
func (s *Store) Conversations(ctx context.Context, kind, query string, f Filter) ([]ConversationRow, bool, error) {
	var w where
	from := ` from conversations c left join conversations t on t.tenant_id=c.tenant_id and t.user_id=c.user_id and c.team_id<>'' and c.team_id<>c.id and t.id=c.team_id`
	order := ` order by c.last_message_at desc, c.id`
	var orderArgs []any
	if query != "" {
		match := buildFTSQuery(query)
		if match == "" {
			return nil, false, errs.Usage("conversation query has no searchable terms")
		}
		q := strings.ToLower(escapeLike(query))
		names := [2]string{`lower(` + cdnExpr + `)`, `lower(` + chanName + `)`}
		like := func(pat string) string {
			return `(` + names[0] + ` like ? escape '\' or ` + names[1] + ` like ? escape '\')`
		}
		w.add(`(c.rowid in (select rowid from conversation_fts where conversation_fts match ?) or `+like("%")+`)`, match, "%"+q+"%", "%"+q+"%")
		order = ` order by case when ` + names[0] + `=? or ` + names[1] + `=? then 0 when ` + like("") + ` then 1 when ` + like("") + ` then 2 else 3 end, c.last_message_at desc, c.id`
		orderArgs = []any{strings.ToLower(query), strings.ToLower(query), q + "%", q + "%", "%" + q + "%", "%" + q + "%"}
	}
	if f.Account != nil {
		w.add(`c.tenant_id=? and c.user_id=?`, f.Account.TenantID, f.Account.UserID)
	}
	if !f.IncludeSystem {
		w.add(notSystemCond(`c.id`))
	}
	if kind != "" {
		w.add(`c.kind=? collate nocase`, kind)
	}
	if f.Team != "" {
		id, err := s.resolveTeam(ctx, f.Account, f.Team)
		if err != nil {
			return nil, false, err
		}
		w.add(teamCond, id, id)
	}
	if !f.Since.IsZero() {
		w.add(`c.last_message_at>=?`, fmtTime(f.Since))
	}
	if !f.Until.IsZero() {
		w.add(`c.last_message_at<=?`, fmtTime(f.Until))
	}
	limit := f.limit()
	rows, err := s.db.QueryContext(ctx, `select c.tenant_id,c.user_id,c.id,c.kind,c.title,c.topic,`+cdnExpr+`,c.team_id,c.parent_id,c.members_json,c.last_message_at,c.read_horizon_at,c.favorite`+ //nolint:gosec // G202: fragments are package constants; values are placeholders
		from+w.sql()+order+` limit ?`, append(append(w.args, orderArgs...), limit+1)...)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = rows.Close() }()
	out := []ConversationRow{}
	for rows.Next() {
		var r ConversationRow
		var members, last, horizon sql.NullString
		var fav int
		if err := rows.Scan(&r.TenantID, &r.UserID, &r.ID, &r.Kind, &r.Title, &r.Topic, &r.DisplayName, &r.TeamID, &r.ParentID, &members, &last, &horizon, &fav); err != nil {
			return nil, false, err
		}
		r.Members, r.LastMessageAt, r.ReadHorizonAt, r.Favorite = unmarshalNull[string](members), parseTime(last), parseTime(horizon), fav == 1
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	trunc := len(out) > limit
	if trunc {
		out = out[:limit]
		if err := s.countTotal(ctx, f.Total, from, &w); err != nil {
			return nil, false, err
		}
	}
	if err := nameUntitled(ctx, s, out, func(r *ConversationRow) (convRef, *string) {
		return convRef{r.TenantID, r.UserID, r.ID}, &r.DisplayName
	}); err != nil {
		return nil, false, err
	}
	if err := s.linkCalendar(ctx, out); err != nil {
		return nil, false, err
	}
	return out, trunc, nil
}

// People lists the people of Teams and the correspondents of mail, newest seen first, merged by
// last_seen_at. query matches a case-insensitive part of a display name or mail address, or an
// exact id. f.Account narrows the Teams people by tenant; mail has no tenant and is not narrowed.
// The limit applies to the merged list, which is truncated when either source had more.
func (s *Store) People(ctx context.Context, query string, f Filter) ([]PersonRow, bool, error) {
	limit := f.limit()
	var w where
	if f.Account != nil {
		w.add(`p.tenant_id=?`, f.Account.TenantID)
	}
	if query != "" {
		w.add(`(p.id=? or lower(p.display_name) like ? escape '\')`, query, "%"+strings.ToLower(escapeLike(query))+"%")
	}
	rows, err := s.db.QueryContext(ctx, `select p.tenant_id,p.id,p.display_name,p.first_seen_at,p.last_seen_at from people p`+w.sql()+` order by p.last_seen_at is null, p.last_seen_at desc, p.display_name collate nocase, p.id limit ?`, append(w.args, limit+1)...) //nolint:gosec // G202: fragments are package constants; values are placeholders
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = rows.Close() }()
	out := []PersonRow{}
	for rows.Next() {
		r := PersonRow{Sources: []string{"chats"}}
		var first, last sql.NullString
		if err := rows.Scan(&r.TenantID, &r.ID, &r.DisplayName, &first, &last); err != nil {
			return nil, false, err
		}
		r.FirstSeenAt, r.LastSeenAt = parseTime(first), parseTime(last)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	trunc := len(out) > limit
	mail, err := s.mailPeople(ctx, query, limit+1)
	if err != nil {
		return nil, false, err
	}
	trunc = trunc || len(mail) > limit
	out = append(out, mail...)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if !a.LastSeenAt.Equal(b.LastSeenAt) {
			return a.LastSeenAt.After(b.LastSeenAt)
		}
		if na, nb := strings.ToLower(a.DisplayName), strings.ToLower(b.DisplayName); na != nb {
			return na < nb
		}
		return a.ID < b.ID
	})
	if len(out) <= limit {
		return out, trunc, nil
	}
	if f.Total != nil {
		var teams, mailN int
		if err := s.countTotal(ctx, &teams, ` from people p`, &w); err != nil {
			return nil, false, err
		}
		if mailN, err = s.mailPeopleCount(ctx, query); err != nil {
			return nil, false, err
		}
		*f.Total = teams + mailN
	}
	return out[:limit], true, nil
}

// mailPeopleCTE is one row per mail correspondent: the lower-case address, the newest name it was
// given (an empty name only when it never had one) and the first and last time it was seen. Senders
// and recipients with an address count; gone messages do not.
const mailPeopleCTE = `with c(addr, name, at) as (
  select lower(m.sender_address), m.sender_name, m.received_at from mail_messages m where m.sender_address<>'' and m.gone_at is null
  union all
  select lower(r.address), r.name, m.received_at from mail_recipients r join mail_messages m on m.rowid=r.message_rowid where coalesce(r.address,'')<>'' and m.gone_at is null
), mp as (
  select addr, name, row_number() over (partition by addr order by name='', at desc, name) rn, min(at) over (partition by addr) first_at, max(at) over (partition by addr) last_at from c
) `

// mailPeopleWhere matches query as a part of the address or name; "mail:<address>" names an
// address exactly, which the part match covers.
func mailPeopleWhere(query string) where {
	var w where
	w.add(`mp.rn=1`)
	if query != "" {
		q := strings.ToLower(query)
		q = strings.TrimPrefix(q, "mail:")
		like := "%" + escapeLike(q) + "%"
		w.add(`(mp.addr like ? escape '\' or lower(mp.name) like ? escape '\')`, like, like)
	}
	return w
}

// mailPeople lists at most limit mail correspondents, newest seen first. An archive without mail
// tables has none.
func (s *Store) mailPeople(ctx context.Context, query string, limit int) ([]PersonRow, error) {
	out := []PersonRow{}
	if ok, err := s.hasMail(ctx); err != nil || !ok {
		return out, err
	}
	w := mailPeopleWhere(query)
	err := mailEach(ctx, s.db, mailPeopleCTE+`select mp.addr, mp.name, mp.first_at, mp.last_at from mp`+w.sql()+` order by mp.last_at desc, mp.addr limit ?`, append(w.args, limit), func(r *sql.Rows) error { //nolint:gosec // G202: fragments are package constants; values are placeholders
		var p PersonRow
		var first, last sql.NullString
		if err := r.Scan(&p.Email, &p.DisplayName, &first, &last); err != nil {
			return err
		}
		p.ID, p.Sources = "mail:"+p.Email, []string{"mail"}
		p.FirstSeenAt, p.LastSeenAt = parseTime(first), parseTime(last)
		if p.DisplayName == "" {
			p.DisplayName = p.Email
		}
		out = append(out, p)
		return nil
	})
	return out, err
}

// mailPeopleCount counts the mail correspondents query matches.
func (s *Store) mailPeopleCount(ctx context.Context, query string) (int, error) {
	if ok, err := s.hasMail(ctx); err != nil || !ok {
		return 0, err
	}
	w := mailPeopleWhere(query)
	var n int
	err := s.db.QueryRowContext(ctx, mailPeopleCTE+`select count(*) from mp`+w.sql(), w.args...).Scan(&n) //nolint:gosec // G202: fragments are package constants; values are placeholders
	return n, err
}

// Activity lists activity-feed items newest first, joined to their message text and sender (when
// the message is archived) and to the conversation's composed display name.
func (s *Store) Activity(ctx context.Context, f ActivityFilter) ([]ActivityRow, bool, error) {
	var w where
	if f.Account != nil {
		w.add(`a.tenant_id=? and a.user_id=?`, f.Account.TenantID, f.Account.UserID)
	}
	if f.Unread {
		w.add(`a.is_read=0`)
	}
	types := splitTypes(f.Type)
	if f.Type != "" && len(types) == 0 {
		return nil, false, errs.Usage("--type names no activity type: give one or more, comma separated (for example --type mention,reply)")
	}
	if len(types) > 0 {
		conds := make([]string, len(types))
		args := make([]any, len(types))
		for i, t := range types {
			conds[i], args[i] = `a.type=? collate nocase`, t
		}
		w.add(`(`+strings.Join(conds, ` or `)+`)`, args...)
	}
	if f.Team != "" {
		id, err := s.resolveTeam(ctx, f.Account, f.Team)
		if err != nil {
			return nil, false, err
		}
		w.add(teamCond, id, id)
	}
	if f.DirectMentions {
		// Same meaning as Filter.DirectMentions: a person-subtype item, or a mention by id on the
		// item's archived message.
		w.add(`(a.type like 'mention%' and (lower(a.subtype)='person' or m.mentions_me=1))`)
	}
	if !f.Since.IsZero() {
		w.add(`a.at>=?`, fmtTime(f.Since))
	}
	rows, trunc, err := s.activityRows(ctx, &w, Filter{Limit: f.Limit}.limit(), f.IncludeSystem, f.Total)
	if err != nil {
		return nil, false, err
	}
	if err := s.fillActors(ctx, rows); err != nil {
		return nil, false, err
	}
	return rows, trunc, nil
}

// fillActors sets ActorID and ActorName on the items whose type names who acted:
//   - mention*, reply*, follow: the sender of the message the item points at (the person who
//     mentioned, replied or posted); empty when that message is not archived;
//   - reaction*: the reactor, inferred from the reacted-to message's reactions (the item's own
//     sender is that message's author) and flagged ActorInferred; empty when no one but the
//     account reacted. The account's own id is taken as "8:orgid:"+user id, as whoami's self_id;
//   - any other type (msGraph system notices, membership changes, thread activity): no actor.
func (s *Store) fillActors(ctx context.Context, rows []ActivityRow) error {
	for i := range rows {
		r := &rows[i]
		switch typ := strings.ToLower(r.Type); {
		case strings.HasPrefix(typ, "mention"), strings.HasPrefix(typ, "reply"), typ == "follow":
			r.ActorID, r.ActorName = r.SenderID, r.SenderName
		case strings.HasPrefix(typ, "reaction"):
			id, err := s.reactor(ctx, r)
			if err != nil {
				return err
			}
			if r.ActorID = id; id != "" {
				r.ActorInferred = true
				if err := s.db.QueryRowContext(ctx, `select coalesce((select display_name from people where tenant_id=? and id=?),'')`, r.TenantID, id).Scan(&r.ActorName); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// reactor is the MRI of whoever caused the reaction item r, or "" when the reacted-to message is
// not archived or names no one else.
func (s *Store) reactor(ctx context.Context, r *ActivityRow) (string, error) {
	var raw sql.NullString
	err := s.db.QueryRowContext(ctx, `select raw_json from messages where tenant_id=? and user_id=? and conversation_id=? and id=?`, r.TenantID, r.UserID, r.ConversationID, r.MessageID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return teamsdesktop.ReactionActor([]byte(raw.String), r.Subtype, r.At, "8:orgid:"+r.UserID), nil
}

// splitTypes splits a comma-separated list of activity types, dropping blanks.
func splitTypes(list string) []string {
	var out []string
	for _, t := range strings.Split(list, ",") {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// activityRows runs the activity query for the conditions in w, newest first, and trims the extra
// row that detects truncation.
func (s *Store) activityRows(ctx context.Context, w *where, limit int, includeSystem bool, total *int) ([]ActivityRow, bool, error) {
	msgOn := ``
	if !includeSystem {
		msgOn = ` and ` + notSystemCond(`m.conversation_id`)
	}
	from := ` from activity a
 left join messages m on m.tenant_id=a.tenant_id and m.user_id=a.user_id and m.conversation_id=a.conversation_id and m.id=a.message_id` + msgOn + `
 left join people sp on sp.tenant_id=m.tenant_id and sp.id=m.sender_id
 left join conversations c on c.tenant_id=a.tenant_id and c.user_id=a.user_id and c.id=a.conversation_id
 left join conversations t on t.tenant_id=c.tenant_id and t.user_id=c.user_id and c.team_id<>'' and c.team_id<>c.id and t.id=c.team_id`
	//nolint:gosec // G202: fragments are package constants; values are placeholders
	rows, err := s.db.QueryContext(ctx, `select a.tenant_id,a.user_id,a.id,a.type,a.subtype,a.is_read,a.at,a.conversation_id,`+cdnExpr+`,a.message_id,a.reply_chain_id,a.app_id,coalesce(m.content_text,''),coalesce(m.sender_id,''),coalesce(nullif(m.sender_name,''),sp.display_name,''),m.sent_at,coalesce(m.link,'')`+
		from+w.sql()+` order by a.at desc, a.id desc limit ?`, append(w.args, limit+1)...)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = rows.Close() }()
	out := []ActivityRow{}
	for rows.Next() {
		var r ActivityRow
		var at, sent sql.NullString
		var read int
		if err := rows.Scan(&r.TenantID, &r.UserID, &r.ID, &r.Type, &r.Subtype, &read, &at, &r.ConversationID, &r.ConversationDisplayName, &r.MessageID, &r.ReplyChainID, &r.AppID, &r.MessageText, &r.SenderID, &r.SenderName, &sent, &r.MessageLink); err != nil {
			return nil, false, err
		}
		r.IsRead, r.At, r.MessageSentAt = read == 1, parseTime(at), parseTime(sent)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	trunc := len(out) > limit
	if trunc {
		out = out[:limit]
		if err := s.countTotal(ctx, total, from, w); err != nil {
			return nil, false, err
		}
	}
	if err := nameUntitled(ctx, s, out, func(r *ActivityRow) (convRef, *string) {
		return convRef{r.TenantID, r.UserID, r.ConversationID}, &r.ConversationDisplayName
	}); err != nil {
		return nil, false, err
	}
	return out, trunc, nil
}

// Whoami lists the archived accounts with their own MRI and display name (when the account's own
// user has been seen as a person).
func (s *Store) Whoami(ctx context.Context) ([]WhoamiRow, error) {
	rows, err := s.db.QueryContext(ctx, `select a.tenant_id,a.user_id,a.locale,a.first_seen_at,a.last_synced_at,
 coalesce((select p.display_name from people p where p.tenant_id=a.tenant_id and lower(p.id)=lower('8:orgid:'||a.user_id)),'')
from accounts a order by a.tenant_id,a.user_id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []WhoamiRow{}
	for rows.Next() {
		var r WhoamiRow
		var first, last sql.NullString
		if err := rows.Scan(&r.TenantID, &r.UserID, &r.Locale, &first, &last, &r.DisplayName); err != nil {
			return nil, err
		}
		r.SelfID, r.FirstSeenAt, r.LastSyncedAt = "8:orgid:"+r.UserID, parseTime(first), parseTime(last)
		out = append(out, r)
	}
	return out, rows.Err()
}

// buildFTSQuery turns user input into an FTS5 MATCH expression: whitespace-separated terms ANDed,
// "quoted" segments as phrases, a trailing * on a bare term as a prefix. Everything else is
// quoted so FTS5 operators in the input are plain words. It returns "" when nothing is searchable.
func buildFTSQuery(in string) string {
	var out []string
	var cur strings.Builder
	inPhrase := false
	flush := func(phrase bool) {
		text := cur.String()
		cur.Reset()
		if strings.TrimSpace(text) == "" {
			return
		}
		if !phrase && strings.HasSuffix(text, "*") {
			stem := strings.TrimRight(text, "*")
			if stem != "" {
				out = append(out, crawlstore.FTS5Phrase(stem)+"*")
			}
			return
		}
		out = append(out, crawlstore.FTS5Phrase(text))
	}
	for _, r := range in {
		switch {
		case r == '"':
			flush(inPhrase)
			inPhrase = !inPhrase
		case !inPhrase && (r == ' ' || r == '\t' || r == '\n' || r == '\r'):
			flush(false)
		default:
			cur.WriteRune(r)
		}
	}
	flush(inPhrase)
	return strings.Join(out, " ")
}

// searchUsage is a usage error for an empty search that points at messages for filter-only listing.
func searchUsage(msg string) *errs.Coded {
	c := errs.Usage(msg)
	c.Fix = "To list messages without a text query, use `m365crawl messages` with filters (for example `m365crawl messages --mentions-me --since 24h`)."
	return c
}
