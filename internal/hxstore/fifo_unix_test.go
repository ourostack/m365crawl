//go:build unix

package hxstore

import (
	"errors"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestOpenFileRefusesNamedPipe: a FIFO has no writer, so opening it for reading
// would block forever. OpenFile must refuse it before opening.
func TestOpenFileRefusesNamedPipe(t *testing.T) {
	p := filepath.Join(t.TempDir(), "pipe")
	if err := syscall.Mkfifo(p, 0o600); err != nil {
		t.Skip("cannot make a named pipe here:", err)
	}
	done := make(chan error, 1)
	go func() { _, err := OpenFile(p); done <- err }()
	select {
	case err := <-done:
		if !errors.Is(err, ErrNotRegular) {
			t.Fatalf("got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("OpenFile blocked on a named pipe")
	}
}
