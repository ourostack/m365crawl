package onedriveusage

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"
)

var errJSONStructureLimit = errors.New("JSON structure limit exceeded")

func decodeDocument(id, format, raw string) (DocumentObservation, bool, error) {
	if !utf8.ValidString(raw) || !validUnicodeEscapes([]byte(`"`+raw+`"`)) {
		return DocumentObservation{}, false, nil
	}
	var decoded string
	if err := json.Unmarshal([]byte(`"`+raw+`"`), &decoded); err != nil {
		return DocumentObservation{}, false, nil
	}
	root, ok, err := jsonObject(decoded)
	if !ok {
		return DocumentObservation{}, false, err
	}
	m := fieldReader{valid: true}
	file := m.object(root, "file")
	if file == nil {
		return DocumentObservation{}, false, nil
	}
	sp, visualization := m.object(file, "SharePointItem"), m.object(file, "Visualization")
	d := DocumentObservation{
		ID: id, Format: format, Title: m.text(file, "FileName"), URL: m.text(visualization, "AccessUrl"),
		Extension: m.text(file, "FileExtension"), Owner: m.text(file, "FileOwner"),
		CreatedRaw: m.text(file, "FileCreatedTime"), ModifiedRaw: m.text(file, "FileModifiedTime"),
		Size: m.number(file, "FileSize"), SiteID: m.text(sp, "SiteId"), WebID: m.text(sp, "WebId"),
		ListID: m.text(sp, "ListId"), UniqueID: m.text(sp, "UniqueId"),
	}
	if d.Title == "" {
		d.Title = m.text(visualization, "Title")
	}
	latest := m.object(m.object(file, "ItemProperties"), "Shared")
	if latest != nil {
		d.Latest = &Share{
			DisplayName:    m.text(latest, "LastSharedWithMailboxOwnerByDisplayName"),
			SMTP:           m.text(latest, "LastSharedWithMailboxOwnerBySmtp"),
			AtRaw:          m.text(latest, "LastSharedWithMailboxOwnerDateTime"),
			ConversationID: m.text(latest, "TeamsMessageThreadId"),
			AttachmentID:   m.text(latest, "AttachmentItemReferenceId"), Subject: m.text(latest, "SubjectProperty"),
		}
	}
	history := m.object(m.object(file, "AllExtensions"), "SharingHistory")
	for _, value := range m.array(history, "Instances") {
		row, ok := value.(map[string]any)
		if !ok {
			m.valid = false
		}
		s := Share{
			DisplayName: m.text(row, "SharedByDisplayName"), SMTP: m.text(row, "SharedBySmtp"),
			AadID: m.text(row, "SharedByAadId"), AtRaw: m.text(row, "SharedByTime"),
			ConversationID: m.text(row, "ConversationId"), Subject: m.text(row, "Subject"),
			ParticipantsCount: m.number(row, "ParticipantsCount"), MeetingStartRaw: m.text(row, "MeetingStartTime"),
			ICalUID: m.text(row, "ICalUid"), MeetingSubject: m.text(row, "MeetingSubject"), Recurring: m.boolean(row, "isRecurring"),
		}
		for _, value := range m.array(row, "Participants") {
			person, ok := value.(map[string]any)
			if !ok {
				m.valid = false
			}
			s.Participants = append(s.Participants, Person{DisplayName: m.text(person, "DisplayName"), SMTP: m.text(person, "Smtp")})
		}
		d.History = append(d.History, s)
	}
	if !m.valid {
		return DocumentObservation{}, false, nil
	}
	return d, true, nil
}

func decodeCollaborator(id, raw string) (Collaborator, bool, error) {
	row, ok, err := jsonObject(raw)
	if !ok {
		return Collaborator{}, false, err
	}
	m := fieldReader{valid: true}
	c := Collaborator{ID: id, UserID: m.text(row, "Id"), DisplayName: m.text(row, "DisplayName"),
		Department: m.text(row, "Department"), JobTitle: m.text(row, "JobTitle"), Office: m.text(row, "OfficeLocation")}
	for _, value := range m.array(row, "EmailAddresses") {
		text, ok := value.(string)
		if !ok || strings.ContainsRune(text, 0) {
			m.valid = false
		}
		c.Emails = append(c.Emails, text)
	}
	if !m.valid {
		return Collaborator{}, false, nil
	}
	return c, true, nil
}

