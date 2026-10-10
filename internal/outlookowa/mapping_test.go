package outlookowa

import (
	"reflect"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/outlookmail"
	"github.com/ourostack/m365crawl/internal/v8"
)

func obj(kv ...any) *v8.Object {
	if len(kv)%2 != 0 {
		panic("synthetic object requires key/value pairs")
	}
	o := &v8.Object{}
	for i := 0; i+1 < len(kv); i += 2 {
		o.Keys = append(o.Keys, kv[i].(string))
		o.Values = append(o.Values, kv[i+1])
	}
	return o
}

func ident(id string) *v8.Object { return obj("Id", id) }

func header(id, folder string) *v8.Object {
	return obj("ItemId", ident(id), "ParentFolderId", ident(folder), "HasAttachments", false)
}

func TestMapFolderExactKinds(t *testing.T) {
	for _, typ := range []string{"inbox", "archive", "drafts", "sent", "deleted", "SentItems", "DeletedItems", "junk", "custom", ""} {
		t.Run(typ, func(t *testing.T) {
			r := Result{}
			f, ok := mapFolder(obj("id", "Case/opaque==", "parentFolderId", "ROOT", "displayName", "Inbox", "distinguishedFolderType", typ), &r)
			want := typ
			if typ == "SentItems" {
				want = "sent"
			}
			if typ == "DeletedItems" {
				want = "deleted"
			}
			if typ == "junk" || typ == "custom" || typ == "" {
				want = "other"
			}
			if !ok || f.ID != "Case/opaque==" || f.ParentID != "ROOT" || f.Value.Kind != want || f.Value.Name != "Inbox" || f.Value.Generic {
				t.Fatalf("folder mapping: %+v, ok=%v", f, ok)
			}
		})
	}
	r := Result{}
	if _, ok := mapFolder(obj("Id", "wrong-case"), &r); ok || !r.Doubtful || len(r.Losses) == 0 {
		t.Fatal("case-inexact ID was accepted")
	}
}

func TestMapMessageApprovedFields(t *testing.T) {
	mailbox := func(name, address string) *v8.Object { return obj("Name", name, "EmailAddress", address) }
	h := header("Opaque/Message==", "Inbox-ID")
	h.Keys = append(h.Keys, "Subject", "Preview", "From", "IsRead", "Flag", "Importance", "DateTimeReceived", "DateTimeSent", "ItemClass", "InternetMessageId", "UID", "ConversationId", "ToRecipients", "CcRecipients", "BccRecipients", "ReplyTo", "Attachments")
	h.Values = append(h.Values, "Synthetic subject", "Synthetic preview", obj("Mailbox", mailbox("Synthetic Sender", "sender@example.invalid")), false, obj("FlagStatus", "fLaGgEd"), "HIGH", "2026-10-01T10:00:00Z", "2026-10-01T09:00:00+00:00", "IPM.Note", "<synthetic@example.invalid>", "AbC-UID", ident("Conversation/Case"), []any{mailbox("To One", "one@example.invalid"), mailbox("To Two", "two@example.invalid")}, []any{mailbox("Cc", "cc@example.invalid")}, []any{mailbox("Bcc", "bcc@example.invalid")}, []any{mailbox("Not recipient", "reply@example.invalid")}, []any{obj("AttachmentId", ident("Attachment/Case"), "Name", "inline.png", "ContentType", "image/png", "IsInline", true)})
	r := Result{}
	m, ok := mapMessage(h, "outlook/Fixture", &r)
	if !ok {
		t.Fatal("valid header rejected")
	}
	v := m.Value
	if m.ID != "Opaque/Message==" || m.FolderID != "Inbox-ID" || v.Account != "outlook/Fixture" || v.Subject != "Synthetic subject" || v.Preview != "Synthetic preview" || v.SenderName != "Synthetic Sender" || v.SenderAddress != "sender@example.invalid" || v.ReadState != "unread" || v.Unread == nil || !*v.Unread || v.Flag != "flagged" || v.Importance != "high" || v.ICalUID != "abc-uid" || v.ConversationID != "Conversation/Case" || v.Class != "IPM.Note" || v.MessageID != "<synthetic@example.invalid>" || v.InReplyTo != "" || v.ToMe || v.HasAttachments == nil || *v.HasAttachments {
		t.Fatalf("approved header mapping: %+v", v)
	}
	if !v.Received.Equal(time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)) || !v.Sent.Equal(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)) {
		t.Fatal("date mapping")
	}
	var roles, names []string
	for _, rc := range v.Recipients {
		roles = append(roles, rc.Role)
		names = append(names, rc.Name)
		if rc.KindRaw != 0 {
			t.Fatal("invented recipient kind")
		}
	}
	if !reflect.DeepEqual(roles, []string{"to", "to", "cc", "bcc"}) || !reflect.DeepEqual(names, []string{"To One", "To Two", "Cc", "Bcc"}) {
		t.Fatalf("recipients: %+v", v.Recipients)
	}
	if !reflect.DeepEqual(m.AttachmentIDs, []string{"Attachment/Case"}) || len(v.Attachments) != 1 || v.Attachments[0].Downloaded || v.Attachments[0].Size != 0 || !v.Attachments[0].Inline || v.Attachments[0].Path != "" {
		t.Fatal("attachment metadata mapping")
	}
	if len(r.Losses) != 0 {
		t.Fatalf("valid mapping losses: %+v", r.Losses)
	}
}

