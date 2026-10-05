//go:build !darwin

package outlookdesktop

import "runtime"

// platformRoot has no answer off macOS: another engineer owns the Windows layout.
func platformRoot(string) (string, error) {
	return "", &NotSupportedError{GOOS: runtime.GOOS}
}
