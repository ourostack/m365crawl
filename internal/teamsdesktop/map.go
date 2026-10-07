package teamsdesktop

import (
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ourostack/m365crawl/internal/v8"
)

// Mapping turns decoded records (*v8.Object trees) into the shared types. Teams stores several
// fields inconsistently: the same field may be a native array in one record and a JSON-encoded
// string in the next, a number in one and a digit string in another. The accessors below accept
// both forms and treat anything else as absent, so a wrong-shaped optional field never fails a
// record; only a record that lacks its identity is an *UnmappedError.

// minEpochMillis is the smallest number read as a millisecond timestamp (1973). The real cache
// stores 0, 1 and other small counters in time fields.
const minEpochMillis = 1e11

// fld returns the named field of a decoded object, or nil when v is not an object or the field
// is missing or undefined.
func fld(v any, key string) any {
	o, ok := v.(*v8.Object)
	if !ok || o == nil {
		return nil
	}
	for i, k := range o.Keys {
		if k == key {
			if _, undef := o.Values[i].(v8.Undefined); undef {
				return nil
			}
			return o.Values[i]
		}
	}
	return nil
}

func path(v any, keys ...string) any {
	for _, k := range keys {
		v = fld(v, k)
	}
	return v
}

// str returns v when it is a string. Other types give "".
func str(v any) string {
	s, _ := v.(string)
	return s
}

// idStr is str that also accepts an integral number, for ids Teams sometimes stores numerically.
func idStr(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		if x == math.Trunc(x) && math.Abs(x) < 1<<53 {
			return strconv.FormatInt(int64(x), 10)
		}
	case int64:
		return strconv.FormatInt(x, 10)
	}
	return ""
}

func asInt(v any) int64 {
	switch x := v.(type) {
	case float64:
		if math.Abs(x) < 1<<63 {
			return int64(x)
		}
	case int64:
		return x
	case string:
		n, err := strconv.ParseInt(strings.TrimSpace(x), 10, 64)
		if err == nil {
			return n
		}
	}
	return 0
}

func asBool(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return strings.EqualFold(strings.TrimSpace(x), "true")
	}
	return false
}

// asTime reads a millisecond epoch (number or digit string), an RFC 3339 string, or a Date, and
// returns it in UTC. Anything else, and numbers below minEpochMillis, give the zero time.
func asTime(v any) time.Time {
	return utc(parseTime(v))
}

func utc(t time.Time) time.Time {
	if t.IsZero() {
		return time.Time{}
	}
	return t.UTC()
}

func parseTime(v any) time.Time {
	switch x := v.(type) {
	case time.Time:
		return x
	case float64, int64:
		if n := asInt(x); float64(n) >= minEpochMillis {
			return time.UnixMilli(n)
		}
	case string:
		x = strings.TrimSpace(x)
		if n, err := strconv.ParseInt(x, 10, 64); err == nil {
			if float64(n) >= minEpochMillis {
				return time.UnixMilli(n)
			}
			return time.Time{}
		}
		if t, err := time.Parse(time.RFC3339Nano, x); err == nil {
			return t
		}
	}
	return time.Time{}
}

// items returns v as a list: a native array, or a string holding a JSON array.
func items(v any) []any {
	switch x := v.(type) {
	case []any:
		return x
	case *v8.ArrayWithProps:
		return x.Items
	case string:
		if t := strings.TrimSpace(x); strings.HasPrefix(t, "[") {
			var j []any
			if json.Unmarshal([]byte(t), &j) == nil {
				return fromJSON(j).([]any)
			}
		}
	}
	return nil
}

// object returns v as an object: a native object, or a string holding a JSON object.
func object(v any) *v8.Object {
	switch x := v.(type) {
	case *v8.Object:
		return x
	case string:
		if t := strings.TrimSpace(x); strings.HasPrefix(t, "{") {
			var j map[string]any
			if json.Unmarshal([]byte(t), &j) == nil {
				return fromJSON(j).(*v8.Object)
			}
		}
	}
	return nil
}

// fromJSON converts encoding/json output into the v8 shapes the accessors read.
func fromJSON(v any) any {
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		o := &v8.Object{Keys: keys, Values: make([]any, len(keys))}
		for i, k := range keys {
			o.Values[i] = fromJSON(x[k])
		}
		return o
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = fromJSON(e)
		}
		return out
	}
	return v
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

