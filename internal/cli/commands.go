package cli

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/syncer"
	"github.com/ourostack/m365crawl/internal/teamsdesktop"
)

// Item shapes. JSON names are snake_case and stable; optional fields are omitted when empty.

type mention struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
}

type reaction struct {
	Key     string   `json:"key"`
	Count   int      `json:"count"`
	UserIDs []string `json:"user_ids,omitempty"`
}

type file struct {
	Name string `json:"name"`
	URL  string `json:"url,omitempty"`
	Type string `json:"type,omitempty"`
}

type messageItem struct {
	TenantID                string    `json:"tenant_id"`
	UserID                  string    `json:"user_id"`
	ConversationID          string    `json:"conversation_id"`
	ConversationDisplayName string    `json:"conversation_display_name"`
	ID                      string    `json:"id"`
	ReplyChainID            string    `json:"reply_chain_id,omitempty"`
	ParentMessageID         string    `json:"parent_message_id,omitempty"`
	SenderID                string    `json:"sender_id"`
	SenderName              string    `json:"sender_name"`
	SentAt                  time.Time `json:"sent_at"`
	EditedAt                time.Time `json:"edited_at,omitzero"`
	DeletedAt               time.Time `json:"deleted_at,omitzero"`
	MessageType             string    `json:"message_type"`
	Text                    string    `json:"text"`
	TextTruncated           bool      `json:"text_truncated,omitempty"`
	HTML                    string    `json:"html,omitempty"`
	Mentions                []mention `json:"mentions,omitempty"`
	// mentions_me and pinned are always present, false included: stable keys are easier for agents
	// than keys that come and go. Every other optional field is omitted when empty.
	MentionsMe bool `json:"mentions_me"`
	// mention_kind says how you were mentioned, only when mentions_me is true: person (by name),
	// channel, team, tag, everyone, or other for a kind this version does not know.
	MentionKind string     `json:"mention_kind,omitempty"`
	Reactions   []reaction `json:"reactions,omitempty"`
	Files       []file     `json:"files,omitempty"`
	Links       []string   `json:"links,omitempty"`
	Subject     string     `json:"subject,omitempty"`
	Importance  string     `json:"importance,omitempty"`
	Pinned      bool       `json:"pinned"`
	Link        string     `json:"link,omitempty"`
	// reply_count (live replies) and last_reply_at appear on channel thread roots only; a root
	// nobody has answered has reply_count 0, so a missing key means "not a channel root".
	ReplyCount  *int      `json:"reply_count,omitempty"`
	LastReplyAt time.Time `json:"last_reply_at,omitzero"`
}

type conversationItem struct {
	TenantID      string    `json:"tenant_id"`
	UserID        string    `json:"user_id"`
	ID            string    `json:"id"`
	Kind          string    `json:"kind"`
	Title         string    `json:"title,omitempty"`
	DisplayName   string    `json:"display_name"`
	TeamID        string    `json:"team_id,omitempty"`
	MemberCount   int       `json:"member_count"`
	LastMessageAt time.Time `json:"last_message_at,omitzero"`
	ReadHorizonAt time.Time `json:"read_horizon_at,omitzero"`
	Favorite      bool      `json:"favorite"`
	// CalendarSeriesKey and CalendarEventCount are on a Meeting conversation that calendar events
	// name as their chat: the series key they share (absent when they belong to several) and the
	// number of live occurrences. A chat with no event has neither key.
	CalendarSeriesKey  string `json:"calendar_series_key,omitempty"`
	CalendarEventCount int    `json:"calendar_event_count,omitempty"`
}

type personItem struct {
	TenantID    string    `json:"tenant_id"`
	ID          string    `json:"id"`
	DisplayName string    `json:"display_name"`
	LastSeenAt  time.Time `json:"last_seen_at,omitzero"`
}

type activityItem struct {
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
	SenderID                string    `json:"sender_id,omitempty"`
	SenderName              string    `json:"sender_name"`
	MessageSentAt           time.Time `json:"message_sent_at,omitzero"`
	Text                    string    `json:"text"`
	TextTruncated           bool      `json:"text_truncated,omitempty"`
	Link                    string    `json:"link,omitempty"`
	// actor_id and actor_name say who did the thing: who reacted, replied or mentioned (the
	// sender_* fields stay the related message's author). Omitted for types whose data names no one.
	ActorID   string `json:"actor_id,omitempty"`
	ActorName string `json:"actor_name,omitempty"`
	// actor_inferred is true on reaction items: the reactor is the user whose reaction is nearest
	// in time, a best-effort match that can be wrong when several people reacted together.
	ActorInferred bool `json:"actor_inferred,omitempty"`
}

