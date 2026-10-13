package officecapture

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/onedriveusage"
)

func TestUsageFailureKeepsMostSpecificFixedCode(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		err  error
		code string
	}{
		{errors.New("plain"), "onedrive_usage_capture_failed"},
		{&ReadError{}, "onedrive_usage_capture_failed"},
		{fmt.Errorf("wrapped: %w", &ReadError{Code: "office_registry_source_busy"}), "onedrive_usage_source_busy"},
		{errors.Join(&ReadError{Code: "office_json_capture_failed"}, &ReadError{Code: "office_json_policy_restore_failed"}), "onedrive_usage_policy_restore_failed"},
		{&ReadError{Code: "office_json_input_unqualified"}, "onedrive_usage_input_unqualified"},
		{&ReadError{Code: "office_registry_cleanup_failed"}, "onedrive_usage_cleanup_failed"},
		{&ReadError{Code: "office_registry_index_too_large"}, "onedrive_usage_index_too_large"},
		{&ReadError{Code: "onedrive_usage_unavailable"}, "onedrive_usage_unavailable"},
		{fmt.Errorf("reader: %w", &onedriveusage.ReadError{Code: "onedrive_usage_index_unrecognized"}), "onedrive_usage_index_unrecognized"},
		{errors.Join(&onedriveusage.ReadError{Code: "onedrive_usage_index_unreadable"}, &ReadError{Code: "office_registry_capture_failed"}), "onedrive_usage_index_unreadable"},
		{&ReadError{Code: "office_surface_unknown"}, "onedrive_usage_capture_failed"},
	} {
		if got := usageCode(usageFailure(ctx, tc.err)); got != tc.code {
			t.Fatalf("%v mapped to %s, want %s", tc.err, got, tc.code)
		}
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := usageFailure(cancelled, errors.New("plain")); !errors.Is(err, context.Canceled) || usageCode(err) != "onedrive_usage_capture_failed" {
		t.Fatalf("cancellation lost from a usage failure: %v", err)
	}
}