func canonical(v any) ([]byte, error) {
	raw, err := v8.Canonical(v)
	if err != nil {
		return nil, &UnmappedError{Reason: "not_canonicalizable: " + err.Error()}
	}
	return raw, nil
}

// isChannelID reports whether a conversation id is a channel thread: the current "@thread.tacv2"
// ids and the legacy "@thread.skype" ids (real caches hold both, of kinds Topic and Space).
func isChannelID(id string) bool {
	return strings.HasSuffix(id, "@thread.tacv2") || strings.HasSuffix(id, "@thread.skype")
}

// DeepLink returns the Teams link to one message, in the documented form
// https://teams.microsoft.com/l/message/<conversationId>/<messageId>?tenantId=<tenant>[&parentMessageId=<root>]&context={"contextType":"chat"|"channel"}.
// parentMessageID is added for a reply in a channel (when it differs from messageID). The cache
// holds no ready-made links (targetLink and conversationLink are internal chat-service URLs), so
// the format follows Microsoft's documentation. An empty conversation or message id gives "".
func DeepLink(conversationID, messageID, parentMessageID, tenantID string, isChannel bool) string {
	if conversationID == "" || messageID == "" {
		return ""
	}
	var q []string
	if tenantID != "" {
		q = append(q, "tenantId="+url.QueryEscape(tenantID))
	}
	if isChannel && parentMessageID != "" && parentMessageID != messageID {
		q = append(q, "parentMessageId="+url.QueryEscape(parentMessageID))
	}
	contextType := "chat"
	if isChannel {
		contextType = "channel"
	}
	q = append(q, "context="+url.QueryEscape(`{"contextType":"`+contextType+`"}`))
	return "https://teams.microsoft.com/l/message/" + url.PathEscape(conversationID) + "/" + url.PathEscape(messageID) + "?" + strings.Join(q, "&")
}

// MapReplyChain maps one replychains-2 record to its messages (one per entry of messageMap, in
// that order) and the people seen as senders or mentioned. Each message's Raw is the canonical
// JSON of the message object alone. Message time is originalArrivalTime; a message is deleted
// when it carries a deletionInfo object or properties.deletetime. A record without a messageMap,
// or a message without an id or a conversation, is an *UnmappedError.
func MapReplyChain(acct Account, v any) ([]Message, []Person, error) {
	chain, ok := v.(*v8.Object)
	if !ok || chain == nil {
		return nil, nil, &UnmappedError{Reason: fmt.Sprintf("reply chain is %T, not an object", v)}
	}
	mm, ok := fld(chain, "messageMap").(*v8.Object)
	if !ok {
		return nil, nil, &UnmappedError{Reason: "reply chain has no messageMap object"}
	}
	if len(mm.Keys) == 0 {
		return nil, nil, &UnmappedError{Reason: "reply chain has an empty messageMap"}
	}
	chainConv := idStr(fld(chain, "conversationId"))
	chainID := idStr(fld(chain, "replyChainId"))
	var msgs []Message
	people := newPeople(acct)
	for _, mv := range mm.Values {
		mo, ok := mv.(*v8.Object)
		if !ok || mo == nil {
			return nil, nil, &UnmappedError{Reason: fmt.Sprintf("message is %T, not an object", mv)}
		}
		m, err := mapMessage(acct, mo, chainConv, chainID, people)
		if err != nil {
			return nil, nil, err
		}
		msgs = append(msgs, m)
	}
	return msgs, people.list(), nil
}

