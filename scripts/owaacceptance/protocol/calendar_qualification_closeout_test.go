package protocol

import (
	"encoding/json"
	"testing"
)

func calendarQualificationClosed() CalendarQualificationCloseout {
	return CalendarQualificationCloseout{
		Version: Version, Kind: CalendarQualificationKind, Owner: calendarQualificationOwner(),
		Outcome: "qualified", Reason: "none", Exit: 0, ConsumersReleased: true, NativeCloseVerified: true,
		RootState: "absent", Qualified: true,
	}
}

func TestCalendarQualificationCloseoutDistinctScope(t *testing.T) {
	wire, _ := Encode(calendarQualificationClosed())
	if _, err := DecodeCalendarQualificationCloseout(wire); err != nil {
		t.Fatal("complete_product_qualification_closeout_refused")
	}
	if _, err := DecodeCloseout(wire); err == nil {
		t.Fatal("product_closeout_became_mail_closeout")
	}
	if _, err := DecodeCalendarObservationCloseout(wire); err == nil {
		t.Fatal("product_closeout_became_observation_closeout")
	}
	for _, old := range []any{calendarClosed(), Closeout{Version: Version, Owner: validOwner(), Outcome: "passed",
		Reason: "none", Exit: 0, ConsumersReleased: true, NativeCloseVerified: true, RootState: "absent", Qualified: true}} {
		wire, _ = Encode(old)
		if _, err := DecodeCalendarQualificationCloseout(wire); err == nil {
			t.Fatal("old_closeout_became_calendar_product_qualification")
		}
	}
}

func TestCalendarQualificationCloseoutRejectsUnprovedSuccess(t *testing.T) {
	for _, mutate := range []func(*CalendarQualificationCloseout){
		func(c *CalendarQualificationCloseout) { c.Version++ },
		func(c *CalendarQualificationCloseout) { c.Kind = CalendarObservationKind },
		func(c *CalendarQualificationCloseout) { c.Owner = calendarOwner() },
		func(c *CalendarQualificationCloseout) { c.Owner.Adopted, c.Owner.Child = false, Process{} },
		func(c *CalendarQualificationCloseout) { c.Outcome = "passed" },
		func(c *CalendarQualificationCloseout) { c.Reason = "private-invalid" },
		func(c *CalendarQualificationCloseout) { c.Exit = -2 },
		func(c *CalendarQualificationCloseout) { c.Exit = 1 },
		func(c *CalendarQualificationCloseout) { c.ConsumersReleased = false },
		func(c *CalendarQualificationCloseout) { c.NativeCloseVerified = false },
		func(c *CalendarQualificationCloseout) { c.RootState = "retained_unverified" },
		func(c *CalendarQualificationCloseout) { c.RootState = "private-invalid" },
		func(c *CalendarQualificationCloseout) { c.ObservedRootNative = c.Owner.RootNative },
		func(c *CalendarQualificationCloseout) { c.Qualified = false },
	} {
		value := calendarQualificationClosed()
		mutate(&value)
		wire, _ := Encode(value)
		if _, err := DecodeCalendarQualificationCloseout(wire); err == nil {
			t.Fatal("unproved_product_qualification_closeout_accepted")
		}
	}
}

func TestCalendarQualificationCloseoutPreservesFailureAndAdoptionFacts(t *testing.T) {
	for _, adopted := range []bool{false, true} {
		for _, outcome := range []string{"test_failed", "timeout", "refused", "retained"} {
			for _, reason := range []string{"qualification_refused", "test_exit_failed", "lifecycle_refused", "cleanup_refused", "binary_close_failed"} {
				for _, state := range []string{"absent", "retained_owned", "retained_unverified"} {
					value := calendarQualificationClosed()
					value.Outcome, value.Reason, value.Exit, value.Qualified = outcome, reason, -1, false
					value.Owner.Adopted, value.RootState = adopted, state
					if !adopted {
						value.Owner.Child = Process{}
					}
					if state == "retained_owned" {
						value.ObservedRootNative = value.Owner.RootNative
					}
					wire, _ := Encode(value)
					if _, err := DecodeCalendarQualificationCloseout(wire); err != nil {
						t.Fatal("truthful_failed_product_closeout_refused")
					}
					if state == "retained_owned" {
						value.ObservedRootNative[2]++
						wire, _ = Encode(value)
						if _, err := DecodeCalendarQualificationCloseout(wire); err == nil {
							t.Fatal("foreign_retained_root_accepted")
						}
					}
				}
			}
		}
	}
}

func TestCalendarQualificationCloseoutRequiresCompleteJSON(t *testing.T) {
	wire, _ := Encode(calendarQualificationClosed())
	var fields map[string]json.RawMessage
	if json.Unmarshal(wire, &fields) != nil {
		t.Fatal("synthetic_closeout_wire_setup_refused")
	}
	for field := range fields {
		copy := map[string]json.RawMessage{}
		for name, value := range fields {
			copy[name] = value
		}
		delete(copy, field)
		incomplete, _ := json.Marshal(copy)
		if _, err := DecodeCalendarQualificationCloseout(incomplete); err == nil {
			t.Fatal("missing_terminal_closeout_fact_accepted")
		}
	}
}