func activityItems(rows []store.ActivityRow, maxText int) []activityItem {
	items := make([]activityItem, len(rows))
	for i, r := range rows {
		text, cut := truncateRunes(r.MessageText, maxText)
		items[i] = activityItem{TenantID: r.TenantID, UserID: r.UserID, ID: r.ID, Type: r.Type, Subtype: r.Subtype, IsRead: r.IsRead, At: r.At,
			ConversationID: r.ConversationID, ConversationDisplayName: r.ConversationDisplayName, MessageID: r.MessageID, ReplyChainID: r.ReplyChainID,
			AppID: r.AppID, SenderID: r.SenderID, SenderName: r.SenderName, MessageSentAt: r.MessageSentAt, Text: text, TextTruncated: cut, Link: r.MessageLink,
			ActorID: r.ActorID, ActorName: r.ActorName, ActorInferred: r.ActorInferred}
	}
	return items
}

func messageItems(rows []store.MessageRow, maxText int, html map[store.MessageKey]string) []messageItem {
	out := make([]messageItem, len(rows))
	for i, r := range rows {
		text, cut := truncateRunes(r.ContentText, maxText)
		it := messageItem{
			TenantID: r.TenantID, UserID: r.UserID, ConversationID: r.ConversationID, ConversationDisplayName: r.ConversationDisplayName,
			ID: r.ID, ReplyChainID: r.ReplyChainID, ParentMessageID: r.ParentMessageID, SenderID: r.SenderID, SenderName: r.SenderName,
			SentAt: r.SentAt, EditedAt: r.EditedAt, DeletedAt: r.DeletedAt, MessageType: r.MessageType, Text: text, TextTruncated: cut,
			HTML:       html[store.MessageKey{TenantID: r.TenantID, UserID: r.UserID, ConversationID: r.ConversationID, ID: r.ID}],
			MentionsMe: r.MentionsMe, MentionKind: r.MentionKind, Links: r.Links, Subject: r.Subject, Importance: r.Importance, Pinned: r.Pinned, Link: r.Link,
			ReplyCount: r.ReplyCount, LastReplyAt: r.LastReplyAt,
		}
		for _, m := range r.Mentions {
			it.Mentions = append(it.Mentions, mention{ID: m.ID, DisplayName: m.DisplayName})
		}
		for _, x := range r.Reactions {
			it.Reactions = append(it.Reactions, reaction{Key: x.Key, Count: x.Count, UserIDs: x.UserIDs})
		}
		for _, f := range r.Files {
			it.Files = append(it.Files, file{Name: f.Name, URL: f.URL, Type: f.Type})
		}
		out[i] = it
	}
	return out
}

// chatHTML fetches the HTML bodies of rows when asked; nil otherwise.
func (rt *runtime) chatHTML(st *store.Store, rows []store.MessageRow, withHTML bool) (map[store.MessageKey]string, error) {
	if !withHTML {
		return nil, nil
	}
	keys := make([]store.MessageKey, len(rows))
	for i, r := range rows {
		keys[i] = store.MessageKey{TenantID: r.TenantID, UserID: r.UserID, ConversationID: r.ConversationID, ID: r.ID}
	}
	return st.MessageHTML(rt.ctx, keys)
}

// messageList turns message rows into a list result, adding html when asked.
func (rt *runtime) messageList(_ context.Context, st *store.Store, rows []store.MessageRow, truncated bool, total int, withHTML bool) (result, error) {
	html, err := rt.chatHTML(st, rows, withHTML)
	if err != nil {
		return nil, err
	}
	return newList(shape(rt, messageItems(rows, rt.g.MaxText, html)), truncated).withTotal(total), nil
}

