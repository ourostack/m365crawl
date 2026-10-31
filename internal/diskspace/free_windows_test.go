//go:build windows

package diskspace

import "testing"

func TestFreeRejectsANulInThePath(t *testing.T) {
	if _, _, err := Free("C:\\a\x00b"); err == nil {
		t.Fatal("want an error for a path with a NUL")
	}
}
