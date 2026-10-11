//go:build windows

package officecapture

import (
	"context"
	"errors"
	"os"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsSourceScopeCloseFailureIsTerminalAfterSuccessfulOpen(t *testing.T) {
	for _, operation := range []string{"observe", "capture"} {
		t.Run(operation, func(t *testing.T) {
			path, _ := ordinaryFixture(t)
			closeFile, openRoot := jsonClose, inputWinRoot
			t.Cleanup(func() { jsonClose, inputWinRoot = closeFile, openRoot })
			original := windows.InvalidHandle
			inputWinRoot = func(name *uint16, access, share uint32, security *windows.SecurityAttributes,
				disposition, options uint32, template windows.Handle) (windows.Handle, error) {
				handle, err := openRoot(name, access, share, security, disposition, options, template)
				if err == nil && original == windows.InvalidHandle {
					original = handle
				}
				return handle, err
			}
			refused := false
			jsonClose = func(file *os.File) error {
				isOriginal := windows.Handle(file.Fd()) == original
				err := closeFile(file)
				if isOriginal {
					refused = true
					return errors.Join(err, os.ErrPermission)
				}
				return err
			}
			if operation == "observe" {
				signature, err := ObserveJSON(context.Background(), path)
				if err == nil || signature != "" || !refused {
					t.Fatal("late native close failure published a reusable signature")
				}
				return
			}
			called := false
			err := WithJSONCapture(context.Background(), DocumentInput{Path: path, Surface: "recent", Application: "supplied"},
				func(*JSONCapture) error { called = true; return nil })
			if err == nil || !called || !refused {
				t.Fatal("late source scope close did not fail after successful capture callback")
			}
		})
	}
}