// msgFlags are the filters messages, search and unread share.
type msgFlags struct {
	Conversation  string `short:"c" help:"Conversation id, or its exact title or display name."`
	From          string `help:"Sender: a person id, or a case-insensitive part of the name."`
	Since         string `help:"Only messages at or after this time: RFC3339, YYYY-MM-DD (local midnight) or a relative duration (90m, 24h, 7d, 2w)."`
	Until         string `help:"Only messages at or before this time (same formats as --since)."`
	Team          string `help:"Only this team and its channels: the team's exact name (any case for ASCII letters) or its id. An unknown or ambiguous name is a usage error."`
	Limit         int    `default:"50" help:"Maximum items to return; truncated says whether more exist."`
	IncludeSystem bool   `name:"include-system" help:"Also include Teams' system pseudo-conversations (48:notifications, 48:calllogs, 48:annotations), which mirror real messages and are left out by default."`
}

func (rt *runtime) filter(f msgFlags) (store.Filter, error) {
	if err := checkLimit(f.Limit); err != nil {
		return store.Filter{}, err
	}
	rt.team = f.Team
	since, err := rt.when("--since", f.Since)
	if err != nil {
		return store.Filter{}, err
	}
	until, err := rt.when("--until", f.Until)
	if err != nil {
		return store.Filter{}, err
	}
	return store.Filter{Account: rt.account, Conversation: f.Conversation, From: f.From, Since: since, Until: until, Limit: f.Limit, IncludeSystem: f.IncludeSystem, Team: f.Team}, nil
}

type searchCmd struct {
	Query string `arg:"" optional:"" help:"Words to find; \"quoted phrases\" and a trailing * for prefixes are supported. Optional when a filter (--mentions-me, --direct-mentions, --from, --conversation, --team, --folder, --since, --until) is given: then the filters alone select the messages."`
	msgFlags
	Source         string `default:"all" enum:"chats,mail,all" help:"What to search: chats (Teams), mail (Outlook) or all (both, the default)."`
	Folder         string `help:"Only mail in this folder, by name or kind (inbox, sent, ...). Mail only: with --source all it leaves Teams chats out."`
	IncludeDeleted bool   `name:"include-deleted" help:"Also search deleted messages (Teams chats only)."`
	MentionsMe     bool   `name:"mentions-me" help:"Only messages that mention you (by name, or through a channel, team, tag or @everyone mention; see mention_kind). Teams chats only."`
	DirectMentions bool   `name:"direct-mentions" help:"Only messages that mention you by name (mention_kind person), not channel, team, tag or @everyone broadcasts. Teams chats only."`
	HTML           bool   `name:"html" help:"Add each message's HTML body as html (Teams chats only)."`
}

// Help is the long help of search.
func (searchCmd) Help() string {
	return "Searches Teams chats and Outlook mail together; every item has a source (chats or mail) and the result's sources says how many items each source gave and whether it had more. Items are newest first across both sources, by sent_at for chats and received_at for mail.\n" +
		"--mentions-me, --direct-mentions, --conversation, --team, --include-system, --include-deleted and --html belong to Teams chats; --folder belongs to mail. With --source all, such a flag narrows the search to its own source and note says so; with the other --source it is a usage error (flag_source_conflict). --from, --since, --until, --limit and --fields apply to both; --fields accepts the keys of both kinds of item.\n" +
		"note also says when mail is left out because it is not in the archive yet or not yet read on this platform. --account takes a Teams account (TENANT/USER), which narrows chats and searches all mail, or a mail account (outlook/PROFILE), which narrows mail and skips chats.\n" +
		"Gone and evicted mail is never searched; --include-deleted is for Teams chats only."
}

func (c *searchCmd) Run(rt *runtime) error {
	if err := checkKeys(rt, searchKeys()); err != nil {
		return err
	}
	p, err := c.plan(rt)
	if err != nil {
		return err
	}
	f, err := rt.filter(c.msgFlags)
	if err != nil {
		return err
	}
	f.IncludeDeleted, f.MentionsMe, f.DirectMentions = c.IncludeDeleted, c.MentionsMe, c.DirectMentions
	f.Total = new(int)
	return rt.read("search", func(st *store.Store) (result, error) { return c.search(rt, p, f, st) })
}

