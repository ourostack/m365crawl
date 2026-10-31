package workiq

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/outlookmail"
	"github.com/ourostack/m365crawl/internal/strictjson"
)

// NotesToSelf is Teams' own Notes-to-self stream: a chat whose only reader is the signed-in user.
const NotesToSelf = "48:notes"

var chatID = regexp.MustCompile(`^19:[A-Za-z0-9._@:=-]{1,400}$`)

// ValidChatID says whether id is a chat a directed write may name: Notes to self, or a 19: thread
// id of plain characters, so it can be put into a path without escaping.
func ValidChatID(id string) bool { return id == NotesToSelf || chatID.MatchString(id) }

// Writer is the directed-write dispatcher. Only the writes commands open one; the read client never
// carries a write tool. It adds exactly two create_entity calls, with paths and bodies it builds.
type Writer struct{ c *Client }

func OpenWriter(ctx context.Context, inv Invocation, account string) (*Writer, error) {
	c, err := Open(ctx, inv, account)
	if err != nil {
		return nil, err
	}
	return &Writer{c: c}, nil
}

// Fetch reads a path a directed write needs: /me, the target and its messages.
func (w *Writer) Fetch(ctx context.Context, path string) (Page, error) { return w.c.Fetch(ctx, path) }

func (w *Writer) Close() error { return w.c.Close() }

type WriteKind string

const (
	// WriteCreated is a 2xx answer that names the created item.
	WriteCreated WriteKind = "created"
	// WriteRejected provably did not act: the server refused with a coded 4xx, or the call never started.
	WriteRejected WriteKind = "rejected"
	// WriteUnknown may have acted: every other answer or failure after the request could have left.
	WriteUnknown WriteKind = "unknown"
	// WriteAccepted is a 2xx answer to an action with no result, such as send's 202.
	WriteAccepted WriteKind = "accepted"
)

// WriteAnswer is what one create_entity call said, without any server text.
type WriteAnswer struct {
	Kind       WriteKind
	Status     int
	Code       string // a Graph error code, or the m365crawl code of a transport failure
	Server     string // allowlisted descriptor of a failure
	ServerID   string
	ChatID     string
	FromUserID string
	IsDraft    bool
	// InternetMessageID is a created mail's Internet Message-ID, which its sent copy keeps.
	InternetMessageID string
}

var graphMessageID = regexp.MustCompile(`^[A-Za-z0-9=_-]{1,512}$`)

// ValidMessageID says whether id is a Graph mail id a directed write may put into a path.
func ValidMessageID(id string) bool { return graphMessageID.MatchString(id) }

// CreateReply makes a reply draft to one mail; it never sends. comment is the reply's HTML.
func (w *Writer) CreateReply(ctx context.Context, messageID, comment string) WriteAnswer {
	if !ValidMessageID(messageID) {
		return WriteAnswer{Kind: WriteRejected, Code: "workiq_path_invalid"}
	}
	return w.act(ctx, "do_action", map[string]any{"actionUrl": "/me/messages/" + messageID + "/createReply",
		"jsonBody": map[string]any{"comment": comment}}, false)
}

// SendDraft sends one draft of the person's own mailbox.
func (w *Writer) SendDraft(ctx context.Context, draftID string) WriteAnswer {
	if !ValidMessageID(draftID) {
		return WriteAnswer{Kind: WriteRejected, Code: "workiq_path_invalid"}
	}
	return w.act(ctx, "do_action", map[string]any{"actionUrl": "/me/messages/" + draftID + "/send", "jsonBody": map[string]any{}}, true)
}

func (w *Writer) CreateChatMessage(ctx context.Context, chat, text string) WriteAnswer {
	if !ValidChatID(chat) {
		return WriteAnswer{Kind: WriteRejected, Code: "workiq_path_invalid"}
	}
	return w.create(ctx, "/chats/"+chat+"/messages", map[string]any{"body": map[string]any{"contentType": "text", "content": text}})
}

func (w *Writer) CreateMailDraft(ctx context.Context, subject, text string, to []string) WriteAnswer {
	recipients := make([]any, 0, len(to))
	for _, address := range to {
		recipients = append(recipients, map[string]any{"emailAddress": map[string]any{"address": address}})
	}
	return w.create(ctx, "/me/messages", map[string]any{"subject": subject,
		"body": map[string]any{"contentType": "text", "content": text}, "toRecipients": recipients})
}

func (w *Writer) create(ctx context.Context, parent string, body map[string]any) WriteAnswer {
	return w.act(ctx, "create_entity", map[string]any{"parentUrl": parent, "jsonBody": body}, false)
}

// act makes one write call; empty says a 2xx without a result is the answer (WriteAccepted).
func (w *Writer) act(ctx context.Context, tool string, arguments map[string]any, empty bool) WriteAnswer {
	if ctx.Err() != nil {
		return WriteAnswer{Kind: WriteRejected, Code: "workiq_timeout"} // the call never started
	}
	raw, err := w.c.call(ctx, requestTimeout, "tools/call", map[string]any{"name": tool, "arguments": arguments})
	if err != nil {
		_ = w.c.Close()
		answer := WriteAnswer{Kind: WriteUnknown, Code: "workiq_failed"}
		var coded *errs.Coded
		if errors.As(err, &coded) {
			answer.Code = coded.Code
		}
		return answer
	}
	return decodeWrite(ctx, raw, empty)
}

