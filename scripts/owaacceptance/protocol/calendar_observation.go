package protocol

import "errors"

const (
	CalendarObservationTest = "TestRealOWACalendarFieldObservation"
	CalendarObservationKind = "owa_calendar_field_shapes"
	CalendarObservationName = "calendar-observation.json"
)

// CalendarObservationFields returns a copy: callers cannot widen the accepted wire vocabulary.
func CalendarObservationFields() []string {
	return []string{
		"records", "decoded", "decode_failed", "objects", "non_objects",
		"uid_valid", "uid_missing", "uid_invalid", "uid_duplicate", "uid_conflict",
		"start_valid", "end_valid", "modified_valid", "reversed_intervals", "utc_midnight_intervals",
		"all_day_true", "all_day_false", "all_day_unknown", "all_day_invalid", "all_day_unaligned",
		"body_html", "body_text", "body_unknown", "body_truncated", "body_invalid",
		"attendees_complete", "attendees_partial", "attendees_unknown", "attendees_invalid",
		"attendee_rows", "attendee_required", "attendee_optional", "attendee_resource",
		"time_zone_present", "item_type_present", "organizer_present", "response_present",
		"start_unknown", "start_invalid", "end_unknown", "end_invalid", "modified_unknown", "modified_invalid",
		"time_zone_invalid", "item_type_invalid", "organizer_invalid", "response_invalid",
	}
}

// CalendarObservation is a field-shape observation, never product or mapping qualification.
type CalendarObservation struct {
	Version              int              `json:"version"`
	Kind                 string           `json:"kind"`
	Nonce                string           `json:"nonce"`
	Test                 string           `json:"test"`
	BinaryHash           string           `json:"binary_hash"`
	BinaryNative         [3]uint32        `json:"binary_native"`
	RootNative           [3]uint32        `json:"root_native"`
	Supervisor           Process          `json:"supervisor"`
	Child                Process          `json:"child"`
	Outcome              string           `json:"outcome"`
	Observed             int64            `json:"observed"`
	Unstable             int64            `json:"unstable"`
	ValueReadersReleased bool             `json:"value_readers_released"`
	Counts               map[string]int64 `json:"counts"`
}

func calendarObservationRefused() error {
	return errors.New("calendar_observation_protocol_refused")
}

func DecodeCalendarOwnership(data []byte) (Ownership, error) {
	var value Ownership
	if strict(data, &value) != nil || validateOwnershipTest(value, CalendarObservationTest) != nil {
		return Ownership{}, calendarObservationRefused()
	}
	return value, nil
}

func DecodeCalendarObservation(data []byte) (CalendarObservation, error) {
	var value CalendarObservation
	if strict(data, &value) != nil || value.Version != Version || value.Kind != CalendarObservationKind ||
		value.Test != CalendarObservationTest || !hash(value.Nonce) || !hash(value.BinaryHash) ||
		value.BinaryNative == [3]uint32{} || value.RootNative == [3]uint32{} ||
		!process(value.Supervisor) || !process(value.Child) || value.Supervisor.PID == value.Child.PID ||
		value.Outcome != "observed" || value.Observed <= 0 || value.Observed > MaxCount ||
		value.Unstable != 0 || !value.ValueReadersReleased || !calendarObservationCounts(value.Counts) {
		return CalendarObservation{}, calendarObservationRefused()
	}
	return value, nil
}

func calendarObservationCounts(counts map[string]int64) bool {
	fields := CalendarObservationFields()
	if len(counts) != len(fields) {
		return false
	}
	for _, field := range fields {
		n, ok := counts[field]
		if !ok || n < 0 || n > MaxCount {
			return false
		}
	}
	for _, partition := range [][]string{
		{"records", "decoded", "decode_failed"},
		{"decoded", "objects", "non_objects"},
		{"objects", "uid_valid", "uid_missing", "uid_invalid"},
		{"objects", "all_day_true", "all_day_false", "all_day_unknown", "all_day_invalid"},
		{"objects", "body_html", "body_text", "body_unknown", "body_truncated", "body_invalid"},
		{"objects", "attendees_complete", "attendees_partial", "attendees_unknown", "attendees_invalid"},
		{"objects", "start_valid", "start_unknown", "start_invalid"},
		{"objects", "end_valid", "end_unknown", "end_invalid"},
		{"objects", "modified_valid", "modified_unknown", "modified_invalid"},
		{"attendee_rows", "attendee_required", "attendee_optional", "attendee_resource"},
	} {
		var sum int64
		for _, field := range partition[1:] {
			sum += counts[field]
		}
		if counts[partition[0]] != sum {
			return false
		}
	}
	if counts["uid_duplicate"] > 0 && counts["uid_duplicate"] >= counts["uid_valid"] ||
		counts["uid_conflict"] > counts["uid_duplicate"] ||
		counts["all_day_unaligned"] > counts["all_day_true"] ||
		counts["all_day_true"]-counts["all_day_unaligned"] > counts["utc_midnight_intervals"] ||
		counts["utc_midnight_intervals"]+counts["reversed_intervals"] > min(counts["start_valid"], counts["end_valid"]) {
		return false
	}
	for _, prefix := range []string{"time_zone", "item_type", "organizer", "response"} {
		if counts[prefix+"_present"]+counts[prefix+"_invalid"] > counts["objects"] {
			return false
		}
	}
	return true
}

func MatchCalendarObservation(owner Ownership, observation CalendarObservation) error {
	_, err := calendarObservationData(owner, observation)
	return err
}

func calendarObservationData(owner Ownership, observation CalendarObservation) ([]byte, error) {
	if validateOwnershipTest(owner, CalendarObservationTest) != nil || !owner.Adopted {
		return nil, calendarObservationRefused()
	}
	data, err := Encode(observation)
	if err != nil {
		return nil, calendarObservationRefused()
	}
	if _, err := DecodeCalendarObservation(data); err != nil {
		return nil, calendarObservationRefused()
	}
	if owner.Nonce != observation.Nonce || owner.Test != observation.Test ||
		owner.BinaryHash != observation.BinaryHash || owner.BinaryNative != observation.BinaryNative ||
		owner.RootNative != observation.RootNative || owner.Supervisor != observation.Supervisor || owner.Child != observation.Child {
		return nil, calendarObservationRefused()
	}
	return data, nil
}
