package teamsdesktop

import (
	"strings"
	"testing"

	"github.com/ourostack/teamscrawl/internal/v8"
)

func TestDeriveMessageMatchesMapper(t *testing.T) {
	cards := `[{"contentType":"application/vnd.microsoft.card.hero","content":{"title":"Hero","text":"Body"}}]`
	for name, m := range map[string]Message{
		"card":  mapOne(t, "messageType", "RichText/Html", "content", "<p>See</p>", "properties", obj("cards", cards)),
		"call":  mapOne(t, "messageType", "Event/Call", "content", "<ended/>", "fromDisplayNameInToken", "Ana Token"),
		"plain": mapOne(t, "messageType", "Text", "content", "hi", "imDisplayName", "Im"),
		"empty": mapOne(t, "messageType", "Text", "content", ""),
	} {
		text, sender, err := DeriveMessage(m.Raw)
		if err != nil || text != m.ContentText || sender != m.SenderName {
			t.Errorf("%s: derived (%q, %q, %v), mapper gave (%q, %q)", name, text, sender, err, m.ContentText, m.SenderName)
		}
	}
	for _, bad := range []string{"not json", "[1]", ""} {
		if _, _, err := DeriveMessage([]byte(bad)); err == nil {
			t.Errorf("DeriveMessage(%q) should fail", bad)
		}
	}
}

func TestDeriveConversationNameMatchesMapper(t *testing.T) {
	member := func(id, name string) *v8.Object { return obj("id", id, "friendlyName", name) }
	co := obj("id", "19:g@thread.v2", "type", "Chat", "members", []any{
		member(me1, "Me"), member("8:orgid:1", "Ana"), member("8:orgid:2", "Ben")})
	c, _, err := MapConversation(acct1, co)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DeriveConversationName(acct1.UserID, c.Raw)
	if err != nil || got != c.DisplayName || got == "" {
		t.Errorf("derived %q (%v), mapper gave %q", got, err, c.DisplayName)
	}
	for _, bad := range []string{"not json", "[1]", `{"type":"Chat"}`} {
		if _, err := DeriveConversationName(acct1.UserID, []byte(bad)); err == nil {
			t.Errorf("DeriveConversationName(%q) should fail", bad)
		}
	}
}

func TestCardDepthLimit(t *testing.T) {
	// A card nested far deeper than any real one (as a cycle in a structured clone would be) is
	// cut off instead of overflowing the stack; text within the limit is still read.
	deep := func(levels int) string {
		return strings.Repeat(`{"type":"Container","items":[`, levels) + `{"type":"TextBlock","text":"bottom"}` + strings.Repeat(`]}`, levels)
	}
	card := func(levels int) string {
		return `[{"contentType":"application/vnd.microsoft.card.adaptive","content":` + deep(levels) + `}]`
	}
	if got := cardsText(card(10)); got != "bottom" {
		t.Errorf("shallow card: %q", got)
	}
	if got := cardsText(card(200)); got != "" {
		t.Errorf("deep card should be cut off, got %q", got)
	}
}

func TestCardDedupeNeedsWholeLines(t *testing.T) {
	card := func(text string) string {
		return `[{"contentType":"application/vnd.microsoft.card.hero","content":{"title":"` + text + `"}}]`
	}
	// A card word that only occurs inside a longer message line is kept.
	m := mapOne(t, "messageType", "RichText/Html", "content", "<p>Done with the build</p>", "properties", obj("cards", card("Done")))
	if m.ContentText != "Done with the build\nDone" {
		t.Errorf("substring card dropped: %q", m.ContentText)
	}
	// A card whose lines are all whole lines of the message adds nothing.
	m = mapOne(t, "messageType", "RichText/Html", "content", "<p>Done</p><p>Next</p>", "properties", obj("cards", card("Done")))
	if m.ContentText != "Done\nNext" {
		t.Errorf("repeated card kept: %q", m.ContentText)
	}
}
