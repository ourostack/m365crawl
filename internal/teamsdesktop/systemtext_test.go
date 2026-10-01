package teamsdesktop

import (
	"strings"
	"testing"

	"github.com/ourostack/teamscrawl/internal/v8"
)

// mapOne maps one synthetic message (extra fields appended to the id and conversation) and
// returns it.
func mapOne(t *testing.T, kv ...any) Message {
	t.Helper()
	base := []any{"id", "100", "conversationId", "19:c@thread.v2", "originalArrivalTime", float64(1700000000000)}
	m := obj(append(base, kv...)...)
	rec := obj("conversationId", "19:c@thread.v2", "replyChainId", "100", "messageMap", obj("100", m))
	ms, _, err := MapReplyChain(acct1, rec)
	if err != nil || len(ms) != 1 {
		t.Fatalf("MapReplyChain: %v %v", ms, err)
	}
	return ms[0]
}

func TestCardTextAdaptive(t *testing.T) {
	adaptive := `{"type":"AdaptiveCard","body":[
	  {"type":"TextBlock","text":"Build failed"},
	  {"type":"Container","items":[{"type":"TextBlock","text":"main branch"},{"type":"FactSet","facts":[{"title":"Owner","value":"Ana"},{"title":"Run","value":"42"}]}]},
	  {"type":"ColumnSet","columns":[{"items":[{"type":"TextBlock","text":"left"}]},{"items":[{"type":"RichTextBlock","inlines":[{"type":"TextRun","text":"right "},{"type":"TextRun","text":"side"}]}]}]},
	  {"type":"Image","url":"https://example.invalid/x.png"},
	  {"type":"TextBlock","text":""}],
	 "actions":[{"type":"Action.OpenUrl","title":"Open","url":"https://example.invalid"}]}`
	cards := `[{"contentType":"application/vnd.microsoft.card.adaptive","content":` + adaptive + `}]`
	want := "Build failed\nmain branch\nOwner: Ana\nRun: 42\nleft\nright side"

	// cards stored as a JSON string, content as an object.
	m := mapOne(t, "messageType", "RichText/Html", "content", `<attachment id="c1"></attachment>`, "properties", obj("cards", cards))
	if m.ContentText != want {
		t.Errorf("string cards: %q, want %q", m.ContentText, want)
	}
	// raw payload is untouched and still holds the card JSON.
	if !strings.Contains(string(m.Raw), "Build failed") {
		t.Errorf("raw lost the card: %s", m.Raw)
	}
	// content stored as a JSON string inside a native array.
	m = mapOne(t, "messageType", "RichText/Html", "content", "", "properties", obj("cards", []any{obj("contentType", "application/vnd.microsoft.card.adaptive", "content", adaptive)}))
	if m.ContentText != want {
		t.Errorf("string content: %q", m.ContentText)
	}
	// Message text and card text are both kept, message first; a card repeating the message adds nothing.
	m = mapOne(t, "messageType", "RichText/Html", "content", "<p>See card</p>", "properties", obj("cards", cards))
	if m.ContentText != "See card\n"+want {
		t.Errorf("text plus card: %q", m.ContentText)
	}
	m = mapOne(t, "messageType", "RichText/Html", "content", "<p>"+"Build failed</p>", "properties", obj("cards", `[{"contentType":"application/vnd.microsoft.card.adaptive","content":{"type":"AdaptiveCard","body":[{"type":"TextBlock","text":"Build failed"}]}}]`))
	if m.ContentText != "Build failed" {
		t.Errorf("repeated text: %q", m.ContentText)
	}
}

