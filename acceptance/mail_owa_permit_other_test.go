//go:build acceptance && !windows

package acceptance

import "errors"

func owaRealAcceptancePermit() error {
	return errors.New("qualification_protocol_refused")
}

func owaEmitQualification(map[string]int64, int64, int64) error {
	return errors.New("qualification_protocol_refused")
}

func owaEmitDiagnostic(string, []string) error {
	return errors.New("qualification_protocol_refused")
}
