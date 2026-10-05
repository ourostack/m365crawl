//go:build unix

package outlookdesktop

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestSourceOpenFlagsDoNotBlockOnAFIFO(t *testing.T) {
	if sourceOpenFlags&syscall.O_NONBLOCK == 0 || sourceOpenFlags&(os.O_WRONLY|os.O_RDWR) != 0 {
		t.Fatalf("flags = %#x", sourceOpenFlags)
	}
	fifo := filepath.Join(t.TempDir(), "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skip("mkfifo unavailable:", err)
	}
	done := make(chan error, 1)
	go func() {
		f, err := os.OpenFile(fifo, sourceOpenFlags, 0) //nolint:gosec // test opens its own temp FIFO
		if err == nil {
			_ = f.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("opening a FIFO blocked")
	}
}

func TestSnapshotRejectsAFIFO(t *testing.T) {
	quiet(t)
	tmp := tempHome(t)
	fifo := filepath.Join(t.TempDir(), StoreFileName)
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skip("mkfifo unavailable:", err)
	}
	_, cleanup, err := Snapshot(t.Context(), fifo)
	if err == nil {
		t.Fatal("a FIFO was accepted")
	}
	cleanup()
	assertClean(t, tmp)
}
