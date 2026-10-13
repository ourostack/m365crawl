package officecapture

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/ourostack/m365crawl/internal/officeregistry"
)

func TestRegistryFailureKeepsMostSpecificFixedCode(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		err  error
		code string
	}{
		{errors.New("plain"), "office_registry_capture_failed"},
		{&ReadError{}, "office_registry_capture_failed"},
		{fmt.Errorf("wrapped: %w", &ReadError{Code: "office_registry_source_busy"}), "office_registry_source_busy"},
		{errors.Join(&ReadError{Code: "office_json_capture_failed"}, &ReadError{Code: "office_json_policy_restore_failed"}), "office_registry_policy_restore_failed"},
		{&ReadError{Code: "office_json_input_unqualified"}, "office_registry_input_unqualified"},
		{fmt.Errorf("reader: %w", &officeregistry.ReadError{Code: "office_registry_index_unrecognized"}), "office_registry_index_unrecognized"},
	} {
		if got := registryCode(registryFailure(ctx, tc.err)); got != tc.code {
			t.Fatalf("%v mapped to %s, want %s", tc.err, got, tc.code)
		}
	}
}

func TestRegistryEntryPointRefusals(t *testing.T) {
	path := registrySource(t)
	if err := WithRegistryCapture(context.Background(), RegistryInput{Path: path}, nil); registryCode(err) != "office_registry_input_unqualified" {
		t.Fatal("missing consumer admitted")
	}
	if _, err := ObserveRegistry(context.Background(), "relative.reg"); registryCode(err) != "office_registry_input_unqualified" {
		t.Fatal("relative observation admitted")
	}
	if _, err := ObserveRegistry(context.Background(), path+".missing"); registryCode(err) != "office_registry_input_unqualified" {
		t.Fatal("missing observation admitted")
	}
	if err := WithRegistryCapture(context.Background(), RegistryInput{Path: path + ".missing"}, func(*RegistryCapture) error { return nil }); registryCode(err) != "office_registry_input_unqualified" {
		t.Fatal("missing source admitted")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := WithRegistryCapture(cancelled, RegistryInput{Path: path}, func(*RegistryCapture) error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled capture: %v", err)
	}
	if err := captureRegistry(cancelled, RegistryInput{Path: path}, func(*RegistryCapture) error { return nil }); registryCode(err) != "office_registry_capture_failed" {
		t.Fatal("cancelled capture opened its source")
	}
	closeFile := jsonClose
	t.Cleanup(func() { jsonClose = closeFile })
	jsonClose = func(f *os.File) error {
		if f.Name() == filepath.Base(path) {
			return errors.Join(closeFile(f), os.ErrPermission)
		}
		return closeFile(f)
	}
	if signature, err := ObserveRegistry(context.Background(), path); signature != "" || registryCode(err) != "office_registry_capture_failed" {
		t.Fatal("unclosed observation published evidence")
	}
	if err := WithRegistryCapture(context.Background(), RegistryInput{Path: path}, func(*RegistryCapture) error { return nil }); registryCode(err) != "office_registry_capture_failed" {
		t.Fatal("source close failure admitted")
	}
}

func TestRegistryDiscoveryFaults(t *testing.T) {
	if !discoveryMac {
		t.Skip("default registry discovery is Mac only")
	}
	home, read := discoveryHome, discoveryRead
	t.Cleanup(func() { discoveryHome, discoveryRead = home, read })
	discoveryHome = func() (string, error) { return "", errors.New("home fault") }
	if _, err := DiscoverRegistry(context.Background()); registryCode(err) != "office_registry_input_unqualified" {
		t.Fatal("home fault admitted")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	discoveryHome = func() (string, error) { return root, nil }
	container := filepath.Join(root, "Library", "Group Containers", "UBF8T346G9.Office")
	if err := os.MkdirAll(container, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(container, "MicrosoftRegistrationDB"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := DiscoverRegistry(context.Background()); registryCode(err) != "office_registry_input_unqualified" {
		t.Fatal("non-directory default admitted")
	}
	if err := os.Remove(filepath.Join(container, "MicrosoftRegistrationDB")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(container, "MicrosoftRegistrationDB"), 0o700); err != nil {
		t.Fatal(err)
	}
	discoveryRead = func(*os.File, int) ([]string, error) { return nil, os.ErrPermission }
	if _, err := DiscoverRegistry(context.Background()); registryCode(err) != "office_registry_input_unqualified" {
		t.Fatal("directory read fault admitted")
	}
}

func TestRegistryAcquisitionFaults(t *testing.T) {
	for _, fault := range []string{"stat", "header-read", "lock", "unlock", "root", "canonical", "create", "read",
		"short-read", "overlong-read", "write", "short-write", "no-progress", "output-close", "private-open",
		"private-digest", "private-close", "consume", "remove", "absent", "mutate-after-lock", "mutate-after-locked"} {
		t.Run(fault, func(t *testing.T) {
			path := registrySource(t)
			stat, readAt, lock, root, create, write := registryStat, registryReadAt, registryLock, jsonRoot, jsonCreate, jsonWrite
			private, closeFile, remove, lstat := registryPrivate, jsonClose, jsonRemove, jsonLstat
			t.Cleanup(func() {
				registryStat, registryReadAt, registryLock, jsonRoot, jsonCreate, jsonWrite = stat, readAt, lock, root, create, write
				registryPrivate, jsonClose, jsonRemove, jsonLstat = private, closeFile, remove, lstat
			})
			reads := 0
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
			case "read", "short-read", "overlong-read", "no-progress":
				registryReadAt = func(f *os.File, b []byte, offset int64) (int, error) {
					reads++
					if f.Name() != filepath.Base(path) || len(b) == sqliteHeaderBytes {
						return readAt(f, b, offset)
					}
					switch fault {
					case "read":
						return 0, os.ErrPermission
					case "short-read":
						n, _ := readAt(f, b[:1], offset)
						return n, io.EOF
					case "overlong-read":
						return len(b) + 1, nil
					}
					return 0, nil
				}
			case "mutate-after-lock":
				registryLock = func(f *os.File) (func() error, error) {
					unlock, err := lock(f)
					if appendErr := appendByte(path); appendErr != nil {
						t.Error(appendErr)
					}
					return unlock, err
				}
			case "mutate-after-locked":
				stage := registryStage
				t.Cleanup(func() { registryStage = stage })
				registryStage = func(name string) {
					if name == "locked" {
						if err := appendByte(path); err != nil {
							t.Error(err)
						}
					}
				}
			case "write":
				jsonWrite = func(*os.File, []byte) (int, error) { return 0, os.ErrPermission }
			case "short-write":
				jsonWrite = func(f *os.File, b []byte) (int, error) { return write(f, b[:len(b)-1]) }
			case "output-close":
				jsonClose = func(f *os.File) error {
					if f.Name() != filepath.Base(path) && filepath.Base(f.Name()) == "registry.db" {
						return errors.Join(closeFile(f), os.ErrPermission)
					}
					return closeFile(f)
				}
			case "private-open":
				registryPrivate = func(string) (*os.File, error) { return nil, os.ErrPermission }
			case "private-digest":
				registryPrivate = func(name string) (*os.File, error) {
					if err := os.WriteFile(name+".other", []byte("different"), 0o600); err != nil {
						return nil, err
					}
					return os.Open(name + ".other") //nolint:gosec // test-owned private sibling
				}
			case "private-close":
				calls := 0
				registryPrivate = func(name string) (*os.File, error) {
					calls++
					file, err := private(name)
					if calls == 1 && err == nil {
						jsonClose = func(f *os.File) error {
							if f == file {
								return errors.Join(closeFile(f), os.ErrPermission)
							}
							return closeFile(f)
						}
					}
					return file, err
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
			}
			called := false
			err := WithRegistryCapture(context.Background(), RegistryInput{Path: path}, func(c *RegistryCapture) error {
				called = true
				if fault == "consume" {
					return errors.New("consumer fault")
				}
				return nil
			})
			if err == nil {
				t.Fatalf("%s fault admitted a capture (called=%t)", fault, called)
			}
			jsonRemove, jsonLstat = remove, lstat
		})
	}
}

func TestRegistryReaderFaults(t *testing.T) {
	for _, fault := range []string{"closed", "cancelled", "fields", "source-changed", "private-before", "reader-cancel",
		"private-after", "reader-error"} {
		t.Run(fault, func(t *testing.T) {
			path := registrySource(t)
			read, private := registryReadIndex, registryPrivate
			t.Cleanup(func() { registryReadIndex, registryPrivate = read, private })
			err := WithRegistryCapture(context.Background(), RegistryInput{Path: path}, func(c *RegistryCapture) error {
				ctx := context.Background()
				switch fault {
				case "closed":
					if err := c.Close(); err != nil {
						return err
					}
				case "cancelled":
					cancelled, cancel := context.WithCancel(ctx)
					cancel()
					ctx = cancelled
				case "fields":
					c.ContentDigest = "changed"
				case "source-changed":
					c.baseline = "changed"
				case "private-before":
					registryPrivate = func(string) (*os.File, error) { return nil, os.ErrPermission }
				case "reader-cancel":
					cancelled, cancel := context.WithCancel(ctx)
					ctx = cancelled
					registryReadIndex = func(ctx context.Context, path string) (officeregistry.Result, error) {
						cancel()
						return read(ctx, path)
					}
				case "private-after":
					registryReadIndex = func(ctx context.Context, path string) (officeregistry.Result, error) {
						registryPrivate = func(string) (*os.File, error) { return nil, os.ErrPermission }
						return read(ctx, path)
					}
				case "reader-error":
					registryReadIndex = func(context.Context, string) (officeregistry.Result, error) {
						return officeregistry.Result{}, &officeregistry.ReadError{Code: "office_registry_index_unrecognized"}
					}
				}
				result, err := c.ReadRegistry(ctx)
				if err == nil || len(result.Documents) != 0 {
					t.Fatalf("%s reader fault returned claims", fault)
				}
				if fault == "reader-error" && registryCode(err) != "office_registry_index_unrecognized" {
					t.Fatalf("reader code lost: %v", err)
				}
				return err
			})
			if err == nil {
				t.Fatal("reader fault admitted a capture")
			}
		})
	}
}

func TestRegistryCopyBounds(t *testing.T) {
	path := registrySource(t)
	file, err := os.Open(path) //nolint:gosec // synthetic fixture
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := copyRegistry(cancelled, io.Discard, file, 10); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled copy continued")
	}
	if n, err := copyRegistry(context.Background(), io.Discard, file, 1<<30); err == nil || n >= 1<<30 {
		t.Fatal("source shorter than its bound admitted")
	}
}

func appendByte(path string) error {
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0) //nolint:gosec // synthetic fixture
	if err != nil {
		return err
	}
	_, err = file.Write([]byte{0})
	return errors.Join(err, file.Close())
}
