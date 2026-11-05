package imageexport

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/errs"
)

func TestImagePublishDisappearingDestinationIsUncertain(t *testing.T) {
	dir := imagePrivateParent(t)
	output := filepath.Join(dir, "selected.png")
	reservation, _, err := Reserve(context.Background(), output)
	if err != nil {
		t.Fatal(err)
	}
	oldLink := imageLink
	t.Cleanup(func() { imageLink = oldLink })
	imageLink = func(root *os.Root, old, destination string) error {
		if err := root.Link(old, destination); err != nil {
			return err
		}
		return root.Rename(destination, destination+"-moved")
	}
	data := []byte("complete validated image bytes")
	effect, err := reservation.Publish(context.Background(), data)
	if err == nil || effect.FilePublished != nil || effect.OutputVerified == nil || *effect.OutputVerified {
		t.Fatalf("successful link followed by a missing locator falsely denied publication: %+v %v", effect, err)
	}
	if _, err := reservation.Abort(); err != nil {
		t.Fatal(err)
	}
	actual, err := os.ReadFile(output + "-moved")
	if err != nil || !bytes.Equal(actual, data) {
		t.Fatal("uncertain publication lost the completed file", err)
	}
}

func TestImageReservationInvalidInputsAndClosedHandles(t *testing.T) {
	ctx := context.Background()
	dir := imagePrivateParent(t)
	for _, path := range []string{"", "bad\nname", string(filepath.Separator), filepath.Join(dir, "absent", "selected.png")} {
		reservation, effect, err := Reserve(ctx, path)
		if err == nil || reservation != nil || effect.FilePublished == nil || *effect.FilePublished {
			t.Fatalf("invalid output produced an authorized reservation: %q %+v %v", path, effect, err)
		}
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if reservation, _, err := Reserve(cancelled, filepath.Join(dir, "selected.png")); reservation != nil || !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled reservation accessed output", err)
	}
	var missing *Reservation
	if _, err := missing.Publish(ctx, nil); err == nil {
		t.Fatal("missing reservation authorized publication")
	}
	if _, err := missing.Abort(); err == nil {
		t.Fatal("missing reservation claimed successful cleanup")
	}
	if _, err := new(Reservation).Publish(ctx, nil); err == nil {
		t.Fatal("uninitialized reservation authorized publication")
	}
	if err := new(Reservation).verify(); err == nil {
		t.Fatal("uninitialized reservation verified a locator")
	}
	reservation, _, err := Reserve(ctx, filepath.Join(dir, "selected.png"))
	if err != nil {
		t.Fatal(err)
	}
	if err := reservation.parent.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := reservation.Publish(ctx, []byte("never expose these bytes")); err == nil {
		t.Fatal("closed parent authorized publication")
	}
	effect, err := reservation.Abort()
	var coded *errs.Coded
	if !errors.As(err, &coded) || coded.Code != "image_output_close_failed" || effect.RetainedTemp != nil {
		t.Fatal("closed-parent cleanup lost its error or owned-temp cleanup", effect, err)
	}
}

func TestImagePublishCancellationBeforeExposure(t *testing.T) {
	for _, phase := range []string{"before-write", "after-write", "after-sync"} {
		t.Run(phase, func(t *testing.T) {
			dir := imagePrivateParent(t)
			path := filepath.Join(dir, "selected.png")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			reservation, _, err := Reserve(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			oldWrite, oldSync := imageWrite, imageSync
			t.Cleanup(func() { imageWrite, imageSync = oldWrite, oldSync })
			switch phase {
			case "before-write":
				cancel()
			case "after-write":
				imageWrite = func(file *os.File, data []byte) (int, error) {
					n, err := file.Write(data)
					cancel()
					return n, err
				}
			case "after-sync":
				imageSync = func(file *os.File) error {
					err := file.Sync()
					cancel()
					return err
				}
			}
			effect, err := reservation.Publish(ctx, []byte("complete validated bytes"))
			if !errors.Is(err, context.Canceled) || effect.FilePublished == nil || *effect.FilePublished ||
				!strings.Contains(strings.Join(effect.Codes, " "), "interrupted") {
				t.Fatal("cancelled prepublication lost its refusal or effects", effect, err)
			}
			if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("cancelled prepublication exposed output", err)
			}
			if effect, err := reservation.Abort(); err != nil || effect.RetainedTemp != nil {
				t.Fatal("cancelled prepublication did not clean its owned temporary", effect, err)
			}
		})
	}
}

func TestImageAbortAlreadyMissingTemporary(t *testing.T) {
	dir := imagePrivateParent(t)
	reservation, _, err := Reserve(context.Background(), filepath.Join(dir, "selected.png"))
	if err != nil {
		t.Fatal(err)
	}
	if err := reservation.root.Remove(reservation.temp); err != nil {
		t.Fatal(err)
	}
	if effect, err := reservation.Abort(); err != nil || effect.RetainedTemp != nil ||
		effect.FilePublished == nil || *effect.FilePublished {
		t.Fatal("already absent owned temporary was reported retained or published", effect, err)
	}
}
