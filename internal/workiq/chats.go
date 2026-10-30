package workiq

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/ourostack/m365crawl/internal/outlookmail"
	"github.com/ourostack/m365crawl/internal/teamsdesktop"
)

type MessageClass string

const (
	ClassChat         MessageClass = "chat_message"
	ClassChannelRoot  MessageClass = "channel_root"
	ClassChannelReply MessageClass = "channel_reply"
)

// IDQualified is a code-owned native qualification, never a user/configuration switch.
func IDQualified(MessageClass) bool { return false }

type Chat struct {
	ID, TenantID, Type, Topic string
	Raw                       json.RawMessage
}

type ChatMessage struct {
	Native     teamsdesktop.Message
	Class      MessageClass
	ModifiedAt time.Time
	Raw        json.RawMessage
	Removed    bool
}

type MessageHeader struct {
	ID, MessageType, ReplyToID string
	CreatedAt                  *time.Time
	Removed                    bool
	// Version is the exact server created and modified clocks, so a repeated ID can be told apart
	// from Graph repeating an unchanged message on the next page.
	Version string
}

func DecodeMessageHeader(ctx context.Context, raw json.RawMessage) (MessageHeader, error) {
	var wire struct {
		ID, MessageType, ReplyToID, CreatedDateTime, LastModifiedDateTime string
		Removed                                                           json.RawMessage `json:"@removed"`
	}
	if _, err := decodeEntity(ctx, raw, &wire); err != nil {
		return MessageHeader{}, err
	}
	if wire.ID == "" || len(wire.ID) > maxPath {
		return MessageHeader{}, failure("workiq_protocol")
	}
	created, err := graphTime(wire.CreatedDateTime)
	if err != nil {
		return MessageHeader{}, err
	}
	return MessageHeader{ID: wire.ID, MessageType: wire.MessageType, ReplyToID: wire.ReplyToID, CreatedAt: created,
		Removed: len(wire.Removed) > 0 && string(wire.Removed) != "null", Version: wire.CreatedDateTime + "\x00" + wire.LastModifiedDateTime}, nil
}

func DecodeChat(ctx context.Context, raw json.RawMessage) (Chat, error) {
	var wire struct{ ID, TenantID, ChatType, Topic string }
	safe, err := decodeEntity(ctx, raw, &wire)
	if err != nil {
		return Chat{}, err
	}
	if wire.ID == "" || len(wire.ID) > maxPath {
		return Chat{}, failure("workiq_protocol")
	}
	return Chat{ID: wire.ID, TenantID: wire.TenantID, Type: wire.ChatType, Topic: wire.Topic, Raw: safe}, nil
}

func DecodeChatMessage(ctx context.Context, account AccountFact, chat Chat, raw json.RawMessage) (ChatMessage, error) {
	if account.Family != "chats" || account.UserID == "" || account.TenantID == "" || chat.TenantID != account.TenantID {
		return ChatMessage{}, failure("workiq_chat_account_unmatched")
	}
	var wire struct {
		ID, MessageType, ReplyToID, WebURL, Subject, Importance                    string
		CreatedDateTime, LastModifiedDateTime, DeletedDateTime, LastEditedDateTime string
		Body                                                                       *struct{ ContentType, Content string }
		From                                                                       struct {
			User struct{ ID, DisplayName string }
		}
		Mentions []struct {
			MentionText string
			Mentioned   struct {
				User struct{ ID, DisplayName string }
			}
		}
		Removed json.RawMessage `json:"@removed"`
	}
	safe, err := decodeEntity(ctx, raw, &wire)
	if err != nil {
		return ChatMessage{}, err
	}
	if wire.ID == "" || chat.ID == "" || len(wire.ID) > maxPath {
		return ChatMessage{}, failure("workiq_protocol")
	}
	result := ChatMessage{Raw: safe, Class: ClassChat, Removed: len(wire.Removed) > 0 && string(wire.Removed) != "null"}
	if chat.Type == "channel" {
		result.Class = ClassChannelRoot
		if wire.ReplyToID != "" {
			result.Class = ClassChannelReply
		}
	}
	m := &result.Native
	m.TenantID, m.UserID, m.ConversationID, m.ID = account.TenantID, account.UserID, chat.ID, wire.ID
	m.ParentMessageID, m.ReplyChainID, m.Link = wire.ReplyToID, wire.ReplyToID, wire.WebURL
	if m.ReplyChainID == "" {
		m.ReplyChainID = m.ID
	}
	if result.Removed {
		return result, nil
	}
	created, err := graphTime(wire.CreatedDateTime)
	if err != nil {
		return ChatMessage{}, err
	}
	modified, err := graphTime(wire.LastModifiedDateTime)
	if err != nil {
		return ChatMessage{}, err
	}
	deleted, err := graphTime(wire.DeletedDateTime)
	if err != nil {
		return ChatMessage{}, err
	}
	edited, err := graphTime(wire.LastEditedDateTime)
	if err != nil {
		return ChatMessage{}, err
	}
	if modified == nil || created == nil && deleted == nil {
		return ChatMessage{}, failure("workiq_protocol")
	}
	if created != nil {
		m.SentAt = *created
	}
	if deleted != nil {
		m.DeletedAt = *deleted
	}
	if edited != nil {
		m.EditedAt = *edited
	}
	result.ModifiedAt = *modified
	// RFC3339's four-digit year range fits int64 milliseconds. The source clock above retains
	// its full precision; only the legacy numeric projection truncates sub-millisecond digits.
	m.Version = modified.UnixMilli()
	m.Subject, m.Importance = wire.Subject, wire.Importance
	if wire.From.User.ID != "" {
		m.SenderID, m.SenderName = "8:orgid:"+wire.From.User.ID, wire.From.User.DisplayName
	}
	m.MessageType, m.ContentType = "graph:"+wire.MessageType, "unknown"
	if wire.Body != nil {
		switch strings.ToLower(wire.Body.ContentType) {
		case "html":
			m.ContentType, m.ContentHTML, m.ContentText = "Html", wire.Body.Content, outlookmail.HTMLText([]byte(wire.Body.Content))
			if wire.MessageType == "message" {
				m.MessageType = "RichText/Html"
			}
		case "text":
			m.ContentType, m.ContentText = "Text", wire.Body.Content
			if wire.MessageType == "message" {
				m.MessageType = "Text"
			}
		}
	}
	if wire.Mentions != nil {
		m.Mentions = []teamsdesktop.Mention{}
	}
	for _, mention := range wire.Mentions {
		if mention.Mentioned.User.ID == "" {
			continue
		}
		name := mention.Mentioned.User.DisplayName
		if name == "" {
			name = mention.MentionText
		}
		id := "8:orgid:" + mention.Mentioned.User.ID
		m.Mentions = append(m.Mentions, teamsdesktop.Mention{ID: id, DisplayName: name})
		m.MentionsMe = m.MentionsMe || mention.Mentioned.User.ID == account.UserID
	}
	return result, nil
}
