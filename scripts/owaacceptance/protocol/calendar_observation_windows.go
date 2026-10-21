package protocol

import (
	"os"
	"path/filepath"
)

func CurrentCalendarObservationGuard() (Ownership, error) {
	owner, err := currentGuardFor(CalendarObservationTest, DecodeCalendarOwnership)
	if err != nil {
		return Ownership{}, calendarObservationRefused()
	}
	return owner, nil
}

func EmitCalendarObservation(owner Ownership, counts map[string]int64, observed, unstable int64, released bool) error {
	current, err := CurrentCalendarObservationGuard()
	if err != nil || current != owner {
		return calendarObservationRefused()
	}
	value := CalendarObservation{
		Version: Version, Kind: CalendarObservationKind, Nonce: owner.Nonce, Test: owner.Test,
		BinaryHash: owner.BinaryHash, BinaryNative: owner.BinaryNative, RootNative: owner.RootNative,
		Supervisor: owner.Supervisor, Child: owner.Child, Outcome: "observed",
		Observed: observed, Unstable: unstable, ValueReadersReleased: released, Counts: counts,
	}
	data, err := calendarObservationData(owner, value)
	if err != nil {
		return calendarObservationRefused()
	}
	if WriteProtectedNew(filepath.Join(filepath.Dir(os.Getenv(OwnerEnv)), CalendarObservationName), data) != nil {
		return calendarObservationRefused()
	}
	return nil
}