type messagesCmd struct {
	msgFlags
	IncludeDeleted  bool `name:"include-deleted" help:"Also list deleted messages."`
	MentionsMe      bool `name:"mentions-me" help:"Only messages that mention you (by name, or through a channel, team, tag or @everyone mention; see mention_kind)."`
	DirectMentions  bool `name:"direct-mentions" help:"Only messages that mention you by name (mention_kind person), not channel, team, tag or @everyone broadcasts."`
	Unread          bool `name:"unread" help:"Only unread messages (chats and meetings unless --include-channels)."`
	IncludeChannels bool "name:\"include-channels\" help:\"include channels (off by default: most channels are never opened, so their unread counts are noise; channel mentions and replies reach you through `activity`)\""
	HTML            bool `name:"html" help:"Add each message's HTML body as html."`
}

func (c *messagesCmd) Run(rt *runtime) error {
	if err := checkFields[messageItem](rt); err != nil {
		return err
	}
	f, err := rt.filter(c.msgFlags)
	if err != nil {
		return err
	}
	f.IncludeDeleted, f.MentionsMe, f.DirectMentions, f.Unread, f.IncludeChannels = c.IncludeDeleted, c.MentionsMe, c.DirectMentions, c.Unread, c.IncludeChannels
	f.Total = new(int)
	return rt.read("messages", func(st *store.Store) (result, error) {
		if st == nil {
			return newList(nil, false), nil
		}
		rows, trunc, err := st.Messages(rt.ctx, f)
		if err != nil {
			return nil, err
		}
		return excludingChannels(rt.messageList(rt.ctx, st, rows, trunc, *f.Total, c.HTML))(c.Unread && !c.IncludeChannels)
	})
}

// excludingChannels marks a list result as having left channels out when on is true.
func excludingChannels(res result, err error) func(on bool) (result, error) {
	return func(on bool) (result, error) {
		if l, ok := res.(*listResult); ok && err == nil {
			l.ChannelsExcluded = on
		}
		return res, err
	}
}

type unreadCmd struct {
	Conversation    string `short:"c" help:"Conversation id, or its exact title or display name."`
	Team            string `help:"Only this team and its channels: the team's exact name (any case for ASCII letters) or its id. An unknown or ambiguous name is a usage error."`
	Since           string `help:"Count only unread messages sent at or after this time: RFC3339, YYYY-MM-DD (local midnight) or a relative duration (90m, 24h, 7d, 2w). Use it for \"what needs my attention\": old read markers leave stale conversations with hundreds of unread messages."`
	Limit           int    `default:"50" help:"Maximum items to return; truncated says whether more exist."`
	HTML            bool   `name:"html" help:"Add each message's HTML body as html."`
	IncludeChannels bool   "name:\"include-channels\" help:\"include channels (off by default: most channels are never opened, so their unread counts are noise; channel mentions and replies reach you through `activity`)\""
	ByConversation  bool   `name:"by-conversation" help:"One item per conversation with its unread count, oldest and newest unread time and a link, most unread first (the overview; ignores --html)."`
	IncludeSystem   bool   `name:"include-system" help:"Also include Teams' system pseudo-conversations (48:notifications, 48:calllogs, 48:annotations), which mirror real messages and are left out by default."`
}

type unreadConversationItem struct {
	ConversationID          string    `json:"conversation_id"`
	ConversationDisplayName string    `json:"conversation_display_name"`
	Kind                    string    `json:"kind"`
	UnreadCount             int       `json:"unread_count"`
	OldestUnreadAt          time.Time `json:"oldest_unread_at,omitzero"`
	NewestUnreadAt          time.Time `json:"newest_unread_at,omitzero"`
	Link                    string    `json:"link,omitempty"`
}

func (c *unreadCmd) Run(rt *runtime) error {
	if c.ByConversation {
		if err := checkFields[unreadConversationItem](rt); err != nil {
			return err
		}
	} else if err := checkFields[messageItem](rt); err != nil {
		return err
	}
	f, err := rt.filter(msgFlags{Conversation: c.Conversation, Team: c.Team, Since: c.Since, Limit: c.Limit, IncludeSystem: c.IncludeSystem})
	if err != nil {
		return err
	}
	f.IncludeChannels = c.IncludeChannels
	f.Total = new(int)
	return rt.read("unread", func(st *store.Store) (result, error) {
		if st == nil {
			return newList(nil, false), nil
		}
		if c.ByConversation {
			rows, trunc, err := st.UnreadByConversation(rt.ctx, f)
			if err != nil {
				return nil, err
			}
			items := make([]unreadConversationItem, len(rows))
			for i, r := range rows {
				items[i] = unreadConversationItem{ConversationID: r.ConversationID, ConversationDisplayName: r.DisplayName, Kind: r.Kind,
					UnreadCount: r.UnreadCount, OldestUnreadAt: r.OldestUnreadAt, NewestUnreadAt: r.NewestUnreadAt, Link: r.Link}
			}
			return excludingChannels(newList(shape(rt, items), trunc).withTotal(*f.Total), nil)(!c.IncludeChannels)
		}
		rows, trunc, err := st.Unread(rt.ctx, f)
		if err != nil {
			return nil, err
		}
		return excludingChannels(rt.messageList(rt.ctx, st, rows, trunc, *f.Total, c.HTML))(!c.IncludeChannels)
	})
}

