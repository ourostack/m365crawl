//go:build !darwin && !linux

package outlookdesktop

import "testing"

func TestRealFreeBytesUnmeasured(t *testing.T) {
	if _, ok, err := realFreeBytes(t.TempDir()); ok || err != nil {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
}