// Review focus (Task 1): a result the consumer staged is published only when the wrapper returns
// nil. Every source, private-copy, reader, close, unlock, removal and policy failure must return
// an error with a fixed usage code, release its lock, close its descriptors, remove its private
// copy and leave a separate writer free.
func TestUsageLateFailureDisposal(t *testing.T) {
	for fault, want := range map[string]string{
		"stat": "onedrive_usage_capture_failed", "header-read": "onedrive_usage_input_unqualified",
		"lock": "onedrive_usage_source_busy", "unlock": "onedrive_usage_capture_failed",
		"root": "onedrive_usage_capture_failed", "canonical": "onedrive_usage_capture_failed",
		"create": "onedrive_usage_capture_failed", "read": "onedrive_usage_capture_failed",
		"write": "onedrive_usage_capture_failed", "output-close": "onedrive_usage_capture_failed",
		"private-open": "onedrive_usage_capture_failed", "private-digest": "onedrive_usage_capture_failed",
		"consume": "onedrive_usage_capture_failed", "reader-error": "onedrive_usage_index_unrecognized",
		"reader-too-large": "onedrive_usage_index_too_large", "reader-cancel": "onedrive_usage_capture_failed",
		"private-after-reader": "onedrive_usage_capture_failed", "source-after-reader": "onedrive_usage_capture_failed",
		"source-close": "onedrive_usage_capture_failed", "remove": "onedrive_usage_cleanup_failed",
		"absent": "onedrive_usage_cleanup_failed", "policy": "onedrive_usage_policy_restore_failed",
		"mutate-after-locked": "onedrive_usage_capture_failed", "cancel-copy": "onedrive_usage_capture_failed",
		"cancel-after-callback": "onedrive_usage_capture_failed", "cancel-cleanup": "onedrive_usage_capture_failed",
		"fields": "onedrive_usage_capture_failed", "closed": "onedrive_usage_capture_failed",
	} {
		t.Run(fault, func(t *testing.T) {
			path := usageSource(t)
			stat, readAt, lock, root, create, write := registryStat, registryReadAt, registryLock, jsonRoot, jsonCreate, jsonWrite
			private, closeFile, remove, lstat := registryPrivate, jsonClose, jsonRemove, jsonLstat
			read, guard, stage := usageReadIndex, captureGuard, registryStage
			restore := func() {
				registryStat, registryReadAt, registryLock, jsonRoot, jsonCreate, jsonWrite = stat, readAt, lock, root, create, write
				registryPrivate, jsonClose, jsonRemove, jsonLstat = private, closeFile, remove, lstat
				usageReadIndex, captureGuard, registryStage = read, guard, stage
			}
			t.Cleanup(restore)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			writeRefused := false
			var privateRoot string
			jsonRoot = func() (string, error) {
				value, err := root()
				privateRoot = value
				return value, err
			}
			switch fault {
			case "stat":
				registryStat = func(*os.File) (os.FileInfo, error) { return nil, os.ErrPermission }
			case "header-read":
				registryReadAt = func(*os.File, []byte, int64) (int, error) { return 0, os.ErrPermission }
			case "lock":
				registryLock = func(*os.File) (func() error, error) { return nil, errors.New("busy") }
			case "unlock":
				registryLock = func(f *os.File) (func() error, error) {
					unlock, err := lock(f)
					return func() error { return errors.Join(unlock(), errors.New("unlock fault")) }, err
				}
			case "root":
				jsonRoot = func() (string, error) { return "", errors.New("root fault") }
			case "canonical":
				jsonRoot = func() (string, error) { return filepath.Join(t.TempDir(), "absent"), nil }
			case "create":
				jsonCreate = func(string) (*os.File, error) { return nil, errors.New("create fault") }
			case "read", "cancel-copy":
				registryReadAt = func(f *os.File, b []byte, offset int64) (int, error) {
					if f.Name() != filepath.Base(path) || len(b) == sqliteHeaderBytes {
						return readAt(f, b, offset)
					}
					if fault == "cancel-copy" {
						cancel()
						return readAt(f, b, offset)
					}
					return 0, os.ErrPermission
				}
			case "write":
				jsonWrite = func(*os.File, []byte) (int, error) { return 0, os.ErrPermission }
			case "output-close":
				jsonClose = func(f *os.File) error {
					if filepath.Base(f.Name()) == "usage.db" {
						return errors.Join(closeFile(f), os.ErrPermission)
					}
					return closeFile(f)
				}
			case "private-open":
				registryPrivate = func(string) (*os.File, error) { return nil, os.ErrPermission }
			case "private-digest":
				registryPrivate = func(string) (*os.File, error) {
					other := filepath.Join(t.TempDir(), "other")
					if err := os.WriteFile(other, []byte("different"), 0o600); err != nil {
						return nil, err
					}
					return os.Open(other) //nolint:gosec // test-owned synthetic file
				}
			case "reader-error":
				usageReadIndex = func(context.Context, string) (onedriveusage.Result, error) {
					return onedriveusage.Result{}, &onedriveusage.ReadError{Code: "onedrive_usage_index_unrecognized"}
				}
			case "reader-too-large":
				usageReadIndex = func(context.Context, string) (onedriveusage.Result, error) {
					return onedriveusage.Result{}, &onedriveusage.ReadError{Code: "onedrive_usage_index_too_large"}
				}
			case "reader-cancel":
				usageReadIndex = func(readCtx context.Context, name string) (onedriveusage.Result, error) {
					cancel()
					return read(readCtx, name)
				}
			case "private-after-reader":
				usageReadIndex = func(readCtx context.Context, name string) (onedriveusage.Result, error) {
					registryPrivate = func(string) (*os.File, error) { return nil, os.ErrPermission }
					return read(readCtx, name)
				}
			case "source-after-reader", "mutate-after-locked":
				at := map[string]string{"source-after-reader": "before-reader", "mutate-after-locked": "locked"}[fault]
				registryStage = func(name string) {
					if name != at {
						return
					}
					if err := appendByte(path); err != nil && heldSourceDeniesWrite(err) {
						writeRefused = true
					} else if err != nil {
						t.Error(err)
					}
				}
			case "source-close":
				closes := 0
				jsonClose = func(f *os.File) error {
					if f.Name() == filepath.Base(path) {
						if closes++; closes > leafCloseBeforeBound {
							return errors.Join(closeFile(f), os.ErrPermission)
						}
					}
					return closeFile(f)
				}
			case "remove":
				jsonRemove = func(string) error { return os.ErrPermission }
			case "absent":
				jsonLstat = func(name string) (os.FileInfo, error) {
					info, err := lstat(name)
					if os.IsNotExist(err) {
						return lstat(filepath.Dir(name))
					}
					return info, err
				}
			case "policy":
				captureGuard = func(guardCtx context.Context, operation func() error) error {
					return errors.Join(guard(guardCtx, operation), &ReadError{Code: "office_json_policy_restore_failed"})
				}
			case "cancel-after-callback", "cancel-cleanup":
				at := map[string]string{"cancel-after-callback": "after-reader", "cancel-cleanup": "after-callback"}[fault]
				registryStage = func(name string) {
					if name == at {
						cancel()
					}
				}
			}
			locks, unlocks := 0, 0
			faulted := registryLock
			registryLock = func(f *os.File) (func() error, error) {
				unlock, err := faulted(f)
				if err != nil {
					return unlock, err
				}
				locks++
				return func() error { unlocks++; return unlock() }, nil
			}
			descriptors := openDescriptors(t)
			var staged *onedriveusage.Result
			err := WithOneDriveUsageCapture(ctx, UsageInput{Path: path}, func(c *UsageCapture) error {
				switch fault {
				case "consume":
					return errors.New("consumer fault")
				case "fields":
					c.ContentDigest = "changed"
				case "closed":
					if err := c.Close(); err != nil {
						return err
					}
				}
				result, err := c.ReadUsage(ctx)
				if err != nil {
					if len(result.Recent)+len(result.Documents)+len(result.Collaborators) != 0 {
						t.Errorf("%s: a failed read returned claims", fault)
					}
					return err
				}
				staged = &result
				return nil
			})
			restore()
			if writeRefused {
				// The platform's held handle denied the writer, so the source never changed.
				if err != nil {
					t.Fatalf("a refused write failed the capture: %v", err)
				}
			} else {
				// The caller publishes only on nil: any error disposes of whatever was staged.
				if err == nil {
					t.Fatalf("%s fault admitted a capture (staged=%t)", fault, staged != nil)
				}
				if usageCode(err) != want {
					t.Fatalf("%s: code %q, want %s (%v)", fault, usageCode(err), want, err)
				}
				if strings.HasPrefix(fault, "cancel") || fault == "reader-cancel" {
					if !errors.Is(err, context.Canceled) {
						t.Fatalf("%s: cancellation lost: %v", fault, err)
					}
				}
			}
			if locks != unlocks {
				t.Fatalf("%s: %d SQLite locks taken, %d released", fault, locks, unlocks)
			}
			if after := openDescriptors(t); after != descriptors {
				t.Fatalf("%s: open descriptors went from %d to %d", fault, descriptors, after)
			}
			if privateRoot != "" && fault != "remove" && fault != "canonical" {
				if _, err := os.Lstat(privateRoot); !os.IsNotExist(err) {
					t.Fatalf("%s: the private copy survived: %v", fault, err)
				}
			}
			if privateRoot != "" && fault == "remove" {
				_ = os.RemoveAll(privateRoot)
			}
			if output := officeWriterProbe(t, path); !strings.Contains(output, "WRITE_OK") {
				t.Fatalf("%s: a writer is still held out after the capture returned: %s", fault, output)
			}
			// The same source is capturable again: no failure path leaves the lifetime held.
			if fault != "source-after-reader" && fault != "mutate-after-locked" {
				if err := WithOneDriveUsageCapture(context.Background(), UsageInput{Path: path}, func(c *UsageCapture) error {
					_, err := c.ReadUsage(context.Background())
					return err
				}); err != nil {
					t.Fatalf("%s: capture after the failure: %v", fault, err)
				}
			}
		})
	}
}

func TestUsageEntryPointRefusals(t *testing.T) {
	path := usageSource(t)
	if err := WithOneDriveUsageCapture(context.Background(), UsageInput{Path: path}, nil); usageCode(err) != "onedrive_usage_input_unqualified" {
		t.Fatal("missing consumer admitted")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	if err := WithOneDriveUsageCapture(cancelled, UsageInput{Path: path}, func(*UsageCapture) error { called = true; return nil }); !errors.Is(err, context.Canceled) || called {
		t.Fatalf("cancelled capture: %v", err)
	}
	if _, err := ObserveOneDriveUsage(cancelled, path); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled observation: %v", err)
	}
	closeFile := jsonClose
	t.Cleanup(func() { jsonClose = closeFile })
	closes := 0
	jsonClose = func(f *os.File) error {
		if f.Name() == filepath.Base(path) {
			if closes++; closes > leafCloseBeforeBound {
				return errors.Join(closeFile(f), os.ErrPermission)
			}
		}
		return closeFile(f)
	}
	if signature, err := ObserveOneDriveUsage(context.Background(), path); signature != "" || usageCode(err) != "onedrive_usage_capture_failed" {
		t.Fatalf("unclosed observation published evidence: %q %v", signature, err)
	}
}
