package officecapture

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ourostack/m365crawl/internal/documenttext"
)

func TestTextAcquisitionFaultsNeverPublish(t *testing.T) {
	for _, fault := range []string{"stat", "root", "canonical", "create", "read", "write", "output-close", "private-open", "private-stat", "private-close", "lstat", "remove", "source-close", "late-source", "late-private"} {
		t.Run(fault, func(t *testing.T) {
			path := textFixture(t)
			stat, root, create, read, write := registryStat, jsonRoot, jsonCreate, registryReadAt, jsonWrite
			private, closeFile, lstat, remove := textOpenPrivate, jsonClose, jsonLstat, jsonRemove
			t.Cleanup(func() {
				registryStat, jsonRoot, jsonCreate, registryReadAt, jsonWrite = stat, root, create, read, write
				textOpenPrivate, jsonClose, jsonLstat, jsonRemove = private, closeFile, lstat, remove
			})
			switch fault {
			case "stat":
				registryStat = func(*os.File) (os.FileInfo, error) { return nil, os.ErrPermission }
			case "root":
				jsonRoot = func() (string, error) { return "", os.ErrPermission }
			case "canonical":
				jsonRoot = func() (string, error) { return path + ".missing", nil }
			case "create":
				jsonCreate = func(string) (*os.File, error) { return nil, os.ErrPermission }
			case "read":
				registryReadAt = func(*os.File, []byte, int64) (int, error) { return 0, os.ErrPermission }
			case "write":
				jsonWrite = func(*os.File, []byte) (int, error) { return 0, os.ErrPermission }
			case "output-close":
				jsonClose = func(f *os.File) error { return errors.Join(closeFile(f), os.ErrPermission) }
			case "private-open":
				textOpenPrivate = func(string, bool) (*guardedInput, error) { return nil, os.ErrPermission }
			case "private-stat":
				registryStat = func(f *os.File) (os.FileInfo, error) {
					if filepath.Base(f.Name()) == "resident-ooxml" {
						return nil, os.ErrPermission
					}
					return stat(f)
				}
			case "private-close":
				jsonClose = func(f *os.File) error {
					if filepath.Base(f.Name()) == "resident-ooxml" {
						return errors.Join(closeFile(f), os.ErrPermission)
					}
					return closeFile(f)
				}
			case "lstat":
				jsonLstat = func(string) (os.FileInfo, error) { return nil, os.ErrPermission }
			case "remove":
				jsonRemove = func(string) error { return os.ErrPermission }
			case "source-close":
				jsonClose = func(f *os.File) error {
					if f.Name() == filepath.Base(path) {
						return errors.Join(closeFile(f), os.ErrPermission)
					}
					return closeFile(f)
				}
			}
			err := WithTextCapture(context.Background(), TextInput{path, "docx"}, func(c *TextCapture) error {
				if _, err := c.ReadText(context.Background()); err != nil {
					return err
				}
				switch fault {
				case "late-source":
					err := os.WriteFile(path, []byte("changed"), 0o600)
					if heldSourceDeniesWrite(err) {
						return errors.New("native source mutation denied")
					}
					if err != nil {
						t.Fatal(err)
					}
				case "late-private":
					if err := os.WriteFile(c.privatePath, []byte("changed"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				return nil
			})
			if err == nil {
				t.Fatal("fault admitted staged result")
			}
		})
	}
}

func TestTextEntryAndObservationFaults(t *testing.T) {
	path := textFixture(t)
	if err := WithTextCapture(context.Background(), TextInput{path, "docx"}, nil); err == nil {
		t.Fatal("nil consumer")
	}
	for _, path := range []string{"relative.docx", path + ".missing"} {
		if _, err := ObserveText(context.Background(), path); err == nil {
			t.Fatal("invalid metadata observation")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ObserveText(ctx, path); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled metadata observation")
	}
	closeFile := jsonClose
	t.Cleanup(func() { jsonClose = closeFile })
	jsonClose = func(f *os.File) error { return errors.Join(closeFile(f), os.ErrPermission) }
	if signature, err := ObserveText(context.Background(), path); err == nil || signature != "" {
		t.Fatal("failed metadata close published")
	}
	for _, tc := range []struct {
		err  error
		code string
	}{
		{&ReadError{Code: "office_json_policy_restore_failed"}, "local_text_policy_restore_failed"},
		{&ReadError{Code: "office_json_input_unqualified"}, "local_text_input_unqualified"},
		{&ReadError{Code: "office_json_capture_failed"}, "local_text_capture_failed"},
		{errors.Join(&documenttext.ReadError{Code: "malformed"}, &ReadError{Code: "office_json_policy_restore_failed"}), "local_text_policy_restore_failed"},
		{errors.Join(&ReadError{Code: "local_text_capture_failed"}, &ReadError{Code: "local_text_cleanup_failed"}), "local_text_cleanup_failed"},
		{&documenttext.ReadError{Code: "malformed"}, "local_text_malformed"},
	} {
		if registryCode(textFailure(context.Background(), tc.err)) != tc.code {
			t.Fatal("unsafe native diagnostic mapping")
		}
	}
}
