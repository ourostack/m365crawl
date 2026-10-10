//go:build acceptance

package acceptance

import (
	"context"
	"errors"
	"math"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ourostack/m365crawl/internal/indexeddb"
	"github.com/ourostack/m365crawl/internal/teamsdesktop"
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
	text        string
	textKnown   bool
	detail      owaDetailInventory
}
type owaRawCensus struct {
	folders  map[string]owaRawFolder
	messages map[string]owaRawMessage
	bodies   map[string]any
}

// This path never calls the importer, its field accessors, Collect or Normalize. Metadata is selected independently; only the three case-exact content stores can expose values.
func owaCensus(level, blob string) (c owaRawCensus, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), owaWorkTimeout)
	defer cancel()
	return owaCensusContext(ctx, level, blob)
}

func owaCensusContext(ctx context.Context, level, blob string) (c owaRawCensus, err error) {
	c = owaRawCensus{folders: map[string]owaRawFolder{}, messages: map[string]owaRawMessage{}, bodies: map[string]any{}}
	if err := owaCheckSource(ctx, teamsdesktop.Source{LevelDBDir: level, BlobDir: blob}); err != nil {
		return c, err
	}
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
	var recordCount, decodedBytes int64
	for _, name := range []string{"folders", "messages", "messageBodies"} {
		raw[name] = map[string]any{}
		err := o.Records(db.ID, stores[name], func(rec indexeddb.Record) error {
			if ctx.Err() != nil {
				return errors.New("census_context_refused")
			}
			recordCount++
			if recordCount > owaMaxQueryRows || len(rec.Raw) > owaMaxRawValueBytes {
				return errors.New("census_resource_refused")
			}
			if rec.Err != nil {
				return errors.New("census_decoder_refused")
			}
			payload, err := o.Payload(db.ID, rec.Raw)
			if err != nil {
				return errors.New("census_decoder_refused")
			}
			if len(payload) > owaMaxPayloadBytes || int64(len(payload)) > owaMaxDecodedBytes-decodedBytes {
				return errors.New("census_resource_refused")
			}
			decodedBytes += int64(len(payload))
			value, err := v8.Deserialize(payload)
			if err != nil {
				return errors.New("census_decoder_refused")
			}
			f := owaFields{ctx: ctx}
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
		f := owaFields{ctx: ctx}
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
		m, err := owaRawHeaderContext(ctx, value, c.bodies[id])
		if err != nil {
			return c, err
		}
		if _, exists := c.folders[m.folder]; !exists {
			return c, errors.New("census_folder_join_refused")
		}
		c.messages[id] = m
	}
	if _, err := c.rootContext(ctx); err != nil {
		return c, err
	}
	return c, nil
}

func (c owaRawCensus) root() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), owaWorkTimeout)
	defer cancel()
	return c.rootContext(ctx)
}

