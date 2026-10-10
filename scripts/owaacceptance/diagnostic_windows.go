package main

import (
	"encoding/json"
	"errors"

	"github.com/ourostack/m365crawl/scripts/owaacceptance/protocol"
)

type failureDiagnosticError struct {
	stage  string
	fields []string
	cause  error
}

func (e *failureDiagnosticError) Error() string { return "qualification_unproved" }
func (e *failureDiagnosticError) Unwrap() error { return e.cause }

func failurePacket(err error) string {
	var failure *failureDiagnosticError
	if errors.As(err, &failure) && protocol.DiagnosticStage(failure.stage) && protocol.DiagnosticFields(failure.fields) {
		fields, encodeErr := json.Marshal(failure.fields)
		if encodeErr == nil {
			return "owa_acceptance status=unqualified stage=" + failure.stage + " fields=" + string(fields)
		}
	}
	return "owa_acceptance status=unqualified stage=unproved fields=[]"
}

func diagnosticFailure(stage string, fields []string, cause error) error {
	return &failureDiagnosticError{stage: stage, fields: append([]string{}, fields...), cause: cause}
}