type threadCmd struct {
	Target         string `arg:"" help:"Conversation id, or a Teams message link."`
	Root           string `arg:"" optional:"" help:"Root message id (not needed with a link)."`
	Limit          int    `default:"50" help:"Maximum items to return; truncated says whether more exist."`
	IncludeDeleted bool   `name:"include-deleted" help:"Also show deleted messages."`
	HTML           bool   `name:"html" help:"Add each message's HTML body as html."`
	IncludeSystem  bool   `name:"include-system" help:"Also include Teams' system pseudo-conversations (48:notifications, 48:calllogs, 48:annotations), which mirror real messages and are left out by default."`
}

func (c *threadCmd) Run(rt *runtime) error {
	if err := checkFields[messageItem](rt); err != nil {
		return err
	}
	if err := checkLimit(c.Limit); err != nil {
		return err
	}
	conv, root, err := parseThreadTarget(c.Target, c.Root)
	if err != nil {
		return err
	}
	return rt.read("thread", func(st *store.Store) (result, error) {
		if st == nil {
			return newList(nil, false), nil
		}
		var total int
		rows, trunc, err := st.Thread(rt.ctx, conv, root, store.Filter{Account: rt.account, IncludeDeleted: c.IncludeDeleted, IncludeSystem: c.IncludeSystem, Limit: c.Limit, Total: &total})
		if err != nil {
			return nil, err
		}
		return rt.messageList(rt.ctx, st, rows, trunc, total, c.HTML)
	})
}

func badLink(why string) *errs.Coded {
	c := errs.Usage(why)
	c.Fix = "Pass a link like https://teams.microsoft.com/l/message/<conversationId>/<messageId>, or use thread <conversation-id> <root-message-id>."
	return c
}

// sqlUsage is a usage error for sql that says why the archive rejects writes.
func sqlUsage(msg string) *errs.Coded {
	c := errs.Usage(msg)
	c.Fix = "The archive is read-only by design. Run `m365crawl sql --help`; sql accepts one SELECT, WITH, EXPLAIN or VALUES statement."
	return c
}

// sqlEngineError is a usage error for a query SQLite itself rejected (a typo, a missing table).
func sqlEngineError(err error) *errs.Coded {
	c := errs.Usage("sql: " + err.Error())
	c.Fix = "Fix the query. List the tables with `m365crawl sql \"select name from sqlite_master where type = 'table'\"` and a table's columns with `m365crawl sql \"select name from pragma_table_info('messages')\"`."
	return c
}

// parseThreadTarget resolves "<conversation> <root>" or a Teams message link to a conversation
// id and the thread's root message id. A channel reply's link names the root as parentMessageId.
func parseThreadTarget(target, root string) (conversation, rootID string, err error) {
	if !strings.HasPrefix(target, "http://") && !strings.HasPrefix(target, "https://") {
		if root == "" {
			return "", "", errs.Usage("thread needs a root message id after the conversation, or a Teams message link")
		}
		return target, root, nil
	}
	u, perr := url.Parse(target)
	if perr != nil {
		return "", "", badLink("the link is not a valid URL")
	}
	rest, ok := strings.CutPrefix(u.EscapedPath(), "/l/message/")
	if !ok || !strings.Contains(u.Hostname(), "teams") {
		return "", "", badLink("not a Teams message link")
	}
	convEsc, msgEsc, ok := strings.Cut(rest, "/")
	if !ok || msgEsc == "" {
		return "", "", badLink("the link has no message id")
	}
	conversation, err1 := url.PathUnescape(convEsc)
	msg, err2 := url.PathUnescape(strings.TrimSuffix(msgEsc, "/"))
	if err1 != nil || err2 != nil || conversation == "" || msg == "" {
		return "", "", badLink("the link has a malformed conversation or message id")
	}
	if p := u.Query().Get("parentMessageId"); p != "" {
		msg = p
	}
	return conversation, msg, nil
}

