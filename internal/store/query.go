package store

import (
	"context"
	"database/sql"
	"strings"
	"time"

	crawlstore "github.com/openclaw/crawlkit/store"

	"github.com/ourostack/teamscrawl/internal/errs"
	"github.com/ourostack/teamscrawl/internal/teamsdesktop"
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
	Unread         bool // only unread messages (see unreadCond)
	// IncludeChannels counts channel and team conversations (kinds Topic, Space) as unread
	// sources too; by default unread covers chats and meetings only.
	IncludeChannels bool
}

func (f Filter) limit() int {
	if f.Limit <= 0 {
		return DefaultLimit
	}
	return f.Limit
}

// MessageRow is a message as read back, with its conversation's composed display name.
type MessageRow struct {
	TenantID                string                  `json:"tenant_id"`
	UserID                  string                  `json:"user_id"`
	ConversationID          string                  `json:"conversation_id"`
	ConversationDisplayName string                  `json:"conversation_display_name"`
	ID                      string                  `json:"id"`
	ReplyChainID            string                  `json:"reply_chain_id,omitempty"`
	ParentMessageID         string                  `json:"parent_message_id,omitempty"`
	ClientMessageID         string                  `json:"client_message_id,omitempty"`
	SenderID                string                  `json:"sender_id"`
	SenderName              string                  `json:"sender_name"`
	SentAt                  time.Time               `json:"sent_at"`
	EditedAt                time.Time               `json:"edited_at,omitzero"`
	DeletedAt               time.Time               `json:"deleted_at,omitzero"`
	MessageType             string                  `json:"message_type"`
	ContentType             string                  `json:"content_type"`
	ContentText             string                  `json:"content_text"`
	Version                 int64                   `json:"version"`
	Mentions                []teamsdesktop.Mention  `json:"mentions"`
	MentionsMe              bool                    `json:"mentions_me"`
	Reactions               []teamsdesktop.Reaction `json:"reactions"`
	Files                   []teamsdesktop.File     `json:"files"`
	Links                   []string                `json:"links"`
	Subject                 string                  `json:"subject,omitempty"`
	Importance              string                  `json:"importance,omitempty"`
	Pinned                  bool                    `json:"pinned"`
	Link                    string                  `json:"link,omitempty"`
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
}

// PersonRow is someone seen as a sender or member.
type PersonRow struct {
	TenantID    string    `json:"tenant_id"`
	ID          string    `json:"id"`
	DisplayName string    `json:"display_name"`
	FirstSeenAt time.Time `json:"first_seen_at,omitzero"`
	LastSeenAt  time.Time `json:"last_seen_at,omitzero"`
}

