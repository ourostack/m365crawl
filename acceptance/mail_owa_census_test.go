//go:build acceptance

package acceptance

import (
	"errors"
	"math"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ourostack/m365crawl/internal/indexeddb"
	"github.com/ourostack/m365crawl/internal/v8"
)

type owaRawFolder struct{ parent, name, kind string }
type owaRawMessage struct {
	folder      string
	fields      []any
	recipients  [][]any
	attachments map[string][]any
	html        string
	inline      bool
}
type owaRawCensus struct {
	folders  map[string]owaRawFolder
	messages map[string]owaRawMessage
	bodies   map[string]any
}

// This path never calls the importer, its field accessors, Collect or Normalize. Metadata is selected independently; only the three case-exact content stores can expose values.
func owaCensus(level, blob string) (c owaRawCensus, err error) {
	c = owaRawCensus{folders: map[string]owaRawFolder{}, messages: map[string]owaRawMessage{}, bodies: map[string]any{}}
	_, dbs, err := indexeddb.Census(level)
	if err != nil {
		return c, errors.New("census_metadata_refused")
	}
	var selected []indexeddb.Database
	for _, db := range dbs {
		if db.Name == "owa-offline-data" || strings.HasPrefix(db.Name, "owa-offline-data-") {
			selected = append(selected, db)
		}
	}
	if len(selected) != 1 {
		return c, errors.New("census_layout_refused")
	}
	db := selected[0]
	stores := map[string]int64{}
	for _, s := range db.Stores {
		if s.Name == "folders" || s.Name == "messages" || s.Name == "messageBodies" {
			if _, duplicate := stores[s.Name]; duplicate {
				return c, errors.New("census_layout_refused")
			}
			stores[s.Name] = s.ID
		}
	}
	if len(stores) != 3 {
		return c, errors.New("census_layout_refused")
	}
	o, err := indexeddb.OpenWith(level, blob, indexeddb.OpenOptions{KeepStore: func(d, s int64) bool {
		return d == db.ID && (s == stores["folders"] || s == stores["messages"] || s == stores["messageBodies"])
	}})
	if err != nil {
		return c, errors.New("census_open_refused")
	}
	defer func() {
		if closeErr := o.Close(); closeErr != nil {
			err = errors.New("census_close_refused")
		}
	}()
	if o.Stats().TruncatedLogTails != 0 {
		return c, errors.New("census_decoder_refused")
	}
	raw := map[string]map[string]any{}
	for _, name := range []string{"folders", "messages", "messageBodies"} {
		raw[name] = map[string]any{}
		err := o.Records(db.ID, stores[name], func(rec indexeddb.Record) error {
			if rec.Err != nil {
				return errors.New("census_decoder_refused")
			}
			payload, err := o.Payload(db.ID, rec.Raw)
			if err != nil {
				return errors.New("census_decoder_refused")
			}
			value, err := v8.Deserialize(payload)
			if err != nil {
				return errors.New("census_decoder_refused")
			}
			f := owaFields{}
			id := f.text(value, "ItemId", "Id")
			if name == "folders" {
				id = f.text(value, "id")
			}
			if name == "messageBodies" && f.get(value, "ItemId") == nil {
				id, _ = rec.Key.(string)
			}
			if f.err != nil || strings.TrimSpace(id) == "" {
				return errors.New("census_identity_refused")
			}
			if old, exists := raw[name][id]; exists && !reflect.DeepEqual(old, value) {
				return errors.New("census_duplicate_refused")
			}
			raw[name][id] = value
			return nil
		})
		if err != nil {
			return c, err
		}
	}
	for id, value := range raw["folders"] {
		f := owaFields{}
		folder := owaRawFolder{parent: f.text(value, "parentFolderId"), name: f.text(value, "displayName"), kind: "other"}
		switch kind := strings.ToLower(f.text(value, "distinguishedFolderType")); kind {
		case "inbox", "archive", "drafts", "sent", "deleted":
			folder.kind = kind
		case "sentitems":
			folder.kind = "sent"
		case "deleteditems":
			folder.kind = "deleted"
		}
		if f.err != nil || strings.TrimSpace(folder.parent) == "" {
			return c, errors.New("census_folder_refused")
		}
		c.folders[id] = folder
	}
	c.bodies = raw["messageBodies"]
	for id, value := range raw["messages"] {
		m, err := owaRawHeader(value, c.bodies[id])
		if err != nil {
			return c, err
		}
		if _, exists := c.folders[m.folder]; !exists {
			return c, errors.New("census_folder_join_refused")
		}
		c.messages[id] = m
	}
	if _, err := c.root(); err != nil {
		return c, err
	}
	return c, nil
}

