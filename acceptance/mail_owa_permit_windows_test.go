//go:build acceptance && windows

package acceptance

import "github.com/ourostack/m365crawl/scripts/owaacceptance/protocol"

func owaRealAcceptancePermit() error {
	_, err := protocol.CurrentGuard()
	return err
}

func owaEmitQualification(counts map[string]int64, observed, unstable int64) error {
	owner, err := protocol.CurrentGuard()
	if err != nil {
		return err
	}

	return protocol.EmitQualification(owner, counts, observed, unstable)
}

func owaEmitDiagnostic(stage, reason string, fields []string, omissions map[string]int64) error {
	owner, err := protocol.CurrentGuard()
	if err != nil {
		return err
	}
	return protocol.EmitDiagnostic(owner, stage, reason, fields, omissions)
}
