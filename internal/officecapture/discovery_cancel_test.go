package officecapture

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestJSONDiscoveryCancelledInputDoesNotEnumerate(t *testing.T) {
	path, _ := ordinaryFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	read := discoveryRead
	t.Cleanup(func() { discoveryRead = read })
	enumerated := false
	discoveryRead = func(f *os.File, n int) ([]string, error) {
		enumerated = true
		return read(f, n)
	}
	if names, err := inputNames(ctx, filepath.Dir(path)); err == nil || names != nil || enumerated {
		t.Fatal("cancelled input enumerated source directory")
	}
}
