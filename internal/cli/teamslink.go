package cli

import (
	"net/url"
	"strings"

	"github.com/ourostack/m365crawl/internal/errs"
)

// teamsHosts are the hosts a Teams link is served from; any other host is not a Teams link.
var teamsHosts = []string{"teams.microsoft.com", "teams.cloud.microsoft", "teams.live.com"}

// linkKinds are the kinds of Teams link (the path segment after /l/) that name a conversation.
var linkKinds = []string{"channel", "chat", "message", "meetup-join"}

// linkFix is the fix of a link that names no conversation m365crawl can read.
const linkFix = "Pass a Teams channel, chat, message or meeting link (https://teams.microsoft.com/l/channel/<conversationId>/..., /l/chat/<conversationId>/..., /l/message/<conversationId>/<messageId> or /l/meetup-join/<conversationId>/...), or the conversation id itself; `m365crawl conversations --query <name>` finds a conversation's id."

// peopleLinkFix is the fix of a chat link that names people instead of a chat (/l/chat/0/0?users=...).
const peopleLinkFix = "Find the chat with `m365crawl conversations --query <name>` and pass its conversation_id, or copy the link of the chat or of one of its messages in Teams."

// linkError is why a Teams link names nothing m365crawl can read. people says it is a chat link
// that names people instead of a chat, whose fix differs.
type linkError struct {
	why    string
	people bool
}

func (e *linkError) Error() string { return e.why }

// coded is the usage error of e: fix is the caller's own, except for a link that names people.
func (e *linkError) coded(fix string) *errs.Coded {
	c := errs.Usage(e.why)
	c.Fix = fix
	if e.people {
		c.Fix = peopleLinkFix
	}
	return c
}

// isLink says the argument is a URL rather than an id or a name.
func isLink(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

// parseTeamsLink reads the conversation, the message and the thread root (parentMessageId, a
// channel reply's root) a Teams link names. Channel (/l/channel/), chat (/l/chat/), message
// (/l/message/) and meeting (/l/meetup-join/) links are understood, their ids percent-encoded or
// not; only a message link names a message.
func parseTeamsLink(raw string) (conversation, messageID, parentID string, err *linkError) {
	u, perr := url.Parse(raw)
	if perr != nil {
		return "", "", "", &linkError{why: "the link is not a valid URL"}
	}
	host := strings.ToLower(u.Hostname())
	if (u.Scheme != "http" && u.Scheme != "https") || !contains(teamsHosts, host) {
		return "", "", "", &linkError{why: "not a Teams link: " + raw}
	}
	parts := strings.Split(strings.TrimPrefix(u.EscapedPath(), "/"), "/")
	if len(parts) < 2 || parts[0] != "l" || !contains(linkKinds, parts[1]) {
		return "", "", "", &linkError{why: "not a Teams channel, chat, message or meeting link: " + raw}
	}
	kind, ids := parts[1], parts[2:]
	seg := func(i int) string {
		if i >= len(ids) {
			return ""
		}
		s, _ := url.PathUnescape(ids[i]) // cannot fail: EscapedPath is validly escaped
		return s
	}
	conversation = seg(0)
	if conversation == "" {
		return "", "", "", &linkError{why: "the link has no conversation id"}
	}
	if kind == "chat" && conversation == "0" {
		return "", "", "", &linkError{why: "the chat link names people, not a chat, so it has no conversation id", people: true}
	}
	if kind != "message" {
		return conversation, "", "", nil
	}
	if messageID = seg(1); messageID == "" {
		return "", "", "", &linkError{why: "the link has no message id"}
	}
	return conversation, messageID, u.Query().Get("parentMessageId"), nil
}

// convLink is a --conversation that was a Teams link: the link and whether it named a message.
type convLink struct {
	raw          string
	conversation string
	message      bool
}

// conversationArg resolves a --conversation value: a Teams link becomes the conversation it
// names, remembered on rt so a result can say what the link was taken as; anything else is
// passed on as it is.
func (rt *runtime) conversationArg(v string) (string, error) {
	if !isLink(v) {
		return v, nil
	}
	conv, msg, _, err := parseTeamsLink(v)
	if err != nil {
		c := err.coded(linkFix)
		c.Message = "--conversation: " + c.Message
		return "", c
	}
	rt.link = &convLink{raw: v, conversation: conv, message: msg != ""}
	return conv, nil
}

// linkNote is the note of a read whose --conversation was a message link: the link named one
// message, and thread reads its thread. Empty otherwise.
func (rt *runtime) linkNote() string {
	if rt.link == nil || !rt.link.message {
		return ""
	}
	return "the link named one message; --conversation took its conversation, and m365crawl thread '" + rt.link.raw + "' reads that message's thread"
}

// uncachedNote is the note of an empty read of a conversation a link named that the archive does
// not hold: Teams may not have cached it.
func uncachedNote(conversation string) string {
	return "the archive holds no conversation " + conversation + ": the Teams desktop app may not have cached it; open it once in Teams, then run m365crawl sync"
}

// joinNotes joins two notes into one, either of which may be empty.
func joinNotes(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	}
	return a + "; " + b
}