func (c owaRawCensus) rootContext(ctx context.Context) (string, error) {
	roots := map[string]bool{}
	for id := range c.folders {
		seen := map[string]bool{}
		for {
			if ctx.Err() != nil {
				return "", errors.New("census_context_refused")
			}
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
type owaFields struct {
	err error
	ctx context.Context
}

func (f *owaFields) get(value any, path ...string) any {
	if f.ctx != nil && f.ctx.Err() != nil {
		f.err = errors.New("census_context_refused")
		return nil
	}
	for _, key := range path {
		if value == nil {
			return nil
		}
		object, ok := value.(*v8.Object)
		if !ok || object == nil || len(object.Keys) != len(object.Values) {
			f.err = errors.New("census_field_type_refused")
			return nil
		}
		if len(object.Keys) > owaMaxQueryRows {
			f.err = errors.New("census_resource_refused")
			return nil
		}
		value = nil
		found := false
		for i, k := range object.Keys {
			if f.ctx != nil && f.ctx.Err() != nil {
				f.err = errors.New("census_context_refused")
				return nil
			}
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
		return nil
	}
	if len(a) > owaMaxQueryRows {
		f.err = errors.New("census_resource_refused")
		return nil
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

type owaDetailInventory struct {
	present, unproved []string
	selected          map[string]any
}

// The six-field contract is independently applied to exact raw values: unavailable means null/undefined/absent, empty values are present, equal overlap is used once and unequal overlap is not clean coverage. Other header fields retain their authoritative header projection.
var owaDetailFields = []string{"Attachments", "BccRecipients", "CcRecipients", "ConversationId", "DateTimeReceived", "DateTimeSent", "Flag", "From", "HasAttachments", "Importance", "InternetMessageId", "ItemClass", "Preview", "ReplyTo", "Subject", "ToRecipients", "UID", "IsRead"}
var owaJoinedMetadataFields = []string{"Attachments", "BccRecipients", "CcRecipients", "InternetMessageId", "ToRecipients", "UID"}

type owaCoverageRefusal struct{ fields []string }

func (*owaCoverageRefusal) Error() string { return "detail_metadata_coverage_refused" }

func owaInventoryDetailContext(ctx context.Context, header, detail any) (owaDetailInventory, error) {
	f := owaFields{ctx: ctx}
	inventory := owaDetailInventory{selected: map[string]any{}}
	for _, name := range owaDetailFields {
		v := f.get(detail, name)
		if v == nil {
			continue
		}
		inventory.present = append(inventory.present, name)
	}
	for _, name := range owaJoinedMetadataFields {
		h, d := f.get(header, name), f.get(detail, name)
		switch {
		case h == nil:
			inventory.selected[name] = d
		case d == nil:
			inventory.selected[name] = h
		case reflect.DeepEqual(h, d):
			inventory.selected[name] = h
		default:
			inventory.unproved = append(inventory.unproved, name)
		}
	}
	if f.err != nil {
		return inventory, f.err
	}
	if len(inventory.unproved) != 0 {
		return inventory, &owaCoverageRefusal{fields: inventory.unproved}
	}
	return inventory, nil
}

var owaMessageFields = []string{"internet_message_id", "conversation_id", "to_me", "subject", "sender_name", "sender_address", "preview", "in_reply_to", "received_at", "sent_at", "class", "importance", "ical_uid", "is_read", "read_state", "flag", "has_attachments", "body_state"}

func owaRawHeader(value, body any) (owaRawMessage, error) {
	ctx, cancel := context.WithTimeout(context.Background(), owaWorkTimeout)
	defer cancel()
	return owaRawHeaderContext(ctx, value, body)
}

func owaRawHeaderContext(ctx context.Context, value, body any) (owaRawMessage, error) {
	f := owaFields{ctx: ctx}
	m := owaRawMessage{folder: f.text(value, "ParentFolderId", "Id"), attachments: map[string][]any{}}
	var detailErr error
	m.detail, detailErr = owaInventoryDetailContext(ctx, value, body)
	if detailErr != nil {
		return m, detailErr
	}
	metadata := &v8.Object{}
	for _, name := range owaJoinedMetadataFields {
		metadata.Keys = append(metadata.Keys, name)
		metadata.Values = append(metadata.Values, m.detail.selected[name])
	}
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
		if kind := f.text(body, "NormalizedBody", "BodyType"); kind != "" && kind != "HTML" {
			return m, errors.New("body_derivation_unproved")
		}
		m.html = f.text(v)
		m.inline = true
		bodyState = "inline"
	}
	if m.inline {
		var textErr error
		m.text, textErr = owaIndependentText(ctx, m.html)
		if textErr != nil {
			return m, textErr
		}
	}
	m.textKnown = true
	m.fields = []any{
		f.text(metadata, "InternetMessageId"), f.text(value, "ConversationId", "Id"), int64(0),
		f.text(value, "Subject"), f.text(value, "From", "Mailbox", "Name"), f.text(value, "From", "Mailbox", "EmailAddress"),
		f.text(value, "Preview"), "", f.date(value, "DateTimeReceived"), f.date(value, "DateTimeSent"),
		f.text(value, "ItemClass"), importance, strings.ToLower(f.text(metadata, "UID")), read, state, flag, has, bodyState,
	}
	for _, role := range []struct{ field, name string }{{"ToRecipients", "to"}, {"CcRecipients", "cc"}, {"BccRecipients", "bcc"}} {
		for _, r := range f.list(metadata, role.field) {
			if ctx.Err() != nil {
				return m, errors.New("census_context_refused")
			}
			if object, ok := r.(*v8.Object); !ok || object == nil {
				return m, errors.New("census_recipient_refused")
			}
			name, address := f.text(r, "Name"), f.text(r, "EmailAddress")
			var addr any
			if address != "" {
				addr = address
			}
			m.recipients = append(m.recipients, []any{int64(len(m.recipients)), name, addr, int64(0), role.name})
		}
	}
	for _, a := range f.list(metadata, "Attachments") {
		if ctx.Err() != nil {
			return m, errors.New("census_context_refused")
		}
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