type conversationsCmd struct {
	Kind          string `help:"Only this kind, for example Chat, Topic (channel), Space (team) or Meeting."`
	Query         string `help:"Find conversations by name, best match first: an exact name, then names that start with the query, then names that contain it, then conversations whose title holds all the words; ties go newest first."`
	Team          string `help:"Only this team's own conversation and its channels: the team's exact name (any case for ASCII letters) or its id. An unknown or ambiguous name is a usage error."`
	Limit         int    `default:"50" help:"Maximum items to return; truncated says whether more exist."`
	IncludeSystem bool   `name:"include-system" help:"Also include Teams' system pseudo-conversations (48:notifications, 48:calllogs, 48:annotations), which mirror real messages and are left out by default."`
}

func (c *conversationsCmd) Run(rt *runtime) error {
	if err := checkFields[conversationItem](rt); err != nil {
		return err
	}
	if err := checkLimit(c.Limit); err != nil {
		return err
	}
	rt.team = c.Team
	return rt.read("conversations", func(st *store.Store) (result, error) {
		if st == nil {
			return newList(nil, false), nil
		}
		var total int
		rows, trunc, err := st.Conversations(rt.ctx, c.Kind, c.Query, store.Filter{Account: rt.account, Limit: c.Limit, IncludeSystem: c.IncludeSystem, Team: c.Team, Total: &total})
		if err != nil {
			return nil, err
		}
		items := make([]conversationItem, len(rows))
		for i, r := range rows {
			items[i] = conversationItem{TenantID: r.TenantID, UserID: r.UserID, ID: r.ID, Kind: r.Kind, Title: r.Title, DisplayName: r.DisplayName,
				TeamID: r.TeamID, MemberCount: len(r.Members), LastMessageAt: r.LastMessageAt, ReadHorizonAt: r.ReadHorizonAt, Favorite: r.Favorite,
				CalendarSeriesKey: r.CalendarSeriesKey, CalendarEventCount: r.CalendarEventCount}
		}
		return newList(shape(rt, items), trunc).withTotal(total), nil
	})
}

type teamItem struct {
	TenantID       string    `json:"tenant_id"`
	UserID         string    `json:"user_id"`
	TeamID         string    `json:"team_id"`
	DisplayName    string    `json:"display_name"`
	ChannelCount   int       `json:"channel_count"`
	LastActivityAt time.Time `json:"last_activity_at,omitzero"`
	UnreadCount    int       `json:"unread_count"`
}

type teamsCmd struct {
	Limit int `default:"50" help:"Maximum items to return; truncated says whether more exist."`
}

func (c *teamsCmd) Run(rt *runtime) error {
	if err := checkFields[teamItem](rt); err != nil {
		return err
	}
	if err := checkLimit(c.Limit); err != nil {
		return err
	}
	return rt.read("teams", func(st *store.Store) (result, error) {
		if st == nil {
			return newList(nil, false), nil
		}
		var total int
		rows, trunc, err := st.Teams(rt.ctx, store.Filter{Account: rt.account, Limit: c.Limit, Total: &total})
		if err != nil {
			return nil, err
		}
		items := make([]teamItem, len(rows))
		for i, r := range rows {
			items[i] = teamItem{TenantID: r.TenantID, UserID: r.UserID, TeamID: r.ID, DisplayName: r.DisplayName, ChannelCount: r.ChannelCount, LastActivityAt: r.LastActivityAt, UnreadCount: r.UnreadCount}
		}
		return newList(shape(rt, items), trunc).withTotal(total), nil
	})
}

type peopleCmd struct {
	Query string `help:"Part of a display name, or an exact person id."`
	Limit int    `default:"50" help:"Maximum items to return; truncated says whether more exist."`
}

