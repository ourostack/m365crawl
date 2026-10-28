package workiq

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestWorkIQMailMapping(t *testing.T) {
	raw := []byte(`{"id":"graph:one","parentFolderId":"folder:inbox","conversationId":"conversation:one",
"internetMessageId":" <One@Example.Invalid> ","subject":"Re: Budget","bodyPreview":"preview",
"sender":{"emailAddress":{"name":"Fallback","address":"fallback@example.invalid"}},
"from":{"emailAddress":{"name":"Sender","address":"Sender@Example.Invalid"}},
"toRecipients":[{"emailAddress":{"name":"To","address":"to@example.invalid"}}],
"ccRecipients":[{"emailAddress":{"name":"Cc","address":"cc@example.invalid"}}],
"bccRecipients":[{"emailAddress":{"name":"Bcc","address":"bcc@example.invalid"}}],
"sentDateTime":"2026-10-10T12:00:00.1234567Z","receivedDateTime":"2026-10-10T12:00:01Z",
"lastModifiedDateTime":"2026-10-10T12:02:00.1234567Z","isRead":false,"hasAttachments":true,"importance":"high",
"flag":{"flagStatus":"flagged"},"body":{"contentType":"html","content":"<p>retained body</p>"},
"attachments":[{"id":"attachment:one","name":"fixture.txt","contentType":"text/plain","size":12,"isInline":false}],
"@microsoft.graph.downloadUrl":"https://files.example.invalid/PRIVATE-CREDENTIAL","unknown":{"safe":"retained"}}`)
	m, err := DecodeMail(context.Background(), raw)
	if err != nil || m.ID != "graph:one" || m.FolderID != "folder:inbox" || m.ConversationID != "conversation:one" ||
		m.InternetMessageID != " <One@Example.Invalid> " || m.Sender.Address != "Sender@Example.Invalid" ||
		len(m.To) != 1 || len(m.Cc) != 1 || len(m.Bcc) != 1 || m.IsRead == nil || *m.IsRead || m.HasAttachments == nil || !*m.HasAttachments ||
		!m.BodyPresent || m.BodyType != "html" || m.Body != "<p>retained body</p>" || m.Flag != "flagged" ||
		len(m.Attachments) != 1 || m.Attachments[0].Size != 12 || m.ModifiedAt == nil || m.ModifiedAt.Nanosecond() != 123456700 {
		t.Fatal("Graph mail mapping lost identity, body, exact clock or explicit metadata")
	}
	if strings.Contains(string(m.Raw), "PRIVATE-CREDENTIAL") || !strings.Contains(string(m.Raw), `"safe":"retained"`) {
		t.Fatal("mail retained an unsanitized envelope or discarded safe source JSON")
	}
	minimal, err := DecodeMail(context.Background(), []byte(`{"id":"minimal","sender":{"emailAddress":{"address":"sender@example.invalid"}}}`))
	if err != nil || minimal.Sender.Address != "sender@example.invalid" || minimal.IsRead != nil || minimal.SentAt != nil || minimal.BodyPresent {
		t.Fatal("missing source values became known-empty metadata")
	}
	removed, err := DecodeMail(context.Background(), []byte(`{"id":"removed","@removed":{"reason":"changed"}}`))
	if err != nil || !removed.Removed || removed.DeletedAt != nil {
		t.Fatal("@removed became explicit deletion")
	}
}

func TestWorkIQMailIdentityCorroboration(t *testing.T) {
	at := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	a := MailMessage{InternetMessageID: " <One@Example.Invalid> ", Subject: "RE: Budget", Sender: MailAddress{Address: " Ann@Example.Invalid "}, SentAt: &at}
	b := a
	b.InternetMessageID, b.Subject, b.Sender.Address = "one@example.invalid", "Budget", "ann@example.invalid"
	later := at.Add(5 * time.Second)
	b.SentAt = &later
	if NormalizeMessageID(a.InternetMessageID) != b.InternetMessageID || !MailCorroborates(a, b) {
		t.Fatal("comparison-only normalization or inclusive five-second corroboration failed")
	}
	for _, change := range []func(*MailMessage){
		func(m *MailMessage) { m.InternetMessageID = "" },
		func(m *MailMessage) { m.Sender.Address = "" },
		func(m *MailMessage) { m.Sender.Address = "forged@example.invalid" },
		func(m *MailMessage) { m.Subject = "Unrelated" },
		func(m *MailMessage) { m.SentAt = nil },
		func(m *MailMessage) { t := at.Add(5*time.Second + time.Nanosecond); m.SentAt = &t },
		func(m *MailMessage) { t := at.Add(-5*time.Second - time.Nanosecond); m.SentAt = &t },
	} {
		modified := b
		change(&modified)
		if MailCorroborates(a, modified) {
			t.Fatal("forged, missing or out-of-tolerance evidence merged")
		}
	}
	encoded, err := json.Marshal(a)
	if err != nil || !strings.Contains(string(encoded), "One@Example.Invalid") {
		t.Fatal("normalization changed the retained original Message-ID")
	}
}

func TestWorkIQMailUnknownVersusKnownEmpty(t *testing.T) {
	m, err := DecodeMail(context.Background(), []byte(`{"id":"empty","toRecipients":[],"ccRecipients":[],"bccRecipients":[],"attachments":[],
"body":{"contentType":"text","content":""},"flag":{"flagStatus":"notFlagged"},"importance":"normal"}`))
	if err != nil || m.To == nil || m.Cc == nil || m.Bcc == nil || m.Attachments == nil || !m.BodyPresent || m.Body != "" ||
		m.Flag != "none" || m.Importance != "normal" {
		t.Fatal("known-empty metadata became indistinguishable from unavailable metadata")
	}
}

func TestWorkIQMailRejectsMalformedEntitiesWithoutEcho(t *testing.T) {
	for _, raw := range []string{
		`{`, `null`, `[]`, `{}`, `{"id":"one","id":"two"}`,
		`{"id":"one","sentDateTime":"synthetic-secret"}`,
		`{"id":"one","receivedDateTime":123}`,
		`{"id":"one","lastModifiedDateTime":"99999-01-01T00:00:00Z"}`,
		`{"id":"one","deletedDateTime":"not-a-clock"}`,
		`{"id":"one","isRead":"false"}`,
		`{"id":"one","attachments":[{"id":"attachment","size":-1}]}`,
		`{"id":"one","attachments":[{"size":12}]}`,
		`{"id":"` + strings.Repeat("x", maxPath+1) + `"}`,
	} {
		_, err := DecodeMail(context.Background(), []byte(raw))
		requireCode(t, err, "workiq_protocol")
		if strings.Contains(err.Error(), "synthetic-secret") {
			t.Fatal("malformed source data leaked into a mapping error")
		}
	}
	_, err := DecodeMail(context.Background(), []byte(strings.Repeat("x", maxFrame+1)))
	requireCode(t, err, "workiq_page_too_large")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := DecodeMail(ctx, []byte(`{"id":"cancelled"}`)); err == nil {
		t.Fatal("cancelled mapping accepted more source data")
	}
}
