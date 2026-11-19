package workiq

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strings"
	"time"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/teamsimage"
)

type ImageMessage struct {
	ModifiedAt *time.Time
	teamID     string
	chatID     string
	messageID  string
	body       string
}

type ChatImageRef struct {
	resource  string
	teamID    string
	chatID    string
	messageID string
	imageID   string
	binding   string
}

type ImageBlob struct {
	Data        []byte
	ContentType string
	HTTPStatus  int
}

var chatImageInspect = teamsimage.InspectHTML

func (ImageMessage) String() string     { return "selected image message" }
func (m ImageMessage) GoString() string { return m.String() }
func (ChatImageRef) String() string     { return "selected native image reference" }
func (r ChatImageRef) GoString() string { return r.String() }

func DecodeSelectedImageMessage(ctx context.Context, chatID, messageID string, raw json.RawMessage) (ImageMessage, error) {
	return decodeSelectedImageMessage(ctx, "", chatID, messageID, raw)
}

// Only exact channel and root-message reads join the existing fetch allowlist;
// this does not enable channel enumeration, replies or opaque channel cursors.
func selectedImageChannelPath(u *url.URL) bool {
	if u.RawQuery != "" || u.ForceQuery {
		return false
	}
	segments := strings.Split(strings.TrimPrefix(u.EscapedPath(), "/"), "/")
	if len(segments) != 4 && len(segments) != 6 ||
		segments[0] != "teams" || segments[2] != "channels" || len(segments) == 6 && segments[4] != "messages" {
		return false
	}
	for _, segment := range segments {
		value, err := url.PathUnescape(segment)
		if err != nil || !teamsimage.ValidComponent(value) || strings.Contains(value, "%") ||
			strings.Contains(strings.ToLower(segment), "%2e") {
			return false
		}
	}
	return true
}

func DecodeSelectedChannelImageMessage(ctx context.Context, teamID, channelID, messageID string, raw json.RawMessage) (ImageMessage, error) {
	if !teamsimage.ValidComponent(teamID) || strings.Contains(teamID, "%") {
		return ImageMessage{}, imageFailure("image_message_unbindable")
	}
	return decodeSelectedImageMessage(ctx, teamID, channelID, messageID, raw)
}

func DecodeImageChannel(ctx context.Context, raw json.RawMessage) (Chat, error) {
	if err := ctx.Err(); err != nil {
		return Chat{}, err
	}
	var wire struct{ ID, TenantID, MembershipType string }
	if _, err := decodeEntity(ctx, raw, &wire); err != nil {
		return Chat{}, err
	}
	if !teamsimage.ValidComponent(wire.ID) {
		return Chat{}, failure("workiq_protocol")
	}
	if wire.MembershipType != "standard" && wire.MembershipType != "private" {
		return Chat{}, imageFailure("image_kind_unsupported")
	}
	return Chat{ID: wire.ID, TenantID: wire.TenantID}, nil
}

func decodeSelectedImageMessage(ctx context.Context, teamID, chatID, messageID string, raw json.RawMessage) (ImageMessage, error) {
	if err := ctx.Err(); err != nil {
		return ImageMessage{}, err
	}
	if !teamsimage.ValidComponent(chatID) || !teamsimage.ValidComponent(messageID) {
		return ImageMessage{}, imageFailure("image_message_unbindable")
	}
	var wire struct {
		ID, MessageType, ReplyToID string
		LastModifiedDateTime       *string
		DeletedDateTime            json.RawMessage
		Removed                    json.RawMessage `json:"@removed"`
		ChannelIdentity            json.RawMessage
		Body                       *struct{ ContentType, Content string }
	}
	if _, err := decodeEntity(ctx, raw, &wire); err != nil {
		return ImageMessage{}, err
	}
	present := func(raw json.RawMessage) bool {
		return len(raw) != 0 && string(raw) != "null"
	}
	if wire.ID != messageID || wire.MessageType != "message" || wire.ReplyToID != "" ||
		present(wire.Removed) || present(wire.DeletedDateTime) && string(wire.DeletedDateTime) != `""` ||
		teamID == "" && present(wire.ChannelIdentity) ||
		wire.Body == nil || !strings.EqualFold(wire.Body.ContentType, "html") ||
		strings.TrimSpace(wire.Body.Content) == "" || len(wire.Body.Content) > teamsimage.MaxHTMLBytes {
		return ImageMessage{}, imageFailure("image_message_unbindable")
	}
	if teamID != "" {
		var identity struct{ TeamID, ChannelID string }
		if json.Unmarshal(wire.ChannelIdentity, &identity) != nil || identity.TeamID != teamID || identity.ChannelID != chatID {
			return ImageMessage{}, imageFailure("image_message_unbindable")
		}
	}
	message := ImageMessage{teamID: teamID, chatID: chatID, messageID: messageID, body: wire.Body.Content}
	if wire.LastModifiedDateTime != nil {
		value, err := time.Parse(time.RFC3339Nano, *wire.LastModifiedDateTime)
		if err != nil {
			return ImageMessage{}, failure("workiq_protocol")
		}
		value = value.UTC()
		message.ModifiedAt = &value
	}
	return message, nil
}