type writeResult struct {
	Status int             `json:"statusCode"`
	Data   json.RawMessage `json:"data"`
	Error  json.RawMessage `json:"error"`
}

// decodeWrite classifies a write answer. Live WorkIQ 1.0.1 answers a write with one
// structuredContent object; a one-item results array, as fetch uses, is read the same way. empty
// says the call has no result: any 2xx is WriteAccepted.
func decodeWrite(ctx context.Context, raw []byte, empty bool) WriteAnswer {
	unknown := WriteAnswer{Kind: WriteUnknown, Code: "workiq_protocol"}
	budget := strictjson.Budget{Context: ctx, MaxDepth: 64, MaxMembers: 131072}
	if len(raw) > maxFrame {
		return unknown
	}
	if _, err := budget.Parse(raw); err != nil {
		return unknown
	}
	var envelope struct {
		Structured struct {
			writeResult
			Results []writeResult `json:"results"`
		} `json:"structuredContent"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return unknown
	}
	result := envelope.Structured.writeResult
	if len(envelope.Structured.Results) == 1 {
		result = envelope.Structured.Results[0]
	}
	answer := WriteAnswer{Kind: WriteUnknown, Status: result.Status, Code: "workiq_failed"}
	failed := len(result.Error) > 0 && string(result.Error) != "null"
	switch {
	case result.Status >= 200 && result.Status < 300 && !failed && empty:
		return WriteAnswer{Kind: WriteAccepted, Status: result.Status}
	case result.Status >= 200 && result.Status < 300 && !failed:
		var item struct {
			ID, ChatID, InternetMessageID string
			IsDraft                       bool
			From                          struct{ User struct{ ID string } }
		}
		if json.Unmarshal(result.Data, &item) != nil || item.ID == "" || len(item.ID) > maxPath {
			answer.Code = "workiq_protocol"
			return answer
		}
		return WriteAnswer{Kind: WriteCreated, Status: result.Status, ServerID: item.ID, ChatID: item.ChatID,
			FromUserID: item.From.User.ID, IsDraft: item.IsDraft, InternetMessageID: item.InternetMessageID}
	case result.Status >= 400 && result.Status < 500 && result.Status != 408 && failed:
		if code := graphCode(result.Error); code != "" {
			answer.Kind, answer.Code = WriteRejected, code
		}
		answer.Server = serverDescriptor(result.Status, result.Error, answer.Code)
	case result.Status != 0:
		answer.Server = serverDescriptor(result.Status, result.Error, answer.Code)
	}
	return answer
}

// graphCode is the identifier code of a refusal that proves nothing was created: Graph's own
// error object, or WorkIQ's path refusal, which it answers before calling Graph.
func graphCode(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		if strings.HasPrefix(text, "Access denied for ") {
			return "workiq_policy_denied"
		}
		return ""
	}
	var object struct {
		Code  string
		Error json.RawMessage
	}
	_ = json.Unmarshal(raw, &object)
	var inner struct{ Code string }
	if json.Unmarshal(object.Error, &inner) == nil && inner.Code != "" {
		object.Code = inner.Code
	}
	if graphErrorCode.MatchString(object.Code) {
		return object.Code
	}
	return ""
}

// WrittenMessage is a chat message or draft read back to verify a directed write.
type WrittenMessage struct {
	ID, ChatID, FromUserID, Subject, Text string
	Created                               time.Time
	Deleted, IsDraft                      bool
	Recipients                            []string // lower-case addresses, drafts only
}

// DecodeWrittenMessage reads one chat message or mail message as a write's verification needs it.
// The text is HTML reduced to text when the body is HTML.
func DecodeWrittenMessage(ctx context.Context, raw json.RawMessage) (WrittenMessage, error) {
	var wire struct {
		ID, ChatID, Subject, CreatedDateTime, DeletedDateTime string
		IsDraft                                               bool
		From                                                  struct{ User struct{ ID string } }
		Body                                                  struct{ ContentType, Content string }
		ToRecipients                                          []struct{ EmailAddress struct{ Address string } }
	}
	if _, err := decodeEntity(ctx, raw, &wire); err != nil {
		return WrittenMessage{}, err
	}
	created, err := graphTime(wire.CreatedDateTime)
	if wire.ID == "" || err != nil {
		return WrittenMessage{}, failure("workiq_protocol")
	}
	m := WrittenMessage{ID: wire.ID, ChatID: wire.ChatID, FromUserID: wire.From.User.ID, Subject: wire.Subject,
		Text: wire.Body.Content, Deleted: wire.DeletedDateTime != "", IsDraft: wire.IsDraft}
	if created != nil {
		m.Created = *created
	}
	if strings.EqualFold(wire.Body.ContentType, "html") {
		m.Text = outlookmail.HTMLText([]byte(wire.Body.Content))
	}
	for _, r := range wire.ToRecipients {
		m.Recipients = append(m.Recipients, strings.ToLower(strings.TrimSpace(r.EmailAddress.Address)))
	}
	return m, nil
}

// ScrubText applies the server scrub rules to text an agent wrote, so it compares with what a
// read back returns after the same rules.
func ScrubText(text string) string {
	encoded, _ := json.Marshal(text)
	safe, _ := scrubServer(encoded)
	var out string
	if json.Unmarshal(safe, &out) != nil {
		return text
	}
	return out
}