func (c owaRawCensus) root() (string, error) {
	roots := map[string]bool{}
	for id := range c.folders {
		seen := map[string]bool{}
		for {
			if seen[id] {
				return "", errors.New("census_folder_cycle_refused")
			}
			seen[id] = true
			f, exists := c.folders[id]
			if !exists {
				roots[id] = true
				break
			}
			id = f.parent
		}
	}
	if len(roots) != 1 {
		return "", errors.New("census_folder_root_refused")
	}
	for id := range roots {
		return id, nil
	}
	return "", errors.New("census_folder_root_refused")
}

// Typed reads deliberately refuse new malformed shapes, instead of turning decoder/layout loss into successful zero coverage.
type owaFields struct{ err error }

func (f *owaFields) get(value any, path ...string) any {
	for _, key := range path {
		if value == nil {
			return nil
		}
		object, ok := value.(*v8.Object)
		if !ok || object == nil || len(object.Keys) != len(object.Values) {
			f.err = errors.New("census_field_type_refused")
			return nil
		}
		value = nil
		found := false
		for i, k := range object.Keys {
			if k == key {
				if found {
					f.err = errors.New("census_duplicate_field_refused")
				}
				value, found = object.Values[i], true
			}
		}
		if _, undefined := value.(v8.Undefined); undefined {
			value = nil
		}
	}
	return value
}

func (f *owaFields) text(value any, path ...string) string {
	v := f.get(value, path...)
	if v == nil {
		return ""
	}
	s, ok := v.(string)
	if !ok || !utf8.ValidString(s) {
		f.err = errors.New("census_text_refused")
	}
	return s
}

func (f *owaFields) bool(value any, path ...string) any {
	v := f.get(value, path...)
	if v == nil {
		return nil
	}
	b, ok := v.(bool)
	if !ok {
		f.err = errors.New("census_boolean_refused")
		return nil
	}
	if b {
		return int64(1)
	}
	return int64(0)
}

func (f *owaFields) list(value any, key string) []any {
	v := f.get(value, key)
	if v == nil {
		return nil
	}
	a, ok := v.([]any)
	if !ok {
		f.err = errors.New("census_array_refused")
	}
	return a
}

func (f *owaFields) date(value any, key string) any {
	s := f.text(value, key)
	if s == "" {
		return nil
	}
	at, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		f.err = errors.New("census_date_refused")
		return nil
	}
	return at.UTC().Format(time.RFC3339Nano)
}

var owaMessageFields = []string{"internet_message_id", "conversation_id", "to_me", "subject", "sender_name", "sender_address", "preview", "in_reply_to", "received_at", "sent_at", "class", "importance", "ical_uid", "is_read", "read_state", "flag", "has_attachments", "body_state"}

