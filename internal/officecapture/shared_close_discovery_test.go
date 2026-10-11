package officecapture

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestJSONMetadataCloseFailureCannotPublishSignature(t *testing.T) {
	path, _ := ordinaryFixture(t)
	closeFile := jsonClose
	t.Cleanup(func() { jsonClose = closeFile })
	jsonClose = func(file *os.File) error { return errors.Join(closeFile(file), os.ErrPermission) }
	if signature, err := ObserveJSON(context.Background(), path); err == nil || signature != "" {
		t.Fatal("unclosed metadata scope published a reusable acquisition signature")
	}
}

func TestJSONPrivateReaderCancelledWithoutReturningClaims(t *testing.T) {
	path, _ := ordinaryFixture(t)
	err := WithJSONCapture(context.Background(), DocumentInput{Path: path, Surface: "recent", Application: "supplied"},
		func(capture *JSONCapture) error {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			result, err := capture.ReadJSON(ctx)
			if !errors.Is(err, context.Canceled) || len(result.Documents) != 0 {
				t.Fatal("cancelled private reader returned success-shaped metadata")
			}
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
}

func TestJSONUnqualifiedDefaultAndEmptyExplicitRemainRefusals(t *testing.T) {
	mac := discoveryMac
	t.Cleanup(func() { discoveryMac = mac })
	discoveryMac = false
	if inputs, err := DiscoverJSON(context.Background(), ""); err == nil || inputs != nil {
		t.Fatal("unqualified default platform became available source")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if inputs, err := DiscoverJSON(context.Background(), root); err == nil || inputs != nil {
		t.Fatal("empty supplied directory became complete metadata capture")
	}
}

func TestJSONRemovedPrivateRootFailsBeforeParser(t *testing.T) {
	path, _ := ordinaryFixture(t)
	createRoot := jsonRoot
	t.Cleanup(func() { jsonRoot = createRoot })
	jsonRoot = func() (string, error) {
		root, err := createRoot()
		if err == nil {
			err = os.Remove(root)
		}
		return root, err
	}
	called := false
	err := WithJSONCapture(context.Background(), DocumentInput{Path: path, Surface: "recent", Application: "supplied"},
		func(*JSONCapture) error { called = true; return nil })
	if err == nil || called {
		t.Fatal("missing private root admitted a callback or successful capture")
	}
}

func TestJSONPrivateInputCloseErrorBecomesTerminalFailure(t *testing.T) {
	path, _ := ordinaryFixture(t)
	file, err := os.Open(path) //nolint:gosec // Test-owned synthetic fixture.
	if err != nil {
		t.Fatal(err)
	}
	closeFile := jsonClose
	t.Cleanup(func() { jsonClose = closeFile })
	jsonClose = func(file *os.File) error {
		return errors.Join(closeFile(file), os.ErrPermission)
	}
	if err := safeInputClose(context.Background(), &guardedInput{files: []*os.File{file}}); err == nil {
		t.Fatal("private reader close error became successful admission")
	}
}
