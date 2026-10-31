package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
)

// Synthetic directed writes in WorkIQ 1.0.1's observed write shape (2026-10-31): one
// structuredContent object with statusCode and data. Created items persist in WORKIQ_WRITES_STATE
// so a later process reads back what an earlier one created. WORKIQ_WRITES_MODE selects a fault:
// reject (a coded 400), lost (the process ends after reading the request) or hidden (created, but
// absent when read back).

const notesNewest = "/chats/48:notes/messages?$top=1&$orderby=createdDateTime%20desc"

type writtenItem struct {
	ID, Parent, Subject, Text, Created string
	To                                 []string
	MessageID                          string
	Sent                               bool
}

func writesState() []writtenItem {
	var items []writtenItem
	raw, err := os.ReadFile(os.Getenv("WORKIQ_WRITES_STATE")) //nolint:gosec // G304: test-chosen state path.
	if err == nil {
		_ = json.Unmarshal(raw, &items)
	}
	return items
}

func saveWritesState(items []writtenItem) {
	raw, _ := json.Marshal(items)
	if os.WriteFile(os.Getenv("WORKIQ_WRITES_STATE"), raw, 0o600) != nil { //nolint:gosec // G703: test-chosen state path.
		os.Exit(92)
	}
}

func chatMessage(item writtenItem) map[string]any {
	return map[string]any{"id": item.ID, "chatId": "48:notes", "createdDateTime": item.Created,
		"from": map[string]any{"user": map[string]any{"id": "synthetic-user"}}, "body": map[string]any{"contentType": "text", "content": item.Text}}
}

// createEntity answers create_entity; ok is false for any other parent, which the caller refuses.
func createEntity(parent string, body json.RawMessage) (any, bool) {
	var wire struct {
		Subject string
		Body    struct{ ContentType, Content string }
		To      []struct{ EmailAddress struct{ Address string } } `json:"toRecipients"`
	}
	if (parent != "/chats/48:notes/messages" && parent != "/me/messages") || json.Unmarshal(body, &wire) != nil || wire.Body.ContentType != "text" {
		return nil, false
	}
	switch os.Getenv("WORKIQ_WRITES_MODE") {
	case "lost":
		os.Exit(3)
	case "reject":
		return map[string]any{"isError": true, "structuredContent": map[string]any{"statusCode": 400,
			"error": map[string]any{"error": map[string]any{"code": "BadRequest", "message": "Synthetic private refusal"}}}}, true
	}
	items := writesState()
	item := writtenItem{ID: fmt.Sprintf("17000000%05d", len(items)+1), Parent: parent, Subject: wire.Subject, Text: wire.Body.Content,
		Created: fmt.Sprintf("2026-10-31T11:%02d:00Z", len(items)+1)}
	for _, to := range wire.To {
		item.To = append(item.To, to.EmailAddress.Address)
	}
	if parent == "/me/messages" {
		item.ID = "draft-" + item.ID
	}
	saveWritesState(append(items, item))
	data := chatMessage(item)
	if parent == "/me/messages" {
		data = map[string]any{"id": item.ID, "isDraft": true}
	}
	return map[string]any{"structuredContent": map[string]any{"statusCode": 201, "data": data}}, true
}

// writeFetch answers the reads a directed write makes; ok is false for other paths.
func writeFetch(path string) (any, int, bool) {
	if data, status, ok := replyFetch(path); ok {
		return data, status, true
	}
	items := writesState()
	hidden := os.Getenv("WORKIQ_WRITES_MODE") == "hidden"
	watermark := map[string]any{"id": "1000", "chatId": "48:notes", "createdDateTime": "2026-10-31T10:00:00Z",
		"from": map[string]any{"user": map[string]any{"id": "synthetic-user"}}, "body": map[string]any{"contentType": "text", "content": "old"}}
	switch {
	case path == notesNewest:
		for i := len(items) - 1; i >= 0 && !hidden; i-- {
			if items[i].Parent != "/me/messages" {
				return map[string]any{"value": []any{chatMessage(items[i])}}, 200, true
			}
		}
		return map[string]any{"value": []any{watermark}}, 200, true
	case path == "/chats/48:notes/messages?$top=50&$orderby=createdDateTime%20desc":
		values := []any{}
		for i := len(items) - 1; i >= 0 && !hidden; i-- {
			if items[i].Parent != "/me/messages" {
				values = append(values, chatMessage(items[i]))
			}
		}
		return map[string]any{"value": append(values, watermark)}, 200, true
	case strings.HasPrefix(path, "/chats/48:notes/messages/"), strings.HasPrefix(path, "/me/messages/draft-"):
		id := strings.TrimPrefix(strings.TrimPrefix(path, "/chats/48:notes/messages/"), "/me/messages/")
		id, _, _ = strings.Cut(id, "?")
		for _, item := range items {
			if item.ID != id || hidden {
				continue
			}
			if item.Parent == "/me/messages" {
				to := []any{}
				for _, address := range item.To {
					to = append(to, map[string]any{"emailAddress": map[string]any{"address": address}})
				}
				return map[string]any{"id": item.ID, "isDraft": true, "subject": item.Subject,
					"body": map[string]any{"contentType": "text", "content": item.Text}, "toRecipients": to}, 200, true
			}
			return chatMessage(item), 200, true
		}
		return map[string]any{"error": map[string]any{"code": "NotFound"}}, 404, true
	}
	return nil, 0, false
}

