//go:build !windows

package acceptance

import "testing"

func setCurrentUserAndSystemOnly(t *testing.T, _ string) {
	t.Helper()
}
