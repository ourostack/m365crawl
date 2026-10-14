//go:build !darwin && !windows

package officecapture

import "testing"

func TestTextUnsupportedHostHasNoNativeEvidence(t *testing.T) {
	if _, err := heldTextEvidence(nil); err == nil {
		t.Fatal("unsupported host invented native evidence")
	}
}