// The reply world: one mail the synthetic user sent to themselves, with its sent and received copies.
const selfMail = "<self@example.invalid>"

func selfCopy(id, folder, changeKey string) map[string]any {
	return map[string]any{"id": id, "parentFolderId": folder, "changeKey": changeKey, "isDraft": false, "subject": "Synthetic plan",
		"from": map[string]any{"emailAddress": map[string]any{"address": "test.user@example.invalid"}}}
}

// doAction answers do_action: createReply on the received copy, and send of a reply draft.
func doAction(action string, body json.RawMessage) (any, bool) {
	switch os.Getenv("WORKIQ_WRITES_MODE") {
	case "lost":
		os.Exit(3)
	case "reject":
		return map[string]any{"isError": true, "structuredContent": map[string]any{"statusCode": 400,
			"error": map[string]any{"error": map[string]any{"code": "BadRequest", "message": "Synthetic private refusal"}}}}, true
	}
	items := writesState()
	if action == "/me/messages/inbox-self/createReply" {
		var wire struct{ Comment string }
		if json.Unmarshal(body, &wire) != nil || wire.Comment == "" {
			return nil, false
		}
		n := len(items) + 1
		item := writtenItem{ID: fmt.Sprintf("reply-%d", n), Parent: "reply", Text: wire.Comment, To: []string{"test.user@example.invalid"},
			MessageID: fmt.Sprintf("<reply-%d@example.invalid>", n), Created: fmt.Sprintf("2026-10-31T11:%02d:00Z", n)}
		saveWritesState(append(items, item))
		return map[string]any{"structuredContent": map[string]any{"statusCode": 201,
			"data": map[string]any{"id": item.ID, "isDraft": true, "internetMessageId": item.MessageID}}}, true
	}
	draft, ok := strings.CutSuffix(strings.TrimPrefix(action, "/me/messages/"), "/send")
	if !ok || string(body) != "{}" {
		return nil, false
	}
	for i := range items {
		if items[i].ID == draft && items[i].Parent == "reply" && !items[i].Sent {
			items[i].Sent = true
			saveWritesState(items)
			return map[string]any{"structuredContent": map[string]any{"statusCode": 202, "data": nil}}, true
		}
	}
	return map[string]any{"isError": true, "structuredContent": map[string]any{"statusCode": 404, "error": map[string]any{"code": "ErrorItemNotFound"}}}, true
}

// replyFetch answers the reads a reply makes; ok is false for other paths.
func replyFetch(path string) (any, int, bool) {
	filter := ""
	if u, err := url.ParseRequestURI(path); err == nil {
		filter = u.Query().Get("$filter")
	}
	switch {
	case path == "/me/mailFolders/sentitems?$select=id":
		return map[string]any{"id": "sent-folder"}, 200, true
	case strings.HasPrefix(path, "/me/messages?$filter=") && strings.Contains(filter, selfMail):
		return map[string]any{"value": []any{selfCopy("sent-self", "sent-folder", "ck-sent"), selfCopy("inbox-self", "inbox", "ck-1")}}, 200, true
	case strings.HasPrefix(path, "/me/mailFolders/sentitems/messages?$filter="):
		values := []any{}
		for _, item := range writesState() {
			if item.Sent && strings.Contains(filter, item.MessageID) && os.Getenv("WORKIQ_WRITES_MODE") != "hidden" {
				values = append(values, map[string]any{"id": "sent-" + item.ID, "internetMessageId": item.MessageID,
					"toRecipients": []any{map[string]any{"emailAddress": map[string]any{"address": "test.user@example.invalid"}}}})
			}
		}
		return map[string]any{"value": values}, 200, true
	case strings.HasPrefix(path, "/me/messages/reply-"):
		id, _, _ := strings.Cut(strings.TrimPrefix(path, "/me/messages/"), "?")
		for _, item := range writesState() {
			if item.ID == id {
				return map[string]any{"id": item.ID, "isDraft": !item.Sent, "internetMessageId": item.MessageID,
					"toRecipients": []any{map[string]any{"emailAddress": map[string]any{"address": "test.user@example.invalid"}}},
					"body":         map[string]any{"contentType": "html", "content": "<html><body>" + item.Text + "<hr>quoted</body></html>"}}, 200, true
			}
		}
		return map[string]any{"error": map[string]any{"code": "ErrorItemNotFound"}}, 404, true
	}
	return nil, 0, false
}
