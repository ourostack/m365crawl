//go:build windows

package officecapture

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWindowsSourceScopeCloseFailureIsTerminalAfterSuccessfulOpen(t *testing.T) {
	for _, operation := range []string{"observe", "capture"} {
		t.Run(operation, func(t *testing.T) {
			path, _ := ordinaryFixture(t)
			closeFile := jsonClose
			t.Cleanup(func() { jsonClose = closeFile })
			root := filepath.VolumeName(path) + `\`
			refused := false
			jsonClose = func(file *os.File) error {
				err := closeFile(file)
				if file.Name() == root {
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
