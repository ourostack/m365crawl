package protocol

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func calendarOwner() Ownership {
	o := validOwner()
	o.Test = CalendarObservationTest
	return o
}

func calendarObserved() CalendarObservation {
	o := calendarOwner()
	counts := map[string]int64{}
	for _, key := range CalendarObservationFields() {
		counts[key] = 0
	}
	for key, n := range map[string]int64{
		"records": 2, "decoded": 2, "objects": 2, "uid_valid": 2,
		"start_valid": 2, "end_valid": 2, "modified_valid": 2,
		"utc_midnight_intervals": 1, "all_day_true": 1, "all_day_false": 1,
		"body_html": 1, "body_unknown": 1, "attendees_complete": 2,
		"attendee_rows": 3, "attendee_required": 1, "attendee_optional": 1, "attendee_resource": 1,
	} {
		counts[key] = n
	}
	return CalendarObservation{
		Version: Version, Kind: CalendarObservationKind, Nonce: o.Nonce, Test: o.Test,
		BinaryHash: o.BinaryHash, BinaryNative: o.BinaryNative, RootNative: o.RootNative,
		Supervisor: o.Supervisor, Child: o.Child, Outcome: "observed",
		Observed: 1, ValueReadersReleased: true, Counts: counts,
	}
}

func TestCalendarObservationValidAndScopeSeparated(t *testing.T) {
	owner := calendarOwner()
	ownerWire, _ := Encode(owner)
	gotOwner, err := DecodeCalendarOwnership(ownerWire)
	if err != nil || !reflect.DeepEqual(gotOwner, owner) {
		t.Fatal("valid_calendar_owner_refused")
	}
	if _, err := DecodeOwnership(ownerWire); err == nil {
		t.Fatal("mail_decoder_accepted_calendar_owner")
	}
	mailOwner, _ := Encode(validOwner())
	if _, err := DecodeCalendarOwnership(mailOwner); err == nil {
		t.Fatal("calendar_decoder_accepted_mail_owner")
	}
	witness := calendarObserved()
	wire, _ := Encode(witness)
	got, err := DecodeCalendarObservation(wire)
	if err != nil || !reflect.DeepEqual(got, witness) || MatchCalendarObservation(owner, witness) != nil {
		t.Fatal("valid_calendar_observation_refused")
	}
	if _, err := DecodeQualification(wire); err == nil {
		t.Fatal("shape_observation_became_mail_qualification")
	}
	mailWire, _ := Encode(validWitness())
	if _, err := DecodeCalendarObservation(mailWire); err == nil {
		t.Fatal("mail_qualification_became_calendar_observation")
	}
	if MatchCalendarObservation(validOwner(), witness) == nil || Match(owner, validWitness()) == nil {
		t.Fatal("cross_scope_match_accepted")
	}
}

func TestCalendarObservationEmptyOrDamagedIsNotQualification(t *testing.T) {
	for _, empty := range []bool{true, false} {
		witness := calendarObserved()
		if empty {
			for key := range witness.Counts {
				witness.Counts[key] = 0
			}
		} else {
			witness.Counts["records"]++
			witness.Counts["decode_failed"]++
		}
		wire, _ := Encode(witness)
		if _, err := DecodeCalendarObservation(wire); err != nil {
			t.Fatal("truthful_empty_or_damaged_observation_refused")
		}
		if _, err := DecodeQualification(wire); err == nil {
			t.Fatal("observation_claimed_qualification")
		}
		witness.Outcome = "passed"
		wire, _ = Encode(witness)
		if _, err := DecodeCalendarObservation(wire); err == nil {
			t.Fatal("observational_scope_claimed_product_success")
		}
	}
}

func TestCalendarObservationVocabularyCopy(t *testing.T) {
	fields := CalendarObservationFields()
	if len(fields) != 47 {
		t.Fatal("counter_vocabulary_changed")
	}
	fields[0] = "private-never-output"
	if CalendarObservationFields()[0] != "records" {
		t.Fatal("caller_can_widen_counter_vocabulary")
	}
}

func TestCalendarObservationBindingsAndClaimsRefused(t *testing.T) {
	for _, mutate := range []func(*CalendarObservation){
		func(w *CalendarObservation) { w.Version++ },
		func(w *CalendarObservation) { w.Kind = "qualification" },
		func(w *CalendarObservation) { w.Test = TestName },
		func(w *CalendarObservation) { w.Nonce = "private-invalid" },
		func(w *CalendarObservation) { w.BinaryHash = strings.Repeat("A", 64) },
		func(w *CalendarObservation) { w.BinaryNative = [3]uint32{} },
		func(w *CalendarObservation) { w.RootNative = [3]uint32{} },
		func(w *CalendarObservation) { w.Supervisor.PID = 0 },
		func(w *CalendarObservation) { w.Child.Created = 0 },
		func(w *CalendarObservation) { w.Child.PID = w.Supervisor.PID },
		func(w *CalendarObservation) { w.Outcome = "qualified" },
		func(w *CalendarObservation) { w.Observed = 0 },
		func(w *CalendarObservation) { w.Observed = MaxCount + 1 },
		func(w *CalendarObservation) { w.Unstable = 1 },
		func(w *CalendarObservation) { w.ValueReadersReleased = false },
	} {
		witness := calendarObserved()
		mutate(&witness)
		wire, _ := Encode(witness)
		if _, err := DecodeCalendarObservation(wire); err == nil || strings.Contains(err.Error(), "private") {
			t.Fatal("forged_observation_accepted_or_disclosed")
		}
	}
}