func BindChatImage(message ImageMessage, imageID string) (ChatImageRef, error) {
	if !teamsimage.ValidComponent(message.chatID) || !teamsimage.ValidComponent(message.messageID) {
		return ChatImageRef{}, imageFailure("image_message_unbindable")
	}
	inspection, err := chatImageInspect(context.Background(), message.body)
	if err != nil {
		return ChatImageRef{}, err
	}
	selected, err := teamsimage.Select(inspection, imageID)
	if err != nil {
		return ChatImageRef{}, err
	}
	resource, err := imageResource(selected.Source(), message.teamID, message.chatID, message.messageID)
	if err != nil {
		return ChatImageRef{}, err
	}
	return ChatImageRef{resource: resource, teamID: message.teamID, chatID: message.chatID, messageID: message.messageID,
		imageID: imageID, binding: selected.Binding()}, nil
}

func chatImageResource(raw, chatID, messageID string) (string, error) {
	return imageResource(raw, "", chatID, messageID)
}

func imageResource(raw, teamID, chatID, messageID string) (string, error) {
	refuse := func() (string, error) { return "", imageFailure("image_reference_unsupported") }
	if len(raw) > 8192 || strings.ContainsAny(raw, "?#\\") {
		return refuse()
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Opaque != "" || u.RawQuery != "" || u.Fragment != "" {
		return refuse()
	}
	resource := u.EscapedPath()
	if u.Scheme != "" {
		if u.Scheme != "https" || u.Host != "graph.microsoft.com" || !strings.HasPrefix(resource, "/v1.0/") {
			return refuse()
		}
		resource = strings.TrimPrefix(resource, "/v1.0")
	} else if u.Host != "" || !strings.HasPrefix(raw, "/") {
		return refuse()
	}
	segments := strings.Split(strings.TrimPrefix(resource, "/"), "/")
	scope := []string{"chats", chatID, "messages", messageID}
	if teamID != "" {
		scope = []string{"teams", teamID, "channels", chatID, "messages", messageID}
	}
	if len(segments) != len(scope)+3 {
		return refuse()
	}
	decoded := make([]string, len(segments))
	for i, segment := range segments {
		value, err := url.PathUnescape(segment)
		if err != nil || !teamsimage.ValidComponent(value) || strings.Contains(value, "%") ||
			strings.Contains(strings.ToLower(segment), "%2e") {
			return refuse()
		}
		decoded[i] = value
	}
	for i, value := range scope {
		if decoded[i] != value {
			return refuse()
		}
	}
	if decoded[len(scope)] != "hostedContents" || decoded[len(scope)+2] != "$value" {
		return refuse()
	}
	return resource, nil
}

func SameImageObservation(before, after ImageMessage, selectedID string) error {
	first, err := BindChatImage(before, selectedID)
	if err != nil {
		return err
	}
	second, err := BindChatImage(after, selectedID)
	if err != nil {
		return err
	}
	if first.teamID != second.teamID || first.chatID != second.chatID || first.messageID != second.messageID || first.binding != second.binding ||
		first.resource != second.resource || (before.ModifiedAt == nil) != (after.ModifiedAt == nil) ||
		before.ModifiedAt != nil && !before.ModifiedAt.Equal(*after.ModifiedAt) {
		return imageFailure("image_reference_changed")
	}
	return nil
}

func (c *Client) FetchChatImageBlob(ctx context.Context, ref ChatImageRef) (ImageBlob, error) {
	if !teamsimage.ValidComponent(ref.imageID) || ref.binding == "" {
		return ImageBlob{}, imageFailure("image_reference_unsupported")
	}
	resource, err := imageResource(ref.resource, ref.teamID, ref.chatID, ref.messageID)
	if err != nil || resource != ref.resource {
		return ImageBlob{}, imageFailure("image_reference_unsupported")
	}
	raw, err := c.call(ctx, requestTimeout, "tools/call", map[string]any{"name": "fetch_blob",
		"arguments": map[string]any{"path": resource}})
	if err != nil {
		_ = c.Close()
		return ImageBlob{}, err
	}
	blob, err := decodeImageBlob(ctx, raw)
	if err != nil && blob.HTTPStatus == 0 {
		_ = c.Close()
	}
	return blob, err
}

func decodeImageBlob(ctx context.Context, raw []byte) (ImageBlob, error) {
	if err := ctx.Err(); err != nil {
		return ImageBlob{}, err
	}
	wire, status, err := decodeBlobEnvelope(ctx, raw)
	result := ImageBlob{HTTPStatus: status}
	if err != nil {
		return result, err
	}
	if json.Unmarshal(wire.ContentType, &result.ContentType) != nil || result.ContentType != "image/png" {
		return ImageBlob{}, imageFailure("image_format_unsupported")
	}
	if *wire.Size <= 0 {
		return ImageBlob{}, failure("workiq_protocol")
	}
	if *wire.Size > MaxDocumentBlob ||
		len(*wire.Base64) > base64.StdEncoding.EncodedLen(MaxDocumentBlob) {
		return ImageBlob{}, imageFailure("image_too_large")
	}
	result.Data, err = base64.StdEncoding.DecodeString(*wire.Base64)
	if err != nil || int64(len(result.Data)) != *wire.Size || len(result.Data) > MaxDocumentBlob {
		return ImageBlob{}, failure("workiq_protocol")
	}
	return result, nil
}

func imageFailure(code string) *errs.Coded {
	return &errs.Coded{Code: code, Exit: errs.ExitEnvironment,
		Message: "the selected native image could not be verified (" + code + ")",
		Fix:     "Inspect the exact cached message and select one supported image; unsupported or changed native references cannot be acquired."}
}
