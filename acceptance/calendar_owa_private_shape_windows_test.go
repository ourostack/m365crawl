//go:build acceptance && windows

package acceptance

import (
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/ourostack/m365crawl/internal/outlookcal"
)

type owaPrivateShape struct {
	Field    string `json:"field"`
	Type     string `json:"type"`
	Category string `json:"category"`
	Count    int64  `json:"count"`
}

func owaPrivateShapes(value any) []owaPrivateShape {
	counts := map[[3]string]int64{}
	add := func(field, typ, category string) { counts[[3]string{field, typ, category}]++ }
	text := func(fields map[string]any, key, path string) (string, bool) {
		raw := fields[key]
		if !owaSurveyPresent(raw) {
			return "", false
		}
		s, ok := raw.(string)
		if !ok {
			add(path, owaPrivateType(raw), "text_type")
			return "", false
		}
		if !utf8.ValidString(s) {
			category := "invalid_utf8"
			if owaPrivateSurrogatesOnly(s) {
				category = "unpaired_utf16_code_unit"
			}
			add(path, "string", category)
			return "", false
		}
		return s, true
	}
	enum := func(fields map[string]any, key, path string, allowed ...string) {
		s, ok := text(fields, key, path)
		if !ok {
			return
		}
		for _, literal := range allowed {
			if s == literal {
				return
			}
		}
		category := "unsupported_enum"
		if key == "ResponseType" && s == "Unknown" {
			category = "explicit_unknown_enum"
		}
		add(path, "string", category)
	}
	flag := func(fields map[string]any, key, path string) {
		if raw := fields[key]; owaSurveyPresent(raw) {
			if _, ok := raw.(bool); !ok {
				add(path, owaPrivateType(raw), "boolean_type")
			}
		}
	}
	fields, valid := owaSurveyObject(value)
	if !valid {
		add("event", owaPrivateType(value), "object_type")
	} else {
		for _, key := range []string{"UID", "Start", "End", "LastModifiedTime", "Subject", "Location", "OnlineMeetingJoinUrl", "StartTimeZoneId", "EndTimeZoneId", "Preview", "OnlineMeetingConferenceId", "OnlineMeetingTollNumber"} {
			text(fields, key, key)
		}
		for _, key := range []string{"IsAllDayEvent", "IsCancelled", "IsOnlineMeeting", "IsOrganizer", "HasAttachments"} {
			flag(fields, key, key)
		}
		for _, key := range []string{"StartTimeZoneId", "EndTimeZoneId"} {
			if zone, ok := fields[key].(string); ok && utf8.ValidString(zone) {
				if _, _, found := outlookcal.ZoneIANA(zone); !found {
					category := "unresolved_timezone"
					switch {
					case zone == "":
						category = "empty_timezone"
					case strings.TrimSpace(zone) == "":
						category = "whitespace_timezone"
					case strings.HasPrefix(zone, "tzone://Microsoft/Custom"):
						category = "custom_timezone_definition_absent"
					}
					add(key, "string", category)
				}
			}
		}
		enum(fields, "ResponseType", "ResponseType", "Accept", "Tentative", "Decline", "NoResponseReceived", "Organizer")
		enum(fields, "FreeBusyType", "FreeBusyType", "Free", "Tentative", "Busy")
		enum(fields, "CalendarItemType", "CalendarItemType", "Single", "Occurrence", "Exception", "RecurringMaster")
		if raw := fields["Body"]; owaSurveyPresent(raw) {
			if body, ok := owaSurveyObject(raw); ok {
				kind, haveKind := text(body, "BodyType", "Body.BodyType")
				_, haveValue := text(body, "Value", "Body.Value")
				if owaSurveyPresent(body["BodyType"]) != owaSurveyPresent(body["Value"]) {
					add("Body", "object", "incomplete_shape")
				}
				flag(body, "IsTruncated", "Body.IsTruncated")
				if truncated, ok := body["IsTruncated"].(bool); ok && truncated {
					add("Body.IsTruncated", "boolean", "truncated_cached_body")
				}
				if haveKind && haveValue && !strings.EqualFold(kind, "html") && !strings.EqualFold(kind, "text") {
					add("Body.BodyType", "string", "unsupported_enum")
				}
			} else {
				add("Body", owaPrivateType(raw), "object_type")
			}
		}
		if raw := fields["Organizer"]; owaSurveyPresent(raw) {
			if organizer, ok := owaSurveyObject(raw); ok {
				if raw := organizer["Mailbox"]; owaSurveyPresent(raw) {
					if mailbox, ok := owaSurveyObject(raw); ok {
						text(mailbox, "Name", "Organizer.Mailbox.Name")
						text(mailbox, "EmailAddress", "Organizer.Mailbox.EmailAddress")
					} else {
						add("Organizer.Mailbox", owaPrivateType(raw), "object_type")
					}
				}
			} else {
				add("Organizer", owaPrivateType(raw), "object_type")
			}
		}
		roles := []string{"RequiredAttendees", "OptionalAttendees", "Resources"}
		present := 0
		for _, role := range roles {
			if owaSurveyPresent(fields[role]) {
				present++
			}
		}
		if present != 0 && present != len(roles) {
			add("Attendees", "object", "partial_role_lists")
		}
		if present == len(roles) {
			for _, role := range roles {
				list, ok := fields[role].([]any)
				if !ok {
					add(role, owaPrivateType(fields[role]), "array_type")
					break
				}
				for _, raw := range list {
					row, ok := owaSurveyObject(raw)
					if !ok {
						add(role+"[]", owaPrivateType(raw), "object_type")
						break
					}
					mailbox, ok := owaSurveyObject(row["Mailbox"])
					if !ok {
						add(role+"[].Mailbox", owaPrivateType(row["Mailbox"]), "object_type")
						break
					}
					name, _ := text(mailbox, "Name", role+"[].Mailbox.Name")
					address, _ := text(mailbox, "EmailAddress", role+"[].Mailbox.EmailAddress")
					if strings.TrimSpace(name) == "" && strings.TrimSpace(address) == "" {
						add(role+"[].Mailbox", "object", "empty_identity")
						break
					}
					enum(row, "ResponseType", role+"[].ResponseType", "Accept", "Tentative", "Decline", "NoResponseReceived", "Organizer")
				}
			}
		}
	}
	out := make([]owaPrivateShape, 0, len(counts))
	for key, count := range counts {
		out = append(out, owaPrivateShape{key[0], key[1], key[2], count})
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Field != b.Field {
			return a.Field < b.Field
		}
		if a.Type != b.Type {
			return a.Type < b.Type
		}
		return a.Category < b.Category
	})
	return out
}

func owaPrivateSurrogatesOnly(value string) bool {
	found := false
	for len(value) > 0 {
		_, size := utf8.DecodeRuneInString(value)
		if size != 1 || value[0] < utf8.RuneSelf {
			value = value[size:]
			continue
		}
		if len(value) < 3 || value[0] != 0xed || value[1] < 0xa0 || value[1] > 0xbf || value[2] < 0x80 || value[2] > 0xbf {
			return false
		}
		found, value = true, value[3:]
	}
	return found
}

func owaPrivateType(value any) string {
	switch value.(type) {
	case string:
		return "string"
	case bool:
		return "boolean"
	case float64, int64, int32, int, uint32, uint64:
		return "number"
	case []any:
		return "array"
	case nil:
		return "null"
	default:
		if _, ok := owaSurveyObject(value); ok {
			return "object"
		}
		return "other"
	}
}
