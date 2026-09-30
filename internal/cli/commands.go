package cli

import (
	"context"
	"net/url"
	"strings"
	"time"

	"github.com/ourostack/teamscrawl/internal/errs"
	"github.com/ourostack/teamscrawl/internal/store"
	"github.com/ourostack/teamscrawl/internal/syncer"
	"github.com/ourostack/teamscrawl/internal/teamsdesktop"
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
	MentionsMe bool       `json:"mentions_me"`
	Reactions  []reaction `json:"reactions,omitempty"`
	Files      []file     `json:"files,omitempty"`
	Links      []string   `json:"links,omitempty"`
	Subject    string     `json:"subject,omitempty"`
	Importance string     `json:"importance,omitempty"`
	Pinned     bool       `json:"pinned"`
	Link       string     `json:"link,omitempty"`
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
			MentionsMe: r.MentionsMe, Links: r.Links, Subject: r.Subject, Importance: r.Importance, Pinned: r.Pinned, Link: r.Link,
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

// messageList turns message rows into a list result, adding html when asked.
func (rt *runtime) messageList(ctx context.Context, st *store.Store, rows []store.MessageRow, truncated, withHTML bool) (result, error) {
	var html map[store.MessageKey]string
	if withHTML {
		keys := make([]store.MessageKey, len(rows))
		for i, r := range rows {
			keys[i] = store.MessageKey{TenantID: r.TenantID, UserID: r.UserID, ConversationID: r.ConversationID, ID: r.ID}
		}
		var err error
		if html, err = st.MessageHTML(ctx, keys); err != nil {
			return nil, err
		}
	}
	items, err := shape(rt, messageItems(rows, rt.g.MaxText, html))
	if err != nil {
		return nil, err
	}
	return newList(items, truncated), nil
}

// msgFlags are the filters messages, search and unread share.
type msgFlags struct {
	Conversation  string `short:"c" help:"Conversation id, or its exact title or display name."`
	From          string `help:"Sender: a person id, or a case-insensitive part of the name."`
	Since         string `help:"Only messages at or after this time: RFC3339, YYYY-MM-DD (local midnight) or a relative duration (90m, 24h, 7d, 2w)."`
	Until         string `help:"Only messages at or before this time (same formats as --since)."`
	Limit         int    `default:"50" help:"Maximum items to return; truncated says whether more exist."`
	IncludeSystem bool   `name:"include-system" help:"Also include Teams' system pseudo-conversations (48:notifications, 48:calllogs, 48:annotations), which mirror real messages and are left out by default."`
}

func (rt *runtime) filter(f msgFlags) (store.Filter, error) {
	if err := checkLimit(f.Limit); err != nil {
		return store.Filter{}, err
	}
	since, err := rt.when("--since", f.Since)
	if err != nil {
		return store.Filter{}, err
	}
	until, err := rt.when("--until", f.Until)
	if err != nil {
		return store.Filter{}, err
	}
	return store.Filter{Account: rt.account, Conversation: f.Conversation, From: f.From, Since: since, Until: until, Limit: f.Limit, IncludeSystem: f.IncludeSystem}, nil
}

type searchCmd struct {
	Query string `arg:"" help:"Words to find; \"quoted phrases\" and a trailing * for prefixes are supported."`
	msgFlags
	IncludeDeleted bool `name:"include-deleted" help:"Also search deleted messages."`
	MentionsMe     bool `name:"mentions-me" help:"Only messages that mention you."`
	HTML           bool `name:"html" help:"Add each message's HTML body as html."`
}

func (c *searchCmd) Run(rt *runtime) error {
	if err := checkFields[messageItem](rt); err != nil {
		return err
	}
	f, err := rt.filter(c.msgFlags)
	if err != nil {
		return err
	}
	f.IncludeDeleted, f.MentionsMe = c.IncludeDeleted, c.MentionsMe
	return rt.read("search", func(st *store.Store) (result, error) {
		if st == nil {
			return newList(nil, false), nil
		}
		rows, trunc, err := st.Search(rt.ctx, c.Query, f)
		if err != nil {
			return nil, err
		}
		return rt.messageList(rt.ctx, st, rows, trunc, c.HTML)
	})
}

type messagesCmd struct {
	msgFlags
	IncludeDeleted  bool `name:"include-deleted" help:"Also list deleted messages."`
	MentionsMe      bool `name:"mentions-me" help:"Only messages that mention you."`
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
	f.IncludeDeleted, f.MentionsMe, f.Unread, f.IncludeChannels = c.IncludeDeleted, c.MentionsMe, c.Unread, c.IncludeChannels
	return rt.read("messages", func(st *store.Store) (result, error) {
		if st == nil {
			return newList(nil, false), nil
		}
		rows, trunc, err := st.Messages(rt.ctx, f)
		if err != nil {
			return nil, err
		}
		return rt.messageList(rt.ctx, st, rows, trunc, c.HTML)
	})
}

type unreadCmd struct {
	Conversation    string `short:"c" help:"Conversation id, or its exact title or display name."`
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
	f, err := rt.filter(msgFlags{Conversation: c.Conversation, Limit: c.Limit, IncludeSystem: c.IncludeSystem})
	if err != nil {
		return err
	}
	f.IncludeChannels = c.IncludeChannels
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
			shaped, err := shape(rt, items)
			if err != nil {
				return nil, err
			}
			return newList(shaped, trunc), nil
		}
		rows, trunc, err := st.Unread(rt.ctx, f)
		if err != nil {
			return nil, err
		}
		return rt.messageList(rt.ctx, st, rows, trunc, c.HTML)
	})
}

