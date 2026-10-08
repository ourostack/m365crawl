//go:build linux

package browser

import (
	"os"
	"testing"
)

func TestReadProcArgsLinux(t *testing.T) {
	argv, err := readProcArgs(os.Getpid())
	if err != nil || len(argv) == 0 {
		t.Fatalf("own arguments: %q %v", argv, err)
	}
	if _, err := readProcArgs(2147483646); err == nil {
		t.Fatal("a process that does not exist has no arguments")
	}
}