type fieldReader struct{ valid bool }

func (m *fieldReader) text(row map[string]any, field string) string {
	if row[field] == nil {
		return ""
	}
	value, ok := row[field].(string)
	if !ok || strings.ContainsRune(value, 0) {
		m.valid = false
	}
	return value
}

func (m *fieldReader) object(row map[string]any, field string) map[string]any {
	if row[field] == nil {
		return nil
	}
	value, ok := row[field].(map[string]any)
	if !ok {
		m.valid = false
	}
	return value
}

func (m *fieldReader) array(row map[string]any, field string) []any {
	if row[field] == nil {
		return nil
	}
	value, ok := row[field].([]any)
	if !ok {
		m.valid = false
	}
	return value
}

func (m *fieldReader) number(row map[string]any, field string) *int64 {
	if row[field] == nil {
		return nil
	}
	number, ok := row[field].(json.Number)
	if !ok {
		m.valid = false
		return nil
	}
	value, err := number.Int64()
	if err != nil || value < 0 {
		m.valid = false
		return nil
	}
	return &value
}

func (m *fieldReader) boolean(row map[string]any, field string) *bool {
	if row[field] == nil {
		return nil
	}
	value, ok := row[field].(bool)
	if !ok {
		m.valid = false
		return nil
	}
	return &value
}

func jsonObject(raw string) (map[string]any, bool, error) {
	if !utf8.ValidString(raw) || !validUnicodeEscapes([]byte(raw)) {
		return nil, false, nil
	}
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	decoder.UseNumber()
	members := 0
	value, ok, err := jsonValue(decoder, 0, &members)
	if !ok {
		return nil, false, err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, false, nil
	}
	row, ok := value.(map[string]any)
	return row, ok, nil
}

func jsonValue(decoder *json.Decoder, depth int, members *int) (any, bool, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, false, nil
	}
	delim, container := token.(json.Delim)
	if !container {
		return token, true, nil
	}
	if depth >= 64 {
		return nil, false, errJSONStructureLimit
	}
	object := map[string]any{}
	var array []any
	for decoder.More() {
		*members++
		if *members > 131072 {
			return nil, false, errJSONStructureLimit
		}
		var key string
		if delim == '{' {
			keyToken, err := decoder.Token()
			if err != nil {
				return nil, false, nil
			}
			key, _ = keyToken.(string)
			if _, exists := object[key]; exists {
				return nil, false, nil
			}
		}
		value, ok, err := jsonValue(decoder, depth+1, members)
		if !ok {
			return nil, false, err
		}
		if delim == '{' {
			object[key] = value
		} else {
			array = append(array, value)
		}
	}
	if _, err := decoder.Token(); err != nil {
		return nil, false, nil
	}
	if delim == '{' {
		return object, true, nil
	}
	if array == nil {
		array = []any{}
	}
	return array, true, nil
}

func validUnicodeEscapes(data []byte) bool {
	inString := false
	for i := 0; i < len(data); i++ {
		if data[i] == '"' {
			inString = !inString
		}
		if !inString || data[i] != '\\' {
			continue
		}
		i++
		if i >= len(data) {
			return false
		}
		if data[i] != 'u' {
			continue
		}
		if i+4 >= len(data) {
			return false
		}
		code, err := strconv.ParseUint(string(data[i+1:i+5]), 16, 16)
		if err != nil || (code >= 0xdc00 && code <= 0xdfff) {
			return false
		}
		i += 4
		if code < 0xd800 || code > 0xdbff {
			continue
		}
		if i+6 >= len(data) || data[i+1] != '\\' || data[i+2] != 'u' {
			return false
		}
		low, err := strconv.ParseUint(string(data[i+3:i+7]), 16, 16)
		if err != nil || low < 0xdc00 || low > 0xdfff {
			return false
		}
		i += 6
	}
	return true
}