func mapMessage(acct Account, mo *v8.Object, chainConv, chainID string, people *peopleSet) (Message, error) {
	id := idStr(fld(mo, "id"))
	if id == "" {
		return Message{}, &UnmappedError{Reason: "message has no id"}
	}
	conv := firstNonEmpty(idStr(fld(mo, "conversationId")), chainConv)
	if conv == "" {
		return Message{}, &UnmappedError{Reason: "message has no conversation id"}
	}
	raw, err := canonical(mo)
	if err != nil {
		return Message{}, err
	}
	props := fld(mo, "properties")
	parent := idStr(fld(mo, "parentMessageId"))
	sentAt := asTime(fld(mo, "originalArrivalTime"))
	if sentAt.IsZero() {
		sentAt = asTime(fld(mo, "clientArrivalTime"))
	}
	m := Message{
		TenantID: acct.TenantID, UserID: acct.UserID,
		ConversationID: conv, ID: id,
		ReplyChainID:    firstNonEmpty(chainID, parent, id),
		ParentMessageID: parent,
		ClientMessageID: idStr(fld(mo, "clientMessageId")),
		SenderID:        firstNonEmpty(idStr(fld(mo, "creator")), idStr(fld(mo, "from"))),
		SenderName:      firstNonEmpty(str(fld(mo, "imDisplayName")), str(fld(mo, "fromDisplayNameInToken"))),
		SentAt:          sentAt,
		EditedAt:        asTime(fld(props, "edittime")),
		MessageType:     str(fld(mo, "messageType")),
		ContentType:     str(fld(mo, "contentType")),
		ContentHTML:     str(fld(mo, "content")),
		Version:         asInt(fld(mo, "version")),
		Subject:         firstNonEmpty(str(fld(props, "subject")), str(fld(props, "title"))),
		Importance:      str(fld(props, "importance")),
		Pinned:          isPinned(fld(props, "pinned")),
		Raw:             raw,
	}
	m.ContentText = messageText(m.MessageType, m.ContentHTML, fld(props, "cards"))
	m.DeletedAt = deletedAt(mo, props, sentAt)
	m.Mentions = mapMentions(fld(props, "mentions"))
	me := orgIDPrefix + acct.UserID
	for _, mn := range m.Mentions {
		if mn.ID == me {
			m.MentionsMe = true
		}
	}
	m.Reactions = mapReactions(props)
	m.Files = mapFiles(fld(props, "files"))
	m.Links = mapLinks(fld(props, "links"))
	// A channel reply without parentMessageId is still in its root's chain: link through the chain.
	linkParent := parent
	if linkParent == "" && m.ReplyChainID != id {
		linkParent = m.ReplyChainID
	}
	m.Link = DeepLink(conv, id, linkParent, acct.TenantID, isChannelID(conv))

	people.add(m.SenderID, m.SenderName, sentAt)
	for _, mn := range m.Mentions {
		if !strings.HasPrefix(mn.ID, "tag:") {
			people.add(mn.ID, mn.DisplayName, sentAt)
		}
	}
	return m, nil
}

// messageText is the readable text of a message. A system message (call event, thread activity)
// has no text in its markup, so it gets a short synthesized one (see systemText); Control
// messages stay blank. A call recording or transcript notice whose content is bare JSON metadata
// reads as a one-line notice. Any other message is its HTML as text, followed by the text of its
// cards (one per line) unless the message text already says it.
func messageText(messageType, html string, cards any) string {
	if isSystemMessage(messageType) {
		return systemText(messageType, html)
	}
	if t := mediaMetadataText(messageType, html); t != "" {
		return t
	}
	text := HTMLToText(html)
	card := cardsText(cards)
	switch {
	case card == "" || linesWithin(card, text):
		return text
	case text == "":
		return card
	}
	return text + "\n" + card
}

// linesWithin reports whether every line of card is a whole line of text, so a card that the
// message already spells out adds nothing while one whose words merely occur inside a longer
// line ("Done" in "Done with the build") is kept.
func linesWithin(card, text string) bool {
	have := map[string]bool{}
	for _, l := range strings.Split(text, "\n") {
		have[strings.TrimSpace(l)] = true
	}
	for _, l := range strings.Split(card, "\n") {
		if !have[strings.TrimSpace(l)] {
			return false
		}
	}
	return true
}

// isSystemMessage is true for call events, thread activity and control messages, whose content
// is markup with no message text to search.
func isSystemMessage(messageType string) bool {
	for _, p := range []string{"ThreadActivity/", "Event/", "Control/"} {
		if strings.HasPrefix(messageType, p) {
			return true
		}
	}
	return false
}

func deletedAt(mo *v8.Object, props any, sentAt time.Time) time.Time {
	if di, ok := fld(mo, "deletionInfo").(*v8.Object); ok && di != nil {
		return firstTime(asTime(fld(di, "deleteTime")), asTime(fld(props, "deletetime")), sentAt, time.Unix(1, 0).UTC())
	}
	return asTime(fld(props, "deletetime"))
}

