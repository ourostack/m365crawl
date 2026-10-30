// Package scoutsessions reads one Microsoft Scout session file: the JSON copy of a conversation
// that the app keeps in the user's OneDrive. The format is not a documented interface, so the
// reader is tolerant: an unknown field is ignored, a field of an unexpected type is counted as a
// loss and left empty, and only a file that is not a JSON object is refused.
package scoutsessions

import (
	"bytes"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"
)

// ParserVersion numbers how Parse maps a file. Raise it when a change alters what Parse returns
// for the same bytes. It is part of Stamp: the next scout sync then maps every archived
// observation again in place from its stored JSON, gone files included, and opens again every
// resident file whose last read had another stamp.
const ParserVersion = 1

// MaxFileBytes is the largest session file read. Larger files are skipped, never truncated.
const MaxFileBytes = 32 << 20

// maxEpochMillis bounds a numeric clock to about the year 33658; anything larger is not a time.
const maxEpochMillis = 1e15

// ErrNotSession is returned for bytes that are not one JSON object.
var ErrNotSession = errors.New("scout_session_unparsable")

// Session is the mapped content of one session file.
type Session struct {
	NativeID     string
	Name         string
	Model        string
	Origin       string // the file's "source": automation, teams and so on; empty for a conversation the user started
	Kind         string
	DeviceID     string // the file's own deviceId claim
	Automation   bool   // the file names an automation
	CreatedAtRaw string
	CreatedAt    *time.Time
	UpdatedAtRaw string
	UpdatedAt    *time.Time
	Messages     []Message
	// Losses counts what the reader could not map, by fixed code.
	Losses map[string]int
}

// Message is one entry of the session's messages array; Ordinal is its position in the file.
type Message struct {
	Ordinal      int
	NativeID     string
	Role         string
	TimestampRaw string
	At           *time.Time
	Text         string
	ToolCalls    int
	FileNames    int
	Reasoning    bool
}

var (
	deviceDir   = regexp.MustCompile(`^[0-9a-f]{32}$`)
	sessionFile = regexp.MustCompile(`^[a-z0-9-]{1,200}\.json$`)
)

// DeviceFolder reports whether name is a per-device folder name: 32 lower-case hex digits.
func DeviceFolder(name string) bool { return deviceDir.MatchString(name) }

// SessionFile reports whether name is a session file name in a device folder. The index and skip
// list files that sit beside the sessions are not sessions.
func SessionFile(name string) bool {
	return sessionFile.MatchString(name) && name != "sessions-index.json" && name != "skip-list.json"
}

// Parse maps one session file.
func Parse(data []byte) (Session, error) {
	var top map[string]json.RawMessage
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '{' || json.Unmarshal(trimmed, &top) != nil {
		return Session{}, ErrNotSession
	}
	s := Session{Losses: map[string]int{}}
	s.NativeID = text(top, "id", s.Losses)
	s.Name = text(top, "name", s.Losses)
	s.Model = text(top, "model", s.Losses)
	s.Origin = text(top, "source", s.Losses)
	s.Kind = text(top, "kind", s.Losses)
	s.DeviceID = text(top, "deviceId", s.Losses)
	s.Automation = text(top, "automationId", s.Losses) != ""
	s.CreatedAtRaw, s.CreatedAt = clock(top["createdAt"], s.Losses)
	s.UpdatedAtRaw, s.UpdatedAt = clock(top["updatedAt"], s.Losses)
	raw, ok := top["messages"]
	if !ok || isNull(raw) {
		s.Losses["messages_missing"]++
		return s, nil
	}
	var entries []json.RawMessage
	if json.Unmarshal(raw, &entries) != nil {
		s.Losses["messages_not_array"]++
		return s, nil
	}
	for i, entry := range entries {
		var fields map[string]json.RawMessage
		if json.Unmarshal(entry, &fields) != nil || fields == nil {
			s.Losses["message_not_object"]++
			continue
		}
		m := Message{Ordinal: i, NativeID: text(fields, "id", s.Losses), Role: text(fields, "role", s.Losses)}
		m.TimestampRaw, m.At = clock(fields["timestamp"], s.Losses)
		m.Text = content(fields["content"], s.Losses)
		m.ToolCalls = count(fields["toolCalls"], s.Losses)
		m.FileNames = count(fields["fileNames"], s.Losses)
		m.Reasoning = present(fields["reasoning"])
		s.Messages = append(s.Messages, m)
	}
	return s, nil
}

func isNull(raw json.RawMessage) bool {
	return raw == nil || bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

func present(raw json.RawMessage) bool {
	if isNull(raw) {
		return false
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s != ""
	}
	return true
}

// text is a string field; absent and null are empty, any other type is a loss.
func text(fields map[string]json.RawMessage, key string, losses map[string]int) string {
	raw := fields[key]
	if isNull(raw) {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		losses["field_type_unexpected"]++
		return ""
	}
	return s
}

// clock reads an ISO time string or a number of milliseconds since 1970. It returns the raw text
// as written (a number's own digits) and the parsed UTC time, nil when it cannot be read.
func clock(raw json.RawMessage, losses map[string]int) (string, *time.Time) {
	if isNull(raw) {
		return "", nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if s == "" {
			return "", nil
		}
		if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
			u := t.UTC()
			return s, &u
		}
		losses["timestamp_unparsed"]++
		return s, nil
	}
	written := strings.TrimSpace(string(raw))
	var ms float64
	if json.Unmarshal(raw, &ms) == nil && ms > 0 && ms < maxEpochMillis {
		t := time.UnixMilli(int64(ms)).UTC()
		return written, &t
	}
	losses["timestamp_unparsed"]++
	return written, nil
}

// content is a message's text: a string, or the text parts of an array of parts joined by a
// newline. Anything else is a loss with no text.
func content(raw json.RawMessage, losses map[string]int) string {
	if isNull(raw) {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []map[string]json.RawMessage
	if json.Unmarshal(raw, &parts) != nil {
		losses["content_unsupported"]++
		return ""
	}
	texts := []string{}
	for _, part := range parts {
		var t string
		if json.Unmarshal(part["text"], &t) == nil && t != "" {
			texts = append(texts, t)
		} else {
			losses["content_part_unsupported"]++
		}
	}
	return strings.Join(texts, "\n")
}

// count is the length of an array field; absent and null are zero, any other type is a loss.
func count(raw json.RawMessage, losses map[string]int) int {
	if isNull(raw) {
		return 0
	}
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil {
		losses["field_type_unexpected"]++
		return 0
	}
	return len(items)
}
