package protocol

const CalendarQualificationCloseoutName = "calendar-qualification-closeout.json"

type CalendarQualificationCloseout struct {
	Version             int       `json:"version"`
	Kind                string    `json:"kind"`
	Owner               Ownership `json:"owner"`
	Outcome             string    `json:"outcome"`
	Reason              string    `json:"reason"`
	Exit                int       `json:"exit"`
	ConsumersReleased   bool      `json:"consumers_released"`
	NativeCloseVerified bool      `json:"native_close_verified"`
	RootState           string    `json:"root_state"`
	ObservedRootNative  [3]uint32 `json:"observed_root_native"`
	Qualified           bool      `json:"qualified"`
}

func DecodeCalendarQualificationCloseout(data []byte) (CalendarQualificationCloseout, error) {
	var value CalendarQualificationCloseout
	if strict(data, &value) != nil || value.Version != Version || value.Kind != CalendarQualificationKind ||
		validateOwnershipTest(value.Owner, CalendarQualificationTest) != nil || value.Exit < -1 {
		return CalendarQualificationCloseout{}, calendarQualificationRefused()
	}
	switch value.Reason {
	case "none", "qualification_refused", "test_exit_failed", "lifecycle_refused", "cleanup_refused", "binary_close_failed":
	default:
		return CalendarQualificationCloseout{}, calendarQualificationRefused()
	}
	switch value.Outcome {
	case "qualified", "test_failed", "timeout", "refused", "retained":
	default:
		return CalendarQualificationCloseout{}, calendarQualificationRefused()
	}
	switch value.RootState {
	case "absent":
		if value.ObservedRootNative != [3]uint32{} {
			return CalendarQualificationCloseout{}, calendarQualificationRefused()
		}
	case "retained_owned":
		if value.ObservedRootNative != value.Owner.RootNative {
			return CalendarQualificationCloseout{}, calendarQualificationRefused()
		}
	case "retained_unverified":
	default:
		return CalendarQualificationCloseout{}, calendarQualificationRefused()
	}
	if value.Qualified && (!value.Owner.Adopted || value.Outcome != "qualified" || value.Reason != "none" ||
		value.Exit != 0 || !value.ConsumersReleased || !value.NativeCloseVerified || value.RootState != "absent") {
		return CalendarQualificationCloseout{}, calendarQualificationRefused()
	}
	if value.Outcome == "qualified" && !value.Qualified {
		return CalendarQualificationCloseout{}, calendarQualificationRefused()
	}
	return value, nil
}