func firstTime(ts ...time.Time) time.Time {
	for _, t := range ts {
		if !t.IsZero() {
			return t
		}
	}
	return time.Time{}
}

func isPinned(v any) bool {
	if b, ok := v.(bool); ok {
		return b
	}
	o := object(v)
	return o != nil && (str(fld(o, "creatorId")) != "" || asInt(fld(o, "pinnedTime")) > 0)
}

func mapMentions(v any) []Mention {
	var out []Mention
	for _, e := range items(v) {
		id := firstNonEmpty(str(fld(e, "mri")), str(fld(e, "id")))
		name := str(fld(e, "displayName"))
		if id == "" && name == "" {
			continue
		}
		out = append(out, Mention{ID: id, DisplayName: name})
	}
	return out
}

func mapReactions(props any) []Reaction {
	list := items(fld(props, "emotions"))
	if list == nil {
		list = items(fld(props, "deltaEmotions"))
	}
	var out []Reaction
	for _, e := range list {
		key := str(fld(e, "key"))
		if key == "" {
			continue
		}
		r := Reaction{Key: key}
		for _, u := range items(fld(e, "users")) {
			r.Count++
			if id := firstNonEmpty(str(u), str(fld(u, "mri"))); id != "" {
				r.UserIDs = append(r.UserIDs, id)
			}
		}
		out = append(out, r)
	}
	return out
}

func mapFiles(v any) []File {
	var out []File
	for _, e := range items(v) {
		f := File{
			Name: str(fld(e, "fileName")),
			Type: str(fld(e, "fileType")),
			URL: firstNonEmpty(str(fld(e, "objectUrl")), str(path(e, "fileInfo", "fileUrl")),
				str(path(e, "fileInfo", "shareUrl")), str(fld(e, "openUrl"))),
		}
		if f.Name != "" || f.URL != "" {
			out = append(out, f)
		}
	}
	return out
}

func mapLinks(v any) []string {
	var out []string
	for _, e := range items(v) {
		if u := firstNonEmpty(str(e), str(fld(e, "url"))); u != "" {
			out = append(out, u)
		}
	}
	return out
}

// peopleSet collects distinct people by MRI, keeping the newest sighting that has a name.
type peopleSet struct {
	tenant string
	byID   map[string]*Person
	order  []string
}

func newPeople(acct Account) *peopleSet {
	return &peopleSet{tenant: acct.TenantID, byID: map[string]*Person{}}
}

func (s *peopleSet) add(id, name string, seen time.Time) {
	if id == "" || name == "" {
		return
	}
	if p, ok := s.byID[id]; ok {
		if seen.After(p.SeenAt) {
			p.SeenAt, p.DisplayName = seen, name
		}
		return
	}
	s.byID[id] = &Person{TenantID: s.tenant, ID: id, DisplayName: name, SeenAt: seen}
	s.order = append(s.order, id)
}

func (s *peopleSet) list() []Person {
	out := make([]Person, 0, len(s.order))
	for _, id := range s.order {
		out = append(out, *s.byID[id])
	}
	return out
}

