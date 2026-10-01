package teamsdesktop

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/teamscrawl/internal/v8"
)

var updateGolden = flag.Bool("update", false, "rewrite testdata/teams-fixture/expected/mapped-*.json")

// obj builds a decoded-V8 object from alternating keys and values.
func obj(kv ...any) *v8.Object {
	o := &v8.Object{}
	for ; len(kv) >= 2; kv = kv[2:] {
		o.Keys = append(o.Keys, kv[0].(string))
		o.Values = append(o.Values, kv[1])
	}
	return o
}

var (
	acct1 = Account{TenantID: tenant1, UserID: user1, Locale: "en-us"}
	acct2 = Account{TenantID: tenant2, UserID: user2, Locale: "en-us"}
	me1   = "8:orgid:" + user1
)

// fixtureRecords maps every allowlisted fixture record for one account.
type fixtureData struct {
	Messages      []Message
	People        []Person
	Conversations []Conversation
	Activity      []Activity
}

func mapFixture(t *testing.T, acct Account) fixtureData {
	t.Helper()
	snap := fixtureSnapshot(t)
	var d fixtureData
	_, err := Read(context.Background(), snap, &acct, func(a Account, kind string, v any) error {
		switch kind {
		case KindReplyChain:
			m, p, err := MapReplyChain(a, v)
			if err != nil {
				return err
			}
			d.Messages = append(d.Messages, m...)
			d.People = append(d.People, p...)
		case KindConversation:
			c, p, err := MapConversation(a, v)
			if err != nil {
				return err
			}
			d.Conversations = append(d.Conversations, c)
			d.People = append(d.People, p...)
		case KindActivity:
			x, err := MapActivity(a, v)
			if err != nil {
				return err
			}
			d.Activity = append(d.Activity, x)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.SliceStable(d.Messages, func(i, j int) bool {
		a, b := d.Messages[i], d.Messages[j]
		return a.ConversationID+"|"+a.ID < b.ConversationID+"|"+b.ID
	})
	return d
}

func (d fixtureData) msg(t *testing.T, contains string) Message {
	t.Helper()
	for _, m := range d.Messages {
		if strings.Contains(m.ContentHTML, contains) {
			return m
		}
	}
	t.Fatalf("no fixture message containing %q", contains)
	return Message{}
}

func (d fixtureData) conv(t *testing.T, suffixOrTitle string) Conversation {
	t.Helper()
	for _, c := range d.Conversations {
		if strings.HasSuffix(c.ID, suffixOrTitle) || c.Title == suffixOrTitle || c.Topic == suffixOrTitle {
			return c
		}
	}
	t.Fatalf("no fixture conversation %q", suffixOrTitle)
	return Conversation{}
}

// goldenJSON renders mapped values as stable JSON: Raw becomes inline JSON, and anything over
// goldenInline bytes (the multi-megabyte filler messages) is pinned by length and hash.
func goldenJSON(t *testing.T, items any) []byte {
	t.Helper()
	const goldenInline = 4096
	shrink := func(s string) string {
		if len(s) <= goldenInline {
			return s
		}
		sum := sha256.Sum256([]byte(s))
		return fmt.Sprintf("%.40s...[%d bytes sha256:%s]", s, len(s), hex.EncodeToString(sum[:8]))
	}
	rv := reflect.ValueOf(items)
	out := make([]any, 0, rv.Len())
	for i := 0; i < rv.Len(); i++ {
		b, err := json.Marshal(rv.Index(i).Interface())
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		if raw, ok := rv.Index(i).FieldByName("Raw").Interface().([]byte); ok {
			if len(raw) > goldenInline {
				sum := sha256.Sum256(raw)
				m["Raw"] = fmt.Sprintf("[%d bytes sha256:%s]", len(raw), hex.EncodeToString(sum[:8]))
			} else {
				m["Raw"] = json.RawMessage(raw)
			}
		}
		for _, k := range []string{"ContentHTML", "ContentText"} {
			if s, ok := m[k].(string); ok {
				m[k] = shrink(s)
			}
		}
		out = append(out, m)
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(out); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func checkGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "teams-fixture", "expected", name)
	if *updateGolden {
		if err := os.WriteFile(path, got, 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path) //nolint:gosec // fixed fixture path
	if err != nil {
		t.Fatalf("%v (run go test ./internal/teamsdesktop -run Golden -update)", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s mismatch (rerun with -update and review the diff)", name)
	}
}

func TestMapReplyChainGolden(t *testing.T) {
	var all []Message
	for _, a := range []Account{acct1, acct2} {
		all = append(all, mapFixture(t, a).Messages...)
	}
	if len(all) < 90 {
		t.Fatalf("only %d messages mapped", len(all))
	}
	checkGolden(t, "mapped-messages.json", goldenJSON(t, all))
}

func TestMapConversationGolden(t *testing.T) {
	var all []Conversation
	for _, a := range []Account{acct1, acct2} {
		cs := mapFixture(t, a).Conversations
		sort.Slice(cs, func(i, j int) bool { return cs[i].ID < cs[j].ID })
		all = append(all, cs...)
	}
	checkGolden(t, "mapped-conversations.json", goldenJSON(t, all))
}

func TestMapActivityGolden(t *testing.T) {
	var all []Activity
	for _, a := range []Account{acct1, acct2} {
		xs := mapFixture(t, a).Activity
		sort.Slice(xs, func(i, j int) bool { return xs[i].ID < xs[j].ID })
		all = append(all, xs...)
	}
	checkGolden(t, "mapped-activity.json", goldenJSON(t, all))
}

func TestMapFixtureBasics(t *testing.T) {
	d := mapFixture(t, acct1)
	m := d.msg(t, "Hello from Alex Fixture")
	if m.TenantID != tenant1 || m.UserID != user1 || m.SenderID != me1 || m.SenderName != "Alex Fixture" {
		t.Errorf("owner/sender wrong: %+v", m)
	}
	if m.ContentText != "Hello from Alex Fixture" || m.MessageType != "RichText/Html" || m.Version != 1700000001000 {
		t.Errorf("content wrong: %+v", m)
	}
	if !m.SentAt.Equal(time.UnixMilli(1700000001000)) || m.ID != "1700000001000" || m.ParentMessageID != m.ID || m.ReplyChainID != m.ID {
		t.Errorf("ids/time wrong: %+v", m)
	}
	if string(m.Raw) == "" || !strings.Contains(string(m.Raw), `Hello from Alex Fixture</p>`) {
		t.Errorf("Raw is not the canonical message: %s", m.Raw)
	}
	// Senders and mentioned people are reported once, with full MRIs.
	ids := map[string]string{}
	for _, p := range d.People {
		if p.TenantID != tenant1 {
			t.Errorf("person tenant = %q", p.TenantID)
		}
		ids[p.ID] = p.DisplayName
	}
	if ids[me1] != "Alex Fixture" || ids["8:orgid:00000000-0000-4000-8000-0000000000ee"] != "Pat Example" {
		t.Errorf("people = %v", ids)
	}
	if ids["8:orgid:00000000-0000-4000-8000-0000000000dd"] != "Sam Example" {
		t.Errorf("mentioned and member person missing: %v", ids)
	}
}

func TestMapNonTextMessages(t *testing.T) {
	d := mapFixture(t, acct1)
	for _, c := range []struct{ contains, typ, text string }{
		{"AMSImage", "RichText/Html", ""},
		{`<attachment id="card-1">`, "RichText/Html", "Adaptive fixture card"},
		{"<addmember>", "ThreadActivity/AddMember", "Member added"},
		{"<partlist", "Event/Call", "Call ended"},
	} {
		m := d.msg(t, c.contains)
		if m.ContentText != c.text {
			t.Errorf("%s: ContentText = %q, want %q", c.contains, m.ContentText, c.text)
		}
		if m.MessageType != c.typ || m.ContentHTML == "" || m.ID == "" {
			t.Errorf("%s: %+v", c.contains, m)
		}
	}
	// Emoji-only and RTL messages keep their text.
	if got := d.msg(t, "\U0001F44D").ContentText; got != "\U0001F44D" {
		t.Errorf("emoji-only = %q", got)
	}
	if got := d.msg(t, "rtl").ContentText; got != "مرحبا بالعالم שלום" {
		t.Errorf("rtl = %q", got)
	}
	if got := d.msg(t, "Overwritten twice").ContentText; got != "Overwritten twice" {
		t.Errorf("overwritten message = %q", got)
	}
	// A record without content at all (null) still maps.
	rec := obj("conversationId", "19:x@thread.v2", "replyChainId", "1", "messageMap", obj("1", obj("id", "1", "messageType", "Text", "content", nil, "originalArrivalTime", float64(1700000000000))))
	ms, _, err := MapReplyChain(acct1, rec)
	if err != nil || len(ms) != 1 || ms[0].ContentText != "" || ms[0].ContentHTML != "" {
		t.Errorf("null content: %+v %v", ms, err)
	}
}

func TestMapEditedAndDeleted(t *testing.T) {
	d := mapFixture(t, acct1)
	edited := d.msg(t, "Edited text")
	if edited.Version != 1700000011000+60000 || !edited.EditedAt.Equal(time.UnixMilli(1700000011000+60000)) || !edited.DeletedAt.IsZero() {
		t.Errorf("edited: version=%d editedAt=%v", edited.Version, edited.EditedAt)
	}
	deleted := d.msg(t, "Will be deleted")
	if !deleted.DeletedAt.Equal(time.UnixMilli(1700000500000)) || !deleted.EditedAt.IsZero() {
		t.Errorf("deleted: %+v", deleted.DeletedAt)
	}
	plain := d.msg(t, "Hello from Alex Fixture")
	if !plain.DeletedAt.IsZero() || !plain.EditedAt.IsZero() {
		t.Errorf("plain message flagged: %+v %+v", plain.EditedAt, plain.DeletedAt)
	}

	// Real-cache forms: version as a string, edit and delete times as digit strings or numbers,
	// a deletion recorded only in properties.deletetime, and a deletionInfo without a time.
	mk := func(extra ...any) Message {
		base := []any{"id", "5", "messageType", "Text", "version", "1700000005000", "originalArrivalTime", float64(1700000000000)}
		ms, _, err := MapReplyChain(acct1, obj("conversationId", "19:x@thread.v2", "replyChainId", "5", "messageMap", obj("5", obj(append(base, extra...)...))))
		if err != nil || len(ms) != 1 {
			t.Fatalf("map: %v %v", ms, err)
		}
		return ms[0]
	}
	if m := mk("properties", obj("edittime", "1700000009000")); m.Version != 1700000005000 || !m.EditedAt.Equal(time.UnixMilli(1700000009000)) {
		t.Errorf("string edittime/version: %+v", m)
	}
	if m := mk("properties", obj("deletetime", float64(1700000008000))); !m.DeletedAt.Equal(time.UnixMilli(1700000008000)) {
		t.Errorf("properties.deletetime: %v", m.DeletedAt)
	}
	if m := mk("deletionInfo", obj("deleteTime", nil)); m.DeletedAt.IsZero() {
		t.Errorf("deletionInfo without time must still mark the message deleted")
	}
	if m := mk("deletionInfo", nil); !m.DeletedAt.IsZero() {
		t.Errorf("null deletionInfo marked deleted")
	}
}

func TestMentionsMe(t *testing.T) {
	d := mapFixture(t, acct1)
	own := d.msg(t, "Pat Example</span> please review")
	if !own.MentionsMe || len(own.Mentions) != 1 || own.Mentions[0].ID != me1 || own.Mentions[0].DisplayName != "Pat Example" {
		t.Errorf("native-array own mention: %+v", own)
	}
	both := d.msg(t, "Sam Tag")
	if !both.MentionsMe || len(both.Mentions) != 2 || both.Mentions[1].ID != "tag:fixture-tag" || both.Mentions[1].DisplayName != "Sam Tag" {
		t.Errorf("JSON-string mentions: %+v", both.Mentions)
	}
	other := d.msg(t, "is not you")
	if other.MentionsMe || len(other.Mentions) != 1 || other.Mentions[0].DisplayName != "Sam Example" {
		t.Errorf("someone else's mention: %+v", other)
	}
	if none := d.msg(t, "Hello from Alex Fixture"); none.MentionsMe || len(none.Mentions) != 0 {
		t.Errorf("no mentions: %+v", none)
	}
	// Account two sees the same shapes with its own user id: a1's mri is not a2's.
	d2 := mapFixture(t, acct2)
	if m := d2.msg(t, "Sam Tag"); !m.MentionsMe {
		t.Errorf("account 2 own mention missed")
	}
	// The comparison is on full MRIs, so a bare GUID mri from another account never matches.
	rec := obj("conversationId", "19:x@thread.v2", "replyChainId", "1", "messageMap", obj("1", obj("id", "1", "originalArrivalTime", float64(1),
		"properties", obj("mentions", []any{obj("mri", "8:orgid:"+user2, "displayName", "Blair")}))))
	if ms, _, err := MapReplyChain(acct1, rec); err != nil || ms[0].MentionsMe {
		t.Errorf("other account's mention matched: %v %v", ms, err)
	}
}

func TestReactions(t *testing.T) {
	d := mapFixture(t, acct1)
	m := d.msg(t, "Reactions here")
	want := []Reaction{
		{Key: "like", Count: 2, UserIDs: []string{me1, "8:orgid:00000000-0000-4000-8000-0000000000ee"}},
		{Key: "heart", Count: 1, UserIDs: []string{me1}},
	}
	if !reflect.DeepEqual(m.Reactions, want) {
		t.Errorf("reactions = %+v, want %+v", m.Reactions, want)
	}
	if got := d.msg(t, "Hello from Alex Fixture").Reactions; len(got) != 0 {
		t.Errorf("unexpected reactions %+v", got)
	}
	// deltaEmotions is used when emotions is absent; emotions wins when both exist.
	mk := func(p *v8.Object) []Reaction {
		ms, _, err := MapReplyChain(acct1, obj("conversationId", "19:x@thread.v2", "replyChainId", "1", "messageMap", obj("1", obj("id", "1", "originalArrivalTime", float64(1), "properties", p))))
		if err != nil {
			t.Fatal(err)
		}
		return ms[0].Reactions
	}
	delta := []any{obj("key", "laugh", "users", []any{obj("mri", "8:orgid:x")})}
	if got := mk(obj("deltaEmotions", delta)); len(got) != 1 || got[0].Key != "laugh" || got[0].Count != 1 {
		t.Errorf("deltaEmotions fallback: %+v", got)
	}
	if got := mk(obj("emotions", []any{obj("key", "like", "users", []any{})}, "deltaEmotions", delta)); len(got) != 1 || got[0].Key != "like" || got[0].Count != 0 {
		t.Errorf("emotions precedence: %+v", got)
	}
}

func TestFiles(t *testing.T) {
	d := mapFixture(t, acct1)
	m := d.msg(t, "Files attached")
	want := []File{
		{Name: "Quarterly report.docx", URL: "https://fixture.sharepoint.example/teams/fixture/Quarterly%20report.docx", Type: "docx"},
		{Name: "diagram.png", URL: "https://fixture.sharepoint.example/teams/fixture/diagram.png", Type: "png"},
	}
	if !reflect.DeepEqual(m.Files, want) {
		t.Errorf("files = %+v, want %+v", m.Files, want)
	}
	l := d.msg(t, "the docs")
	if !reflect.DeepEqual(l.Links, []string{"https://example.com/docs?a=1&b=2", "https://example.org/plain"}) {
		t.Errorf("links = %v", l.Links)
	}
	if got := d.msg(t, "Hello from Alex Fixture"); len(got.Files) != 0 || len(got.Links) != 0 {
		t.Errorf("unexpected files/links: %+v", got)
	}
	// Native (non-string) forms map the same way.
	rec := obj("conversationId", "19:x@thread.v2", "replyChainId", "1", "messageMap", obj("1", obj("id", "1", "originalArrivalTime", float64(1), "properties", obj(
		"files", []any{obj("fileName", "a.txt", "fileType", "txt", "objectUrl", "https://h/a.txt")},
		"links", []any{obj("url", "https://h/l")}))))
	ms, _, err := MapReplyChain(acct1, rec)
	if err != nil || !reflect.DeepEqual(ms[0].Files, []File{{Name: "a.txt", URL: "https://h/a.txt", Type: "txt"}}) || !reflect.DeepEqual(ms[0].Links, []string{"https://h/l"}) {
		t.Errorf("native files/links: %+v %v", ms, err)
	}
	// A string that is not JSON yields nothing rather than an error.
	rec = obj("conversationId", "19:x@thread.v2", "replyChainId", "1", "messageMap", obj("1", obj("id", "1", "originalArrivalTime", float64(1), "properties", obj("files", "not json", "mentions", "[oops"))))
	if ms, _, err := MapReplyChain(acct1, rec); err != nil || len(ms[0].Files) != 0 || len(ms[0].Mentions) != 0 {
		t.Errorf("bad json strings: %+v %v", ms, err)
	}
}

func TestSubjectImportancePinnedLink(t *testing.T) {
	d := mapFixture(t, acct1)
	root := d.msg(t, "Channel post with a subject</p>")
	if root.Subject != "Fixture subject" || root.Importance != "high" || !root.Pinned {
		t.Errorf("root = subject %q importance %q pinned %v", root.Subject, root.Importance, root.Pinned)
	}
	plain := d.msg(t, "Hello from Alex Fixture")
	if plain.Subject != "" || plain.Importance != "normal" || plain.Pinned {
		t.Errorf("plain = %q %q %v", plain.Subject, plain.Importance, plain.Pinned)
	}
	reply := d.msg(t, "Reply in thread")
	if reply.ParentMessageID != root.ID || reply.ReplyChainID != root.ID || reply.ID == root.ID {
		t.Errorf("reply ids: %+v", reply)
	}
	if !strings.Contains(reply.ContentText, "Reply in thread") || !strings.Contains(reply.ContentText, "Channel post with a subject") {
		t.Errorf("reply text = %q", reply.ContentText)
	}
	if want := DeepLink(root.ConversationID, reply.ID, root.ID, tenant1, true); reply.Link != want || !strings.Contains(want, "parentMessageId="+root.ID) {
		t.Errorf("reply link = %q want %q", reply.Link, want)
	}
	if want := DeepLink(root.ConversationID, root.ID, "", tenant1, true); root.Link != want {
		t.Errorf("root link = %q want %q", root.Link, want)
	}
	if want := DeepLink(plain.ConversationID, plain.ID, "", tenant1, false); plain.Link != want {
		t.Errorf("chat link = %q want %q", plain.Link, want)
	}
	// title stands in for an empty subject, pinned needs a creator or time.
	mk := func(p *v8.Object) Message {
		ms, _, err := MapReplyChain(acct1, obj("conversationId", "19:x@thread.v2", "replyChainId", "1", "messageMap", obj("1", obj("id", "1", "originalArrivalTime", float64(1), "properties", p))))
		if err != nil {
			t.Fatal(err)
		}
		return ms[0]
	}
	if m := mk(obj("subject", "", "title", "Titled")); m.Subject != "Titled" {
		t.Errorf("title fallback: %q", m.Subject)
	}
	if m := mk(obj("pinned", `{"creatorId":"","pinnedTime":0}`)); m.Pinned {
		t.Errorf("empty pin marker counted as pinned")
	}
	if m := mk(obj("pinned", obj("creatorId", "8:orgid:x", "pinnedTime", float64(5)))); !m.Pinned {
		t.Errorf("native pin marker missed")
	}
}

func TestConversationFields(t *testing.T) {
	d := mapFixture(t, acct1)
	chat := d.conv(t, "Fixture chat 1")
	if chat.Kind != "Chat" || chat.DisplayName != "Fixture chat 1" || chat.Title != "Fixture chat 1" || !chat.Favorite {
		t.Errorf("chat: %+v", chat)
	}
	if !reflect.DeepEqual(chat.Members, []string{me1, "8:orgid:00000000-0000-4000-8000-0000000000ee"}) {
		t.Errorf("members = %v", chat.Members)
	}
	if !chat.LastMessageAt.Equal(time.UnixMilli(1700000100000)) {
		t.Errorf("LastMessageAt = %v", chat.LastMessageAt)
	}
	// A channel: DisplayName is the channel topic, TeamID names the team; the store composes
	// "Team › Channel" from the team's own record, which this mapper resolves to its topic.
	ch := d.conv(t, "General")
	team := d.conv(t, "Fixture team 1")
	if ch.Kind != "Topic" || ch.DisplayName != "General" || ch.Topic != "General" || ch.TeamID != team.ID || ch.ParentID != team.ID {
		t.Errorf("channel: %+v", ch)
	}
	if team.Kind != "Space" || team.DisplayName != "Fixture team 1" || team.Topic != "Fixture team 1" {
		t.Errorf("team: %+v", team)
	}
	if p := d.conv(t, "Planning"); p.DisplayName != "Planning" || p.TeamID != team.ID {
		t.Errorf("second channel: %+v", p)
	}
	// Untitled group chat: member display names, without the account's own user.
	g := d.conv(t, "@thread.v2")
	_ = g
	var grp Conversation
	for _, c := range d.Conversations {
		if strings.HasPrefix(c.ID, "19:untitledgroup") {
			grp = c
		}
	}
	if grp.DisplayName != "Pat Example, Sam Example" || grp.Title != "" || grp.Topic != "" {
		t.Errorf("group: %+v", grp)
	}
	// Members' friendly names also become people.
	found := false
	for _, p := range d.People {
		if p.ID == "8:orgid:00000000-0000-4000-8000-0000000000dd" && p.DisplayName == "Sam Example" {
			found = true
		}
	}
	if !found {
		t.Errorf("member person missing")
	}
	// Older string-form chatTitle still works and chat titles fall back to the topic.
	c, _, err := MapConversation(acct1, obj("id", "19:a@thread.v2", "type", "Chat", "chatTitle", "Legacy title"))
	if err != nil || c.Title != "Legacy title" || c.DisplayName != "Legacy title" {
		t.Errorf("string chatTitle: %+v %v", c, err)
	}
	c, _, err = MapConversation(acct1, obj("id", "19:a@thread.v2", "type", "Chat", "chatTitle", obj("shortTitle", "", "longTitle", "Long"), "threadProperties", obj("topic", "Topic text")))
	if err != nil || c.DisplayName != "Topic text" || c.Title != "Long" {
		t.Errorf("topic fallback: %+v %v", c, err)
	}
	c, _, err = MapConversation(acct1, obj("id", "19:a@thread.tacv2", "type", "Topic", "lastMessageTimeUtc", "2023-11-14T22:13:20.000Z", "teamId", "19:t@thread.tacv2", "threadProperties", obj("topicThreadTopic", "Chan", "spaceThreadTopic", "Team")))
	if err != nil || c.DisplayName != "Chan" || c.Topic != "Chan" || !c.LastMessageAt.Equal(time.UnixMilli(1700000000000)) {
		t.Errorf("topicThreadTopic/string time: %+v %v", c, err)
	}
	c, _, err = MapConversation(acct1, obj("id", "19:a@thread.v2", "type", "Space", "threadProperties", obj("topic", "T", "spaceThreadTopic", "Team name")))
	if err != nil || c.Topic != "Team name" {
		t.Errorf("space prefers spaceThreadTopic: %+v %v", c, err)
	}
	// Tiny lastMessageTimeUtc values in the real cache are not times.
	c, _, _ = MapConversation(acct1, obj("id", "19:a@thread.v2", "type", "Chat", "lastMessageTimeUtc", float64(0)))
	if !c.LastMessageAt.IsZero() {
		t.Errorf("zero time mapped to %v", c.LastMessageAt)
	}
	if !json.Valid(chat.Raw) {
		t.Errorf("Raw not JSON")
	}
}

func TestReadHorizon(t *testing.T) {
	d := mapFixture(t, acct1)
	chat := d.conv(t, "Fixture chat 1")
	if !chat.ReadHorizonAt.Equal(time.UnixMilli(1700000004000)) || chat.ReadHorizonClientMessageID != "1700000004000" {
		t.Errorf("chat horizon = %v %q", chat.ReadHorizonAt, chat.ReadHorizonClientMessageID)
	}
	zero := d.conv(t, "Planning")
	if !zero.ReadHorizonAt.IsZero() || zero.ReadHorizonClientMessageID != "" {
		t.Errorf("0;0;0 horizon = %v %q", zero.ReadHorizonAt, zero.ReadHorizonClientMessageID)
	}
	if none := d.conv(t, "Fixture team 1"); !none.ReadHorizonAt.IsZero() {
		t.Errorf("no horizon = %v", none.ReadHorizonAt)
	}
	// Some chat messages sent by someone else are newer than the horizon, and some are older:
	// that split is what "unread" is computed from.
	newer, older := 0, 0
	for _, m := range d.Messages {
		if m.ConversationID != chat.ID || m.SenderID == me1 {
			continue
		}
		if m.SentAt.After(chat.ReadHorizonAt) {
			newer++
		} else {
			older++
		}
	}
	if newer < 3 {
		t.Errorf("only %d fixture messages newer than the horizon (older %d)", newer, older)
	}
	for in, want := range map[string]struct {
		at time.Time
		id string
	}{
		"1635795029397;1635797011290;6482993173580615241": {time.UnixMilli(1635795029397), "6482993173580615241"},
		"0;0;0":                        {time.Time{}, ""},
		"":                             {time.Time{}, ""},
		"junk":                         {time.Time{}, ""},
		"1700000004000;1700000009000;": {time.UnixMilli(1700000004000), ""},
	} {
		c, _, err := MapConversation(acct1, obj("id", "19:a@thread.v2", "type", "Chat", "properties", obj("consumptionhorizon", in)))
		if err != nil || !c.ReadHorizonAt.Equal(want.at) || c.ReadHorizonClientMessageID != want.id {
			t.Errorf("horizon %q = %v %q err %v, want %v %q", in, c.ReadHorizonAt, c.ReadHorizonClientMessageID, err, want.at, want.id)
		}
	}
}

func TestMapActivityFields(t *testing.T) {
	d := mapFixture(t, acct1)
	if len(d.Activity) != 11 {
		t.Fatalf("activity items = %d", len(d.Activity))
	}
	read, unread := 0, 0
	types := map[string]bool{}
	var graph Activity
	for _, a := range d.Activity {
		if a.TenantID != tenant1 || a.UserID != user1 || a.ID == "" || a.At.IsZero() || len(a.Raw) == 0 {
			t.Errorf("activity incomplete: %+v", a)
		}
		if a.IsRead {
			read++
		} else {
			unread++
		}
		types[a.Type] = true
		if a.Type == "msGraph" {
			graph = a
		}
	}
	if read < 2 || unread < 2 || len(types) < 5 || graph.AppID != "fixture-app-id" || graph.ConversationID != "" {
		t.Errorf("read=%d unread=%d types=%v graph=%+v", read, unread, types, graph)
	}
	for _, a := range d.Activity {
		if a.Type == "reply" && (a.Subtype != "channel" || a.ConversationID == "" || a.MessageID == "" || a.ReplyChainID == "" || a.ReplyChainID == a.MessageID) {
			t.Errorf("reply activity: %+v", a)
		}
	}
	// messageId stands in for a missing sourceMessageId.
	a, err := MapActivity(acct1, obj("activityId", "x", "activityType", "mention", "messageId", "m1", "timestamp", float64(1700000000000), "isRead", true))
	if err != nil || a.MessageID != "m1" || !a.IsRead || !a.At.Equal(time.UnixMilli(1700000000000)) {
		t.Errorf("messageId fallback: %+v %v", a, err)
	}
}

func TestDeepLink(t *testing.T) {
	const tenant = "tenant-guid"
	cases := []struct {
		name                      string
		conv, msg, parent, tenant string
		channel                   bool
		want                      string
	}{
		{"chat", "19:abc_def@unq.gbl.spaces", "1700000001000", "", tenant, false,
			"https://teams.microsoft.com/l/message/19:abc_def@unq.gbl.spaces/1700000001000?tenantId=tenant-guid&context=%7B%22contextType%22%3A%22chat%22%7D"},
		{"group chat with the same parent as id", "19:g@thread.v2", "17", "17", tenant, false,
			"https://teams.microsoft.com/l/message/19:g@thread.v2/17?tenantId=tenant-guid&context=%7B%22contextType%22%3A%22chat%22%7D"},
		{"channel root", "19:chan@thread.tacv2", "1700000002000", "1700000002000", tenant, true,
			"https://teams.microsoft.com/l/message/19:chan@thread.tacv2/1700000002000?tenantId=tenant-guid&context=%7B%22contextType%22%3A%22channel%22%7D"},
		{"channel reply", "19:chan@thread.tacv2", "1700000003000", "1700000002000", tenant, true,
			"https://teams.microsoft.com/l/message/19:chan@thread.tacv2/1700000003000?tenantId=tenant-guid&parentMessageId=1700000002000&context=%7B%22contextType%22%3A%22channel%22%7D"},
		{"no tenant", "19:g@thread.v2", "5", "", "", false,
			"https://teams.microsoft.com/l/message/19:g@thread.v2/5?context=%7B%22contextType%22%3A%22chat%22%7D"},
		{"ids needing escapes", "19:a/b c@thread.v2", "5?6", "", tenant, false,
			"https://teams.microsoft.com/l/message/19:a%2Fb%20c@thread.v2/5%3F6?tenantId=tenant-guid&context=%7B%22contextType%22%3A%22chat%22%7D"},
		{"empty ids give no link", "", "5", "", tenant, false, ""},
		{"empty message id gives no link", "19:g@thread.v2", "", "", tenant, false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := DeepLink(c.conv, c.msg, c.parent, c.tenant, c.channel); got != c.want {
				t.Errorf("DeepLink = %q\nwant       %q", got, c.want)
			}
		})
	}
}

func TestUnmapped(t *testing.T) {
	bad := []struct {
		name string
		v    any
	}{
		{"nil", nil},
		{"string", "x"},
		{"number", float64(1)},
		{"array", []any{}},
		{"undefined", v8.Undefined{}},
		{"empty object", obj()},
	}
	for _, c := range bad {
		t.Run(c.name, func(t *testing.T) {
			var um *UnmappedError
			if _, _, err := MapReplyChain(acct1, c.v); !errors.As(err, &um) || um.Reason == "" {
				t.Errorf("MapReplyChain err = %v", err)
			}
			if _, _, err := MapConversation(acct1, c.v); !errors.As(err, &um) || um.Reason == "" {
				t.Errorf("MapConversation err = %v", err)
			}
			if _, err := MapActivity(acct1, c.v); !errors.As(err, &um) || um.Reason == "" {
				t.Errorf("MapActivity err = %v", err)
			}
		})
	}
	more := map[string]any{
		"messageMap not an object":     obj("conversationId", "19:x", "replyChainId", "1", "messageMap", "oops"),
		"messageMap empty":             obj("conversationId", "19:x", "replyChainId", "1", "messageMap", obj()),
		"message not an object":        obj("conversationId", "19:x", "replyChainId", "1", "messageMap", obj("1", "oops")),
		"message without id":           obj("conversationId", "19:x", "replyChainId", "1", "messageMap", obj("1", obj("content", "hi"))),
		"message without conversation": obj("replyChainId", "1", "messageMap", obj("1", obj("id", "1"))),
		"numeric id":                   obj("conversationId", "19:x", "replyChainId", "1", "messageMap", obj("1", obj("id", []any{}))),
	}
	for name, v := range more {
		var um *UnmappedError
		if _, _, err := MapReplyChain(acct1, v); !errors.As(err, &um) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	var um *UnmappedError
	if _, _, err := MapConversation(acct1, obj("type", "Chat")); !errors.As(err, &um) {
		t.Errorf("conversation without id: %v", err)
	}
	if _, _, err := MapConversation(acct1, obj("id", []any{})); !errors.As(err, &um) {
		t.Errorf("conversation with array id: %v", err)
	}
	if _, err := MapActivity(acct1, obj("activityType", "mention")); !errors.As(err, &um) {
		t.Errorf("activity without id: %v", err)
	}
	// Wrong-shaped optional fields degrade to empty values; they never panic or fail the record.
	weird := obj("conversationId", "19:x@thread.v2", "replyChainId", "1", "messageMap", obj("1", obj(
		"id", "1", "originalArrivalTime", "not a number", "content", float64(7), "imDisplayName", []any{}, "creator", obj(),
		"properties", obj("mentions", float64(3), "emotions", "x", "files", []any{"a", nil, float64(1)}, "links", obj(), "pinned", []any{}, "edittime", obj(), "subject", float64(1)),
		"deletionInfo", "deleted")))
	if ms, _, err := MapReplyChain(acct1, weird); err != nil || len(ms) != 1 {
		t.Errorf("weird optional fields: %+v %v", ms, err)
	}
	wc := obj("id", "19:a@thread.v2", "type", float64(1), "chatTitle", []any{}, "threadProperties", "x", "members", []any{float64(1), obj("id", float64(2)), "s"},
		"lastMessageTimeUtc", obj(), "properties", obj("consumptionhorizon", float64(5), "favorite", obj()))
	if _, _, err := MapConversation(acct1, wc); err != nil {
		t.Errorf("weird conversation: %v", err)
	}
	if _, err := MapActivity(acct1, obj("activityId", "a", "timestamp", "soon", "isRead", "yes")); err != nil {
		t.Errorf("weird activity: %v", err)
	}
}

func TestChannelReplyLinkFallsBackToReplyChain(t *testing.T) {
	const conv = "19:chan@thread.tacv2"
	rec := obj("conversationId", conv, "replyChainId", "100", "messageMap", obj(
		"100", obj("id", "100", "originalArrivalTime", float64(1)),
		"101", obj("id", "101", "originalArrivalTime", float64(2)), // a reply with no parentMessageId
	))
	ms, _, err := MapReplyChain(acct1, rec)
	if err != nil || len(ms) != 2 {
		t.Fatalf("%v %v", ms, err)
	}
	if want := DeepLink(conv, "100", "", tenant1, true); ms[0].Link != want {
		t.Errorf("root link = %q want %q (the chain id equals its own id, no parent)", ms[0].Link, want)
	}
	if want := DeepLink(conv, "101", "100", tenant1, true); ms[1].Link != want || !strings.Contains(ms[1].Link, "parentMessageId=100") {
		t.Errorf("reply link = %q want %q", ms[1].Link, want)
	}
	// A chat reply with no parent keeps the plain chat link.
	chat := obj("conversationId", "19:g@thread.v2", "replyChainId", "5", "messageMap", obj("6", obj("id", "6", "originalArrivalTime", float64(2))))
	ms, _, _ = MapReplyChain(acct1, chat)
	if want := DeepLink("19:g@thread.v2", "6", "", tenant1, false); ms[0].Link != want {
		t.Errorf("chat link = %q want %q", ms[0].Link, want)
	}
}

func TestIsChannelID(t *testing.T) {
	for id, want := range map[string]bool{
		"19:a@thread.tacv2": true, "19:a@thread.skype": true,
		"19:a@thread.v2": false, "19:a_b@unq.gbl.spaces": false, "": false, "@thread.tacv2x": false,
	} {
		if got := isChannelID(id); got != want {
			t.Errorf("isChannelID(%q) = %v, want %v", id, got, want)
		}
	}
}

func TestMapMessageChannelLinks(t *testing.T) {
	for _, suffix := range []string{"@thread.tacv2", "@thread.skype"} {
		conv := "19:chan" + suffix
		rec := obj("conversationId", conv, "replyChainId", "100", "messageMap", obj(
			"a", obj("id", "100", "parentMessageId", "100", "originalArrivalTime", float64(1)),
			"b", obj("id", "200", "parentMessageId", "100", "originalArrivalTime", float64(2)),
		))
		ms, _, err := MapReplyChain(acct1, rec)
		if err != nil || len(ms) != 2 {
			t.Fatalf("%s: %v %v", suffix, ms, err)
		}
		root, reply := ms[0].Link, ms[1].Link
		if !strings.Contains(root, "%22channel%22") || strings.Contains(root, "parentMessageId") {
			t.Errorf("%s root link: %s", suffix, root)
		}
		if !strings.Contains(reply, "%22channel%22") || !strings.Contains(reply, "parentMessageId=100") {
			t.Errorf("%s reply link: %s", suffix, reply)
		}
	}
}
