//go:build acceptance

package acceptance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ourostack/m365crawl/internal/indexeddb"
	"github.com/ourostack/m365crawl/internal/teamsdesktop"
	"github.com/ourostack/m365crawl/internal/v8"
	"github.com/ourostack/m365crawl/scripts/owaacceptance/protocol"
)

var owaCalendarSurveyFields = protocol.CalendarObservationFields()

type owaCalendarSurvey struct {
	Counts   map[string]int64
	Released bool
}

var (
	owaSurveyOpen  = indexeddb.OpenWith
	owaSurveyClose = (*indexeddb.Origin).Close
)

// This observes raw field shapes, not mapper correctness, service completeness or a verified principal.
// The caller must retain its private snapshot if Released is false, even when another error is primary.
func owaSurveyCalendar(ctx context.Context, level, blob string) (out owaCalendarSurvey, result error) {
	return owaReadCalendarRaw(ctx, level, blob, nil)
}

func owaReadCalendarRaw(ctx context.Context, level, blob string, visit func(any, []byte) error) (out owaCalendarSurvey, result error) {
	out = owaCalendarSurvey{Counts: map[string]int64{}, Released: true}
	for _, field := range owaCalendarSurveyFields {
		out.Counts[field] = 0
	}
	if ctx.Err() != nil {
		return out, errors.New("calendar_survey_context_refused")
	}
	if err := owaCheckSource(ctx, teamsdesktop.Source{LevelDBDir: level, BlobDir: blob}); err != nil {
		return out, errors.New("calendar_survey_source_refused")
	}
	_, dbs, stats, err := indexeddb.CensusWithStats(level)
	if err != nil || stats.TruncatedLogTails != 0 {
		return out, errors.New("calendar_survey_metadata_refused")
	}
	var candidates []indexeddb.Database
	for _, db := range dbs {
		if (db.Name == "owa-offline-data" || strings.HasPrefix(db.Name, "owa-offline-data-")) && len(db.Stores) > 0 {
			candidates = append(candidates, db)
		}
	}
	if len(candidates) != 1 {
		return out, errors.New("calendar_survey_layout_refused")
	}
	db := candidates[0]
	var eventStores []int64
	for _, store := range db.Stores {
		if store.Name == "events" {
			eventStores = append(eventStores, store.ID)
		}
	}
	if len(eventStores) != 1 {
		return out, errors.New("calendar_survey_layout_refused")
	}
	if ctx.Err() != nil {
		return out, errors.New("calendar_survey_context_refused")
	}
	out.Released = false
	origin, err := owaSurveyOpen(level, blob, indexeddb.OpenOptions{KeepStore: func(d, s int64) bool {
		return d == db.ID && s == eventStores[0]
	}})
	if origin != nil {
		defer func() {
			if closeErr := owaSurveyCloseReader(origin); closeErr != nil {
				result = errors.Join(result, closeErr)
			} else {
				out.Released = true
			}
		}()
	}
	if err != nil || origin == nil {
		return out, errors.New("calendar_survey_open_refused")
	}
	if origin.Stats().TruncatedLogTails != 0 {
		return out, errors.New("calendar_survey_metadata_refused")
	}
	if ctx.Err() != nil {
		return out, errors.New("calendar_survey_context_refused")
	}
	seen := map[string][32]byte{}
	var decodedBytes int64
	err = origin.Records(db.ID, eventStores[0], func(rec indexeddb.Record) error {
		if ctx.Err() != nil {
			return errors.New("calendar_survey_context_refused")
		}
		out.Counts["records"]++
		if out.Counts["records"] > owaMaxQueryRows || len(rec.Raw) > owaMaxRawValueBytes {
			return errors.New("calendar_survey_resource_refused")
		}
		if rec.Err != nil {
			out.Counts["decode_failed"]++
			return nil
		}
		payload, err := origin.Payload(db.ID, rec.Raw)
		if err != nil {
			out.Counts["decode_failed"]++
			return nil
		}
		if owaDecodedBudget(decodedBytes, int64(len(payload))) != nil {
			return errors.New("calendar_survey_resource_refused")
		}
		decodedBytes += int64(len(payload))
		value, err := v8.Deserialize(payload)
		if err != nil {
			out.Counts["decode_failed"]++
			return nil
		}
		out.Counts["decoded"]++
		if err := owaSurveyShape(ctx, out.Counts, seen, value, payload); err != nil {
			return err
		}
		if visit != nil {
			return visit(value, payload)
		}
		return nil
	})
	if ctx.Err() != nil {
		return out, errors.New("calendar_survey_context_refused")
	}
	if err != nil {
		return out, errors.New("calendar_survey_records_refused")
	}
	return out, nil
}

func owaSurveyCloseReader(origin *indexeddb.Origin) (err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("calendar_survey_close_refused")
		}
	}()
	if owaSurveyClose(origin) != nil {
		return errors.New("calendar_survey_close_refused")
	}
	return nil
}

func owaSurveyObject(value any) (map[string]any, bool) {
	obj, ok := value.(*v8.Object)
	if !ok || obj == nil || len(obj.Keys) != len(obj.Values) {
		return nil, false
	}
	fields := map[string]any{}
	for i, key := range obj.Keys {
		if _, duplicate := fields[key]; duplicate {
			return nil, false
		}
		fields[key] = obj.Values[i]
	}
	return fields, true
}

func owaSurveyPresent(value any) bool {
	_, undefined := value.(v8.Undefined)
	return value != nil && !undefined
}

func owaSurveyText(value any) bool {
	text, ok := value.(string)
	return ok && utf8.ValidString(text)
}