func TestMapUnknownsAndLosses(t *testing.T) {
	for _, read := range []any{nil, v8.Undefined{}, "true", true, false} {
		r := Result{}
		h := header("M", "F")
		h.Keys = append(h.Keys, "IsRead", "Flag", "Importance", "DateTimeReceived", "DateTimeSent")
		h.Values = append(h.Values, read, obj("FlagStatus", "unobserved"), v8.Undefined{}, "invalid synthetic date", "invalid synthetic date")
		m, ok := mapMessage(h, "A", &r)
		if !ok || m.Value.Flag != "unknown" || m.Value.Importance != "unknown" || !m.Value.Received.IsZero() || !m.Value.Sent.IsZero() {
			t.Fatal("optional values were invented")
		}
		b, isBool := read.(bool)
		if isBool {
			want := "unread"
			if b {
				want = "read"
			}
			if m.Value.ReadState != want || m.Value.Unread == nil || *m.Value.Unread == b {
				t.Fatal("read state")
			}
		} else if m.Value.ReadState != "unknown" || m.Value.Unread != nil {
			t.Fatal("unknown read state")
		}
		if len(r.Losses) == 0 {
			t.Fatal("invalid timestamps not counted")
		}
	}
	for _, flag := range []string{"NotFlagged", "NONE", "COMPLETE"} {
		r := Result{}
		h := header("M", "F")
		h.Keys = append(h.Keys, "Flag")
		h.Values = append(h.Values, obj("FlagStatus", flag))
		m, _ := mapMessage(h, "A", &r)
		want := "none"
		if flag == "COMPLETE" {
			want = "complete"
		}
		if m.Value.Flag != want {
			t.Fatalf("flag %s = %s", flag, m.Value.Flag)
		}
	}
	for _, h := range []any{nil, obj("ItemId", ident("")), obj("ItemId", ident("M"), "ParentFolderId", ident("")), obj("ItemId", obj("Id", float64(12)))} {
		r := Result{}
		if _, ok := mapMessage(h, "A", &r); ok || !r.Doubtful {
			t.Fatal("invalid required identity accepted")
		}
	}
}

func TestMapBodySparseAndTruncated(t *testing.T) {
	for _, tc := range []struct {
		value       any
		state, html string
	}{
		{nil, "none", ""},
		{v8.Undefined{}, "none", ""},
		{obj("Body", nil), "none", ""},
		{obj("NormalizedBody", obj("Value", "<p>synthetic cached HTML</p>"), "IsTruncated", true), "inline", "<p>synthetic cached HTML</p>"},
	} {
		r := Result{}
		b := mapBody(tc.value, &r)
		if b.State != tc.state || string(b.HTML) != tc.html {
			t.Fatal("sparse/truncated body mapping")
		}
	}
}

