package officecapture

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/sourcecapture"
)

func TestTextOwnedOperationDeadlineBackgroundParent(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TMPDIR", "")
	path, err := filepath.Abs(filepath.Join(root, "synthetic.docx"))
	if err != nil {
		t.Fatal(err)
	}
	input := TextInput{path, "docx"}
	if !ValidTextInput(input) {
		t.Fatalf("noncanonical absolute native fixture: %q", input.Path)
	}
	release, err := sourcecapture.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	done := make(chan error, 1)
	go func() {
		done <- WithTextCapture(context.Background(), input, func(*TextCapture) error { t.Error("held gate opened"); return nil })
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal(err)
		}
	case <-time.After(32 * time.Second):
		release()
		t.Fatal("operation deadline starts after gate wait")
	}
	release()
	retry, err := sourcecapture.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	retry()
}

func TestTextCaptureBoundFieldsMutationAndCancellation(t *testing.T) {
	for _, mode := range []string{"digest", "evidence", "acquired", "source", "private", "cancel", "panic", "close"} {
		t.Run(mode, func(t *testing.T) {
			path := textFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			err := WithTextCapture(ctx, TextInput{path, "docx"}, func(c *TextCapture) error {
				switch mode {
				case "digest":
					c.SourceByteDigest = "changed"
				case "evidence":
					c.Evidence.FileID = "9"
				case "acquired":
					c.AcquiredAt = c.AcquiredAt.Add(time.Second)
				case "source":
					if err := os.WriteFile(path, []byte("changed"), 0o600); err != nil {
						if heldSourceDeniesWrite(err) {
							return errors.New("platform denied mutation")
						}
						t.Fatal(err)
					}
				case "private":
					if err := os.WriteFile(c.privatePath, []byte("changed"), 0o600); err != nil {
						t.Fatal(err)
					}
				case "cancel":
					cancel()
				case "panic":
					panic("synthetic callback")
				case "close":
					if err := c.Close(); err != nil {
						return err
					}
					_, err := c.ReadText(context.Background())
					return err
				}
				return nil
			})
			if err == nil {
				t.Fatal("mutated or cancelled capture succeeded")
			}
		})
	}
}

func TestTextInputRefusalBeforeCopy(t *testing.T) {
	for _, mode := range []string{"kind", "relative", "directory", "link", "too-large", "unsupported"} {
		t.Run(mode, func(t *testing.T) {
			path := textFixture(t)
			input := TextInput{path, "docx"}
			switch mode {
			case "kind":
				input.RequestedKind = "pdf"
			case "relative":
				input.Path = "relative.docx"
			case "directory":
				input.Path = filepath.Dir(path)
			case "link":
				if err := os.Link(path, path+".link"); err != nil {
					t.Skip("native fixture hard links unavailable")
				}
			case "too-large":
				root, err := os.OpenRoot(filepath.Dir(path))
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = root.Close() }()
				f, err := root.OpenFile(filepath.Base(path), os.O_WRONLY, 0)
				if err != nil {
					t.Fatal(err)
				}
				err = f.Truncate((64 << 20) + 1)
				closeErr := f.Close()
				if err != nil || closeErr != nil {
					t.Fatal("fixture truncate")
				}
			case "unsupported":
				if err := os.WriteFile(path, []byte("unsupported bytes"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			called := false
			err := WithTextCapture(context.Background(), input, func(c *TextCapture) error {
				called = true
				_, err := c.ReadText(context.Background())
				return err
			})
			if err == nil || (mode != "unsupported" && called) {
				t.Fatalf("unsafe source copied: %v", err)
			}
			if mode == "too-large" && !strings.Contains(err.Error(), "local_text_too_large") {
				t.Fatal("source bound lost specific diagnostic")
			}
		})
	}
}