func (c *peopleCmd) Run(rt *runtime) error {
	if err := checkFields[personItem](rt); err != nil {
		return err
	}
	if err := checkLimit(c.Limit); err != nil {
		return err
	}
	return rt.read("people", func(st *store.Store) (result, error) {
		if st == nil {
			return newList(nil, false), nil
		}
		var total int
		rows, trunc, err := st.People(rt.ctx, c.Query, store.Filter{Account: rt.account, Limit: c.Limit, Total: &total})
		if err != nil {
			return nil, err
		}
		items := make([]personItem, len(rows))
		for i, r := range rows {
			items[i] = personItem{TenantID: r.TenantID, ID: r.ID, DisplayName: r.DisplayName, LastSeenAt: r.LastSeenAt}
		}
		return newList(shape(rt, items), trunc).withTotal(total), nil
	})
}

type activityCmd struct {
	Unread         bool   `help:"Only unread items."`
	Type           string `help:"Only these activity types, comma separated, matched exactly in any case. Seen in the cache: mention (you were @-mentioned in a channel, as a team or tag), mentionInChat (in a chat, or by @everyone), reply, replyToReply, follow, reaction, reactionInChat, msGraph (system notices such as meeting updates and approvals), teamMembershipChange, threadActivity. Example: --type mention,mentionInChat."`
	Team           string `help:"Only items in this team and its channels: the team's exact name (any case for ASCII letters) or its id. An unknown or ambiguous name is a usage error."`
	DirectMentions bool   `name:"direct-mentions" help:"Only mention items that name you (type mention or mentionInChat, subtype person, or a message that mentions you by name), not channel, team, tag or @everyone mentions."`
	Since          string `help:"Only items at or after this time (RFC3339, YYYY-MM-DD or a relative duration such as 24h)."`
	Limit          int    `default:"50" help:"Maximum items to return; truncated says whether more exist."`
	IncludeSystem  bool   `name:"include-system" help:"Also include Teams' system pseudo-conversations (48:notifications, 48:calllogs, 48:annotations), which mirror real messages and are left out by default."`
}

func (c *activityCmd) Run(rt *runtime) error {
	if err := checkFields[activityItem](rt); err != nil {
		return err
	}
	if err := checkLimit(c.Limit); err != nil {
		return err
	}
	since, err := rt.when("--since", c.Since)
	if err != nil {
		return err
	}
	rt.team = c.Team
	return rt.read("activity", func(st *store.Store) (result, error) {
		if st == nil {
			return newList(nil, false), nil
		}
		var total int
		rows, trunc, err := st.Activity(rt.ctx, store.ActivityFilter{Account: rt.account, Unread: c.Unread, Type: c.Type, DirectMentions: c.DirectMentions, Since: since, Limit: c.Limit, IncludeSystem: c.IncludeSystem, Team: c.Team, Total: &total})
		if err != nil {
			return nil, err
		}
		items := activityItems(rows, rt.g.MaxText)
		return newList(shape(rt, items), trunc).withTotal(total), nil
	})
}

// --- sync, status, whoami, sql ---

type syncCmd struct {
	FullRead bool `name:"full-read" env:"M365CRAWL_FULL_READ" help:"Read every record in full, even from a cache that has not changed since the last sync, instead of skipping the records whose bytes are unchanged. The archive comes out the same either way; this is a check, not a repair."`
}

func (c syncCmd) Run(rt *runtime) error {
	if err := rt.checkLink(); err != nil {
		return err
	}
	if err := rt.checkOutlookRoot(); err != nil {
		return err
	}
	if rt.account != nil && rt.outlookLink != "" {
		e := errs.Usage("--outlook-account cannot be combined with --account on sync: a Teams account filter leaves Outlook out of the run")
		e.Fix = "Drop --account, or run the link on its own."
		return e
	}
	rep, _, err := runSync(rt.ctx, rt.linkOptions(rt.syncOptions(syncer.Options{Root: rt.root, DBPath: rt.dbPath, Account: rt.account, Progress: rt.progress(), FullRead: c.FullRead})))
	var coded *errs.Coded
	if errors.As(err, &coded) && (coded.Code == errs.CodePartialSync || coded.Code == errs.CodeUsage && rep.Status != "") && rt.ctx.Err() == nil {
		// Some sources committed: the report says which, and the error makes the exit status nonzero.
		// A refused link is the same: the sources are in the archive, the link is not.
		if werr := rt.write("sync", rep); werr != nil {
			return werr
		}
		return err
	}
	if err != nil {
		return err
	}
	return rt.write("sync", rep)
}