func TestTypedShapesAreNotCoerced(t *testing.T) {
	for _, h := range []*v8.Object{
		obj("ItemId", "M"),
		obj("ItemId", ident("M"), "ParentFolderId", "F"),
		obj("ItemId", ident(" "), "ParentFolderId", ident("F")),
	} {
		r := Result{}
		if _, ok := mapMessage(h, "A", &r); ok || !r.Doubtful || len(r.Losses) == 0 {
			t.Fatal("malformed identity accepted")
		}
	}

	for _, kv := range [][]any{
		{"Subject", 12}, {"From", 12}, {"From", obj("Mailbox", 12)}, {"From", obj("Mailbox", obj("Name", true))},
		{"IsRead", 12}, {"Flag", 12}, {"Flag", obj("FlagStatus", 12)}, {"Importance", 12},
		{"ConversationId", 12}, {"ConversationId", obj("Id", 12)}, {"DateTimeReceived", 12},
		{"ToRecipients", 12}, {"CcRecipients", []any{12}}, {"BccRecipients", []any{obj("Name", 12)}},
		{"Attachments", 12}, {"Attachments", []any{12}}, {"Attachments", []any{obj("AttachmentId", ident(""))}},
		{"Attachments", []any{obj("AttachmentId", ident("A"), "Size", "12", "IsInline", "true")}},
		{"Attachments", []any{obj("AttachmentId", ident("A"), "Size", float64(-1))}},
		{"Attachments", []any{obj("AttachmentId", ident("A"), "Size", float64(1.25))}},
		{"Attachments", []any{obj("AttachmentId", ident("A"), "Size", float64(1<<63))}},
	} {
		r := Result{}
		h := header("M", "F")
		h.Keys = append(h.Keys, kv[0].(string))
		h.Values = append(h.Values, kv[1])
		m, ok := mapMessage(h, "A", &r)
		if !ok || len(r.Losses) == 0 {
			t.Fatalf("bad optional %s did not count a loss", kv[0])
		}
		if m.Value.ToMe || m.Value.InReplyTo != "" {
			t.Fatal("invented data")
		}
	}
	for _, v := range []any{nil, v8.Undefined{}, "false", 12} {
		r := Result{}
		h := obj("ItemId", ident("M"), "ParentFolderId", ident("F"), "HasAttachments", v)
		m, _ := mapMessage(h, "A", &r)
		if m.Value.HasAttachments != nil || len(r.Losses) == 0 {
			t.Fatal("missing/bad authoritative attachment flag")
		}
	}
	r := Result{}
	m, _ := mapMessage(obj("ItemId", ident("M"), "ParentFolderId", ident("F"), "HasAttachments", true, "Attachments", []any{obj("AttachmentId", ident("A"), "Size", float64(12), "IsInline", false)}), "A", &r)
	if m.Value.HasAttachments == nil || !*m.Value.HasAttachments || m.Value.Attachments[0].Size != 12 {
		t.Fatal("true attachment flag or typed size lost")
	}
	for _, v := range []any{12, obj("NormalizedBody", 12), obj("NormalizedBody", obj("Value", 12))} {
		r := Result{}
		b := mapBody(v, &r)
		if b.State != "none" || len(r.Losses) == 0 {
			t.Fatal("malformed body accepted")
		}
	}
	r = Result{}
	f, ok := mapFolder(obj("id", "F", "parentFolderId", "R", "displayName", 12, "distinguishedFolderType", 12), &r)
	if !ok || f.Value.Name != "" || f.Value.Kind != "other" || len(r.Losses) == 0 {
		t.Fatal("folder fields coerced")
	}
}

func TestMapAttachmentConflictsDoNotPickWinner(t *testing.T) {
	r := Result{}
	a := obj("AttachmentId", ident("A"), "Name", "first.txt")
	b := obj("AttachmentId", ident("A"), "Name", "conflicting.txt")
	h := header("M", "F")
	h.Keys = append(h.Keys, "Attachments")
	h.Values = append(h.Values, []any{a, a, b, a, obj("AttachmentId", ident("B"), "Name", "kept.txt")})
	m, ok := mapMessage(h, "A", &r)
	if !ok || !reflect.DeepEqual(m.AttachmentIDs, []string{"B"}) || len(m.Value.Attachments) != 1 || m.Value.Attachments[0].Name != "kept.txt" || !r.Doubtful || !hasLoss(r, "outlook_owa_duplicate_conflict") {
		t.Fatal("conflicting attachment arbitrarily won")
	}
}

