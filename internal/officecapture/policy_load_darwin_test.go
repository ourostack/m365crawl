//go:build darwin

package officecapture

import (
	"context"
	"errors"
	"os"
	"testing"
)

func TestPolicyBindingFailuresRefuseBeforeOperation(t *testing.T) {
	if err := withInputGuard(context.Background(), func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	for _, fault := range []string{"load", "get-symbol", "set-symbol"} {
		t.Run(fault, func(t *testing.T) {
			open, symbol, closeLibrary, savedErr := policyOpen, policySymbol, policyCloseLibrary, policyErr
			t.Cleanup(func() {
				policyOpen, policySymbol, policyCloseLibrary, policyErr = open, symbol, closeLibrary, savedErr
			})
			policyOpen = func(string, int) (uintptr, error) {
				if fault == "load" {
					return 0, os.ErrPermission
				}
				return 1, nil
			}
			policySymbol = func(_ uintptr, name string) (uintptr, error) {
				if (fault == "get-symbol" && name == "getiopolicy_np") || (fault == "set-symbol" && name == "setiopolicy_np") {
					return 0, os.ErrPermission
				}
				return 1, nil
			}
			policyCloseLibrary = func(uintptr) error { return os.ErrPermission }
			policyErr = nil
			loadPolicy()
			if !errors.Is(policyErr, os.ErrPermission) {
				t.Fatal("bootstrap failure not retained")
			}
			called := false
			if err := withInputGuard(context.Background(), func() error {
				called = true
				return nil
			}); err == nil || called {
				t.Fatal("unknown native binding performed source operation")
			}
		})
	}
}
