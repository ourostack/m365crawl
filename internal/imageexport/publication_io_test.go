package imageexport

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ourostack/m365crawl/internal/errs"
)

func imageRestoreIO(t *testing.T) {
	t.Helper()
	oldAbs, oldLstat, oldRoot, oldOpen := imageAbs, imageLstat, imageOpenRoot, imageOpen
	oldFile, oldRootStat, oldTemporary := imageOpenFile, imageRootLstat, imageTemporary
	t.Cleanup(func() {
		imageAbs, imageLstat, imageOpenRoot, imageOpen = oldAbs, oldLstat, oldRoot, oldOpen
		imageOpenFile, imageRootLstat, imageTemporary = oldFile, oldRootStat, oldTemporary
	})
}

func TestImageReserveFilesystemFailureEffects(t *testing.T) {
	for _, tc := range []struct{ phase, code string }{
		{"absolute", "image_output_invalid"},
		{"root", "image_output_open_failed"},
		{"parent-open", "image_output_open_failed"},
		{"parent-stat", "image_output_changed"},
		{"parent-privacy", "image_output_unprotected"},
		{"temporary-open", "image_output_reserve_failed"},
		{"temporary-stat", "image_output_changed"},
		{"temporary-privacy", "image_output_unprotected"},
		{"recheck", "image_output_changed"},
	} {
		t.Run(tc.phase, func(t *testing.T) {
			imageRestoreIO(t)
			dir := imagePrivateParent(t)
			path := filepath.Join(dir, "selected.png")
			sentinel := errors.New("synthetic filesystem failure")
			var temporary string
			switch tc.phase {
			case "absolute":
				imageAbs = func(string) (string, error) { return "", sentinel }
			case "root":
				imageOpenRoot = func(string) (*os.Root, error) { return nil, sentinel }
			case "parent-open":
				imageOpen = func(*os.Root, string) (*os.File, error) { return nil, sentinel }
			case "parent-stat":
				imageOpen = func(root *os.Root, name string) (*os.File, error) {
					file, err := root.Open(name)
					if err == nil {
						err = file.Close()
					}
					return file, err
				}
			case "parent-privacy":
				if runtime.GOOS == "windows" {
					t.Skip("Unix permission-bit race; Windows private-parent ACL policy has native fileprivacy tests")
				}
				imageOpen = func(root *os.Root, name string) (*os.File, error) {
					file, err := root.Open(name)
					if err == nil {
						err = file.Chmod(0o777)
					}
					return file, err
				}
			case "temporary-open":
				imageOpenFile = func(*os.Root, string, int, os.FileMode) (*os.File, error) { return nil, sentinel }
			case "temporary-stat":
				imageOpenFile = func(root *os.Root, name string, flags int, mode os.FileMode) (*os.File, error) {
					file, err := root.OpenFile(name, flags, mode)
					if err == nil {
						temporary = filepath.Join(dir, name)
						err = file.Close()
					}
					return file, err
				}
			case "temporary-privacy":
				original := imageTemporary
				imageTemporary = func(*os.File) error { return original(nil) }
			case "recheck":
				checks := 0
				imageLstat = func(path string) (os.FileInfo, error) {
					checks++
					if checks == 2 {
						return nil, sentinel
					}
					return os.Lstat(path)
				}
			}
			reservation, effect, err := Reserve(context.Background(), path)
			var coded *errs.Coded
			if reservation != nil || !errors.As(err, &coded) || coded.Code != tc.code ||
				effect.FilePublished == nil || *effect.FilePublished {
				t.Fatal("filesystem admission failure lost refusal/effects", effect, err)
			}
			if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("failed reservation exposed a destination", err)
			}
			if tc.phase == "temporary-stat" {
				if effect.RetainedTemp == nil || effect.RetainedTemp.LastKnownPath != temporary ||
					effect.RetainedTemp.State != "empty" || effect.RetainedTemp.LocatorVerified == nil ||
					*effect.RetainedTemp.LocatorVerified {
					t.Fatal("unverifiable created temporary lost its retained locator/effect", effect, err)
				}
				if data, err := os.ReadFile(temporary); err != nil || len(data) != 0 {
					t.Fatal("unverifiable temporary was removed or written", err)
				}
			} else if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 || effect.RetainedTemp != nil {
				t.Fatal("failed reservation leaked known-owned scratch", entries, effect, err)
			}
		})
	}
}

