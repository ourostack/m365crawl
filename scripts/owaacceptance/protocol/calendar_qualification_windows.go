package protocol

import (
	"os"
	"path/filepath"
)

func CurrentCalendarQualificationGuard() (Ownership, error) {
	owner, err := currentGuardFor(CalendarQualificationTest, DecodeCalendarQualificationOwnership)
	if err != nil {
		return Ownership{}, calendarQualificationRefused()
	}
	return owner, nil
}

func EmitCalendarQualification(owner Ownership, value CalendarQualification) error {
	current, err := CurrentCalendarQualificationGuard()
	if err != nil || current != owner {
		return calendarQualificationRefused()
	}
	data, err := calendarQualificationData(owner, value)
	if err != nil {
		return calendarQualificationRefused()
	}
	if WriteProtectedNew(filepath.Join(filepath.Dir(os.Getenv(OwnerEnv)), CalendarQualificationName), data) != nil {
		return calendarQualificationRefused()
	}
	return nil
}
