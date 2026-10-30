package scoutsessions

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// Every session here is synthetic: invented ids, names and text.
const fullSession = `{
 "id": "s-0001", "name": "Synthetic planning chat", "createdAt": "2026-01-02T03:04:05.000Z",
 "updatedAt": "2026-01-02T04:00:00Z", "model": "model-x", "source": "automation", "automationId": "auto-1", "kind": "chat",
 "deviceId": "0123456789abcdef0123456789abcdef", "unknownField": {"x": 1}, "anotherUnknown": "ignored",
 "messages": [
  {"id": "m1", "role": "system", "content": "You are a synthetic helper.", "timestamp": "2026-01-02T03:04:05Z"},
  {"id": "m2", "role": "user", "content": "Summarize the synthetic report", "timestamp": "2026-01-02T03:05:00.5Z", "fileNames": ["a.docx", "b.xlsx"], "reasoning": {"steps": 1}},
  {"id": "m3", "role": "assistant", "content": "Here is the summary.", "timestamp": 1767323160000, "toolCalls": [{}, {}, {}], "reasoning": "thinking"},
  {"id": "m4", "role": "assistant", "content": [{"type": "text", "text": "part one"}, {"type": "image"}, {"text": "part two"}], "timestamp": "yesterday"},
  "not an object",
  {"id": 5, "role": "user", "content": {"odd": true}, "timestamp": true, "toolCalls": "x", "fileNames": null, "reasoning": ""},
  {"id": "m6", "role": "user", "content": null, "timestamp": null, "reasoning": null}
 ]
}`

func TestParseFullSession(t *testing.T) {
	s, err := Parse([]byte(fullSession))
	if err != nil {
		t.Fatal(err)
	}
	if s.NativeID != "s-0001" || s.Name != "Synthetic planning chat" || s.Model != "model-x" || s.Origin != "automation" ||
		s.Kind != "chat" || s.DeviceID != "0123456789abcdef0123456789abcdef" || !s.Automation {
		t.Fatalf("session fields: %+v", s)
	}
	if s.CreatedAt == nil || !s.CreatedAt.Equal(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)) || s.CreatedAtRaw != "2026-01-02T03:04:05.000Z" {
		t.Fatalf("created: %v %q", s.CreatedAt, s.CreatedAtRaw)
	}
	if s.UpdatedAt == nil || s.UpdatedAt.Hour() != 4 {
		t.Fatalf("updated: %v", s.UpdatedAt)
	}
	if len(s.Messages) != 6 {
		t.Fatalf("messages: %d", len(s.Messages))
	}
	m := s.Messages
	if m[0].Role != "system" || m[0].Ordinal != 0 || m[0].NativeID != "m1" {
		t.Fatalf("m0: %+v", m[0])
	}
	if m[1].FileNames != 2 || !m[1].Reasoning || m[1].At == nil || m[1].At.Nanosecond() != 500_000_000 {
		t.Fatalf("m1: %+v", m[1])
	}
	if m[2].ToolCalls != 3 || !m[2].Reasoning || m[2].At == nil || m[2].At.Year() != 2026 || m[2].TimestampRaw != "1767323160000" {
		t.Fatalf("m2: %+v", m[2])
	}
	if m[3].Text != "part one\npart two" || m[3].At != nil || m[3].TimestampRaw != "yesterday" {
		t.Fatalf("m3: %+v", m[3])
	}
	// The non-object entry is skipped, so ordinals keep the file's positions.
	if m[4].Ordinal != 5 || m[4].NativeID != "" || m[4].Text != "" || m[4].At != nil || m[4].TimestampRaw != "true" || m[4].ToolCalls != 0 || m[4].Reasoning {
		t.Fatalf("m4: %+v", m[4])
	}
	if m[5].Ordinal != 6 || m[5].Text != "" || m[5].At != nil || m[5].Reasoning {
		t.Fatalf("m5: %+v", m[5])
	}
	want := map[string]int{"timestamp_unparsed": 2, "content_part_unsupported": 1, "message_not_object": 1,
		"field_type_unexpected": 2, "content_unsupported": 1}
	if len(s.Losses) != len(want) {
		t.Fatalf("losses: %v", s.Losses)
	}
	for code, n := range want {
		if s.Losses[code] != n {
			t.Fatalf("loss %s: %d, want %d (all %v)", code, s.Losses[code], n, s.Losses)
		}
	}
}

func TestParseOlderShapeWithoutNewFields(t *testing.T) {
	s, err := Parse([]byte(`{"id":"s-2","name":"","createdAt":"","updatedAt":"2026-02-01T00:00:00Z","messages":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	if s.Kind != "" || s.Origin != "" || s.Automation || s.CreatedAt != nil || s.CreatedAtRaw != "" || len(s.Messages) != 0 || len(s.Losses) != 0 {
		t.Fatalf("older shape: %+v", s)
	}
}

func TestParseMessagesMissingOrWrong(t *testing.T) {
	for body, loss := range map[string]string{`{"id":"a"}`: "messages_missing", `{"messages":null}`: "messages_missing", `{"messages":{"a":1}}`: "messages_not_array"} {
		s, err := Parse([]byte(body))
		if err != nil || s.Losses[loss] != 1 || len(s.Messages) != 0 {
			t.Fatalf("%s: %+v %v", body, s, err)
		}
	}
}

func TestParseRefusesNonObjects(t *testing.T) {
	for _, body := range []string{"", "  ", "[]", "null", `{"id":`, "\xff"} {
		if _, err := Parse([]byte(body)); !errors.Is(err, ErrNotSession) {
			t.Fatalf("%q: %v", body, err)
		}
	}
}

func TestParseOddClocks(t *testing.T) {
	for raw, ok := range map[string]bool{`-5`: false, `1e300`: false, `{}`: false, `"2026-03-04T05:06:07+02:00"`: true} {
		s, err := Parse([]byte(`{"createdAt":` + raw + `,"messages":[]}`))
		if err != nil || (s.CreatedAt != nil) != ok || s.CreatedAtRaw != strings.Trim(raw, `"`) {
			t.Fatalf("%s: %+v %v", raw, s, err)
		}
		if ok && s.CreatedAt.Location() != time.UTC {
			t.Fatalf("not UTC: %v", s.CreatedAt)
		}
	}
}

func TestLayoutNames(t *testing.T) {
	for name, want := range map[string]bool{"0123456789abcdef0123456789abcdef": true, "0123456789ABCDEF0123456789abcdef": false, "0123": false} {
		if DeviceFolder(name) != want {
			t.Fatalf("device %q", name)
		}
	}
	for name, want := range map[string]bool{"a1-b2.json": true, "sessions-index.json": false, "skip-list.json": false,
		"A.json": false, "x.txt": false, ".json": false, strings.Repeat("a", 201) + ".json": false} {
		if SessionFile(name) != want {
			t.Fatalf("session %q", name)
		}
	}
}