func TestImagePublishVerificationFailuresRetainTruthfulEffects(t *testing.T) {
	for _, phase := range []string{"privacy", "prepublish-recheck", "locator", "destination-stat", "destination-replaced"} {
		t.Run(phase, func(t *testing.T) {
			imageRestoreIO(t)
			dir := imagePrivateParent(t)
			path := filepath.Join(dir, "selected.png")
			reservation, _, err := Reserve(context.Background(), path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if _, err := reservation.Abort(); err != nil {
					t.Error(err)
				}
			})
			oldLink := imageLink
			t.Cleanup(func() { imageLink = oldLink })
			switch phase {
			case "privacy":
				original := imageTemporary
				imageTemporary = func(*os.File) error { return original(nil) }
			case "locator":
				imageLstat = func(name string) (os.FileInfo, error) {
					if name == path {
						return nil, os.ErrNotExist
					}
					return os.Lstat(name)
				}
			case "prepublish-recheck":
				imageLstat = func(name string) (os.FileInfo, error) {
					if name == dir && reservation.file == nil {
						return nil, os.ErrNotExist
					}
					return os.Lstat(name)
				}
			case "destination-stat":
				imageRootLstat = func(root *os.Root, name string) (os.FileInfo, error) {
					if name == "selected.png" {
						return nil, os.ErrPermission
					}
					return root.Lstat(name)
				}
			case "destination-replaced":
				imageLink = func(root *os.Root, source, destination string) error {
					if err := root.Link(source, destination); err != nil {
						return err
					}
					if err := root.Rename(destination, destination+"-original"); err != nil {
						return err
					}
					return os.WriteFile(path, []byte("competing output"), 0o600)
				}
			}
			data := []byte("complete validated image bytes")
			effect, err := reservation.Publish(context.Background(), data)
			if err == nil || effect.OutputVerified == nil || *effect.OutputVerified {
				t.Fatal("failed verification claimed successful verified output", effect, err)
			}
			switch phase {
			case "privacy", "prepublish-recheck":
				if effect.FilePublished == nil || *effect.FilePublished {
					t.Fatal("unsafe temporary reached publication", effect)
				}
				if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("unsafe temporary exposed destination", err)
				}
			case "locator":
				if effect.FilePublished == nil || !*effect.FilePublished {
					t.Fatal("lost absolute locator denied actual publication", effect)
				}
				if actual, err := os.ReadFile(path); err != nil || !bytes.Equal(actual, data) {
					t.Fatal("unverified locator lost complete destination", err)
				}
			default:
				if effect.FilePublished != nil {
					t.Fatal("unreadable or replaced destination guessed publication state", effect)
				}
				if actual, err := os.ReadFile(path); err != nil ||
					phase == "destination-stat" && !bytes.Equal(actual, data) ||
					phase == "destination-replaced" && string(actual) != "competing output" {
					t.Fatal("failed verification removed or changed a destination", err)
				}
			}
		})
	}
}

func TestImageAbortReportsFileCloseFailure(t *testing.T) {
	dir := imagePrivateParent(t)
	reservation, _, err := Reserve(context.Background(), filepath.Join(dir, "selected.png"))
	if err != nil {
		t.Fatal(err)
	}
	old := imageClose
	t.Cleanup(func() { imageClose = old })
	sentinel := errors.New("synthetic close failure")
	imageClose = func(file *os.File) error { return errors.Join(file.Close(), sentinel) }
	effect, err := reservation.Abort()
	if !errors.Is(err, sentinel) || effect.RetainedTemp != nil {
		t.Fatal("abort hid close failure or failed known-owned cleanup", effect, err)
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Fatal("close failure prevented safe owned-temp cleanup", entries, err)
	}
}
