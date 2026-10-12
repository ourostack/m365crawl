//go:build darwin

package officecapture

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestJSONCapturePathRebindingAndPrivateOpenFailures(t *testing.T) {
	for _, change := range []string{"source-link", "source-file", "private-open", "reader-close", "source-stat", "canonical-root"} {
		t.Run(change, func(t *testing.T) {
			path, payload := ordinaryFixture(t)
			open, closeFile, root := inputOpen, jsonClose, jsonRoot
			t.Cleanup(func() { inputOpen, jsonClose, jsonRoot = open, closeFile, root })
			var removedRoot string
			if change == "canonical-root" {
				jsonRoot = func() (string, error) {
					value, err := root()
					if err == nil {
						err = os.Remove(value)
					}
					removedRoot = value
					return value, err
				}
			}
			inputOpen = func(fd int, name string, flags int, mode uint32) (int, error) {
				native, err := open(fd, name, flags, mode)
				if err == nil && change == "source-stat" && name == "recent.json" {
					if err := os.NewFile(uintptr(native), "closed").Close(); err != nil {
						return -1, err
					}
				}
				return native, err
			}
			err := WithJSONCapture(context.Background(), DocumentInput{Path: path, Surface: "recent", Application: "supplied"}, func(c *JSONCapture) error {
				switch change {
				case "source-link", "source-file":
					if err := os.Rename(path, path+".old"); err != nil {
						return err
					}
					if change == "source-link" {
						return os.Symlink(path+".old", path)
					}
					return os.WriteFile(path, payload, 0o600)
				case "private-open":
					inputOpen = func(fd int, name string, flags int, mode uint32) (int, error) {
						if name == "source.json" {
							return -1, os.ErrPermission
						}
						return open(fd, name, flags, mode)
					}
				case "reader-close":
					jsonClose = func(f *os.File) error {
						err := closeFile(f)
						if f.Name() == "source.json" {
							return errors.Join(err, os.ErrPermission)
						}
						return err
					}
					result, err := c.ReadJSON(context.Background())
					if err == nil || len(result.Documents) != 0 {
						t.Fatal("reader close failure admitted decoded content")
					}
					return err
				}
				return nil
			})
			if err == nil {
				t.Fatal("rebound or failed source admitted")
			}
			if removedRoot != "" {
				if _, err := os.Lstat(removedRoot); !os.IsNotExist(err) {
					t.Fatal("failed canonical root not absent")
				}
			}
		})
	}
	directory, err := openInput("/", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := directory.close(); err != nil {
		t.Fatal(err)
	}
}

func TestJSONReaderPreservesCallerCancellation(t *testing.T) {
	path, _ := ordinaryFixture(t)
	if err := WithJSONCapture(context.Background(), DocumentInput{Path: path, Surface: "recent", Application: "supplied"}, func(c *JSONCapture) error {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		result, err := c.ReadJSON(ctx)
		if !errors.Is(err, context.Canceled) || len(result.Documents) != 0 {
			t.Fatal("private reader lost caller cancellation")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestJSONObservationCloseFailureErasesSignature(t *testing.T) {
	path, _ := ordinaryFixture(t)
	closeFile := jsonClose
	t.Cleanup(func() { jsonClose = closeFile })
	jsonClose = func(f *os.File) error { return errors.Join(closeFile(f), os.ErrPermission) }
	if signature, err := ObserveJSON(context.Background(), path); err == nil || signature != "" {
		t.Fatal("failed metadata close published signature")
	}
}

func TestJSONDiscoveryOtherPlatformAndEmptyExplicitRefuse(t *testing.T) {
	path, _ := ordinaryFixture(t)
	defaultMac := discoveryMac
	t.Cleanup(func() { discoveryMac = defaultMac })
	discoveryMac = false
	if inputs, err := DiscoverJSON(context.Background(), ""); err == nil || inputs != nil {
		t.Fatal("unqualified platform default inferred")
	}
	empty, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if inputs, err := DiscoverJSON(context.Background(), empty); err == nil || inputs != nil {
		t.Fatal("empty source root became complete success")
	}
	_ = path
}
