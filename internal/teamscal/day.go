package teamscal

import (
	"encoding/json"
	"strings"
	"time"
)

// EventDay is the day (YYYY-MM-DD, in zone) an event record starts on, from the two values the
// store reads out of the record with SQL (json_extract of startTime and of eventType): startText
// is the SQLite text of the startTime value, which is the JSON text of {"$date":"..."}, a bare
// RFC 3339 string or an epoch number. A master never has a day (it is a rule, not a day's event),
// and a record with no usable start has none either: ok is false for both. The store uses it to
// find the days the Teams cache still holds, without mapping every record.
func EventDay(startText, eventType string, zone *time.Location) (day string, ok bool) {
	if strings.EqualFold(strings.TrimSpace(eventType), "master") || strings.EqualFold(strings.TrimSpace(eventType), "recurringmaster") {
		return "", false
	}
	var v any
	dec := json.NewDecoder(strings.NewReader(startText))
	dec.UseNumber()
	if dec.Decode(&v) != nil || dec.More() {
		v = startText // json_extract unquotes a string: it is not JSON text
	}
	t := moment(v)
	if t.IsZero() {
		return "", false
	}
	return t.In(zone).Format(time.DateOnly), true
}

// SyncStamp reads the Teams cache's own "last successful sync" time from the record of that key in
// calendar-internal-data; ok is false when the record has no usable time.
func SyncStamp(valueJSON []byte) (time.Time, bool) {
	m, err := decode(valueJSON)
	if err != nil {
		return time.Time{}, false
	}
	t := moment(m["value"])
	return t, !t.IsZero()
}
