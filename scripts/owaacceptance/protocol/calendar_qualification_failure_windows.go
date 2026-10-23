package protocol

import (
	"os"
	"path/filepath"
)

func EmitCalendarQualificationFailure(owner Ownership, value CalendarQualificationFailure) error {
	current, err := CurrentCalendarQualificationGuard()
	if err != nil || current != owner {
		return calendarQualificationRefused()
	}
	data, err := calendarQualificationFailureData(owner, value)
	if err != nil {
		return calendarQualificationRefused()
	}
	if WriteProtectedNew(filepath.Join(filepath.Dir(os.Getenv(OwnerEnv)), CalendarQualificationFailureName), data) != nil {
		return calendarQualificationRefused()
	}
	return nil
}