func TestCardTextOtherKinds(t *testing.T) {
	hero := `{"title":"Hero title","subtitle":"Hero sub","text":"Hero body","buttons":[{"title":"Go"}]}`
	conn := `{"title":"Conn title","summary":"Conn summary","themeColor":"0072C6","sections":[{"activityTitle":"Act","activitySubtitle":"Sub","text":"Section text","facts":[{"name":"Env","value":"prod"}]}]}`
	cases := []struct{ name, cards, want string }{
		{"hero", `[{"contentType":"application/vnd.microsoft.card.hero","content":` + hero + `}]`, "Hero title\nHero sub\nHero body"},
		{"thumbnail", `[{"contentType":"application/vnd.microsoft.card.thumbnail","content":{"title":"T"}}]`, "T"},
		{"connector", `[{"contentType":"application/vnd.microsoft.teams.card.o365connector","content":` + conn + `}]`, "Conn title\nConn summary\nAct\nSub\nSection text\nEnv: prod"},
		{"two cards", `[{"contentType":"application/vnd.microsoft.card.hero","content":{"title":"One"}},{"contentType":"application/vnd.microsoft.card.hero","content":{"title":"Two"}}]`, "One\nTwo"},
		{"no text card", `[{"contentType":"application/vnd.microsoft.card.fluidEmbedCard","content":{"componentUrl":"https://example.invalid","sourceType":"x"}}]`, ""},
		{"banner", `[{"contentType":"application/vnd.microsoft.teams.messaging-announcementBanner","content":{"colorTheme":"red"}}]`, ""},
		{"content as string", `[{"contentType":"application/vnd.microsoft.card.hero","content":"{\"title\":\"Str\"}"}]`, "Str"},
		{"content not an object", `[{"contentType":"x","content":5},"junk",5]`, ""},
		{"empty list", `[]`, ""},
		{"not json", `not json`, ""},
		{"facts without names", `[{"contentType":"application/vnd.microsoft.card.adaptive","content":{"body":[{"type":"FactSet","facts":[{"title":"","value":"only value"},{"title":"only title","value":""},{"title":"","value":""},"x"]}]}}]`, "only value\nonly title"},
	}
	for _, c := range cases {
		m := mapOne(t, "messageType", "RichText/Html", "content", "", "properties", obj("cards", c.cards))
		if m.ContentText != c.want {
			t.Errorf("%s: %q, want %q", c.name, m.ContentText, c.want)
		}
	}
	// A message with no cards property is unaffected.
	if m := mapOne(t, "messageType", "RichText/Html", "content", "<p>plain</p>"); m.ContentText != "plain" {
		t.Errorf("plain: %q", m.ContentText)
	}
}

func TestCallEventText(t *testing.T) {
	ended := func(dur string) string {
		return `<ended/><partlist alt="" count="2"><part identity="8:orgid:a"><name>x</name><duration>` + dur + `</duration></part><part identity="8:orgid:b"><name>y</name><duration>60</duration></part></partlist>`
	}
	meeting := `<meetingDetails><meetingDetails><meetingType>scheduled</meetingType></meetingDetails></meetingDetails>`
	cases := []struct{ name, content, want string }{
		{"call ended with duration", ended("1380"), "Call ended · 23m"},
		{"hours", ended("3900"), "Call ended · 1h 5m"},
		{"whole hours", ended("7200"), "Call ended · 2h"},
		{"longest part wins", ended("45"), "Call ended · 1m"},
		{"under a minute", `<ended/><partlist><part><duration>45</duration></part></partlist>`, "Call ended · 45s"},
		{"no duration", `<ended/><partlist type="ended" alt=""><part identity="x"><name>n</name></part></partlist>`, "Call ended"},
		{"zero duration", `<ended/><partlist><part><duration>0</duration></part></partlist>`, "Call ended"},
		{"bad duration", `<ended/><partlist><part><duration>abc</duration></part></partlist>`, "Call ended"},
		{"meeting ended", ended("600") + meeting, "Meeting ended · 10m"},
		{"callEnded event", `<partlist alt=""></partlist><callEventType>callEnded</callEventType>`, "Call ended"},
		{"call started", `<partlist alt="" count="1"><part identity="x"></part></partlist><callEventType>callStarted</callEventType>`, "Call started"},
		{"meeting started", `<partlist alt=""></partlist>` + meeting + `<callEventType>callStarted</callEventType>`, "Meeting started"},
		{"no event type", `<partlist alt=""></partlist>`, "Call started"},
		{"empty content", ``, "Call event"},
	}
	for _, c := range cases {
		m := mapOne(t, "messageType", "Event/Call", "content", c.content)
		if m.ContentText != c.want {
			t.Errorf("%s: %q, want %q", c.name, m.ContentText, c.want)
		}
		if m.ContentHTML != c.content {
			t.Errorf("%s: html changed", c.name)
		}
	}
}

func TestCallMediaMetadataText(t *testing.T) {
	meta := `{"CallId":"x","ContentTypes":"y","MessageId":"1","ThreadId":"19:c@thread.v2","UserParticipantId":"u"}`
	for _, c := range []struct{ typ, content, want string }{
		{"RichText/Media_CallTranscript", meta, "Call transcript available"},
		{"RichText/Media_CallLogTranscript", meta, "Call transcript available"},
		{"RichText/Media_CallLogRecording", meta, "Call recording available"},
		{"RichText/Media_CallRecording", meta, "Call recording available"},
		// The real cache stores the metadata with backslash-escaped quotes, which is not valid JSON.
		{"RichText/Media_CallTranscript", `{\"CallId\":\"x\",\"ThreadId\":\"y\"}`, "Call transcript available"},
		// Real recording messages carry a description in markup: that stays the text.
		{"RichText/Media_CallRecording", `<URIObject type="Video.1/Message.1"><Title>Weekly sync</Title><Description>Recording</Description></URIObject>`, "Weekly syncRecording"},
		// A transcript whose content is not JSON keeps its text.
		{"RichText/Media_CallTranscript", "<p>hello</p>", "hello"},
		// JSON-looking content of any other media type is text.
		{"RichText/Media_Other", "{x}", "{x}"},
		// A user's own JSON in an ordinary message stays text.
		{"RichText/Html", "<p>{\"a\":1}</p>", `{"a":1}`},
	} {
		m := mapOne(t, "messageType", c.typ, "content", c.content)
		if m.ContentText != c.want {
			t.Errorf("%s %.20s: %q, want %q", c.typ, c.content, m.ContentText, c.want)
		}
	}
}

