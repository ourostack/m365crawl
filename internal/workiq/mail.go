package workiq

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/ourostack/m365crawl/internal/outlookmail"
)

type MailAddress struct{ Name, Address string }
type Folder struct {
	ID, ParentID, Name, WellKnown string
	ChildCount, TotalCount        *int64
	Hidden                        *bool
	Raw                           json.RawMessage
}

func DecodeFolder(ctx context.Context, raw json.RawMessage) (Folder, error) {
	var wire struct {
		ID, ParentFolderID, DisplayName  string
		ChildFolderCount, TotalItemCount *int64
		IsHidden                         *bool
	}
	safe, err := decodeEntity(ctx, raw, &wire)
	if err != nil {
		return Folder{}, err
	}
	if wire.ID == "" || len(wire.ID) > maxPath || wire.ChildFolderCount != nil && *wire.ChildFolderCount < 0 ||
		wire.TotalItemCount != nil && *wire.TotalItemCount < 0 {
		return Folder{}, failure("workiq_protocol")
	}
	// Only the runner's fixed well-known-ID lookups can assign WellKnown, never this payload.
	return Folder{ID: wire.ID, ParentID: wire.ParentFolderID, Name: wire.DisplayName, ChildCount: wire.ChildFolderCount,
		TotalCount: wire.TotalItemCount, Hidden: wire.IsHidden, Raw: safe}, nil
}

type MailAttachment struct {
	ID, Name, ContentType string
	Size                  int64
	Inline                bool
}

type MailMessage struct {
	ID, FolderID, ConversationID, InternetMessageID string
	Subject, Preview, InReplyTo, ICalUID            string
	Sender                                          MailAddress
	To, Cc, Bcc                                     []MailAddress
	SentAt, ReceivedAt, ModifiedAt, DeletedAt       *time.Time
	IsRead, HasAttachments                          *bool
	Importance, Flag, BodyType, Body                string
	BodyPresent, Removed                            bool
	Attachments                                     []MailAttachment
	Raw                                             json.RawMessage
}

type graphAddress struct {
	EmailAddress MailAddress `json:"emailAddress"`
}

func DecodeMail(ctx context.Context, raw json.RawMessage) (MailMessage, error) {
	var wire struct {
		ID, ParentFolderID, ConversationID, InternetMessageID string
		Subject, BodyPreview, InReplyTo, ICalUID              string
		SentDateTime, ReceivedDateTime, LastModifiedDateTime  string
		DeletedDateTime                                       string
		From, Sender                                          graphAddress
		ToRecipients, CcRecipients, BccRecipients             []graphAddress
		IsRead, HasAttachments                                *bool
		Importance                                            string
		Flag                                                  struct{ FlagStatus string }
		Body                                                  *struct{ ContentType, Content string }
		Removed                                               json.RawMessage `json:"@removed"`
		Attachments                                           []struct {
			ID, Name, ContentType string
			Size                  int64
			IsInline              bool
		}
	}
	safe, err := decodeEntity(ctx, raw, &wire)
	if err != nil {
		return MailMessage{}, err
	}
	if wire.ID == "" || len(wire.ID) > maxPath {
		return MailMessage{}, failure("workiq_protocol")
	}
	m := MailMessage{ID: wire.ID, FolderID: wire.ParentFolderID, ConversationID: wire.ConversationID,
		InternetMessageID: wire.InternetMessageID, Subject: wire.Subject, Preview: wire.BodyPreview,
		InReplyTo: wire.InReplyTo, ICalUID: wire.ICalUID, Sender: wire.From.EmailAddress,
		IsRead: wire.IsRead, HasAttachments: wire.HasAttachments, Raw: safe}
	if m.Sender.Address == "" {
		m.Sender = wire.Sender.EmailAddress
	}
	for _, field := range []struct {
		raw  string
		into **time.Time
	}{
		{wire.SentDateTime, &m.SentAt}, {wire.ReceivedDateTime, &m.ReceivedAt},
		{wire.LastModifiedDateTime, &m.ModifiedAt}, {wire.DeletedDateTime, &m.DeletedAt},
	} {
		*field.into, err = graphTime(field.raw)
		if err != nil {
			return MailMessage{}, err
		}
	}
	for _, group := range []struct {
		source []graphAddress
		into   *[]MailAddress
	}{{wire.ToRecipients, &m.To}, {wire.CcRecipients, &m.Cc}, {wire.BccRecipients, &m.Bcc}} {
		if group.source != nil {
			*group.into = make([]MailAddress, 0, len(group.source))
		}
		for _, address := range group.source {
			*group.into = append(*group.into, address.EmailAddress)
		}
	}
	switch wire.Importance {
	case "low", "normal", "high":
		m.Importance = wire.Importance
	default:
		m.Importance = "unknown"
	}
	switch wire.Flag.FlagStatus {
	case "flagged", "complete":
		m.Flag = wire.Flag.FlagStatus
	case "notFlagged":
		m.Flag = "none"
	default:
		m.Flag = "unknown"
	}
	if wire.Body != nil {
		m.BodyPresent, m.BodyType, m.Body = true, strings.ToLower(wire.Body.ContentType), wire.Body.Content
	}
	m.Removed = len(wire.Removed) > 0 && string(wire.Removed) != "null"
	if wire.Attachments != nil {
		m.Attachments = make([]MailAttachment, 0, len(wire.Attachments))
	}
	for _, attachment := range wire.Attachments {
		if attachment.ID == "" || attachment.Size < 0 {
			return MailMessage{}, failure("workiq_protocol")
		}
		m.Attachments = append(m.Attachments, MailAttachment{ID: attachment.ID, Name: attachment.Name,
			ContentType: attachment.ContentType, Size: attachment.Size, Inline: attachment.IsInline})
	}
	return m, nil
}

// NormalizeMessageID changes comparison values only, never the retained original header.
func NormalizeMessageID(raw string) string {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "<") && strings.HasSuffix(raw, ">") {
		raw = strings.TrimSpace(raw[1 : len(raw)-1])
	}
	return strings.ToLower(raw)
}

func MailCorroborates(a, b MailMessage) bool {
	id := NormalizeMessageID(a.InternetMessageID)
	sender := strings.TrimSpace(a.Sender.Address)
	if id == "" || id != NormalizeMessageID(b.InternetMessageID) || sender == "" ||
		!strings.EqualFold(sender, strings.TrimSpace(b.Sender.Address)) || a.SentAt == nil || b.SentAt == nil ||
		a.SentAt.IsZero() || b.SentAt.IsZero() ||
		!strings.EqualFold(outlookmail.NormalizeSubject(a.Subject), outlookmail.NormalizeSubject(b.Subject)) {
		return false
	}
	delta := a.SentAt.Sub(*b.SentAt)
	return delta >= -5*time.Second && delta <= 5*time.Second
}
