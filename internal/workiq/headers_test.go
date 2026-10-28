package workiq

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestWorkIQMessageHeaderCountsWithoutContentOrVersionQualification(t *testing.T) {
	header, err := DecodeMessageHeader(context.Background(), []byte(`{"id":"one","messageType":"message","replyToId":"parent",
"createdDateTime":"2026-10-10T00:00:00.1234567Z","body":{"contentType":"text","content":"discarded-private-body"}}`))
	if err != nil || header.ID != "one" || header.CreatedAt == nil || header.CreatedAt.Nanosecond() != 123456700 ||
		header.MessageType != "message" || header.ReplyToID != "parent" {
		t.Fatal("counts-only header required a qualified body/modification clock or lost exact scope")
	}
	encoded := string(mustHeaderJSON(t, header))
	if strings.Contains(encoded, "discarded-private-body") {
		t.Fatal("counts-only projection retained message content")
	}
	unknown, err := DecodeMessageHeader(context.Background(), []byte(`{"id":"unknown"}`))
	if err != nil || unknown.CreatedAt != nil {
		t.Fatal("unknown creation clock became a known timestamp")
	}
	for _, raw := range []string{`{`, `{}`, `{"id":"one","createdDateTime":"bad"}`} {
		_, err := DecodeMessageHeader(context.Background(), []byte(raw))
		requireCode(t, err, "workiq_protocol")
	}
}

func mustHeaderJSON(t *testing.T, value MessageHeader) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal("synthetic message header could not encode")
	}
	return raw
}
