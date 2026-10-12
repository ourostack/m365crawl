package officecapture

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestJSONCopyBoundsProgressAndReturnedFaults(t *testing.T) {
	path, _ := ordinaryFixture(t)
	for _, fault := range []string{"cancel", "negative", "oversized-read", "zero-progress", "read", "size-bound", "write", "short-write", "empty"} {
		t.Run(fault, func(t *testing.T) {
			source, err := os.Open(path) //nolint:gosec // Test-owned synthetic fixture.
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := source.Close(); err != nil {
					t.Error(err)
				}
			}()
			read := jsonRead
			t.Cleanup(func() { jsonRead = read })
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if fault == "cancel" {
				cancel()
			}
			jsonRead = func(*os.File, []byte) (int, error) {
				switch fault {
				case "negative":
					return -1, nil
				case "oversized-read":
					return 65537, nil
				case "zero-progress":
					return 0, nil
				case "read":
					return 0, os.ErrPermission
				case "size-bound":
					return 65536, nil
				case "empty":
					return 0, io.EOF
				}
				return 1, io.EOF
			}
			var writer io.Writer = &bytes.Buffer{}
			if fault == "write" || fault == "short-write" {
				writer = faultWriter{fail: fault == "write"}
			}
			n, err := copyJSON(ctx, writer, source)
			if fault == "empty" {
				if err != nil || n != 0 {
					t.Fatal("empty input not copied exactly")
				}
			} else if err == nil {
				t.Fatal("fault or size bound ignored")
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := settleJSON(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("settle lost cancellation")
	}
}

type faultWriter struct{ fail bool }

func (w faultWriter) Write([]byte) (int, error) {
	if w.fail {
		return 0, os.ErrPermission
	}
	return 0, nil
}

func TestJSONCaptureReturnedIOFaultsRefuseAndClean(t *testing.T) {
	for _, fault := range []string{"root", "create", "read", "write", "output-stat", "output-close", "source-close", "settle", "settle-mutation", "private-stat", "private-open", "private-read", "private-close", "remove", "absence"} {
		t.Run(fault, func(t *testing.T) {
			path, _ := ordinaryFixture(t)
			root, create, read, write, closeFile, remove, lstat, settle := jsonRoot, jsonCreate, jsonRead, jsonWrite, jsonClose, jsonRemove, jsonLstat, jsonSettle
			t.Cleanup(func() {
				jsonRoot, jsonCreate, jsonRead, jsonWrite, jsonClose, jsonRemove, jsonLstat, jsonSettle = root, create, read, write, closeFile, remove, lstat, settle
			})
			var privateRoot, privatePath string
			t.Cleanup(func() {
				if privatePath != "" {
					if err := os.Remove(privatePath); err != nil && !os.IsNotExist(err) {
						t.Error(err)
					}
				}
				if privateRoot != "" {
					if err := os.Remove(privateRoot); err != nil && !os.IsNotExist(err) {
						t.Error(err)
					}
				}
			})
			jsonRoot = func() (string, error) {
				if fault == "root" {
					return "", os.ErrPermission
				}
				value, err := root()
				if err == nil {
					privateRoot, err = filepath.EvalSymlinks(value)
					privatePath = filepath.Join(privateRoot, "source.json")
				}
				return value, err
			}
			jsonCreate = func(name string) (*os.File, error) {
				if fault == "create" {
					return nil, os.ErrPermission
				}
				return create(name)
			}
			jsonRead = func(f *os.File, b []byte) (int, error) {
				if fault == "read" || (fault == "private-read" && f.Name() == "source.json") {
					return 0, os.ErrPermission
				}
				return read(f, b)
			}
			jsonWrite = func(f *os.File, b []byte) (int, error) {
				if fault == "write" {
					return 0, os.ErrPermission
				}
				n, err := write(f, b)
				if fault == "output-stat" {
					return n, errors.Join(err, f.Close())
				}
				return n, err
			}
			jsonClose = func(f *os.File) error {
				err := closeFile(f)
				if (fault == "output-close" && f.Name() == privatePath) ||
					(fault == "source-close" && f.Name() == "recent.json") ||
					(fault == "private-close" && f.Name() == "source.json") {
					return errors.Join(err, os.ErrPermission)
				}
				return err
			}
			jsonSettle = func(ctx context.Context) error {
				if fault == "settle" {
					return os.ErrPermission
				}
				if fault == "settle-mutation" {
					if err := os.Chtimes(path, time.Now(), time.Now().Add(time.Hour)); err != nil {
						return err
					}
				}
				return settle(ctx)
			}
			jsonLstat = func(name string) (os.FileInfo, error) {
				if fault == "private-stat" && name == privatePath {
					return nil, os.ErrPermission
				}
				if fault == "absence" && name == privateRoot {
					return os.Stat(filepath.Dir(path))
				}
				return lstat(name)
			}
			jsonRemove = func(name string) error {
				err := remove(name)
				if fault == "remove" {
					return errors.Join(err, os.ErrPermission)
				}
				return err
			}
			err := WithJSONCapture(context.Background(), DocumentInput{Path: path, Surface: "recent", Application: "supplied"}, func(c *JSONCapture) error {
				if fault == "private-open" {
					if err := os.Remove(c.Path); err != nil {
						return err
					}
					return os.Mkdir(c.Path, 0o700)
				}
				return nil
			})
			if fault == "private-open" {
				if err := os.Remove(privatePath); err != nil && !os.IsNotExist(err) {
					t.Error(err)
				}
			}
			if err == nil {
				t.Fatal("returned fault admitted")
			}
		})
	}
}

func TestJSONCaptureBoundsAndPreCancelledOperation(t *testing.T) {
	path, _ := ordinaryFixture(t)
	if err := os.Truncate(path, jsonLimit+1); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, ctx := range []context.Context{context.Background(), ctx} {
		called := false
		if err := WithJSONCapture(ctx, DocumentInput{Path: path, Surface: "recent", Application: "supplied"}, func(*JSONCapture) error {
			called = true
			return nil
		}); err == nil || called {
			t.Fatal("oversized or pre-cancelled source consumed")
		}
	}
	if err := WithJSONCapture(context.Background(), DocumentInput{Path: path, Surface: "recent", Application: "supplied"}, nil); err == nil {
		t.Fatal("nil consumer admitted")
	}
	if signature, err := ObserveJSON(context.Background(), filepath.Join(filepath.Dir(path), "missing")); err == nil || signature != "" {
		t.Fatal("missing observation gained retry signature")
	}
}