func owaSurveyShape(ctx context.Context, counts map[string]int64, seen map[string][32]byte, value any, payload []byte) error {
	fields, ok := owaSurveyObject(value)
	if !ok {
		counts["non_objects"]++
		return nil
	}
	counts["objects"]++
	uid, text := fields["UID"].(string)
	if !owaSurveyPresent(fields["UID"]) {
		counts["uid_missing"]++
	} else if decoded, err := hex.DecodeString(uid); !text || err != nil || len(decoded) == 0 {
		counts["uid_invalid"]++
	} else {
		counts["uid_valid"]++
		key, digest := strings.ToLower(uid), sha256.Sum256(payload)
		if previous, exists := seen[key]; exists {
			counts["uid_duplicate"]++
			if previous != digest {
				counts["uid_conflict"]++
			}
		} else {
			// Count later disagreements with the first raw payload, not just adjacent changes.
			seen[key] = digest
		}
	}
	instants := map[string]time.Time{}
	for key, name := range map[string]string{"Start": "start", "End": "end", "LastModifiedTime": "modified"} {
		raw := fields[key]
		if !owaSurveyPresent(raw) {
			counts[name+"_unknown"]++
			continue
		}
		text, ok := raw.(string)
		instant, err := time.Parse(time.RFC3339Nano, text)
		instant = instant.UTC()
		if !ok || !utf8.ValidString(text) || err != nil || instant.IsZero() || instant.Year() < 1 || instant.Year() > 9999 {
			counts[name+"_invalid"]++
			continue
		}
		counts[name+"_valid"]++
		instants[key] = instant
	}
	start, end := instants["Start"], instants["End"]
	aligned := !start.IsZero() && end.After(start) && start.Equal(start.Truncate(24*time.Hour)) && end.Equal(end.Truncate(24*time.Hour))
	if aligned {
		counts["utc_midnight_intervals"]++
	}
	if !start.IsZero() && !end.IsZero() && end.Before(start) {
		counts["reversed_intervals"]++
	}
	if flag, ok := fields["IsAllDayEvent"].(bool); ok {
		if flag {
			counts["all_day_true"]++
			if !aligned {
				counts["all_day_unaligned"]++
			}
		} else {
			counts["all_day_false"]++
		}
	} else if owaSurveyPresent(fields["IsAllDayEvent"]) {
		counts["all_day_invalid"]++
	} else {
		counts["all_day_unknown"]++
	}
	for key, name := range map[string]string{"StartTimeZoneId": "time_zone", "CalendarItemType": "item_type", "ResponseType": "response"} {
		if owaSurveyPresent(fields[key]) {
			if owaSurveyText(fields[key]) {
				counts[name+"_present"]++
			} else {
				counts[name+"_invalid"]++
			}
		}
	}
	if owaSurveyPresent(fields["Organizer"]) {
		if organizer, ok := owaSurveyObject(fields["Organizer"]); ok {
			if _, ok := owaSurveyObject(organizer["Mailbox"]); ok {
				counts["organizer_present"]++
			} else {
				counts["organizer_invalid"]++
			}
		} else {
			counts["organizer_invalid"]++
		}
	}
	owaSurveyBody(counts, fields["Body"])
	return owaSurveyAttendees(ctx, counts, fields)
}

func owaSurveyBody(counts map[string]int64, raw any) {
	if !owaSurveyPresent(raw) {
		counts["body_unknown"]++
		return
	}
	body, ok := owaSurveyObject(raw)
	if !ok {
		counts["body_invalid"]++
		return
	}
	if !owaSurveyPresent(body["BodyType"]) && !owaSurveyPresent(body["Value"]) {
		counts["body_unknown"]++
		return
	}
	if !owaSurveyText(body["BodyType"]) || !owaSurveyText(body["Value"]) {
		counts["body_invalid"]++
		return
	}
	if owaSurveyPresent(body["IsTruncated"]) {
		truncated, ok := body["IsTruncated"].(bool)
		if !ok {
			counts["body_invalid"]++
			return
		}
		if truncated {
			counts["body_truncated"]++
			return
		}
	}
	switch strings.ToLower(body["BodyType"].(string)) {
	case "html":
		counts["body_html"]++
	case "text":
		counts["body_text"]++
	default:
		counts["body_invalid"]++
	}
}

func owaSurveyAttendees(ctx context.Context, counts map[string]int64, fields map[string]any) error {
	present := 0
	sizes := map[string]int64{}
	for key, role := range map[string]string{"RequiredAttendees": "required", "OptionalAttendees": "optional", "Resources": "resource"} {
		raw := fields[key]
		if !owaSurveyPresent(raw) {
			continue
		}
		present++
		list, ok := raw.([]any)
		if !ok {
			counts["attendees_invalid"]++
			return nil
		}
		for _, entry := range list {
			if ctx.Err() != nil {
				return errors.New("calendar_survey_context_refused")
			}
			row, ok := owaSurveyObject(entry)
			if !ok {
				counts["attendees_invalid"]++
				return nil
			}
			mailbox, ok := owaSurveyObject(row["Mailbox"])
			if !ok {
				counts["attendees_invalid"]++
				return nil
			}
			name, address := mailbox["Name"], mailbox["EmailAddress"]
			nameText, _ := name.(string)
			addressText, _ := address.(string)
			if owaSurveyPresent(name) && !owaSurveyText(name) || owaSurveyPresent(address) && !owaSurveyText(address) ||
				strings.TrimSpace(nameText) == "" && strings.TrimSpace(addressText) == "" {
				counts["attendees_invalid"]++
				return nil
			}
			sizes[role]++
		}
	}
	switch present {
	case 0:
		counts["attendees_unknown"]++
	case 3:
		counts["attendees_complete"]++
		for role, n := range sizes {
			counts["attendee_"+role] += n
			counts["attendee_rows"] += n
		}
	default:
		counts["attendees_partial"]++
	}
	return nil
}
