package onenote

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestObserveIndexMetadataFaults(t *testing.T) {
	path, err := filepath.EvalSymlinks(indexFixture(t, "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	for _, fault := range []string{"absolute", "canonical", "companion-stat", "second-stat"} {
		t.Run(fault, func(t *testing.T) {
			abs, eval, lstat := snapshotAbs, snapshotEval, snapshotLstat
			t.Cleanup(func() { snapshotAbs, snapshotEval, snapshotLstat = abs, eval, lstat })
			calls := 0
			switch fault {
			case "absolute":
				snapshotAbs = func(string) (string, error) { return "", errors.New("absolute fault") }
			case "canonical":
				snapshotEval = func(string) (string, error) { return "", errors.New("canonical fault") }
			case "companion-stat", "second-stat":
				snapshotLstat = func(name string) (os.FileInfo, error) {
					calls++
					if (fault == "companion-stat" && strings.HasSuffix(name, "-journal")) ||
						(fault == "second-stat" && calls == 5) {
						return nil, os.ErrPermission
					}
					return lstat(name)
				}
			}
			if fault != "second-stat" {
				if signature, err := ObserveIndex(path); err == nil || signature != "" {
					t.Fatal("unknown observation turned into retry evidence")
				}
			}
			if capture, err := CaptureIndex(context.Background(), path); err == nil || capture != nil {
				t.Fatal("metadata fault admitted")
			}
		})
	}
}

func TestCaptureIndexValidationAndPrivateReadFaults(t *testing.T) {
	for _, fault := range []string{"held-closed", "size", "companion", "header", "deadline-after-header", "private-missing", "private-open", "private-read", "private-close", "private-modified", "seek", "cleanup"} {
		t.Run(fault, func(t *testing.T) {
			path, err := filepath.EvalSymlinks(indexFixture(t, "index.db"))
			if err != nil {
				t.Fatal(err)
			}
			capture, err := CaptureIndex(context.Background(), path)
			if err != nil {
				t.Fatal(err)
			}
			open, read, readAt, closeFile, remove := snapshotOpen, snapshotRead, snapshotReadAt, snapshotClose, snapshotRemove
			t.Cleanup(func() {
				snapshotOpen, snapshotRead, snapshotReadAt, snapshotClose, snapshotRemove = open, read, readAt, closeFile, remove
				_ = capture.Close()
				_ = os.Remove(capture.Path)
				_ = os.Remove(filepath.Dir(capture.Path))
			})
			switch fault {
			case "held-closed", "seek":
				if err := capture.source.Close(); err != nil {
					t.Fatal(err)
				}
				if fault == "seek" {
					if _, err := capture.copy(nil); err == nil {
						t.Fatal("closed source seek accepted")
					}
				}
			case "size":
				if err := os.Truncate(path, capture.info.Size()-1); err != nil {
					t.Skipf("host disallows mutation while locked: %v", err)
				}
			case "companion":
				if err := os.WriteFile(path+"-wal", []byte("active"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "header":
				snapshotReadAt = func(*os.File, []byte, int64) (int, error) { return 0, errors.New("header fault") }
			case "deadline-after-header":
				snapshotReadAt = func(f *os.File, b []byte, off int64) (int, error) {
					n, err := readAt(f, b, off)
					capture.cancel()
					return n, err
				}
			case "private-missing":
				if err := os.Remove(capture.Path); err != nil {
					t.Fatal(err)
				}
			case "private-open":
				snapshotOpen = func(string) (*os.File, error) { return nil, errors.New("private open fault") }
			case "private-read":
				snapshotRead = func(f *os.File, b []byte) (int, error) {
					if f.Name() == capture.Path {
						return 0, errors.New("private read fault")
					}
					return read(f, b)
				}
			case "private-close":
				snapshotClose = func(f *os.File) error {
					err := closeFile(f)
					if f.Name() == capture.Path {
						return errors.Join(err, errors.New("private close fault"))
					}
					return err
				}
			case "private-modified":
				if err := os.WriteFile(capture.Path, []byte("modified"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "cleanup":
				snapshotRemove = func(string) error { return os.ErrPermission }
			}
			if err := capture.Close(); err == nil {
				t.Fatalf("%s fault concealed", fault)
			}
		})
	}
}

func TestSnapshotDigestBoundsAndCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bytes")
	if err := os.WriteFile(path, []byte("literal"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, size := range []int64{0, 8} {
		f, err := os.Open(path) //nolint:gosec // path is a synthetic file created in this test's owned temporary directory
		if err != nil {
			t.Fatal(err)
		}
		if _, err := snapshotDigest(context.Background(), f, nil, size); err == nil {
			t.Fatal("inexact byte count admitted")
		}
		_ = f.Close()
	}
	f, err := os.Open(path) //nolint:gosec // path is the same owned synthetic fixture, never a user input
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := snapshotDigest(ctx, f, nil, 7); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled digest: %v", err)
	}
	read := snapshotRead
	t.Cleanup(func() { snapshotRead = read })
	snapshotRead = func(*os.File, []byte) (int, error) { return 0, nil }
	if _, err := snapshotDigest(context.Background(), f, nil, 7); err == nil {
		t.Fatal("stalled read accepted")
	}
}

func TestSnapshotDigestRecognizesWrappedTerminalEOF(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bytes")
	if err := os.WriteFile(path, []byte("literal"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path) //nolint:gosec // literal fixture owned by this test
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	read := snapshotRead
	t.Cleanup(func() { snapshotRead = read })
	snapshotRead = func(f *os.File, b []byte) (int, error) {
		n, err := read(f, b)
		if errors.Is(err, io.EOF) {
			err = errors.Join(errors.New("terminal read"), err)
		}
		return n, err
	}
	if _, err := snapshotDigest(context.Background(), file, nil, 7); err != nil {
		t.Fatalf("complete exact-byte copy rejected wrapped EOF: %v", err)
	}
}

func TestCaptureIndexPostCopyFaults(t *testing.T) {
	for _, fault := range []string{"companion", "second-read", "content-changed"} {
		t.Run(fault, func(t *testing.T) {
			path, err := filepath.EvalSymlinks(indexFixture(t, "index.db"))
			if err != nil {
				t.Fatal(err)
			}
			closeFile, read := snapshotClose, snapshotRead
			t.Cleanup(func() { snapshotClose, snapshotRead = closeFile, read })
			if fault == "companion" {
				snapshotClose = func(f *os.File) error {
					err := closeFile(f)
					if f.Name() != path {
						err = errors.Join(err, os.WriteFile(path+"-wal", []byte("active"), 0o600))
					}
					return err
				}
			} else {
				reads := 0
				snapshotRead = func(f *os.File, b []byte) (int, error) {
					reads++
					if reads == 3 {
						if fault == "second-read" {
							return 0, errors.New("second source read fault")
						}
						n, err := read(f, b)
						b[0] ^= 1
						return n, err
					}
					return read(f, b)
				}
			}
			if capture, err := CaptureIndex(context.Background(), path); err == nil || capture != nil {
				t.Fatal("post-copy validation failure admitted")
			}
		})
	}
}
