package outlookowa

import (
	"context"
	"reflect"
	"testing"

	"github.com/ourostack/m365crawl/internal/outlookmail"
	"github.com/ourostack/m365crawl/internal/v8"
)

func detailMetadata() *v8.Object {
	return obj("ItemId", ident("M"), "NormalizedBody", obj("Value", "<p>Synthetic detail</p>"),
		"ToRecipients", []any{obj("Name", "To One", "EmailAddress", "one@example.invalid"), obj("Name", "To Two", "EmailAddress", "two@example.invalid")},
		"CcRecipients", []any{obj("Name", "Cc", "EmailAddress", "cc@example.invalid")},
		"BccRecipients", []any{obj("Name", "Bcc", "EmailAddress", "bcc@example.invalid")},
		"Attachments", []any{obj("AttachmentId", ident("Attachment/Case=="), "Name", "synthetic.txt", "Size", int64(42), "IsInline", true)},
		"UID", "Synthetic-UID", "InternetMessageId", "<synthetic@example.invalid>")
}

func collectDetail(t *testing.T, h, d any) Result {
	t.Helper()
	f := readerWith(h)
	if d != nil {
		f.values[3] = []any{d}
	}
	r, err := collect(context.Background(), f, "Synthetic", false)
	if err != nil || len(r.Messages) != 1 {
		t.Fatal("synthetic joined-detail collection failed")
	}
	return r
}

func TestCollectDetailOnlyMetadata(t *testing.T) {
	h := header("M", "F")
	h.Keys = append(h.Keys, "ConversationId", "DateTimeSent", "ItemClass")
	h.Values = append(h.Values, ident("HeaderConversation"), "2026-10-01T09:00:00Z", "IPM.Note")
	d := detailMetadata()
	d.Keys = append(d.Keys, "HasAttachments", "ConversationId", "DateTimeSent", "ItemClass", "ReplyTo")
	d.Values = append(d.Values, true, ident("NotHeaderConversation"), "invalid", "NotHeaderClass", []any{obj("Name", "NotRecipient")})
	r := collectDetail(t, h, d)
	m := r.Messages[0]
	want := []outlookmail.Recipient{{Name: "To One", Address: "one@example.invalid", Role: "to"}, {Name: "To Two", Address: "two@example.invalid", Role: "to"}, {Name: "Cc", Address: "cc@example.invalid", Role: "cc"}, {Name: "Bcc", Address: "bcc@example.invalid", Role: "bcc"}}
	if !reflect.DeepEqual(m.Value.Recipients, want) || !reflect.DeepEqual(m.AttachmentIDs, []string{"Attachment/Case=="}) || len(m.Value.Attachments) != 1 || m.Value.Attachments[0].Size != 42 || !m.Value.Attachments[0].Inline || m.Value.Attachments[0].Downloaded || m.Value.ICalUID != "synthetic-uid" || m.Value.MessageID != "<synthetic@example.invalid>" {
		t.Error("six joined detail metadata fields were not preserved")
	}
	if len(r.Losses) != 0 || r.Doubtful || m.Value.HasAttachments == nil || *m.Value.HasAttachments || m.Value.ConversationID != "HeaderConversation" || m.Value.Class != "IPM.Note" || m.Value.Sent.IsZero() || m.Value.InReplyTo != "" || m.Value.Body.State != "inline" {
		t.Fatal("detail metadata changed independent header/body semantics")
	}
}

