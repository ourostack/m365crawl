package imageexport

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestImagePublishPhaseFaults(t *testing.T) {
	sentinel := errors.New("synthetic publication phase failure")
	for _, phase := range []string{"write", "short-write", "sync", "close", "link", "link-after-publication", "cleanup", "root-close", "cancel-after-link"} {
		t.Run(phase, func(t *testing.T) {
			oldWrite, oldSync, oldClose := imageWrite, imageSync, imageClose
			oldLink, oldRemove, oldRootClose := imageLink, imageRemove, imageRootClose
			t.Cleanup(func() {
				imageWrite, imageSync, imageClose = oldWrite, oldSync, oldClose
				imageLink, imageRemove, imageRootClose = oldLink, oldRemove, oldRootClose
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			dir := imagePrivateParent(t)
			output := filepath.Join(dir, "selected.png")
			reservation, _, err := Reserve(ctx, output)
			if err != nil {
				t.Fatal(err)
			}
			published := false
			switch phase {
			case "write":
				imageWrite = func(*os.File, []byte) (int, error) { return 0, sentinel }
			case "short-write":
				imageWrite = func(f *os.File, data []byte) (int, error) { return f.Write(data[:len(data)/2]) }
			case "sync":
				imageSync = func(*os.File) error { return sentinel }
			case "close":
				imageClose = func(f *os.File) error { return errors.Join(f.Close(), sentinel) }
			case "link":
				imageLink = func(*os.Root, string, string) error { return sentinel }
			case "link-after-publication":
				published = true
				imageLink = func(root *os.Root, old, new string) error {
					return errors.Join(root.Link(old, new), sentinel)
				}
			case "cleanup":
				published = true
				imageRemove = func(*os.Root, string) error { return sentinel }
			case "root-close":
				published = true
				imageRootClose = func(root *os.Root) error { return errors.Join(root.Close(), sentinel) }
			case "cancel-after-link":
				published = true
				imageLink = func(root *os.Root, old, new string) error {
					err := root.Link(old, new)
					cancel()
					return err
				}
			}
			data := []byte("complete validated selected bytes")
			effect, err := reservation.Publish(ctx, data)
			if err == nil || effect.FilePublished == nil || *effect.FilePublished != published {
				t.Fatalf("phase %s lost actual publication: %+v %v", phase, effect, err)
			}
			actual, readErr := os.ReadFile(output)
			if published {
				if readErr != nil || !bytes.Equal(actual, data) {
					t.Fatal("post-publication failure lost complete destination", readErr)
				}
			} else if !errors.Is(readErr, os.ErrNotExist) {
				t.Fatal("pre-publication failure exposed incomplete destination", readErr)
			}
			if _, err := reservation.Abort(); err != nil {
				t.Log("secondary cleanup failure remains explicit:", err)
			}
			if published {
				actual, readErr = os.ReadFile(output)
				if readErr != nil || !bytes.Equal(actual, data) {
					t.Fatal("secondary abort removed completed destination", readErr)
				}
			}
		})
	}
}

func TestImageReserveHandleAndEntryIdentity(t *testing.T) {
	ctx := context.Background()
	dir := imagePrivateParent(t)
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	parent, err := root.Open(".")
	if err != nil {
		t.Fatal("actual platform cannot open the bound parent handle", err)
	}
	opened, err := parent.Stat()
	if err != nil {
		t.Fatal(err)
	}
	located, err := os.Stat(dir)
	if err != nil || !os.SameFile(opened, located) {
		t.Fatal("bound parent does not name admitted directory", err)
	}
	if err := parent.Close(); err != nil {
		t.Fatal(err)
	}
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(dir, "selected.png")
	reservation, effect, err := Reserve(ctx, output)
	if err != nil || reservation == nil || effect.FilePublished == nil || *effect.FilePublished {
		t.Fatalf("private reservation failed: %+v %v", effect, err)
	}
	if _, err := os.Lstat(output); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("reservation exposed a destination before acquisition", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatal("exclusive temporary reservation not observed", err)
	}
	info, err := entries[0].Info()
	if err != nil || !info.Mode().IsRegular() {
		t.Fatal("reserved temporary entry is not regular", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatal("Unix temporary bytes were not private at creation")
	}
	effect, err = reservation.Abort()
	if err != nil || effect.FilePublished == nil || *effect.FilePublished {
		t.Fatalf("owned reservation did not abort truthfully: %+v %v", effect, err)
	}
	entries, err = os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatal("owned temporary entry not cleaned", err)
	}
}

func TestImagePublishNoClobberRace(t *testing.T) {
	ctx := context.Background()
	dir := imagePrivateParent(t)
	output := filepath.Join(dir, "selected.png")
	reservation, _, err := Reserve(ctx, output)
	if err != nil {
		t.Fatal(err)
	}
	competing := []byte("another invocation owns this file")
	if err := os.WriteFile(output, competing, 0o600); err != nil {
		t.Fatal(err)
	}
	effect, err := reservation.Publish(ctx, []byte("complete validated bytes"))
	if err == nil || effect.FilePublished == nil || *effect.FilePublished {
		t.Fatalf("competing destination overwritten or effect lost: %+v %v", effect, err)
	}
	actual, err := os.ReadFile(output)
	if err != nil || !bytes.Equal(actual, competing) {
		t.Fatal("competing destination changed", err)
	}
	if _, err := reservation.Abort(); err != nil {
		t.Fatal(err)
	}
	if r, _, err := Reserve(ctx, output); err == nil || r != nil {
		t.Fatal("preexisting destination admitted")
	}
}

func TestImagePublishCompleteAndNoOverwrite(t *testing.T) {
	ctx := context.Background()
	dir := imagePrivateParent(t)
	output := filepath.Join(dir, "selected.png")
	reservation, _, err := Reserve(ctx, output)
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("complete validated selected image bytes")
	effect, err := reservation.Publish(ctx, data)
	if err != nil || effect.FilePublished == nil || !*effect.FilePublished ||
		effect.OutputVerified == nil || !*effect.OutputVerified {
		t.Fatalf("complete output/locator not verified: %+v %v", effect, err)
	}
	actual, err := os.ReadFile(output)
	if err != nil || !bytes.Equal(actual, data) {
		t.Fatal("published bytes incomplete", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != "selected.png" {
		t.Fatal("successful publication retained scratch", err)
	}
	if _, err := reservation.Abort(); err != nil {
		t.Fatal(err)
	}
	actual, err = os.ReadFile(output)
	if err != nil || !bytes.Equal(actual, data) {
		t.Fatal("abort deleted a completed destination", err)
	}
}

func TestImageParentLocatorAndTempSubstitution(t *testing.T) {
	ctx := context.Background()
	t.Run("parent", func(t *testing.T) {
		dir := imagePrivateParent(t)
		output := filepath.Join(dir, "selected.png")
		reservation, _, err := Reserve(ctx, output)
		if err != nil {
			t.Fatal(err)
		}

		moved := dir + "-moved"
		before, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(dir, moved); err != nil {
			if runtime.GOOS != "windows" {
				t.Fatal(err)
			}
			after, statErr := os.Stat(dir)
			if statErr != nil || !os.SameFile(before, after) {
				t.Fatal("prevented Windows rename did not retain parent identity", statErr)
			}
			effect, err := reservation.Publish(ctx, []byte("protected by the open handle"))
			if err != nil || effect.OutputVerified == nil || !*effect.OutputVerified {
				t.Fatal("prevented rename failed verified publication", err)
			}
			return
		}
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		effect, err := reservation.Publish(ctx, []byte("must not publish to substituted locator"))
		if err == nil || effect.OutputVerified != nil && *effect.OutputVerified {
			t.Fatalf("replaced parent locator claimed verified: %+v %v", effect, err)
		}
		if _, err := os.Lstat(output); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("substituted directory received output", err)
		}
		if _, err := reservation.Abort(); err != nil {
			t.Log("renamed parent cleanup explicitly reported:", err)
		}
	})
	t.Run("temporary", func(t *testing.T) {
		dir := imagePrivateParent(t)
		output := filepath.Join(dir, "selected.png")
		reservation, _, err := Reserve(ctx, output)
		if err != nil {
			t.Fatal(err)
		}
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) != 1 {
			t.Fatal("one reservation expected", err)
		}
		temp := filepath.Join(dir, entries[0].Name())
		moved := temp + "-owned"
		if err := os.Rename(temp, moved); err != nil {
			t.Fatal("temporary substitution could not be exercised", err)
		}
		substituted := []byte("not owned by this reservation")
		if err := os.WriteFile(temp, substituted, 0o600); err != nil {
			t.Fatal(err)
		}
		effect, err := reservation.Publish(ctx, []byte("complete validated bytes"))
		if err == nil || effect.FilePublished != nil && *effect.FilePublished {
			t.Fatalf("substituted temp published: %+v %v", effect, err)
		}
		_, _ = reservation.Abort()
		actual, err := os.ReadFile(temp)
		if err != nil || !bytes.Equal(actual, substituted) {
			t.Fatal("cleanup removed or changed someone else's entry", err)
		}
	})
}
