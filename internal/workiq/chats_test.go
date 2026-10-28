package workiq

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestWorkIQChatMappingAndUnverifiedClasses(t *testing.T) {
	ctx := context.Background()
	chat, err := DecodeChat(ctx, []byte(`{"id":"chat-one","tenantId":"guest-tenant","chatType":"group","topic":"Fixture","@microsoft.graph.downloadUrl":"private-download"}`))
	if err != nil || chat.ID != "chat-one" || chat.TenantID != "guest-tenant" || chat.Type != "group" || strings.Contains(string(chat.Raw), "private-download") {
		t.Fatal("chat mapper lost actual tenant identity or retained an unsafe source field")
	}
	account := AccountFact{ID: "guest-tenant/owner", Family: "chats", TenantID: "guest-tenant", UserID: "owner"}
	raw := []byte(`{"id":"message-one","messageType":"message","replyToId":"parent-one","webUrl":"https://teams.example.invalid/link",
"createdDateTime":"2026-10-10T12:00:00Z","lastModifiedDateTime":"2026-10-10T12:00:01.1234567Z","lastEditedDateTime":"2026-10-10T12:00:01Z",
"body":{"contentType":"html","content":"<p>retained text</p>"},"from":{"user":{"id":"sender-one","displayName":"Fixture Sender"}},
"mentions":[{"mentionText":"Owner","mentioned":{"user":{"id":"owner","displayName":"Owner"}}}]}`)
	m, err := DecodeChatMessage(ctx, account, chat, raw)
	if err != nil || m.Native.ID != "message-one" || m.Native.TenantID != "guest-tenant" || m.Native.UserID != "owner" ||
		m.Native.ConversationID != "chat-one" || m.Native.MessageType != "RichText/Html" || m.Native.ContentType != "Html" ||
		m.Native.ContentText != "retained text" || m.Native.SenderID != "8:orgid:sender-one" || m.Native.ParentMessageID != "parent-one" ||
		m.Native.ReplyChainID != "parent-one" || len(m.Native.Mentions) != 1 || !m.Native.MentionsMe || m.Native.Link == "" ||
		m.ModifiedAt.Nanosecond() != 123456700 || m.Native.Version != m.ModifiedAt.UnixMilli() || m.Native.EditedAt.IsZero() || len(m.Native.Raw) != 0 || len(m.Raw) == 0 {
		t.Fatal("qualified Graph message projection lost mapping or put server JSON into cache Raw")
	}
	if m.Class != ClassChat {
		t.Fatal("own-chat message was mistaken for a channel class")
	}
	for _, class := range []MessageClass{ClassChat, ClassChannelRoot, ClassChannelReply, "unknown"} {
		if IDQualified(class) {
			t.Fatal("synthetic mapping enabled an unverified production identity class")
		}
	}

	for _, tenant := range []string{"", "home-tenant", "unmatched-tenant"} {
		other := chat
		other.TenantID = tenant
		_, err := DecodeChatMessage(ctx, account, other, raw)
		requireCode(t, err, "workiq_chat_account_unmatched")
	}
}

func TestWorkIQChatMappingVariants(t *testing.T) {
	ctx := context.Background()
	account := AccountFact{Family: "chats", TenantID: "tenant", UserID: "owner"}
	for _, tc := range []struct {
		chatType, reply, bodyType, messageType, expectedType string
		class                                                MessageClass
	}{
		{"group", "", "text", "message", "Text", ClassChat},
		{"channel", "", "text", "systemEventMessage", "graph:systemEventMessage", ClassChannelRoot},
		{"channel", "parent", "html", "message", "RichText/Html", ClassChannelReply},
		{"group", "", "unsupported", "message", "graph:message", ClassChat},
	} {
		payload := map[string]any{
			"id": "one", "replyToId": tc.reply, "messageType": tc.messageType,
			"createdDateTime": "2026-10-10T00:00:00Z", "lastModifiedDateTime": "2026-10-10T00:01:00Z",
			"body": map[string]string{"contentType": tc.bodyType, "content": "body"},
			"mentions": []any{
				map[string]any{"mentionText": "Fallback", "mentioned": map[string]any{"user": map[string]string{"id": "other"}}},
				map[string]any{"mentionText": "Not a user"},
			},
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal("synthetic message fixture could not encode")
		}
		m, err := DecodeChatMessage(ctx, account, Chat{ID: "chat", TenantID: "tenant", Type: tc.chatType}, raw)
		if err != nil || m.Class != tc.class || m.Native.MessageType != tc.expectedType || len(m.Native.Mentions) != 1 ||
			m.Native.Mentions[0].DisplayName != "Fallback" || m.Native.MentionsMe {
			t.Fatal("message class, unknown type or non-user mention was misrepresented")
		}
	}
	removed, err := DecodeChatMessage(ctx, account, Chat{ID: "chat", TenantID: "tenant"}, []byte(`{"id":"one","@removed":{"reason":"deleted"}}`))
	if err != nil || !removed.Removed || !removed.Native.DeletedAt.IsZero() {
		t.Fatal("delta removal without an explicit deletion clock became a tombstone")
	}
	deleted, err := DecodeChatMessage(ctx, account, Chat{ID: "chat", TenantID: "tenant"},
		[]byte(`{"id":"one","lastModifiedDateTime":"2026-10-10T00:01:00Z","deletedDateTime":"2026-10-10T00:01:00Z"}`))
	if err != nil || deleted.Native.DeletedAt.IsZero() || !deleted.Native.SentAt.IsZero() {
		t.Fatal("explicit deletion was lost or invented a creation clock")
	}
}

func TestWorkIQChatMappingRejectsMalformedEntities(t *testing.T) {
	ctx := context.Background()
	account := AccountFact{Family: "chats", TenantID: "tenant", UserID: "owner"}
	chat := Chat{ID: "chat", TenantID: "tenant"}
	for _, raw := range []string{
		`{`, `{}`, `{"id":"one","id":"two"}`,
		`{"id":"one","createdDateTime":"bad"}`,
		`{"id":"one","createdDateTime":"2026-10-10T00:00:00Z","lastModifiedDateTime":"bad"}`,
		`{"id":"one","createdDateTime":"2026-10-10T00:00:00Z","lastModifiedDateTime":"2026-10-10T00:01:00Z","deletedDateTime":"bad"}`,
		`{"id":"one","createdDateTime":"2026-10-10T00:00:00Z","lastModifiedDateTime":"2026-10-10T00:01:00Z","lastEditedDateTime":"bad"}`,
		`{"id":"one","createdDateTime":"2026-10-10T00:00:00Z"}`,
		`{"id":"one","lastModifiedDateTime":"2026-10-10T00:01:00Z"}`,
	} {
		_, err := DecodeChatMessage(ctx, account, chat, []byte(raw))
		requireCode(t, err, "workiq_protocol")
	}
	for _, raw := range []string{`{`, `{}`, `{"id":"` + strings.Repeat("x", maxPath+1) + `"}`} {
		_, err := DecodeChat(ctx, []byte(raw))
		requireCode(t, err, "workiq_protocol")
	}
}