func TestCollectDetailMetadataAvailabilityAndEquality(t *testing.T) {
	for _, absent := range []any{nil, v8.Undefined{}} {
		t.Run("detail-unavailable", func(t *testing.T) {
			h := header("M", "F")
			d := obj("ItemId", ident("M"))
			for _, key := range []string{"ToRecipients", "CcRecipients", "BccRecipients", "Attachments", "UID", "InternetMessageId"} {
				h.Keys = append(h.Keys, key)
				h.Values = append(h.Values, field(detailMetadata(), key))
				d.Keys = append(d.Keys, key)
				d.Values = append(d.Values, absent)
			}
			r := collectDetail(t, h, d)
			if len(r.Messages[0].Value.Recipients) != 4 || len(r.Messages[0].Value.Attachments) != 1 || r.Messages[0].Value.ICalUID != "synthetic-uid" || r.Messages[0].Value.MessageID == "" || len(r.Losses) != 0 || r.Doubtful {
				t.Fatal("unavailable detail lost header compatibility")
			}
		})
		t.Run("header-unavailable", func(t *testing.T) {
			h := header("M", "F")
			for _, key := range []string{"ToRecipients", "CcRecipients", "BccRecipients", "Attachments", "UID", "InternetMessageId"} {
				h.Keys = append(h.Keys, key)
				h.Values = append(h.Values, absent)
			}
			r := collectDetail(t, h, detailMetadata())
			if len(r.Messages[0].Value.Recipients) != 4 || len(r.Messages[0].Value.Attachments) != 1 || r.Messages[0].Value.ICalUID != "synthetic-uid" || r.Messages[0].Value.MessageID == "" || len(r.Losses) != 0 {
				t.Fatal("detail did not fill unavailable header")
			}
		})
	}
	h := header("M", "F")
	d := detailMetadata()
	for _, key := range []string{"ToRecipients", "CcRecipients", "BccRecipients", "Attachments", "UID", "InternetMessageId"} {
		h.Keys = append(h.Keys, key)
		h.Values = append(h.Values, field(d, key))
	}
	r := collectDetail(t, h, d)
	if len(r.Messages[0].Value.Recipients) != 4 || len(r.Messages[0].Value.Attachments) != 1 || len(r.Losses) != 0 || r.Doubtful {
		t.Fatal("equal metadata duplicated or marked conflicting")
	}
	r = collectDetail(t, h, nil)
	if len(r.Messages[0].Value.Recipients) != 4 || r.Messages[0].Value.Body.State != "none" || len(r.Losses) != 0 {
		t.Fatal("missing detail lost header")
	}
}

func TestCollectDetailMetadataConflicts(t *testing.T) {
	for _, key := range []string{"ToRecipients", "CcRecipients", "BccRecipients", "Attachments", "UID", "InternetMessageId"} {
		t.Run(key, func(t *testing.T) {
			h := header("M", "F")
			var conflicting any = "different synthetic value"
			if key == "Attachments" {
				conflicting = []any{obj("AttachmentId", ident("Other"), "Name", "other.txt")}
			}
			if key == "ToRecipients" || key == "CcRecipients" || key == "BccRecipients" {
				conflicting = []any{obj("Name", "Other", "EmailAddress", "other@example.invalid")}
			}
			h.Keys = append(h.Keys, key)
			h.Values = append(h.Values, conflicting)
			r := collectDetail(t, h, detailMetadata())
			m := r.Messages[0]
			if !reflect.DeepEqual(r.Losses, []outlookmail.Loss{{Code: "outlook_owa_detail_conflict", Count: 1}}) || !r.Doubtful {
				t.Fatal("metadata conflict not fixed/countable/doubtful")
			}
			switch key {
			case "UID":
				if m.Value.ICalUID != "" {
					t.Fatal("conflicting UID selected")
				}
			case "InternetMessageId":
				if m.Value.MessageID != "" {
					t.Fatal("conflicting MessageID selected")
				}
			case "Attachments":
				if len(m.AttachmentIDs) != 0 || len(m.Value.Attachments) != 0 {
					t.Fatal("conflicting attachments selected")
				}
			default:
				role := map[string]string{"ToRecipients": "to", "CcRecipients": "cc", "BccRecipients": "bcc"}[key]
				for _, rc := range m.Value.Recipients {
					if rc.Role == role {
						t.Fatal("conflicting recipient list selected")
					}
				}
			}
			if m.ID != "M" || m.FolderID != "F" || m.Value.Body.State != "inline" || !reflect.DeepEqual(r.SeenIDs, []string{"M"}) || m.Value.HasAttachments == nil || *m.Value.HasAttachments {
				t.Fatal("conflict damaged independent header/body/seen state")
			}
		})
	}
}

