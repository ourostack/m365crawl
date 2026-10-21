package main

import (
	"errors"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/ourostack/m365crawl/scripts/owaacceptance/protocol"
)

func (q *qualifiedRun) readCalendarFailure() {
	data, err := protocol.ReadProtected(filepath.Join(q.receipt, protocol.CalendarQualificationFailureName))
	if err != nil {
		return
	}
	value, err := protocol.DecodeCalendarQualificationFailure(data)
	if err != nil || protocol.MatchCalendarQualificationFailure(q.owner, value) != nil {
		return
	}
	q.calendarFailure = value
}

type calendarProductFailureError struct {
	failure protocol.CalendarQualificationFailure
	cause   error
}

func (e *calendarProductFailureError) Error() string { return "calendar_qualification_unproved" }
func (e *calendarProductFailureError) Unwrap() error { return e.cause }

func calendarFailurePacket(err error) string {
	const unknown = "owa_calendar_qualification status=unqualified stage=unproved reason=unproved fields=[]"
	var failure *calendarProductFailureError
	if !errors.As(err, &failure) || protocol.MatchCalendarQualificationFailure(failure.failure.Owner, failure.failure) != nil {
		return unknown
	}
	// Every label was validated against fixed ASCII vocabulary; no source text needs quoting.
	fields := "[]"
	if len(failure.failure.Fields) != 0 {
		fields = `["` + strings.Join(failure.failure.Fields, `","`) + `"]`
	}
	packet := "owa_calendar_qualification status=unqualified stage=" + failure.failure.Stage + " reason=" + failure.failure.Reason + " fields=" + fields
	if len(failure.failure.Omissions) != 0 {
		keys := make([]string, 0, len(failure.failure.Omissions))
		for key := range failure.failure.Omissions {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		omissions := make([]string, 0, len(keys))
		for _, key := range keys {
			omissions = append(omissions, `"`+key+`":`+strconv.FormatInt(failure.failure.Omissions[key], 10))
		}
		packet += " omissions={" + strings.Join(omissions, ",") + "}"
	}
	return packet
}