func TestV8IntegerAttachmentSizes(t *testing.T) {
	for _, n := range []int64{0, 42, 2147483647} {
		r := Result{}
		h := header("M", "F")
		h.Keys = append(h.Keys, "Attachments")
		h.Values = append(h.Values, []any{obj("AttachmentId", ident("A"), "Size", n)})
		m, _ := mapMessage(h, "A", &r)
		if m.Value.Attachments[0].Size != n || len(r.Losses) != 0 {
			t.Fatalf("V8 integer size rejected: %d", n)
		}
	}
	r := Result{}
	h := header("M", "F")
	h.Keys = append(h.Keys, "Attachments")
	h.Values = append(h.Values, []any{obj("AttachmentId", ident("A"), "Size", int64(-1))})
	m, _ := mapMessage(h, "A", &r)
	if m.Value.Attachments[0].Size != 0 || len(r.Losses) == 0 {
		t.Fatal("negative integer size accepted")
	}
}

func TestUnavailableBodyDoesNotCreateLoss(t *testing.T) {
	for _, v := range []any{nil, v8.Undefined{}, (*v8.Object)(nil), obj("NormalizedBody", nil), obj("NormalizedBody", v8.Undefined{})} {
		r := Result{}
		b := mapBody(v, &r)
		if b.State != "none" || len(r.Losses) != 0 {
			t.Fatal("unavailable body was treated as malformed")
		}
	}
	r := Result{}
	b := mapBody(obj("NormalizedBody", obj("Value", "\xff")), &r)
	if b.State != "none" || len(r.Losses) == 0 {
		t.Fatal("invalid UTF-8 became inline HTML")
	}
}

func TestMapUnsupportedFlagStatusIsLossy(t *testing.T) {
	h := header("M", "F")
	h.Keys = append(h.Keys, "Flag", "DateTimeReceived", "DateTimeSent")
	h.Values = append(h.Values, obj("FlagStatus", "unobserved"), "2026-10-01T10:00:00Z", "2026-10-01T09:00:00Z")
	r := Result{}
	m, ok := mapMessage(h, "Synthetic", &r)
	if !ok || m.Value.Flag != "unknown" || m.Value.Received.IsZero() || m.Value.Sent.IsZero() {
		t.Fatal("unsupported optional flag must keep the otherwise valid header with unknown flag")
	}
	want := []outlookmail.Loss{{Code: outlookmail.CodeMailUnmapped, Count: 1}}
	if !reflect.DeepEqual(r.Losses, want) {
		t.Errorf("unsupported flag fixed loss: got %+v, want %+v", r.Losses, want)
	}
	if !r.Doubtful {
		t.Fatal("unsupported nonempty flag must make attribution doubtful")
	}
}

func TestMapFlagStatusCompatibility(t *testing.T) {
	for _, tc := range []struct {
		name string
		flag any
		want string
	}{
		{"absent-flag", nil, "unknown"},
		{"undefined-flag", v8.Undefined{}, "unknown"},
		{"absent-status", obj(), "unknown"},
		{"null-status", obj("FlagStatus", nil), "unknown"},
		{"undefined-status", obj("FlagStatus", v8.Undefined{}), "unknown"},
		{"empty-status", obj("FlagStatus", ""), "unknown"},
		{"not-flagged", obj("FlagStatus", "NotFlagged"), "none"},
		{"casefolded-not-flagged", obj("FlagStatus", "nOtFlAgGeD"), "none"},
		{"flagged", obj("FlagStatus", "Flagged"), "flagged"},
		{"casefolded-flagged", obj("FlagStatus", "fLaGgEd"), "flagged"},
		{"complete", obj("FlagStatus", "Complete"), "complete"},
		{"casefolded-complete", obj("FlagStatus", "cOmPlEtE"), "complete"},
		{"existing-none-alias", obj("FlagStatus", "NONE"), "none"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := header("M", "F")
			if tc.flag != nil {
				h.Keys = append(h.Keys, "Flag")
				h.Values = append(h.Values, tc.flag)
			}
			r := Result{}
			m, ok := mapMessage(h, "Synthetic", &r)
			if !ok || m.Value.Flag != tc.want || len(r.Losses) != 0 || r.Doubtful {
				t.Fatalf("flag compatibility: mapped=%v, flag=%s, losses=%+v, doubtful=%v", ok, m.Value.Flag, r.Losses, r.Doubtful)
			}
		})
	}
}
