//go:build acceptance && windows

package acceptance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/ourostack/m365crawl/internal/store"
)

var owaCalendarSQLColumns = strings.Fields(`
	source account_id source_id event_key composite_key global_id ical_uid series_key event_type original_start
	start_at end_at all_day start_date end_date time_zone time_zone_iana utc_offset subject organizer organizer_address
	is_organizer is_private cancelled response show_as is_online_meeting location last_modified online_meeting_url
	short_join_url dial_in_conference_id dial_in_toll_number teams_thread_id attendees_json locations_json
	body_html body_text body_type body_preview attachments_json has_attachments categories_json recurrence_json
	reminder_minutes detail_raw_json unknown_fields removed_at`)

func owaCalendarSQLInstant(instant time.Time) string {
	return instant.UTC().Truncate(time.Millisecond).Format("2006-01-02T15:04:05.000Z")
}

func owaCalendarSQLFlag(flag int8) any {
	if flag < 0 {
		return nil
	}
	return int64(flag)
}

func owaCalendarExpectedSQL(account string, event owaEventExpected) (map[string]any, error) {
	core, detail := event.Temporal, event.Optional
	values := make(map[string]any, len(owaCalendarSQLColumns))
	for _, column := range owaCalendarSQLColumns {
		values[column] = ""
	}
	values["source"], values["account_id"], values["source_id"] = "outlook", account, core.UID
	values["event_key"], values["global_id"], values["ical_uid"], values["series_key"] = core.UID+"|", core.UID, core.UID, event.Series
	suffix := core.Start.UTC().Format(time.RFC3339)
	if core.AllDay == 1 {
		suffix = core.StartDate
	}
	subject := strings.Join(strings.FieldsFunc(strings.ToLower(detail.Fields["subject"].Value), unicode.IsSpace), " ")
	organizer := strings.ToLower(strings.TrimSpace(detail.Fields["organizer"].Value))
	sum := sha256.Sum256([]byte(strings.Join([]string{organizer, subject, suffix}, "\x00")))
	values["composite_key"] = "composite|" + hex.EncodeToString(sum[:12])
	values["original_start"], values["is_private"], values["reminder_minutes"], values["removed_at"] = nil, nil, nil, nil
	values["start_at"], values["end_at"], values["last_modified"] =
		owaCalendarSQLInstant(core.Start), owaCalendarSQLInstant(core.End), owaCalendarSQLInstant(core.Modified)
	values["all_day"], values["start_date"], values["end_date"] = owaCalendarSQLFlag(core.AllDay), core.StartDate, core.EndDate
	values["time_zone"] = core.Zone
	if identity := core.ZoneIdentity; identity != nil {
		// Derive spellings from independent native identities, never the product resolver.
		switch {
		case identity.Canonical == "America/Los_Angeles" && (core.Zone == "Pacific Standard Time" || core.Zone == "America/Los_Angeles"):
			values["time_zone_iana"] = "America/Los_Angeles"
		case identity.Canonical == "Etc/UTC" && (core.Zone == "UTC" || core.Zone == "Etc/UTC"):
			values["time_zone_iana"] = core.Zone
		case !identity.Alias && identity.Windows != "" && identity.Canonical == core.Zone && strings.Contains(core.Zone, "/"):
			values["time_zone_iana"] = core.Zone
		case !identity.Alias && core.Zone == identity.Windows && identity.Canonical != "" &&
			identity.Canonical == identity.Representative && strings.Contains(identity.Representative, "/"):
			// IANA backward links define these preferred spellings independently
			// of both the product's resolver and the archive being compared.
			preferred := identity.Representative
			switch preferred {
			case "Asia/Calcutta":
				preferred = "Asia/Kolkata"
			case "Europe/Kiev":
				preferred = "Europe/Kyiv"
			case "America/Buenos_Aires":
				preferred = "America/Argentina/Buenos_Aires"
			}
			values["time_zone_iana"] = preferred
		default:
			return nil, errors.New("calendar_oracle_persisted_zone_spelling_unproved")
		}
	}
	for column, source := range map[string]string{
		"subject": "subject", "location": "location", "organizer": "organizer", "organizer_address": "organizer_address",
		"response": "response", "show_as": "show_as", "event_type": "event_type", "body_preview": "body_preview", "online_meeting_url": "join_url",
	} {
		values[column] = detail.Fields[source].Value
	}
	for column, source := range map[string]string{
		"is_organizer": "IsOrganizer", "cancelled": "IsCancelled", "is_online_meeting": "IsOnlineMeeting", "has_attachments": "HasAttachments",
	} {
		values[column] = owaCalendarSQLFlag(core.Flags[source])
	}
	values["dial_in_conference_id"], values["dial_in_toll_number"], values["teams_thread_id"] = detail.DialConference.Value, detail.DialNumber.Value, event.Thread
	if detail.AttendeesKnown {
		data, err := json.Marshal(detail.Attendees)
		if err != nil {
			return nil, errors.New("calendar_oracle_attendee_encoding_refused")
		}
		values["attendees_json"] = string(data)
	}
	values["body_html"], values["body_text"], values["body_type"] = event.Body.HTML, event.Body.Text, event.Body.Kind
	unknown := map[string]bool{}
	for _, field := range event.Unknown {
		unknown[field] = true
	}
	if event.Body.HTML == "" && event.Body.Text == "" {
		values["body_type"], unknown["body"] = "", true
	}
	if detail.Fields["body_preview"].Value == "" {
		unknown["body_preview"] = true
	}
	if detail.Fields["join_url"].Value == "" && detail.DialConference.Value == "" && detail.DialNumber.Value == "" && event.Thread == "" {
		for _, field := range []string{"join_url", "short_join_url", "dial_in", "meeting_chat_id"} {
			if core.Flags["IsOnlineMeeting"] == 0 {
				delete(unknown, field)
			} else {
				unknown[field] = true
			}
		}
	}
	names := make([]string, 0, len(unknown))
	for field := range unknown {
		names = append(names, field)
	}
	sort.Strings(names)
	values["unknown_fields"] = strings.Join(names, ",")
	return values, nil
}