type statusResult struct {
	ArchivePath   string `json:"archive_path"`
	ArchiveExists bool   `json:"archive_exists"`
	store.StatusRow
	OtherOrigins []string `json:"other_origins,omitempty"`
	// Outlook is the Outlook root's problem (the same code, message and fix as the error sync gives),
	// present only when the Outlook source is on and its root is not a readable directory.
	Outlook *errorBody `json:"outlook,omitempty"`
	// Mail is the archive's mail in numbers and whether mail is being read.
	Mail *mailStatusBlock `json:"mail,omitempty"`
	meta
}

type statusCmd struct{}

func (statusCmd) Run(rt *runtime) error {
	return rt.read("status", func(st *store.Store) (result, error) {
		res := &statusResult{ArchivePath: rt.dbPath, ArchiveExists: st != nil}
		res.Accounts = []store.AccountStatus{}
		if st != nil {
			row, err := st.Status(rt.ctx)
			if err != nil {
				return nil, err
			}
			res.StatusRow = row
		}
		var err error
		if res.Mail, err = rt.mailStatus(st); err != nil {
			return nil, err
		}
		root := rt.root
		if root == "" {
			root = teamsdesktop.DefaultRoot()
		}
		if _, other, err := teamsdesktop.Discover(root); err == nil || len(other) > 0 {
			res.OtherOrigins = other
		}
		return res, nil
	})
}

type whoamiResult struct {
	Accounts []store.WhoamiRow `json:"accounts"`
	Archive  *statusResult     `json:"archive"`
	meta
}

// setMeta stamps the nested archive block too, so its archive_age_seconds equals the top-level one.
func (r *whoamiResult) setMeta(age *int64, se *syncError) {
	r.meta.setMeta(age, se)
	r.Archive.setMeta(age, se)
}

func (r *whoamiResult) setSynced(si *syncedInfo) {
	r.meta.setSynced(si)
	r.Archive.setSynced(si)
}

func (r *whoamiResult) setNeedsSync(hint string) {
	r.meta.setNeedsSync(hint)
	r.Archive.setNeedsSync(hint)
}

type whoamiCmd struct{}

func (whoamiCmd) Run(rt *runtime) error {
	return rt.read("whoami", func(st *store.Store) (result, error) {
		res := &whoamiResult{Accounts: []store.WhoamiRow{}, Archive: &statusResult{ArchivePath: rt.dbPath, ArchiveExists: st != nil}}
		res.Archive.Accounts = []store.AccountStatus{}
		if st != nil {
			rows, err := st.Whoami(rt.ctx)
			if err != nil {
				return nil, err
			}
			res.Accounts = rows
			if res.Archive.StatusRow, err = st.Status(rt.ctx); err != nil {
				return nil, err
			}
		}
		return res, nil
	})
}

type sqlResult struct {
	Columns   []string `json:"columns"`
	Rows      [][]any  `json:"rows"`
	Count     int      `json:"count"`
	Truncated bool     `json:"truncated"`
	meta
}

type sqlCmd struct {
	Query string `arg:"" help:"One SELECT (or WITH/EXPLAIN/VALUES) statement."`
	Limit int    `default:"50" help:"Maximum rows to return; the query stops there and truncated says whether more rows exist."`
}

func (c *sqlCmd) Run(rt *runtime) error {
	if err := checkLimit(c.Limit); err != nil {
		return err
	}
	q := strings.TrimSpace(c.Query)
	if err := store.CheckSQL(q); err != nil {
		return sqlUsage(strings.TrimPrefix(err.Error(), store.ErrQueryRefused.Error()+": "))
	}
	return rt.read("sql", func(st *store.Store) (result, error) {
		if st == nil {
			return &sqlResult{Columns: []string{}, Rows: [][]any{}}, nil
		}
		cols, rows, truncated, err := st.SQL(rt.ctx, strings.TrimRight(q, "; \t\n"), c.Limit)
		if err != nil {
			if rt.ctx.Err() != nil {
				return nil, err
			}
			return nil, sqlEngineError(err)
		}
		res := &sqlResult{Columns: append([]string{}, cols...), Rows: rows, Truncated: truncated} // never null in JSON
		if res.Rows == nil {
			res.Rows = [][]any{}
		}
		res.Count = len(res.Rows)
		return res, nil
	})
}