// ActivityFilter narrows the activity feed.
type ActivityFilter struct {
	Account *teamsdesktop.Account
	Unread  bool
	Type    string // case-insensitive exact match
	Since   time.Time
	Limit   int // 0 means DefaultLimit
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
	// not deleted; messageWhere also limits it to non-channel kinds unless Filter.IncludeChannels. A conversation with no known horizon has no unread messages.
	unreadCond = `(c.read_horizon_at is not null and m.sent_at > c.read_horizon_at and m.deleted_at is null and lower(m.sender_id) <> lower('8:orgid:' || m.user_id))`

	// channelKinds are the conversation kinds that are channels: Topic (channel) and Space (team).
	// Every other kind (Chat, Meeting, and the few unnamed ones) counts as a chat for unread.
	channelKinds = `('Topic','Space')`
	// mentionsMeExpr: the mapper saw a person mention of the account's user, or the account's own
	// activity feed holds a mention row (mention, mentionInChat; covers team, channel, tag and
	// everyone mentions) for this conversation and message. Evaluated at query time so activity
	// that arrives in a later sync counts without rewriting the message.
	mentionsMeExpr = `(m.mentions_me=1 or exists(select 1 from activity a where a.tenant_id=m.tenant_id and a.user_id=m.user_id and a.conversation_id=m.conversation_id and a.message_id=m.id and a.type like 'mention%'))`

	msgCols = `m.tenant_id,m.user_id,m.conversation_id,` + cdnExpr + `,m.id,m.reply_chain_id,m.parent_message_id,m.client_message_id,m.sender_id,m.sender_name,m.sent_at,m.edited_at,m.deleted_at,m.message_type,m.content_type,m.content_text,m.version,m.mentions_json,` + mentionsMeExpr + `,m.reactions_json,m.files_json,m.links_json,m.subject,m.importance,m.pinned,m.link`
	msgJoin = ` left join conversations c on c.tenant_id=m.tenant_id and c.user_id=m.user_id and c.id=m.conversation_id
 left join conversations t on t.tenant_id=c.tenant_id and t.user_id=c.user_id and c.team_id<>'' and c.team_id<>c.id and t.id=c.team_id`
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
	if f.Conversation != "" {
		w.add(`(m.conversation_id=? or c.title=? collate nocase or c.display_name=? collate nocase or `+cdnExpr+`=? collate nocase)`, f.Conversation, f.Conversation, f.Conversation, f.Conversation)
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
			w.add(`lower(m.sender_name) like ? escape '\'`, "%"+strings.ToLower(escapeLike(f.From))+"%")
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
	if f.Unread {
		w.add(unreadCond)
		if !f.IncludeChannels {
			w.add(`c.kind not in ` + channelKinds)
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
		var mentionsMe, pinned int
		if err := rows.Scan(&r.TenantID, &r.UserID, &r.ConversationID, &r.ConversationDisplayName, &r.ID, &r.ReplyChainID, &r.ParentMessageID, &r.ClientMessageID, &r.SenderID, &r.SenderName, &sent, &edited, &deleted, &r.MessageType, &r.ContentType, &r.ContentText, &r.Version, &mentions, &mentionsMe, &reactions, &files, &links, &r.Subject, &r.Importance, &pinned, &r.Link); err != nil {
			return nil, err
		}
		r.SentAt, r.EditedAt, r.DeletedAt = parseTime(sent), parseTime(edited), parseTime(deleted)
		r.Mentions, r.Reactions, r.Files, r.Links = unmarshalNull[teamsdesktop.Mention](mentions), unmarshalNull[teamsdesktop.Reaction](reactions), unmarshalNull[teamsdesktop.File](files), unmarshalNull[string](links)
		r.MentionsMe, r.Pinned = mentionsMe == 1, pinned == 1
		out = append(out, r)
	}
	return out, rows.Err()
}

// runMessages runs a message query ordered newest first and trims the extra row that detects truncation.
func (s *Store) runMessages(ctx context.Context, from string, w *where, limit int, extraArgs []any) ([]MessageRow, bool, error) {
	q := `select ` + msgCols + from + w.sql() + ` order by m.sent_at desc, m.id desc limit ?` //nolint:gosec // G202: fragments are package constants; values are placeholders
	args := append(append(extraArgs, w.args...), limit+1)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, false, err
	}
	out, err := scanMessages(rows)
	if err != nil {
		return nil, false, err
	}
	if len(out) > limit {
		return out[:limit], true, nil
	}
	return out, false, nil
}

// Messages lists messages in chronological order. With a Limit it returns the newest Limit
// messages matching the filter (still oldest first), and truncated says older ones exist.
func (s *Store) Messages(ctx context.Context, f Filter) ([]MessageRow, bool, error) {
	var w where
	if err := s.messageWhere(ctx, &w, f); err != nil {
		return nil, false, err
	}
	rows, trunc, err := s.runMessages(ctx, ` from messages m`+msgJoin, &w, f.limit(), nil)
	if err != nil {
		return nil, false, err
	}
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
	return rows, trunc, nil
}

// Search matches message text with FTS5, newest first. Terms are ANDed; "quoted" segments are
// phrases and a trailing * makes a prefix term.
func (s *Store) Search(ctx context.Context, query string, f Filter) ([]MessageRow, bool, error) {
	match := buildFTSQuery(query)
	if match == "" {
		return nil, false, errs.Usage("search query has no searchable terms")
	}
	var w where
	w.add(`message_fts match ?`, match)
	if err := s.messageWhere(ctx, &w, f); err != nil {
		return nil, false, err
	}
	// The match argument comes first in w.args; FROM has no placeholders of its own.
	return s.runMessages(ctx, ` from message_fts join messages m on m.rowid=message_fts.rowid`+msgJoin, &w, f.limit(), nil)
}

// Unread lists unread messages, newest first: sent after the conversation's read horizon, not by
// the account's own user, not deleted.
func (s *Store) Unread(ctx context.Context, f Filter) ([]MessageRow, bool, error) {
	f.Unread = true
	var w where
	if err := s.messageWhere(ctx, &w, f); err != nil {
		return nil, false, err
	}
	return s.runMessages(ctx, ` from messages m`+msgJoin, &w, f.limit(), nil)
}

// Thread returns a thread's root message and its replies, oldest first. f.Account narrows to one
// account and f.IncludeDeleted keeps deleted messages; its other fields are ignored.
func (s *Store) Thread(ctx context.Context, conversationID, rootID string, f Filter) ([]MessageRow, error) {
	var w where
	w.add(`m.conversation_id=?`, conversationID)
	w.add(`(m.id=? or m.parent_message_id=? or m.reply_chain_id=?)`, rootID, rootID, rootID)
	if f.Account != nil {
		w.add(`m.tenant_id=? and m.user_id=?`, f.Account.TenantID, f.Account.UserID)
	}
	if !f.IncludeDeleted {
		w.add(`m.deleted_at is null`)
	}
	rows, err := s.db.QueryContext(ctx, `select `+msgCols+` from messages m`+msgJoin+w.sql()+` order by m.sent_at, m.id`, w.args...) //nolint:gosec // G202: fragments are package constants; values are placeholders
	if err != nil {
		return nil, err
	}
	return scanMessages(rows)
}

// Conversations lists conversations by latest activity. kind is a case-insensitive exact match
// on the Teams type; query matches titles and display names through conversation_fts.
func (s *Store) Conversations(ctx context.Context, kind, query string, f Filter) ([]ConversationRow, bool, error) {
	var w where
	from := ` from conversations c left join conversations t on t.tenant_id=c.tenant_id and t.user_id=c.user_id and c.team_id<>'' and c.team_id<>c.id and t.id=c.team_id`
	if query != "" {
		match := buildFTSQuery(query)
		if match == "" {
			return nil, false, errs.Usage("conversation query has no searchable terms")
		}
		from = ` from conversation_fts join conversations c on c.rowid=conversation_fts.rowid left join conversations t on t.tenant_id=c.tenant_id and t.user_id=c.user_id and c.team_id<>'' and c.team_id<>c.id and t.id=c.team_id`
		w.add(`conversation_fts match ?`, match)
	}
	if f.Account != nil {
		w.add(`c.tenant_id=? and c.user_id=?`, f.Account.TenantID, f.Account.UserID)
	}
	if kind != "" {
		w.add(`c.kind=? collate nocase`, kind)
	}
	if !f.Since.IsZero() {
		w.add(`c.last_message_at>=?`, fmtTime(f.Since))
	}
	if !f.Until.IsZero() {
		w.add(`c.last_message_at<=?`, fmtTime(f.Until))
	}
	limit := f.limit()
	rows, err := s.db.QueryContext(ctx, `select c.tenant_id,c.user_id,c.id,c.kind,c.title,c.topic,`+cdnExpr+`,c.team_id,c.parent_id,c.members_json,c.last_message_at,c.read_horizon_at,c.favorite`+ //nolint:gosec // G202: fragments are package constants; values are placeholders
		from+w.sql()+` order by c.last_message_at desc, c.id limit ?`, append(w.args, limit+1)...)
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
	if len(out) > limit {
		return out[:limit], true, nil
	}
	return out, false, nil
}

// People lists people by name, matching query as a case-insensitive substring of the display
// name or as an exact id. f.Account narrows by tenant.
func (s *Store) People(ctx context.Context, query string, f Filter) ([]PersonRow, bool, error) {
	var w where
	if f.Account != nil {
		w.add(`p.tenant_id=?`, f.Account.TenantID)
	}
	if query != "" {
		w.add(`(p.id=? or lower(p.display_name) like ? escape '\')`, query, "%"+strings.ToLower(escapeLike(query))+"%")
	}
	limit := f.limit()
	rows, err := s.db.QueryContext(ctx, `select p.tenant_id,p.id,p.display_name,p.first_seen_at,p.last_seen_at from people p`+w.sql()+` order by p.display_name collate nocase, p.id limit ?`, append(w.args, limit+1)...) //nolint:gosec // G202: fragments are package constants; values are placeholders
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = rows.Close() }()
	out := []PersonRow{}
	for rows.Next() {
		var r PersonRow
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
	if len(out) > limit {
		return out[:limit], true, nil
	}
	return out, false, nil
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
	if f.Type != "" {
		w.add(`a.type=? collate nocase`, f.Type)
	}
	if !f.Since.IsZero() {
		w.add(`a.at>=?`, fmtTime(f.Since))
	}
	limit := Filter{Limit: f.Limit}.limit()
	//nolint:gosec // G202: fragments are package constants; values are placeholders
	rows, err := s.db.QueryContext(ctx, `select a.tenant_id,a.user_id,a.id,a.type,a.subtype,a.is_read,a.at,a.conversation_id,`+cdnExpr+`,a.message_id,a.reply_chain_id,a.app_id,coalesce(m.content_text,''),coalesce(m.sender_id,''),coalesce(m.sender_name,''),m.sent_at,coalesce(m.link,'')
from activity a
 left join messages m on m.tenant_id=a.tenant_id and m.user_id=a.user_id and m.conversation_id=a.conversation_id and m.id=a.message_id
 left join conversations c on c.tenant_id=a.tenant_id and c.user_id=a.user_id and c.id=a.conversation_id
 left join conversations t on t.tenant_id=c.tenant_id and t.user_id=c.user_id and c.team_id<>'' and c.team_id<>c.id and t.id=c.team_id`+
		w.sql()+` order by a.at desc, a.id desc limit ?`, append(w.args, limit+1)...)
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
	if len(out) > limit {
		return out[:limit], true, nil
	}
	return out, false, nil
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
		if !phrase && strings.Trim(text, "*") == "" {
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
