//go:build unix

package cli

import (
	"bytes"
	"errors"
	"os"
	"testing"

	"golang.org/x/sys/unix"
)

func stubWinsize(t *testing.T, cols uint16, err error) {
	t.Helper()
	old := winsize
	winsize = func(int, uint) (*unix.Winsize, error) { return &unix.Winsize{Col: cols}, err }
	t.Cleanup(func() { winsize = old })
}

func TestFileWidthIsTheTerminalColumnCount(t *testing.T) {
	stubWinsize(t, 132, nil)
	if got := fileWidth(os.Stdout); got != 132 {
		t.Fatalf("fileWidth = %d, want 132", got)
	}
}

func TestFileWidthIsZeroWhenTheFileIsNotATerminal(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if got := fileWidth(f); got != 0 {
		t.Fatalf("fileWidth(regular file) = %d, want 0", got)
	}
	stubWinsize(t, 0, errors.New("ioctl failed"))
	if got := fileWidth(os.Stdout); got != 0 {
		t.Fatalf("fileWidth after ioctl error = %d, want 0", got)
	}
}

func TestTermWidthPrefersTheTerminalThenColumnsThenDefault(t *testing.T) {
	rt := &runtime{stdout: os.Stdout}
	stubWinsize(t, 140, nil)
	t.Setenv("COLUMNS", "90")
	if got := rt.termWidth(); got != 140 {
		t.Fatalf("terminal width = %d, want 140", got)
	}
	stubWinsize(t, 0, errors.New("not a tty"))
	if got := rt.termWidth(); got != 90 {
		t.Fatalf("COLUMNS width = %d, want 90", got)
	}
	t.Setenv("COLUMNS", "0")
	if got := rt.termWidth(); got != 100 {
		t.Fatalf("default width = %d, want 100", got)
	}
	// A stdout that is not a file skips the terminal query entirely.
	rt = &runtime{stdout: &bytes.Buffer{}}
	t.Setenv("COLUMNS", "77")
	if got := rt.termWidth(); got != 77 {
		t.Fatalf("buffer width = %d, want 77", got)
	}
}

func TestAtoiPositiveRejectsZeroNegativeAndJunk(t *testing.T) {
	for _, bad := range []string{"0", "-3", "abc", ""} {
		if _, err := atoiPositive(bad); err == nil {
			t.Errorf("atoiPositive(%q) succeeded", bad)
		}
	}
	if n, err := atoiPositive("42"); err != nil || n != 42 {
		t.Fatalf("atoiPositive(42) = %d, %v", n, err)
	}
}
