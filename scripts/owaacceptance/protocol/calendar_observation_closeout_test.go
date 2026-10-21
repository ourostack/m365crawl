package protocol

import "testing"

func calendarClosed() CalendarObservationCloseout {
	return CalendarObservationCloseout{
		Version: Version, Kind: CalendarObservationKind, Owner: calendarOwner(),
		Outcome: "observed", Reason: "none", Exit: 0, ConsumersReleased: true, NativeCloseVerified: true,
		RootState: "absent", ObservationConfirmed: true,
	}
}

func TestCalendarObservationCloseoutDistinctScope(t *testing.T) {
	value := calendarClosed()
	wire, _ := Encode(value)
	if _, err := DecodeCalendarObservationCloseout(wire); err != nil {
		t.Fatal("confirmed_observation_closeout_refused")
	}
	if _, err := DecodeCloseout(wire); err == nil {
		t.Fatal("observation_closeout_became_qualification")
	}
}

func TestCalendarObservationCloseoutRejectsUnprovedSuccess(t *testing.T) {
	for _, mutate := range []func(*CalendarObservationCloseout){
		func(c *CalendarObservationCloseout) { c.Version++ },
		func(c *CalendarObservationCloseout) { c.Kind = "qualification" },
		func(c *CalendarObservationCloseout) { c.Owner = validOwner() },
		func(c *CalendarObservationCloseout) { c.Owner.Adopted, c.Owner.Child = false, Process{} },
		func(c *CalendarObservationCloseout) { c.Outcome = "passed" },
		func(c *CalendarObservationCloseout) { c.Reason = "private-unknown" },
		func(c *CalendarObservationCloseout) { c.Exit = -2 },
		func(c *CalendarObservationCloseout) { c.Exit = 1 },
		func(c *CalendarObservationCloseout) { c.ConsumersReleased = false },
		func(c *CalendarObservationCloseout) { c.NativeCloseVerified = false },
		func(c *CalendarObservationCloseout) { c.RootState = "retained_unverified" },
		func(c *CalendarObservationCloseout) { c.RootState = "private-unknown" },
		func(c *CalendarObservationCloseout) { c.ObservedRootNative = c.Owner.RootNative },
		func(c *CalendarObservationCloseout) { c.ObservationConfirmed = false },
	} {
		value := calendarClosed()
		mutate(&value)
		wire, _ := Encode(value)
		if _, err := DecodeCalendarObservationCloseout(wire); err == nil {
			t.Fatal("unproved_observation_closeout_accepted")
		}
	}
	if _, err := DecodeCalendarObservationCloseout([]byte("{}")); err == nil {
		t.Fatal("missing_closeout_fields_accepted")
	}
}

func TestCalendarObservationCloseoutRetainsFailureFacts(t *testing.T) {
	for _, reason := range []string{"observation_refused", "test_exit_failed", "lifecycle_refused", "cleanup_refused", "binary_close_failed"} {
		for _, state := range []string{"absent", "retained_owned", "retained_unverified"} {
			value := calendarClosed()
			value.Outcome, value.Reason, value.Exit, value.ObservationConfirmed = "retained", reason, -1, false
			if state == "absent" {
				value.Outcome = "test_failed"
			}
			value.RootState = state
			if state == "retained_owned" {
				value.ObservedRootNative = value.Owner.RootNative
			}
			wire, _ := Encode(value)
			if _, err := DecodeCalendarObservationCloseout(wire); err != nil {
				t.Fatal("truthful_failed_closeout_refused")
			}
			if state == "retained_owned" {
				value.ObservedRootNative[2]++
				wire, _ = Encode(value)
				if _, err := DecodeCalendarObservationCloseout(wire); err == nil {
					t.Fatal("retained_root_identity_forgery_accepted")
				}
			}
		}
	}
}