func TestCalendarObservationMatchRequiresAdoptionAndExactIdentity(t *testing.T) {
	owner := calendarOwner()
	owner.Adopted, owner.Child = false, Process{}
	wire, _ := Encode(owner)
	if _, err := DecodeCalendarOwnership(wire); err != nil {
		t.Fatal("valid_pre_adoption_owner_refused")
	}
	if MatchCalendarObservation(owner, calendarObserved()) == nil {
		t.Fatal("unadopted_observation_matched")
	}
	for _, mutate := range []func(*CalendarObservation){
		func(w *CalendarObservation) { w.Nonce = strings.Repeat("c", 64) },
		func(w *CalendarObservation) { w.BinaryHash = strings.Repeat("c", 64) },
		func(w *CalendarObservation) { w.BinaryNative[2]++ },
		func(w *CalendarObservation) { w.RootNative[2]++ },
		func(w *CalendarObservation) { w.Supervisor.Created++ },
		func(w *CalendarObservation) { w.Child.Created++ },
		func(w *CalendarObservation) { w.Outcome = "passed" },
		func(w *CalendarObservation) { w.Counts[strings.Repeat("private-never-output", MaxRecordBytes)] = 1 },
	} {
		witness := calendarObserved()
		mutate(&witness)
		if err := MatchCalendarObservation(calendarOwner(), witness); err == nil || strings.Contains(err.Error(), "private") {
			t.Fatal("forged_or_invalid_observation_matched_or_disclosed")
		}
	}
}

func TestCalendarObservationCounterContractRefused(t *testing.T) {
	for _, mutate := range []func(map[string]int64){
		func(c map[string]int64) { delete(c, "records") },
		func(c map[string]int64) { c["private-extra"] = 1 },
		func(c map[string]int64) { delete(c, "records"); c["private-substitution"] = 2 },
		func(c map[string]int64) { c["records"] = -1 },
		func(c map[string]int64) { c["records"] = MaxCount + 1 },
		func(c map[string]int64) { c["decoded"]++ },
		func(c map[string]int64) { c["non_objects"]++ },
		func(c map[string]int64) { c["uid_missing"]++ },
		func(c map[string]int64) { c["uid_duplicate"] = c["uid_valid"] },
		func(c map[string]int64) { c["uid_conflict"] = 1 },
		func(c map[string]int64) { c["start_unknown"]++ },
		func(c map[string]int64) { c["end_invalid"]++ },
		func(c map[string]int64) { c["modified_unknown"]++ },
		func(c map[string]int64) { c["all_day_unknown"]++ },
		func(c map[string]int64) { c["all_day_unaligned"] = 2 },
		func(c map[string]int64) { c["body_invalid"]++ },
		func(c map[string]int64) { c["attendees_partial"]++ },
		func(c map[string]int64) { c["attendee_rows"]++ },
		func(c map[string]int64) { c["time_zone_present"], c["time_zone_invalid"] = 2, 1 },
		func(c map[string]int64) { c["item_type_present"], c["item_type_invalid"] = 2, 1 },
		func(c map[string]int64) { c["organizer_present"], c["organizer_invalid"] = 2, 1 },
		func(c map[string]int64) { c["response_present"], c["response_invalid"] = 2, 1 },
		func(c map[string]int64) { c["reversed_intervals"] = 3 },
		func(c map[string]int64) { c["utc_midnight_intervals"] = 3 },
		func(c map[string]int64) { c["utc_midnight_intervals"], c["reversed_intervals"] = 1, 2 },
		func(c map[string]int64) { c["utc_midnight_intervals"] = 0 },
	} {
		witness := calendarObserved()
		mutate(witness.Counts)
		wire, _ := Encode(witness)
		if _, err := DecodeCalendarObservation(wire); err == nil || strings.Contains(err.Error(), "private") {
			t.Fatal("incoherent_counters_accepted_or_disclosed")
		}
	}
}

func TestCalendarObservationWireRefusals(t *testing.T) {
	wire, _ := Encode(calendarObserved())
	var missing map[string]json.RawMessage
	_ = json.Unmarshal(wire, &missing)
	delete(missing, "value_readers_released")
	missingWire, _ := json.Marshal(missing)
	for _, bad := range [][]byte{
		nil, []byte("null"), []byte("{"), missingWire,
		[]byte(strings.Repeat(" ", MaxRecordBytes+1)),
		append(append([]byte(nil), wire...), []byte("{}")...),
		[]byte(strings.Replace(string(wire), `"records":2`, `"records":2,"records":2`, 1)),
		[]byte(strings.Replace(string(wire), `"observed":1`, `"observed":null`, 1)),
		[]byte(strings.Replace(string(wire), `"root_native":[1,0,3]`, `"root_native":[1,null,3]`, 1)),
		[]byte(strings.Replace(string(wire), `"root_native":[1,0,3]`, `"root_native":[1,0,3,9]`, 1)),
		[]byte(strings.Replace(string(wire), `"kind":`, `"private-extra":"private-never-output","kind":`, 1)),
	} {
		if _, err := DecodeCalendarObservation(bad); err == nil || strings.Contains(err.Error(), "private-never-output") {
			t.Fatal("ambiguous_or_private_observation_wire_accepted")
		}
	}
}