// The only diagnostics are fixed column names/codes, never account IDs, UIDs or source values.
func owaCompareCalendarContext(ctx context.Context, st *store.Store, account string, expected owaCalendarExpectedRead) ([]string, error) {
	if !expected.Released || len(expected.Counts) != len(owaCalendarSurveyFields) ||
		expected.Counts["decode_failed"] != 0 || expected.Counts["uid_conflict"] != 0 ||
		expected.Counts["records"] < 0 || expected.Counts["uid_duplicate"] < 0 ||
		expected.Counts["records"] != expected.Counts["decoded"] ||
		expected.Counts["records"] != expected.Counts["uid_valid"] ||
		expected.Counts["records"] != expected.Counts["objects"] ||
		expected.Counts["records"]-expected.Counts["uid_duplicate"] != int64(len(expected.Rows)) {
		return nil, errors.New("calendar_oracle_expected_read_unproved")
	}
	wanted := map[string]map[string]any{}
	for _, event := range expected.Rows {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		row, err := owaCalendarExpectedSQL(account, event)
		if err != nil {
			return nil, err
		}
		id := event.Temporal.UID
		if _, duplicate := wanted[id]; duplicate {
			return nil, errors.New("calendar_oracle_expected_identity_conflict")
		}
		wanted[id] = row
	}
	rows, err := owaQueryContext(ctx, st, "select "+strings.Join(owaCalendarSQLColumns, ",")+" from calendar_source_events")
	if err != nil {
		return nil, errors.New("calendar_oracle_archive_query_refused")
	}
	bad, seen := map[string]bool{}, map[string]bool{}
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if len(row) != len(owaCalendarSQLColumns) {
			return nil, errors.New("calendar_oracle_archive_shape_refused")
		}
		if row[0] != "outlook" || row[1] != account {
			continue
		}
		id, valid := row[2].(string)
		want, found := wanted[id]
		if !valid || !found || seen[id] {
			bad["event_census"] = true
			continue
		}
		seen[id] = true
		for index, column := range owaCalendarSQLColumns {
			if !reflect.DeepEqual(row[index], want[column]) {
				bad[column] = true
			}
		}
	}
	if len(seen) != len(wanted) {
		bad["event_census"] = true
	}
	findings := make([]string, 0, len(bad))
	for field := range bad {
		findings = append(findings, field)
	}
	sort.Strings(findings)
	return findings, nil
}