func TestThreadActivityText(t *testing.T) {
	for typ, want := range map[string]string{
		"ThreadActivity/AddMember":            "Member added",
		"ThreadActivity/DeleteMember":         "Member removed",
		"ThreadActivity/MemberJoined":         "Member joined",
		"ThreadActivity/MemberLeft":           "Member left",
		"ThreadActivity/TopicUpdate":          "Topic updated",
		"ThreadActivity/TopicTopicUpdated":    "Topic updated",
		"ThreadActivity/PinnedItemsUpdate":    "Pinned items updated",
		"ThreadActivity/AddCustomApp":         "App added",
		"ThreadActivity/DeleteCustomApp":      "App removed",
		"ThreadActivity/MeetingPolicyUpdated": "Meeting policy updated",
		"ThreadActivity/SomethingNewHappened": "Thread activity: something new happened",
		"ThreadActivity/":                     "Thread activity",
	} {
		m := mapOne(t, "messageType", typ, "content", "<x/>")
		if m.ContentText != want {
			t.Errorf("%s: %q, want %q", typ, m.ContentText, want)
		}
	}
	// Control messages stay blank: they carry no event a reader needs.
	if m := mapOne(t, "messageType", "Control/Typing", "content", "<x/>"); m.ContentText != "" {
		t.Errorf("control: %q", m.ContentText)
	}
}

func TestSenderNameFromToken(t *testing.T) {
	m := mapOne(t, "messageType", "Event/Call", "content", "<ended/>", "creator", "8:orgid:x", "fromDisplayNameInToken", "Ana Token")
	if m.SenderName != "Ana Token" || m.SenderID != "8:orgid:x" {
		t.Errorf("token name: %+v", m)
	}
	// imDisplayName still wins.
	m = mapOne(t, "messageType", "Text", "content", "hi", "creator", "8:orgid:x", "imDisplayName", "Im Name", "fromDisplayNameInToken", "Ana Token")
	if m.SenderName != "Im Name" {
		t.Errorf("imDisplayName: %q", m.SenderName)
	}
}

func TestMemberListName(t *testing.T) {
	for _, c := range []struct {
		names  []string
		others int
		want   string
	}{
		{nil, 4, ""},
		{[]string{"Ana"}, 1, "Ana"},
		{[]string{"Ana", "Ben", "Chao"}, 3, "Ana, Ben, Chao"},
		{[]string{"Ana", "Ben", "Chao", "Dee", "Eli"}, 5, "Ana, Ben, Chao +2"},
		{[]string{"Ana", "Ben", "Chao", "Dee"}, 6, "Ana, Ben, Chao +3"},
		{[]string{"Ana"}, 3, "Ana +2"},
	} {
		if got := MemberListName(c.names, c.others); got != c.want {
			t.Errorf("MemberListName(%v, %d) = %q, want %q", c.names, c.others, got, c.want)
		}
	}
}

func TestUntitledChatDisplayName(t *testing.T) {
	member := func(id, name string) *v8.Object { return obj("id", id, "friendlyName", name) }
	co := obj("id", "19:g@thread.v2", "type", "Chat", "members", []any{
		member(me1, "Me Myself"), member("8:orgid:1", "Ana"), member("8:orgid:2", "Ben"), member("8:orgid:3", "Chao"),
		member("8:orgid:4", "Dee"), member("8:orgid:5", "")})
	c, _, err := MapConversation(acct1, co)
	if err != nil || c.DisplayName != "Ana, Ben, Chao +2" || len(c.Members) != 6 {
		t.Errorf("group: %q %v %v", c.DisplayName, c.Members, err)
	}
	// A title or topic still wins over member names.
	co = obj("id", "19:g@thread.v2", "type", "Chat", "chatTitle", obj("shortTitle", "Named"), "members", []any{member("8:orgid:1", "Ana")})
	if c, _, _ := MapConversation(acct1, co); c.DisplayName != "Named" {
		t.Errorf("titled: %q", c.DisplayName)
	}
}
