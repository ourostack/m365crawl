//go:build acceptance

package acceptance

import (
	"github.com/ourostack/m365crawl/internal/syncer"
	"github.com/ourostack/m365crawl/scripts/owaacceptance/protocol"
)

func (s *owaFailureState) calendarSourceFailure(rep syncer.Report, profile string, step int) {
	if s == nil || s.failure != "" {
		return
	}
	pass := "pass_unproved"
	switch step {
	case 0:
		pass = "pass_initial"
	case 1:
		pass = "pass_unchanged"
	case 2:
		pass = "pass_full_read"
	}
	mail, found := mailSource(rep)
	var cal syncer.SourceReport
	for _, row := range rep.Sources {
		if row.Source == "outlook|"+profile {
			cal = row
		}
	}
	field := "source_reports"
	var omissions map[string]int64
	valid := true
	switch {
	case !found || mail.Source != "outlook|"+profile+"|mail":
		field = "mail_source"
	case len(rep.Sources) != 2:
	case cal.Source == "":
		field = "calendar_source"
	case cal.Error != nil:
		field = protocol.CalendarQualificationSourceErrorField(cal.Error.Code)
	case len(cal.Omissions) != 0:
		field = "calendar_omissions"
		omissions, valid = protocol.CalendarQualificationSourceOmissions(cal.Omissions)
	case len(mail.Omissions) != 0:
		field = "mail_omissions"
		omissions, valid = protocol.DiagnosticSourceOmissions(mail.Omissions)
	}
	reason := "sync_source_refused"
	if !valid {
		reason = "unproved"
	}
	s.recordFields(protocol.StageImport, reason, []string{pass, field})
	if valid && omissions != nil {
		s.omissions = omissions
	}
}
