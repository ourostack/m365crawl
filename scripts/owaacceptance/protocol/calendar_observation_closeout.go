package protocol

const CalendarObservationCloseoutName = "calendar-observation-closeout.json"

type CalendarObservationCloseout struct {
	Version              int       `json:"version"`
	Kind                 string    `json:"kind"`
	Owner                Ownership `json:"owner"`
	Outcome              string    `json:"outcome"`
	Reason               string    `json:"reason"`
	Exit                 int       `json:"exit"`
	ConsumersReleased    bool      `json:"consumers_released"`
	NativeCloseVerified  bool      `json:"native_close_verified"`
	RootState            string    `json:"root_state"`
	ObservedRootNative   [3]uint32 `json:"observed_root_native"`
	ObservationConfirmed bool      `json:"observation_confirmed"`
}

func DecodeCalendarObservationCloseout(data []byte) (CalendarObservationCloseout, error) {
	var value CalendarObservationCloseout
	if strict(data, &value) != nil || value.Version != Version || value.Kind != CalendarObservationKind ||
		validateOwnershipTest(value.Owner, CalendarObservationTest) != nil || value.Exit < -1 {
		return CalendarObservationCloseout{}, calendarObservationRefused()
	}
	switch value.Reason {
	case "none", "observation_refused", "test_exit_failed", "lifecycle_refused", "cleanup_refused", "binary_close_failed":
	default:
		return CalendarObservationCloseout{}, calendarObservationRefused()
	}
	switch value.Outcome {
	case "observed", "test_failed", "timeout", "refused", "retained":
	default:
		return CalendarObservationCloseout{}, calendarObservationRefused()
	}
	switch value.RootState {
	case "absent":
		if value.ObservedRootNative != [3]uint32{} {
			return CalendarObservationCloseout{}, calendarObservationRefused()
		}
	case "retained_owned":
		if value.ObservedRootNative != value.Owner.RootNative {
			return CalendarObservationCloseout{}, calendarObservationRefused()
		}
	case "retained_unverified":
	default:
		return CalendarObservationCloseout{}, calendarObservationRefused()
	}
	if value.ObservationConfirmed && (!value.Owner.Adopted || value.Outcome != "observed" || value.Reason != "none" ||
		value.Exit != 0 || !value.ConsumersReleased || !value.NativeCloseVerified || value.RootState != "absent") {
		return CalendarObservationCloseout{}, calendarObservationRefused()
	}
	if value.Outcome == "observed" && !value.ObservationConfirmed {
		return CalendarObservationCloseout{}, calendarObservationRefused()
	}
	return value, nil
}
