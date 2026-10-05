package teamscal

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// UnmappedError reports a record that cannot be mapped because it lacks its identity or is not
// the JSON object the store holds. The derivation counts it as the omission calendar_unmapped.
type UnmappedError struct{ Reason string }

func (e *UnmappedError) Error() string { return "teamscal: unmapped record: " + e.Reason }

// MapNotes carries facts about a mapped record that the caller counts but that are not errors.
type MapNotes struct {
	// UnknownZone is the event's zone name when it resolves to no IANA id.
	UnknownZone string
	// AllDayUnaligned is set when isAllDayEvent was true but the times fit no whole-day shape, so
	// the event was stored as timed.
	AllDayUnaligned bool
	// EventTypeAbsent is set when the record carried no eventType and was taken as single.
	EventTypeAbsent bool
	// Skipped counts catch-up items dropped for lacking a callId.
	Skipped int
	// SkippedItems counts catch-up tasks and mentions dropped because they were not objects.
	SkippedItems int
	// BadDuration counts catch-up items whose duration was present but not in (0, 24h] in
	// milliseconds; the duration and the end time derived from it are dropped.
	BadDuration int
	// AdaptiveRecapSeen is set when a recap record carries a non-empty adaptiveRecap string. The
	// field is not mapped (it was empty or absent in every measured record); this makes its first
	// real appearance visible.
	AdaptiveRecapSeen bool
	// ReminderOutOfRange is set when the reminder lead time was negative or above four weeks; the
	// reminder is then not stated.
	ReminderOutOfRange bool
	// MissingICalUID is set when the event record carried no iCalUID.
	MissingICalUID bool
}

// decode parses the canonical JSON of one record, which must be a single JSON object. Numbers
// stay json.Number so ids and large values keep their text.
func decode(raw []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, &UnmappedError{Reason: "not JSON: " + err.Error()}
	}
	if dec.More() {
		return nil, &UnmappedError{Reason: "trailing data after the JSON value"}
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, &UnmappedError{Reason: fmt.Sprintf("value is %T, want an object", v)}
	}
	return m, nil
}

// str returns a string, or the text of a number (ids Teams sometimes stores numerically).
func str(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return x.String()
	}
	return ""
}

// parseBool reads a JSON boolean or the strings "true" and "false" (any case). Anything else,
// numbers included, is not a boolean: ok is false.
func parseBool(v any) (value, ok bool) {
	switch x := v.(type) {
	case bool:
		return x, true
	case string:
		switch strings.ToLower(strings.TrimSpace(x)) {
		case "true":
			return true, true
		case "false":
			return false, true
		}
	}
	return false, false
}

// boolean is parseBool without the presence bit.
func boolean(v any) bool {
	b, _ := parseBool(v)
	return b
}

// flag reads the boolean field key of m and reports whether the key held a boolean. Every event
// flag goes through it, so that "absent" (or not a boolean) stays distinguishable from "present
// and false" when the core makes its flags three-valued.
func flag(m map[string]any, key string) (value, present bool) {
	return parseBool(m[key])
}

// integer reads a JSON number, truncating a fraction. Strings are not numbers.
func integer(v any) (int64, bool) {
	n, ok := v.(json.Number)
	if !ok {
		return 0, false
	}
	if i, err := n.Int64(); err == nil {
		return i, true
	}
	f, err := n.Float64()
	return int64(f), err == nil
}

// number reads a JSON number as a float.
func number(v any) (float64, bool) {
	n, ok := v.(json.Number)
	if !ok {
		return 0, false
	}
	f, err := n.Float64()
	return f, err == nil
}

// minEpochMillis is the smallest number read as a millisecond timestamp (1973); the cache stores
// small counters in time fields too.
const minEpochMillis = 1e11

// moment reads a time: {"$date":"..."} (the canonical form of a JS Date), an RFC 3339 string, or
// a millisecond epoch as a number or digit string. Anything else gives the zero time.
func moment(v any) time.Time {
	switch x := v.(type) {
	case map[string]any:
		return moment(x["$date"])
	case json.Number:
		return epochMillis(x.String())
	case string:
		x = strings.TrimSpace(x)
		if t, err := time.Parse(time.RFC3339Nano, x); err == nil {
			return t.UTC()
		}
		return epochMillis(x)
	}
	return time.Time{}
}

func epochMillis(s string) time.Time {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || float64(n) < minEpochMillis {
		return time.Time{}
	}
	return time.UnixMilli(n).UTC()
}

// requiredTime reads a time field that an event cannot do without. The value must be an RFC 3339
// string with a zone offset (bare or inside {"$date":...}) or a millisecond epoch; a missing,
// null, offset-less or unparseable value is an UnmappedError.
func requiredTime(m map[string]any, key string) (time.Time, error) {
	t := moment(m[key])
	if t.IsZero() {
		return t, &UnmappedError{Reason: key + " is missing or not a time with a zone offset"}
	}
	return t, nil
}

// timePtr returns a pointer to t, or nil for the zero time.
func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// jsonish returns v, except that a string holding a JSON object or array is replaced by what it
// holds (Teams stores some fields either way). A string that does not parse stays a string.
func jsonish(v any) any {
	s, ok := v.(string)
	if !ok {
		return v
	}
	t := strings.TrimSpace(s)
	if !strings.HasPrefix(t, "[") && !strings.HasPrefix(t, "{") {
		return v
	}
	dec := json.NewDecoder(strings.NewReader(t))
	dec.UseNumber()
	var out any
	if dec.Decode(&out) != nil || dec.More() {
		return v
	}
	return out
}

// array returns v as a list, or nil when it is neither a list nor a string holding one.
func array(v any) []any {
	a, _ := jsonish(v).([]any)
	return a
}

// object returns v as an object, or nil when it is neither an object nor a string holding one.
// Indexing the nil map is safe.
func object(v any) map[string]any {
	m, _ := jsonish(v).(map[string]any)
	return m
}

// marshal renders v as compact JSON without HTML escaping, so stored text stays readable.
func marshal(v any) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v) // the values here are strings, numbers and decoded JSON: they always encode
	return strings.TrimSuffix(buf.String(), "\n")
}

// textOrJSON reads a text field that is not always text: a string is kept as is, null and absent
// are empty, and any other value is kept as its canonical JSON text rather than dropped.
func textOrJSON(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	}
	return marshal(v)
}