// MapConversation maps one conversations record. DisplayName is the chat title
// (chatTitle.shortTitle, then the topic, then the other members' names) and, for a channel, the
// channel's own topic: the "Team › Channel" form needs the team's record, which may be in a
// different batch, so the store composes it at query time from TeamID and the team's
// conversation. The read marker (properties.consumptionhorizon, "<time>;<time>;<messageId>")
// is split into ReadHorizonAt, the first time (the timestamp of the last message read), and
// ReadHorizonClientMessageID; a horizon of zeros is absent. People are members who carry a name.
func MapConversation(acct Account, v any) (Conversation, []Person, error) {
	co, ok := v.(*v8.Object)
	if !ok || co == nil {
		return Conversation{}, nil, &UnmappedError{Reason: fmt.Sprintf("conversation is %T, not an object", v)}
	}
	id := idStr(fld(co, "id"))
	if id == "" {
		return Conversation{}, nil, &UnmappedError{Reason: "conversation has no id"}
	}
	raw, err := canonical(co)
	if err != nil {
		return Conversation{}, nil, err
	}
	c := Conversation{
		TenantID: acct.TenantID, UserID: acct.UserID, ID: id,
		Kind:          str(fld(co, "type")),
		TeamID:        idStr(fld(co, "teamId")),
		ParentID:      idStr(fld(co, "parentId")),
		LastMessageAt: asTime(fld(co, "lastMessageTimeUtc")),
		Favorite:      asBool(path(co, "properties", "favorite")),
		Raw:           raw,
	}
	short, long := str(fld(co, "chatTitle")), ""
	if ct := fld(co, "chatTitle"); short == "" && ct != nil {
		short, long = str(fld(ct, "shortTitle")), str(fld(ct, "longTitle"))
	}
	c.Title = firstNonEmpty(short, long)
	tp := fld(co, "threadProperties")
	topic, teamTopic, channelTopic := str(fld(tp, "topic")), str(fld(tp, "spaceThreadTopic")), str(fld(tp, "topicThreadTopic"))
	if c.Kind == "Space" {
		c.Topic = firstNonEmpty(teamTopic, topic, channelTopic)
	} else {
		c.Topic = firstNonEmpty(topic, channelTopic, teamTopic)
	}

	people := newPeople(acct)
	var names []string
	others := 0
	me := orgIDPrefix + acct.UserID
	for _, m := range items(fld(co, "members")) {
		mid := firstNonEmpty(str(m), str(fld(m, "id")))
		if mid == "" {
			continue
		}
		c.Members = append(c.Members, mid)
		name := firstNonEmpty(str(fld(m, "friendlyName")), str(fld(m, "displayName")))
		people.add(mid, name, c.LastMessageAt)
		if mid != me {
			others++
			if name != "" {
				names = append(names, name)
			}
		}
	}
	c.DisplayName = firstNonEmpty(short, c.Topic, MemberListName(names, others), long)
	c.ReadHorizonAt, c.ReadHorizonClientMessageID = parseHorizon(str(path(co, "properties", "consumptionhorizon")))
	return c, people.list(), nil
}

// MaxMemberNames is how many member names an untitled group chat's display name lists.
const MaxMemberNames = 3

// MemberListName builds the display name of an untitled chat from its other members' names: up to
// MaxMemberNames of them, then "+N" for the other members not listed, as in "Ana, Ben, Chao +2".
// others counts every other member, named or not; names are the known ones in member order. It
// returns "" when no name is known.
func MemberListName(names []string, others int) string {
	if len(names) == 0 {
		return ""
	}
	shown := names[:min(len(names), MaxMemberNames)]
	out := strings.Join(shown, ", ")
	if rest := others - len(shown); rest > 0 {
		out += " +" + strconv.Itoa(rest)
	}
	return out
}

// parseHorizon splits a Skype consumption horizon "<time>;<time>;<messageId>".
func parseHorizon(s string) (time.Time, string) {
	parts := strings.Split(s, ";")
	if len(parts) < 3 {
		return time.Time{}, ""
	}
	id := strings.TrimSpace(parts[2])
	if id == "0" {
		id = ""
	}
	return asTime(parts[0]), id
}

// MapActivity maps one activity-manager feed-items record. The item carries no text; it points
// at a message by (sourceThreadId, sourceMessageId), falling back to messageId when the source
// id is empty.
func MapActivity(acct Account, v any) (Activity, error) {
	ao, ok := v.(*v8.Object)
	if !ok || ao == nil {
		return Activity{}, &UnmappedError{Reason: fmt.Sprintf("activity is %T, not an object", v)}
	}
	id := idStr(fld(ao, "activityId"))
	if id == "" {
		return Activity{}, &UnmappedError{Reason: "activity has no activityId"}
	}
	raw, err := canonical(ao)
	if err != nil {
		return Activity{}, err
	}
	return Activity{
		TenantID: acct.TenantID, UserID: acct.UserID, ID: id,
		Type:           str(fld(ao, "activityType")),
		Subtype:        str(fld(ao, "activitySubtype")),
		IsRead:         asBool(fld(ao, "isRead")),
		At:             asTime(fld(ao, "timestamp")),
		ConversationID: idStr(fld(ao, "sourceThreadId")),
		MessageID:      firstNonEmpty(idStr(fld(ao, "sourceMessageId")), idStr(fld(ao, "messageId"))),
		ReplyChainID:   idStr(fld(ao, "sourceReplyChainId")),
		AppID:          idStr(fld(ao, "teamsAppId")),
		Raw:            raw,
	}, nil
}
