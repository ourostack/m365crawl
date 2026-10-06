//go:build !darwin

package outlookdesktop

import (
	"errors"
	"runtime"
	"testing"
)

func TestDefaultRootNotSupportedOffMacOS(t *testing.T) {
	orig := userHome
	t.Cleanup(func() { userHome = orig })
	userHome = func() (string, error) { return "/fake/home", nil }
	got, err := DefaultRoot()
	var ns *NotSupportedError
	if got != "" || !errors.Is(err, ErrNotSupported) || !errors.As(err, &ns) || ns.GOOS != runtime.GOOS {
		t.Fatalf("got %q, %v", got, err)
	}
}
