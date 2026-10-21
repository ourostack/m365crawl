package protocol

import "errors"

const (
	CalendarQualificationTest   = "TestRealOWACalendarProductQualification"
	CalendarQualificationKind   = "owa_calendar_product_sql"
	CalendarQualificationName   = "calendar-qualification.json"
	CalendarSQLFieldCount       = 48
	CalendarQualificationPasses = 3
)

// CalendarQualification binds cached-source SQL parity to a distinct product test, not an observation.
// A decoded record alone is neither native admission nor process/root closeout proof.
type CalendarQualification struct {
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
	MailControlPassed    bool             `json:"mail_control_passed"`
	Counts               map[string]int64 `json:"counts"`
	ExpectedEvents       int64            `json:"expected_events"`
	PersistedEvents      int64            `json:"persisted_events"`
	Passes               int64            `json:"passes"`
	ComparedValues       int64            `json:"compared_values"`
	Mismatches           int64            `json:"mismatches"`
}

func calendarQualificationRefused() error {
	return errors.New("calendar_qualification_protocol_refused")
}

func DecodeCalendarQualificationOwnership(data []byte) (Ownership, error) {
	var owner Ownership
	if strict(data, &owner) != nil || validateOwnershipTest(owner, CalendarQualificationTest) != nil {
		return Ownership{}, calendarQualificationRefused()
	}
	return owner, nil
}

func DecodeCalendarQualification(data []byte) (CalendarQualification, error) {
	var value CalendarQualification
	if strict(data, &value) != nil || value.Version != Version || value.Kind != CalendarQualificationKind ||
		value.Test != CalendarQualificationTest || !hash(value.Nonce) || !hash(value.BinaryHash) ||
		value.BinaryNative == [3]uint32{} || value.RootNative == [3]uint32{} ||
		!process(value.Supervisor) || !process(value.Child) || value.Supervisor.PID == value.Child.PID ||
		value.Outcome != "qualified" || value.Observed != 1 || value.Unstable != 0 ||
		!value.ValueReadersReleased || !value.MailControlPassed || !calendarObservationCounts(value.Counts) {
		return CalendarQualification{}, calendarQualificationRefused()
	}
	counts := value.Counts
	records := counts["records"]
	if records <= 0 || counts["decoded"] != records || counts["objects"] != records || counts["uid_valid"] != records ||
		counts["decode_failed"] != 0 || counts["uid_conflict"] != 0 || counts["reversed_intervals"] != 0 ||
		counts["start_valid"] != records || counts["end_valid"] != records || counts["modified_valid"] != records ||
		value.ExpectedEvents <= 0 || value.ExpectedEvents > MaxCount ||
		value.ExpectedEvents != records-counts["uid_duplicate"] || value.PersistedEvents != value.ExpectedEvents ||
		value.Passes != CalendarQualificationPasses || value.ComparedValues < 0 || value.ComparedValues > MaxCount ||
		value.ComparedValues != value.ExpectedEvents*CalendarSQLFieldCount*CalendarQualificationPasses || value.Mismatches != 0 {
		return CalendarQualification{}, calendarQualificationRefused()
	}
	return value, nil
}

func MatchCalendarQualification(owner Ownership, qualification CalendarQualification) error {
	_, err := calendarQualificationData(owner, qualification)
	return err
}

func calendarQualificationData(owner Ownership, qualification CalendarQualification) ([]byte, error) {
	if validateOwnershipTest(owner, CalendarQualificationTest) != nil || !owner.Adopted {
		return nil, calendarQualificationRefused()
	}
	data, err := Encode(qualification)
	if err != nil {
		return nil, calendarQualificationRefused()
	}
	if _, err := DecodeCalendarQualification(data); err != nil {
		return nil, calendarQualificationRefused()
	}
	if owner.Nonce != qualification.Nonce || owner.Test != qualification.Test ||
		owner.BinaryHash != qualification.BinaryHash || owner.BinaryNative != qualification.BinaryNative ||
		owner.RootNative != qualification.RootNative || owner.Supervisor != qualification.Supervisor || owner.Child != qualification.Child {
		return nil, calendarQualificationRefused()
	}
	return data, nil
}