func owaRawHeader(value, body any) (owaRawMessage, error) {
	f := owaFields{}
	m := owaRawMessage{folder: f.text(value, "ParentFolderId", "Id"), attachments: map[string][]any{}}
	read := f.bool(value, "IsRead")
	state := "unknown"
	if read != nil {
		state = "unread"
		if read == int64(1) {
			state = "read"
		}
	}
	flag := strings.ToLower(f.text(value, "Flag", "FlagStatus"))
	switch flag {
	case "", "none", "notflagged":
		if flag == "" {
			flag = "unknown"
		} else {
			flag = "none"
		}
	case "flagged", "complete":
	default:
		f.err = errors.New("census_flag_refused")
	}
	importance := strings.ToLower(f.text(value, "Importance"))
	if importance == "" {
		importance = "unknown"
	}
	has := f.bool(value, "HasAttachments")
	if has == nil {
		return m, errors.New("census_attachment_flag_refused")
	}
	bodyState := "none"
	if v := f.get(body, "NormalizedBody", "Value"); v != nil {
		m.html = f.text(v)
		m.inline = true
		bodyState = "inline"
	}
	m.fields = []any{
		f.text(value, "InternetMessageId"), f.text(value, "ConversationId", "Id"), int64(0),
		f.text(value, "Subject"), f.text(value, "From", "Mailbox", "Name"), f.text(value, "From", "Mailbox", "EmailAddress"),
		f.text(value, "Preview"), "", f.date(value, "DateTimeReceived"), f.date(value, "DateTimeSent"),
		f.text(value, "ItemClass"), importance, strings.ToLower(f.text(value, "UID")), read, state, flag, has, bodyState,
	}
	for _, role := range []struct{ field, name string }{{"ToRecipients", "to"}, {"CcRecipients", "cc"}, {"BccRecipients", "bcc"}} {
		for _, r := range f.list(value, role.field) {
			name, address := f.text(r, "Name"), f.text(r, "EmailAddress")
			var addr any
			if address != "" {
				addr = address
			}
			m.recipients = append(m.recipients, []any{int64(len(m.recipients)), name, addr, int64(0), role.name})
		}
	}
	for _, a := range f.list(value, "Attachments") {
		id := f.text(a, "AttachmentId", "Id")
		if strings.TrimSpace(id) == "" {
			return m, errors.New("census_attachment_identity_refused")
		}
		var size int64
		switch n := f.get(a, "Size").(type) {
		case nil:
		case int64:
			if n < 0 {
				return m, errors.New("census_size_refused")
			}
			size = n
		case float64:
			if n < 0 || n >= math.Exp2(63) || n != math.Trunc(n) {
				return m, errors.New("census_size_refused")
			}
			size = int64(n)
		default:
			return m, errors.New("census_size_refused")
		}
		inline := f.bool(a, "IsInline")
		if inline == nil {
			inline = int64(0)
		}
		fields := []any{f.text(a, "Name"), size, f.text(a, "ContentType"), inline, int64(0)}
		if old, exists := m.attachments[id]; exists && !reflect.DeepEqual(old, fields) {
			return m, errors.New("census_duplicate_refused")
		}
		m.attachments[id] = fields
	}
	if f.err != nil {
		return m, f.err
	}
	return m, nil
}

func (c owaRawCensus) counts() map[string]int {
	n := map[string]int{"messages": len(c.messages), "folders": len(c.folders), "body_records": len(c.bodies), "body_joined": 0, "body_missing": 0, "body_orphan": 0, "body_inline": 0, "body_none": 0, "recipients": 0, "to": 0, "cc": 0, "bcc": 0, "read": 0, "unread": 0, "unknown": 0, "attachments": 0, "size_zero": 0, "inline": 0, "has_attachments": 0, "conversation_present": 0}
	folders, conversations := map[string]bool{}, map[string]bool{}
	for id, folder := range c.folders {
		folders[id], folders[folder.parent] = true, true
	}
	n["folder_ids"] = len(folders)
	for id, m := range c.messages {
		if _, joined := c.bodies[id]; joined {
			n["body_joined"]++
		} else {
			n["body_missing"]++
		}
		n["body_"+m.fields[17].(string)]++
		n[m.fields[14].(string)]++
		if m.fields[16] == int64(1) {
			n["has_attachments"]++
		}
		if conv := m.fields[1].(string); conv != "" {
			n["conversation_present"]++
			conversations[conv] = true
		}
		for _, r := range m.recipients {
			n["recipients"]++
			n[r[4].(string)]++
		}
		for _, a := range m.attachments {
			n["attachments"]++
			if a[1] == int64(0) {
				n["size_zero"]++
			}
			if a[3] == int64(1) {
				n["inline"]++
			}
		}
	}
	for id := range c.bodies {
		if _, joined := c.messages[id]; !joined {
			n["body_orphan"]++
		}
	}
	n["conversation_groups"] = len(conversations)
	return n
}

func (c owaRawCensus) clone() owaRawCensus {
	copy := c
	copy.messages = map[string]owaRawMessage{}
	for id, m := range c.messages {
		copy.messages[id] = m
	}
	return copy
}
