//go:build darwin

package officecapture

import (
	"context"
	"errors"
	"os"
	goruntime "runtime"
	"sync/atomic"
	"testing"
)

func TestTextReadAndCloseGuardForeignThread(t *testing.T) {
	path := textFixture(t)
	read := registryReadAt
	t.Cleanup(func() { registryReadAt = read })
	var foreign atomic.Bool
	registryReadAt = func(f *os.File, b []byte, offset int64) (int, error) {
		if foreign.Load() && policyGet(3, 1) != 1 {
			return 0, errors.New("content read without materialization OFF")
		}
		return read(f, b, offset)
	}
	err := WithTextCapture(context.Background(), TextInput{path, "docx"}, func(c *TextCapture) error {
		done := make(chan error, 1)
		go func() {
			goruntime.LockOSThread()
			previous := policyGet(3, 1)
			if previous < 0 || policySet(3, 1, 0) != 0 {
				done <- errors.New("fixture thread policy failed")
				goruntime.UnlockOSThread()
				return
			}
			foreign.Store(true)
			_, readErr := c.ReadText(context.Background())
			closeErr := c.Close()
			foreign.Store(false)
			restoreErr := error(nil)
			if policySet(3, 1, previous) != 0 {
				restoreErr = errors.New("fixture policy restore failed")
			}
			goruntime.UnlockOSThread()
			done <- errors.Join(readErr, closeErr, restoreErr)
		}()
		return <-done
	})
	if err != nil {
		t.Fatal("foreign-thread read/close lacked its own native guard")
	}
}
