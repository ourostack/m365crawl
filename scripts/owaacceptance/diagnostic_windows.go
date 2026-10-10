package main

import (
	"encoding/json"
	"errors"

	"github.com/ourostack/m365crawl/scripts/owaacceptance/protocol"
)

type failureDiagnosticError struct {
	stage     string
	reason    string
	fields    []string
	omissions map[string]int64
	cause     error
}

func (e *failureDiagnosticError) Error() string { return "qualification_unproved" }
func (e *failureDiagnosticError) Unwrap() error { return e.cause }

func failurePacket(err error) string {
	var failure *failureDiagnosticError
	if errors.As(err, &failure) && protocol.DiagnosticStage(failure.stage) && protocol.DiagnosticReason(failure.stage, failure.reason) && protocol.DiagnosticFields(failure.fields) && protocol.DiagnosticOmissions(failure.stage, failure.reason, failure.omissions) {
		fields, encodeErr := json.Marshal(failure.fields)
		if encodeErr == nil {
			packet := "owa_acceptance status=unqualified stage=" + failure.stage + " reason=" + failure.reason + " fields=" + string(fields)
			if len(failure.omissions) != 0 {
				omissions, err := json.Marshal(failure.omissions)
				if err != nil {
					return "owa_acceptance status=unqualified stage=unproved reason=unproved fields=[]"
				}
				packet += " omissions=" + string(omissions)
			}
			return packet
		}
	}
	return "owa_acceptance status=unqualified stage=unproved reason=unproved fields=[]"
}

func diagnosticFailure(stage string, fields []string, cause error, reasons ...string) error {
	reason := "unproved"
	if len(reasons) > 0 {
		reason = reasons[0]
	}
	return &failureDiagnosticError{stage: stage, reason: reason, fields: append([]string{}, fields...), omissions: map[string]int64{}, cause: cause}
}

func diagnosticWitnessFailure(d protocol.Diagnostic, cause error) error {
	return &failureDiagnosticError{stage: d.Stage, reason: d.Reason, fields: d.Fields, omissions: d.Omissions, cause: cause}
}
