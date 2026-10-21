package main

import (
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/ourostack/m365crawl/scripts/owaacceptance/protocol"
)

func (q *qualifiedRun) verifyCalendarQualification() error {
	data, err := protocol.ReadProtected(filepath.Join(q.receipt, protocol.CalendarQualificationName))
	if err != nil {
		return errors.New("calendar_qualification_missing")
	}
	value, err := protocol.DecodeCalendarQualification(data)
	if err != nil || protocol.MatchCalendarQualification(q.owner, value) != nil {
		return errors.New("calendar_qualification_refused")
	}
	q.calendarQualification = value
	return nil
}

func (q *qualifiedRun) finishCalendarQualification(exit int, result error) error {
	closeErr := errors.Join(q.binary.Close(), q.closePins())
	record := protocol.CalendarQualificationCloseout{
		Version: protocol.Version, Kind: protocol.CalendarQualificationKind, Owner: q.owner,
		Outcome: "refused", Reason: "qualification_refused", Exit: exit, ConsumersReleased: q.consumersReleased,
		NativeCloseVerified: q.nativeCloseVerified && closeErr == nil && q.cleanupVerified, RootState: "retained_unverified",
	}
	// #nosec G703 -- Exact identity-bound generated root; read-only absence observation after the shared release barrier.
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
	case q.calendarQualification.Outcome == "qualified" && q.consumersReleased && record.NativeCloseVerified:
		record.Outcome, record.Reason, record.Qualified = "qualified", "none", true
	}
	identity, identityErr := protocol.NativeIdentity(q.receipt, true)
	data, err := protocol.Encode(record)
	if err != nil || identityErr != nil || identity != q.receiptNative ||
		protocol.WriteProtectedNew(filepath.Join(q.receipt, protocol.CalendarQualificationCloseoutName), data) != nil {
		return errors.Join(result, closeErr, errors.New("calendar_qualification_closeout_write_refused"))
	}
	if _, err := protocol.DecodeCalendarQualificationCloseout(data); err != nil {
		return errors.Join(result, closeErr, errors.New("calendar_qualification_closeout_refused"))
	}
	q.closeoutVerified = true
	if !record.Qualified {
		return errors.Join(result, closeErr, errors.New("calendar_qualification_unproved"))
	}
	return nil
}

func runCalendarQualified(binary, digest, source, parent, receipt string, timeout time.Duration) (protocol.CalendarQualification, error) {
	w, q, err := newBoundRun(binary, digest, source, parent, receipt, protocol.CalendarQualificationTest)
	if err != nil {
		return protocol.CalendarQualification{}, err
	}
	exit, result := w.run(binary, []string{"-test.run=^" + protocol.CalendarQualificationTest + "$", "-test.timeout=4m"}, qualifiedEnv(os.Environ(), q), timeout)
	if err := q.finish(exit, result); err != nil {
		return protocol.CalendarQualification{}, err
	}
	return q.calendarQualification, nil
}
