package main

import (
	"encoding/json"
	"errors"

	"github.com/ourostack/m365crawl/scripts/owaacceptance/protocol"
)

type failureDiagnosticError struct {
	stage  string
	reason string
	fields []string
	cause  error
}

func (e *failureDiagnosticError) Error() string { return "qualification_unproved" }
func (e *failureDiagnosticError) Unwrap() error { return e.cause }

func failurePacket(err error) string {
	var failure *failureDiagnosticError
	if errors.As(err, &failure) && protocol.DiagnosticStage(failure.stage) && protocol.DiagnosticReason(failure.stage, failure.reason) && protocol.DiagnosticFields(failure.fields) {
		fields, encodeErr := json.Marshal(failure.fields)
		if encodeErr == nil {
			return "owa_acceptance status=unqualified stage=" + failure.stage + " reason=" + failure.reason + " fields=" + string(fields)
		}
	}
	return "owa_acceptance status=unqualified stage=unproved reason=unproved fields=[]"
}

func diagnosticFailure(stage string, fields []string, cause error, reasons ...string) error {
	reason := "unproved"
	if len(reasons) > 0 {
		reason = reasons[0]
	}
	return &failureDiagnosticError{stage: stage, reason: reason, fields: append([]string{}, fields...), cause: cause}
}
