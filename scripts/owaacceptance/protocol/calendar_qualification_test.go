package protocol

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func calendarQualificationOwner() Ownership {
	owner := validOwner()
	owner.Test = CalendarQualificationTest
	return owner
}

func calendarQualified() CalendarQualification {
	owner := calendarQualificationOwner()
	return CalendarQualification{
		Version: Version, Kind: CalendarQualificationKind, Nonce: owner.Nonce, Test: owner.Test,
		BinaryHash: owner.BinaryHash, BinaryNative: owner.BinaryNative, RootNative: owner.RootNative,
		Supervisor: owner.Supervisor, Child: owner.Child, Outcome: "qualified", Observed: 1,
		ValueReadersReleased: true, MailControlPassed: true, Counts: calendarObserved().Counts,
		ExpectedEvents: 2, PersistedEvents: 2, Passes: 3, ComparedValues: 288,
	}
}

func TestCalendarQualificationDistinctOwnershipAndWitness(t *testing.T) {
	owner := calendarQualificationOwner()
	wire, _ := Encode(owner)
	gotOwner, err := DecodeCalendarQualificationOwnership(wire)
	if err != nil || !reflect.DeepEqual(gotOwner, owner) {
		t.Fatal("calendar_qualification_owner_refused")
	}
	if _, err := DecodeOwnership(wire); err == nil {
		t.Fatal("calendar_qualification_owner_became_mail_owner")
	}
	if _, err := DecodeCalendarOwnership(wire); err == nil {
		t.Fatal("calendar_qualification_owner_became_observation_owner")
	}
	value := calendarQualified()
	wire, _ = Encode(value)
	got, err := DecodeCalendarQualification(wire)
	if err != nil || !reflect.DeepEqual(got, value) || MatchCalendarQualification(owner, value) != nil {
		t.Fatal("complete_identity_bound_calendar_qualification_refused")
	}
	if _, err := DecodeQualification(wire); err == nil {
		t.Fatal("calendar_qualification_became_mail_qualification")
	}
	if _, err := DecodeCalendarObservation(wire); err == nil {
		t.Fatal("calendar_qualification_became_field_observation")
	}
	for _, old := range []any{validOwner(), calendarOwner(), validWitness(), calendarObserved()} {
		wire, _ = Encode(old)
		if _, err := DecodeCalendarQualificationOwnership(wire); err == nil {
			t.Fatal("old_scope_became_calendar_qualification_ownership")
		}
		if _, err := DecodeCalendarQualification(wire); err == nil {
			t.Fatal("old_scope_became_calendar_product_qualification")
		}
	}
}

func TestCalendarQualificationRejectsIncompleteProof(t *testing.T) {
	for _, mutate := range []func(*CalendarQualification){
		func(q *CalendarQualification) { q.Version++ },
		func(q *CalendarQualification) { q.Kind = CalendarObservationKind },
		func(q *CalendarQualification) { q.Test = CalendarObservationTest },
		func(q *CalendarQualification) { q.Nonce = "private-invalid" },
		func(q *CalendarQualification) { q.BinaryHash = strings.Repeat("A", 64) },
		func(q *CalendarQualification) { q.BinaryNative = [3]uint32{} },
		func(q *CalendarQualification) { q.RootNative = [3]uint32{} },
		func(q *CalendarQualification) { q.Supervisor.PID = 0 },
		func(q *CalendarQualification) { q.Child.Created = 0 },
		func(q *CalendarQualification) { q.Child.PID = q.Supervisor.PID },
		func(q *CalendarQualification) { q.Outcome = "observed" },
		func(q *CalendarQualification) { q.Observed = 0 },
		func(q *CalendarQualification) { q.Observed = 2 },
		func(q *CalendarQualification) { q.Unstable = 1 },
		func(q *CalendarQualification) { q.ValueReadersReleased = false },
		func(q *CalendarQualification) { q.MailControlPassed = false },
		func(q *CalendarQualification) { q.ExpectedEvents = 0 },
		func(q *CalendarQualification) { q.ExpectedEvents = MaxCount + 1 },
		func(q *CalendarQualification) { q.PersistedEvents-- },
		func(q *CalendarQualification) { q.Passes = 2 },
		func(q *CalendarQualification) { q.ComparedValues-- },
		func(q *CalendarQualification) { q.ComparedValues = MaxCount + 1 },
		func(q *CalendarQualification) { q.Mismatches = 1 },
		func(q *CalendarQualification) { q.Counts = nil },
		func(q *CalendarQualification) { q.Counts["private-extra"] = 1 },
		func(q *CalendarQualification) { q.Counts["records"]++ },
		func(q *CalendarQualification) { q.Counts["uid_valid"]-- },
		func(q *CalendarQualification) { q.Counts["uid_duplicate"] = 1 },
		func(q *CalendarQualification) { q.Counts["uid_conflict"] = 1 },
		func(q *CalendarQualification) { q.Counts["start_valid"]-- },
		func(q *CalendarQualification) { q.Counts["end_valid"]-- },
		func(q *CalendarQualification) { q.Counts["modified_valid"]-- },
		func(q *CalendarQualification) { q.Counts["reversed_intervals"] = 1 },
	} {
		value := calendarQualified()
		mutate(&value)
		wire, _ := Encode(value)
		if _, err := DecodeCalendarQualification(wire); err == nil || strings.Contains(err.Error(), "private") {
			t.Fatal("incomplete_or_foreign_qualification_accepted_or_disclosed")
		}
	}
	value := calendarQualified()
	value.ExpectedEvents, value.PersistedEvents, value.ComparedValues = 1, 1, 144
	value.Counts["uid_duplicate"] = 1
	wire, _ := Encode(value)
	if _, err := DecodeCalendarQualification(wire); err != nil {
		t.Fatal("complete_identical_raw_duplicate_coverage_refused")
	}
	value.Counts = calendarObserved().Counts
	value.Counts["records"], value.Counts["decode_failed"] = 3, 1
	wire, _ = Encode(value)
	if _, err := DecodeCalendarQualification(wire); err == nil {
		t.Fatal("truthful_decode_loss_became_product_qualification")
	}
}

