package outlookowa

import (
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ourostack/m365crawl/internal/outlookmail"
	"github.com/ourostack/m365crawl/internal/v8"
)

func field(v any, key string) any {
	o, ok := v.(*v8.Object)
	if !ok || o == nil {
		return nil
	}
	for i, k := range o.Keys {
		if k == key && i < len(o.Values) {
			if _, missing := o.Values[i].(v8.Undefined); missing {
				return nil
			}
			return o.Values[i]
		}
	}
	return nil
}

func (r *Result) loss(code string) {
	for i := range r.Losses {
		if r.Losses[i].Code == code {
			r.Losses[i].Count++
			return
		}
	}
	r.Losses = append(r.Losses, outlookmail.Loss{Code: code, Count: 1})
}

func validID(s string) bool { return strings.TrimSpace(s) != "" && utf8.ValidString(s) }

func (r *Result) path(v any, keys ...string) any {
	for _, k := range keys {
		if v == nil {
			return nil
		}
		if _, undefined := v.(v8.Undefined); undefined {
			return nil
		}
		o, ok := v.(*v8.Object)
		if ok && o == nil {
			return nil
		}
		if !ok {
			r.loss(outlookmail.CodeMailUnmapped)
			return nil
		}
		v = field(v, k)
	}
	return v
}

func (r *Result) text(v any, keys ...string) string {
	v = r.path(v, keys...)
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok && utf8.ValidString(s) {
		return s
	}
	r.Notes.BadString++
	r.loss(outlookmail.CodeMailUnmapped)
	return ""
}

func (r *Result) boolean(v any) *bool {
	if v == nil {
		return nil
	}
	if b, ok := v.(bool); ok {
		return &b
	}
	r.loss(outlookmail.CodeMailUnmapped)
	return nil
}

func (r *Result) date(v any, key string) time.Time {
	s := r.text(v, key)
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		r.loss(outlookmail.CodeMailUnmapped)
		return time.Time{}
	}
	return t.UTC()
}

func (r *Result) list(v any) []any {
	if v == nil {
		return nil
	}
	if a, ok := v.([]any); ok {
		return a
	}
	r.loss(outlookmail.CodeMailUnmapped)
	return nil
}

func mapFolder(v any, r *Result) (Folder, bool) {
	f := Folder{ID: r.text(v, "id"), ParentID: r.text(v, "parentFolderId")}
	if !validID(f.ID) || !validID(f.ParentID) {
		r.Notes.Unmapped++
		r.Doubtful = true
		r.loss(outlookmail.CodeMailUnmapped)
		return Folder{}, false
	}
	f.Value = outlookmail.Folder{Name: r.text(v, "displayName"), Kind: "other"}
	switch typ := strings.ToLower(r.text(v, "distinguishedFolderType")); typ {
	case "inbox", "archive", "drafts", "sent", "deleted":
		f.Value.Kind = typ
	case "sentitems":
		f.Value.Kind = "sent"
	case "deleteditems":
		f.Value.Kind = "deleted"
	}
	return f, true
}

func mapMessage(v any, account string, r *Result) (Message, bool) {
	m := Message{ID: r.text(v, "ItemId", "Id"), FolderID: r.text(v, "ParentFolderId", "Id")}
	if !validID(m.ID) || !validID(m.FolderID) {
		r.Notes.Unmapped++
		r.Doubtful = true
		r.loss(outlookmail.CodeMailUnmapped)
		return Message{}, false
	}
	h := outlookmail.Header{
		Subject: r.text(v, "Subject"), Preview: r.text(v, "Preview"),
		SenderName: r.text(v, "From", "Mailbox", "Name"), SenderAddress: r.text(v, "From", "Mailbox", "EmailAddress"),
		ConversationID: r.text(v, "ConversationId", "Id"), ICalUID: strings.ToLower(r.text(v, "UID")),
		Received: r.date(v, "DateTimeReceived"), ReadState: "unknown", Flag: "unknown", Importance: "unknown",
		HasAttachments: r.boolean(field(v, "HasAttachments")),
	}
	if h.HasAttachments == nil {
		r.loss(outlookmail.CodeMailLayoutPartial)
	}
	if read := r.boolean(field(v, "IsRead")); read != nil {
		unread := !*read
		h.Unread = &unread
		h.ReadState = "unread"
		if *read {
			h.ReadState = "read"
		}
	}
	switch flag := strings.ToLower(r.text(v, "Flag", "FlagStatus")); flag {
	case "none", "notflagged":
		h.Flag = "none"
	case "flagged", "complete":
		h.Flag = flag
	default:
		if flag != "" {
			r.loss(outlookmail.CodeMailUnmapped)
			r.Doubtful = true
		}
	}
	if importance := strings.ToLower(r.text(v, "Importance")); importance != "" {
		h.Importance = importance
	}
	for _, role := range []struct{ key, role string }{{"ToRecipients", "to"}, {"CcRecipients", "cc"}, {"BccRecipients", "bcc"}} {
		for _, rc := range r.list(field(v, role.key)) {
			if o, ok := rc.(*v8.Object); !ok || o == nil {
				r.loss(outlookmail.CodeMailUnmapped)
				continue
			}
			h.Recipients = append(h.Recipients, outlookmail.Recipient{Name: r.text(rc, "Name"), Address: r.text(rc, "EmailAddress"), Role: role.role})
		}
	}
	m.Value = outlookmail.Message{Account: account, Header: h, Detail: outlookmail.Detail{Sent: r.date(v, "DateTimeSent"), MessageID: r.text(v, "InternetMessageId"), Class: r.text(v, "ItemClass")}, Body: outlookmail.Body{State: "none"}, Copies: 1}
	attachments := newRecords()
	var attachmentOrder []string
	for _, a := range r.list(field(v, "Attachments")) {
		id := r.text(a, "AttachmentId", "Id")
		if !validID(id) {
			r.loss(outlookmail.CodeMailUnmapped)
			continue
		}
		if _, exists := attachments.values[id]; !exists && !attachments.conflicts[id] {
			attachmentOrder = append(attachmentOrder, id)
		}
		attachments.put(id, a, r)
	}
	for _, id := range attachmentOrder {
		a, exists := attachments.values[id]
		if !exists {
			continue
		}
		at := outlookmail.Attachment{Name: r.text(a, "Name"), ContentType: r.text(a, "ContentType")}
		if inline := r.boolean(field(a, "IsInline")); inline != nil {
			at.Inline = *inline
		}
		if size := field(a, "Size"); size != nil {
			switch n := size.(type) {
			case int64:
				if n >= 0 {
					at.Size = n
				} else {
					r.loss(outlookmail.CodeMailUnmapped)
				}
			case float64:
				if n >= 0 && n < math.Exp2(63) && n == math.Trunc(n) {
					at.Size = int64(n)
				} else {
					r.loss(outlookmail.CodeMailUnmapped)
				}
			default:
				r.loss(outlookmail.CodeMailUnmapped)
			}
		}
		m.AttachmentIDs = append(m.AttachmentIDs, id)
		m.Value.Attachments = append(m.Value.Attachments, at)
	}
	return m, true
}

func mapBody(v any, r *Result) outlookmail.Body {
	b := outlookmail.Body{State: "none"}
	value := r.path(v, "NormalizedBody", "Value")
	if value == nil {
		return b
	}
	s := r.text(value)
	if raw, ok := value.(string); !ok || !utf8.ValidString(raw) {
		return b
	}
	b.HTML = []byte(s)
	b.State = "inline"
	return b
}
