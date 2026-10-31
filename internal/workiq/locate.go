package workiq

import (
	"os"
	"testing"
)

// Locate finds the executable an explicit WorkIQ command would start, without starting it.
func Locate() error {
	binary, err := chooseBinary(Invocation{}, testing.Testing())
	if err != nil {
		return err
	}
	if _, err := os.Stat(binary); err != nil {
		return failure("workiq_not_installed")
	}
	return nil
}
