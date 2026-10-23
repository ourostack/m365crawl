package main

import (
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/ourostack/m365crawl/scripts/owaacceptance/protocol"
)

func (q *qualifiedRun) verifyObservation() error {
	data, err := protocol.ReadProtected(filepath.Join(q.receipt, protocol.CalendarObservationName))
	if err != nil {
		return errors.New("calendar_observation_missing")
	}
	value, err := protocol.DecodeCalendarObservation(data)
	if err != nil || protocol.MatchCalendarObservation(q.owner, value) != nil {
		return errors.New("calendar_observation_refused")
	}
	q.observation = value
	return nil
}

func (q *qualifiedRun) finishObservation(exit int, result error) error {
	closeErr := errors.Join(q.binary.Close(), q.closePins())
	record := protocol.CalendarObservationCloseout{
		Version: protocol.Version, Kind: protocol.CalendarObservationKind, Owner: q.owner,
		Outcome: "refused", Reason: "observation_refused", Exit: exit, ConsumersReleased: q.consumersReleased,
		NativeCloseVerified: q.nativeCloseVerified && closeErr == nil && q.cleanupVerified, RootState: "retained_unverified",
	}
	// #nosec G703 -- Exact identity-bound generated workspace; read-only absence observation after the shared release barrier.
	if _, err := os.Lstat(q.owner.Root); errors.Is(err, os.ErrNotExist) {
		record.RootState = "absent"
	} else if id, err := protocol.NativeIdentity(q.owner.Root, true); err == nil {
		record.ObservedRootNative = id
		if id == q.owner.RootNative && protocol.CheckPrivate(q.owner.Root, true) == nil {
			record.RootState = "retained_owned"
		}
	}
	switch {
	case record.RootState != "absent":
		record.Outcome, record.Reason = "retained", "cleanup_refused"
	case closeErr != nil:
		record.Reason = "binary_close_failed"
	case errors.Is(result, errTimeout):
		record.Outcome, record.Reason = "timeout", "lifecycle_refused"
	case result != nil:
		record.Reason = "lifecycle_refused"
	case exit != 0:
		record.Outcome, record.Reason = "test_failed", "test_exit_failed"
	case q.observation.Outcome == "observed" && q.consumersReleased && record.NativeCloseVerified:
		record.Outcome, record.Reason, record.ObservationConfirmed = "observed", "none", true
	}
	identity, identityErr := protocol.NativeIdentity(q.receipt, true)
	data, err := protocol.Encode(record)
	if err != nil || identityErr != nil || identity != q.receiptNative ||
		protocol.WriteProtectedNew(filepath.Join(q.receipt, protocol.CalendarObservationCloseoutName), data) != nil {
		return errors.Join(result, closeErr, errors.New("calendar_observation_closeout_write_refused"))
	}
	if _, err := protocol.DecodeCalendarObservationCloseout(data); err != nil {
		return errors.Join(result, closeErr, errors.New("calendar_observation_closeout_refused"))
	}
	q.closeoutVerified = true
	if !record.ObservationConfirmed {
		return errors.Join(result, closeErr, errors.New("calendar_observation_unproved"))
	}
	return nil
}

func runCalendarObserved(binary, digest, source, parent, receipt string, timeout time.Duration) (protocol.CalendarObservation, error) {
	w, q, err := newBoundRun(binary, digest, source, parent, receipt, protocol.CalendarObservationTest)
	if err != nil {
		return protocol.CalendarObservation{}, err
	}
	exit, result := w.run(binary, []string{"-test.run=^" + protocol.CalendarObservationTest + "$", "-test.timeout=4m"}, qualifiedEnv(os.Environ(), q), timeout)
	if err := q.finish(exit, result); err != nil {
		return protocol.CalendarObservation{}, err
	}
	return q.observation, nil
}