func TestCollectMalformedDetailMetadata(t *testing.T) {
	for _, key := range []string{"ToRecipients", "CcRecipients", "BccRecipients", "Attachments", "UID", "InternetMessageId"} {
		for _, overlap := range []bool{false, true} {
			t.Run(key, func(t *testing.T) {
				h := header("M", "F")
				d := obj("ItemId", ident("M"), key, int64(19))
				if overlap {
					h.Keys = append(h.Keys, key)
					h.Values = append(h.Values, field(detailMetadata(), key))
				}
				r := collectDetail(t, h, d)
				if !hasLoss(r, outlookmail.CodeMailUnmapped) || (overlap && (!r.Doubtful || !hasLoss(r, "outlook_owa_detail_conflict"))) {
					t.Fatal("invalid detail silently ignored/fell back")
				}
				m := r.Messages[0]
				if len(m.Value.Recipients) != 0 || len(m.Value.Attachments) != 0 || m.Value.ICalUID != "" || m.Value.MessageID != "" {
					t.Fatal("invalid detail selected convenient header")
				}
			})
		}
	}

	for _, tc := range []struct {
		key   string
		value any
	}{
		{"ToRecipients", []any{int64(1)}},
		{"CcRecipients", []any{obj("Name", int64(1))}},
		{"BccRecipients", []any{nil}},
		{"Attachments", []any{obj("AttachmentId", ident(""))}},
		{"Attachments", []any{obj("AttachmentId", ident("A"), "Size", "bad")}},
	} {
		r := collectDetail(t, header("M", "F"), obj("ItemId", ident("M"), tc.key, tc.value))
		if !hasLoss(r, outlookmail.CodeMailUnmapped) {
			t.Fatal("malformed detail array/item not counted")
		}
	}
}

func TestDetailMapperVersionInvalidatesFingerprint(t *testing.T) {
	p := fixtureProfile(t, sourceTempDir(t), "Synthetic", "mail", true)
	current, fresh, err := Fingerprint(p)
	if err != nil {
		t.Fatal("synthetic current fingerprint failed")
	}
	saved := mapperVersion
	t.Cleanup(func() { mapperVersion = saved })
	mapperVersion = 1
	previous, previousFresh, err := Fingerprint(p)
	if err != nil || current == previous || !fresh.Equal(previousFresh) {
		t.Fatal("detail mapper change did not invalidate unchanged synthetic source fingerprint")
	}
}

func TestCollectDetailEmptyValuesAndHeaderAttachmentAuthority(t *testing.T) {
	h := obj("ItemId", ident("M"), "ParentFolderId", ident("F"), "HasAttachments", true)
	d := obj("ItemId", ident("M"), "HasAttachments", false, "ToRecipients", []any{}, "CcRecipients", []any{}, "BccRecipients", []any{}, "Attachments", []any{}, "UID", "", "InternetMessageId", "")
	r := collectDetail(t, h, d)
	m := r.Messages[0]
	if m.Value.HasAttachments == nil || !*m.Value.HasAttachments || len(r.Losses) != 0 || r.Doubtful || len(m.Value.Recipients) != 0 || len(m.Value.Attachments) != 0 || m.Value.ICalUID != "" || m.Value.MessageID != "" {
		t.Fatal("present empty detail values or authoritative header attachment flag changed")
	}
	for _, key := range []string{"ToRecipients", "CcRecipients", "BccRecipients", "Attachments", "UID", "InternetMessageId"} {
		h.Keys = append(h.Keys, key)
		h.Values = append(h.Values, field(detailMetadata(), key))
	}
	r = collectDetail(t, h, d)
	if !reflect.DeepEqual(r.Losses, []outlookmail.Loss{{Code: "outlook_owa_detail_conflict", Count: 6}}) || !r.Doubtful || len(r.Messages[0].Value.Recipients) != 0 || len(r.Messages[0].Value.Attachments) != 0 || r.Messages[0].Value.ICalUID != "" || r.Messages[0].Value.MessageID != "" {
		t.Fatal("present empty values silently fell back to header metadata")
	}
}

func TestCollectDetailPoisonedDuplicatesDoNotLeakMetadata(t *testing.T) {
	f := readerWith(header("M", "F"))
	d := detailMetadata()
	conflict := obj("ItemId", ident("M"), "UID", "Conflicting")
	f.values[3] = []any{d, conflict, d}
	r, err := collect(context.Background(), f, "Synthetic", false)
	if err != nil || len(r.Messages) != 1 || !r.Doubtful || !hasLoss(r, "outlook_owa_duplicate_conflict") || len(r.Messages[0].Value.Recipients) != 0 || len(r.Messages[0].Value.Attachments) != 0 || r.Messages[0].Value.ICalUID != "" || r.Messages[0].Value.MessageID != "" || r.Messages[0].Value.Body.State != "none" || !reflect.DeepEqual(r.SeenIDs, []string{"M"}) {
		t.Fatal("poisoned joined detail leaked metadata or lost seen header")
	}
}
