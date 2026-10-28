package store

import (
	"context"
	"encoding/json"
	"testing"
)

func TestWorkIQMailGraphFillsMissingNativeChildrenWithoutReplacingKnownText(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	attempt := seedWorkIQMailAttempt(t, s)
	local := mailMsg(100, fInbox, "one@example.test", "Subject", 0)
	local.Recipients = nil
	mustCommitMail(t, s, mailBatch(mailT0, local))
	raw := graphMailFixture("one", "inbox-id", "one@example.test")
	var data map[string]any
	must0(json.Unmarshal(raw, &data))
	data["toRecipients"] = []any{map[string]any{"emailAddress": map[string]any{"name": "Graph recipient", "address": "recipient@example.test"}}}
	data["attachments"] = []any{map[string]any{"id": "graph-attachment", "name": "Graph attachment", "size": 10}}
	data["hasAttachments"] = true
	prepareRealMailInventory(t, s, attempt, []json.RawMessage{must(json.Marshal(data))})
	_ = must(s.CommitWorkIQMailGroup(ctx, attempt, "one@example.test"))
	item := must(s.MailGet(ctx, mailAcct, 100))
	if item.BodyText != "body of Subject" || len(item.Recipients) != 1 || item.Recipients[0].Name != "Graph recipient" ||
		len(item.Attachments) != 1 || item.Attachments[0].Name != "Graph attachment" || !item.HasAttachments {
		t.Fatal("mixed source admission failed to fill missing typed child metadata or replaced known native text")
	}
	no := false
	local.HasAttachments = &no
	mustCommitMail(t, s, mailBatch(mailT0, local))
	item = must(s.MailGet(ctx, mailAcct, 100))
	if !item.HasAttachments || len(item.Attachments) != 1 {
		t.Fatal("empty native child metadata hid retained positive Graph attachment evidence")
	}
}

func TestWorkIQMailMixedChildAdmissionFaultsKeepOriginalNativeData(t *testing.T) {
	var attempt int64
	setup := func(t *testing.T, s *Store) {
		attempt = seedWorkIQMailAttempt(t, s)
		local := mailMsg(100, fInbox, "one@example.test", "Subject", 0)
		local.Recipients = nil
		mustCommitMail(t, s, mailBatch(mailT0, local))
		var data map[string]any
		must0(json.Unmarshal(graphMailFixture("one", "inbox-id", "one@example.test"), &data))
		data["toRecipients"] = []any{map[string]any{"emailAddress": map[string]any{"address": "recipient@example.test"}}}
		data["attachments"] = []any{map[string]any{"id": "graph-attachment", "name": "Graph attachment", "size": 10}}
		prepareRealMailInventory(t, s, attempt, []json.RawMessage{must(json.Marshal(data))})
	}
	sweepFaults(t, setup, func(ctx context.Context, s *Store) error {
		_, err := s.CommitWorkIQMailGroup(ctx, attempt, "one@example.test")
		return err
	})
}