func TestCalendarQualificationRefusesTruthfulPartialRawCoverage(t *testing.T) {
	for _, mutate := range []func(map[string]int64){
		func(c map[string]int64) {
			for key := range c {
				c[key] = 0
			}
		},
		func(c map[string]int64) { c["records"], c["decode_failed"] = 3, 1 },
		func(c map[string]int64) { c["records"], c["decoded"], c["non_objects"] = 3, 3, 1 },
		func(c map[string]int64) { c["uid_valid"], c["uid_missing"] = 1, 1 },
		func(c map[string]int64) { c["start_valid"], c["start_unknown"] = 1, 1 },
		func(c map[string]int64) { c["end_valid"], c["end_unknown"] = 1, 1 },
		func(c map[string]int64) { c["modified_valid"], c["modified_unknown"] = 1, 1 },
		func(c map[string]int64) {
			c["utc_midnight_intervals"], c["all_day_unaligned"], c["reversed_intervals"] = 0, 1, 1
		},
	} {
		value := calendarQualified()
		mutate(value.Counts)
		if !calendarObservationCounts(value.Counts) {
			t.Fatal("synthetic_loss_is_not_a_truthful_raw_partition")
		}
		wire, _ := Encode(value)
		if _, err := DecodeCalendarQualification(wire); err == nil {
			t.Fatal("truthful_partial_or_invalid_raw_coverage_claimed_qualification")
		}
	}
}

func TestCalendarQualificationRequiresAdoptionAndExactBindings(t *testing.T) {
	owner := calendarQualificationOwner()
	owner.Adopted, owner.Child = false, Process{}
	wire, _ := Encode(owner)
	if _, err := DecodeCalendarQualificationOwnership(wire); err != nil {
		t.Fatal("valid_pre_adoption_qualification_owner_refused")
	}
	if MatchCalendarQualification(owner, calendarQualified()) == nil {
		t.Fatal("unadopted_product_qualification_matched")
	}
	for _, mutate := range []func(*CalendarQualification){
		func(q *CalendarQualification) { q.Nonce = strings.Repeat("c", 64) },
		func(q *CalendarQualification) { q.BinaryHash = strings.Repeat("c", 64) },
		func(q *CalendarQualification) { q.BinaryNative[2]++ },
		func(q *CalendarQualification) { q.RootNative[2]++ },
		func(q *CalendarQualification) { q.Supervisor.Created++ },
		func(q *CalendarQualification) { q.Child.Created++ },
		func(q *CalendarQualification) { q.Mismatches++ },
		func(q *CalendarQualification) { q.Counts[strings.Repeat("private", MaxRecordBytes)] = 1 },
	} {
		value := calendarQualified()
		mutate(&value)
		if err := MatchCalendarQualification(calendarQualificationOwner(), value); err == nil || strings.Contains(err.Error(), "private") {
			t.Fatal("unbound_qualification_matched_or_disclosed")
		}
	}
}

func TestCalendarQualificationStrictJSON(t *testing.T) {
	wire, _ := Encode(calendarQualified())
	var fields map[string]json.RawMessage
	if json.Unmarshal(wire, &fields) != nil {
		t.Fatal("synthetic_wire_setup_failed")
	}
	for field := range fields {
		copy := map[string]json.RawMessage{}
		for key, value := range fields {
			copy[key] = value
		}
		delete(copy, field)
		incomplete, _ := json.Marshal(copy)
		if _, err := DecodeCalendarQualification(incomplete); err == nil {
			t.Fatal("missing_product_qualification_field_accepted")
		}
	}
	for _, malformed := range []string{"{}", "null", string(wire) + "{}", `{"version":1,` + string(wire[1:]), strings.Repeat(" ", MaxRecordBytes+1)} {
		if _, err := DecodeCalendarQualification([]byte(malformed)); err == nil {
			t.Fatal("malformed_or_unbounded_product_qualification_accepted")
		}
	}
}