type threadCmd struct {
	Target         string `arg:"" help:"Conversation id, or a Teams message link."`
	Root           string `arg:"" optional:"" help:"Root message id (not needed with a link)."`
	IncludeDeleted bool   `name:"include-deleted" help:"Also show deleted messages."`
	HTML           bool   `name:"html" help:"Add each message's HTML body as html."`
	IncludeSystem  bool   `name:"include-system" help:"Also include Teams' system pseudo-conversations (48:notifications, 48:calllogs, 48:annotations), which mirror real messages and are left out by default."`
}

func (c *threadCmd) Run(rt *runtime) error {
	if err := checkFields[messageItem](rt); err != nil {
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
		rows, err := st.Thread(rt.ctx, conv, root, store.Filter{Account: rt.account, IncludeDeleted: c.IncludeDeleted, IncludeSystem: c.IncludeSystem})
		if err != nil {
			return nil, err
		}
		return rt.messageList(rt.ctx, st, rows, false, c.HTML)
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
	c.Fix = "The archive is read-only by design. Run `teamscrawl sql --help`; sql accepts one SELECT, WITH, EXPLAIN or VALUES statement."
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
	Query         string `help:"Words to find in conversation titles."`
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
	return rt.read("conversations", func(st *store.Store) (result, error) {
		if st == nil {
			return newList(nil, false), nil
		}
		rows, trunc, err := st.Conversations(rt.ctx, c.Kind, c.Query, store.Filter{Account: rt.account, Limit: c.Limit, IncludeSystem: c.IncludeSystem})
		if err != nil {
			return nil, err
		}
		items := make([]conversationItem, len(rows))
		for i, r := range rows {
			items[i] = conversationItem{TenantID: r.TenantID, UserID: r.UserID, ID: r.ID, Kind: r.Kind, Title: r.Title, DisplayName: r.DisplayName,
				TeamID: r.TeamID, MemberCount: len(r.Members), LastMessageAt: r.LastMessageAt, ReadHorizonAt: r.ReadHorizonAt, Favorite: r.Favorite}
		}
		shaped, err := shape(rt, items)
		if err != nil {
			return nil, err
		}
		return newList(shaped, trunc), nil
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
		rows, trunc, err := st.People(rt.ctx, c.Query, store.Filter{Account: rt.account, Limit: c.Limit})
		if err != nil {
			return nil, err
		}
		items := make([]personItem, len(rows))
		for i, r := range rows {
			items[i] = personItem{TenantID: r.TenantID, ID: r.ID, DisplayName: r.DisplayName, LastSeenAt: r.LastSeenAt}
		}
		shaped, err := shape(rt, items)
		if err != nil {
			return nil, err
		}
		return newList(shaped, trunc), nil
	})
}

type activityCmd struct {
	Unread        bool   `help:"Only unread items."`
	Type          string `help:"Only this activity type, for example mentionInChat."`
	Since         string `help:"Only items at or after this time (RFC3339, YYYY-MM-DD or a relative duration such as 24h)."`
	Limit         int    `default:"50" help:"Maximum items to return; truncated says whether more exist."`
	IncludeSystem bool   `name:"include-system" help:"Also include Teams' system pseudo-conversations (48:notifications, 48:calllogs, 48:annotations), which mirror real messages and are left out by default."`
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
	return rt.read("activity", func(st *store.Store) (result, error) {
		if st == nil {
			return newList(nil, false), nil
		}
		rows, trunc, err := st.Activity(rt.ctx, store.ActivityFilter{Account: rt.account, Unread: c.Unread, Type: c.Type, Since: since, Limit: c.Limit, IncludeSystem: c.IncludeSystem})
		if err != nil {
			return nil, err
		}
		items := make([]activityItem, len(rows))
		for i, r := range rows {
			text, cut := truncateRunes(r.MessageText, rt.g.MaxText)
			items[i] = activityItem{TenantID: r.TenantID, UserID: r.UserID, ID: r.ID, Type: r.Type, Subtype: r.Subtype, IsRead: r.IsRead, At: r.At,
				ConversationID: r.ConversationID, ConversationDisplayName: r.ConversationDisplayName, MessageID: r.MessageID, ReplyChainID: r.ReplyChainID,
				AppID: r.AppID, SenderID: r.SenderID, SenderName: r.SenderName, MessageSentAt: r.MessageSentAt, Text: text, TextTruncated: cut, Link: r.MessageLink}
		}
		shaped, err := shape(rt, items)
		if err != nil {
			return nil, err
		}
		return newList(shaped, trunc), nil
	})
}

// --- sync, status, whoami, sql ---

type syncCmd struct{}

func (syncCmd) Run(rt *runtime) error {
	rep, _, err := syncer.Run(rt.ctx, syncer.Options{Root: rt.root, DBPath: rt.dbPath, Account: rt.account, Progress: rt.progress()})
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
	Limit int    `default:"50" help:"Maximum rows to return; truncated says whether more exist."`
}

func (c *sqlCmd) Run(rt *runtime) error {
	if err := checkLimit(c.Limit); err != nil {
		return err
	}
	q := strings.TrimSpace(c.Query)
	if hasSecondStatement(q) {
		return sqlUsage("sql takes a single statement")
	}
	if w := strings.Fields(strings.ToLower(q)); len(w) == 0 || !contains([]string{"select", "with", "explain", "values"}, w[0]) {
		return sqlUsage("sql accepts only read statements")
	}
	return rt.read("sql", func(st *store.Store) (result, error) {
		if st == nil {
			return &sqlResult{Columns: []string{}, Rows: [][]any{}}, nil
		}
		cols, rows, err := st.SQL(rt.ctx, strings.TrimRight(q, "; \t\n"))
		if err != nil {
			if rt.ctx.Err() != nil {
				return nil, err
			}
			return nil, sqlUsage("sql: " + err.Error())
		}
		res := &sqlResult{Columns: cols, Rows: rows}
		if res.Columns == nil {
			res.Columns = []string{}
		}
		if len(rows) > c.Limit {
			res.Rows, res.Truncated = rows[:c.Limit], true
		}
		if res.Rows == nil {
			res.Rows = [][]any{}
		}
		res.Count = len(res.Rows)
		return res, nil
	})
}

// hasSecondStatement reports whether q has anything after a statement-ending semicolon, ignoring
// semicolons inside quoted strings and identifiers. It is only a friendly early error for
// "two statements" mistakes (it does not understand SQL comments). It is not a security control:
// the read-only archive connection is the boundary that stops writes.
func hasSecondStatement(q string) bool {
	var quote rune
	for i, r := range q {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			}
		case r == '\'' || r == '"' || r == '`':
			quote = r
		case r == ';':
			return strings.TrimSpace(q[i+1:]) != ""
		}
	}
	return false
}
