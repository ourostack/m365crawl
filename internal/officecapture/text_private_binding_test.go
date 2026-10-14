package officecapture

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestTextPrivateIncarnationCheckedBeforeContent(t *testing.T) {
	path := textFixture(t)
	stat, read := registryStat, registryReadAt
	t.Cleanup(func() { registryStat, registryReadAt = stat, read })
	original, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	privateReads := 0
	registryStat = func(f *os.File) (os.FileInfo, error) {
		if filepath.Base(f.Name()) == "resident-ooxml" {
			return original, nil
		}
		return stat(f)
	}
	registryReadAt = func(f *os.File, b []byte, offset int64) (int, error) {
		if filepath.Base(f.Name()) == "resident-ooxml" {
			privateReads++
		}
		return read(f, b, offset)
	}
	if err := WithTextCapture(context.Background(), TextInput{path, "docx"}, func(c *TextCapture) error { _, err := c.ReadText(context.Background()); return err }); err == nil {
		t.Fatal("wrong private incarnation admitted")
	}
	if privateReads != 0 {
		t.Fatal("read bytes before checking private incarnation")
	}
}

func TestTextInclusiveSourceLimit(t *testing.T) {
	path := textFixture(t)
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	f, err := root.OpenFile(filepath.Base(path), os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(64 << 20); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}
	called := false
	err = WithTextCapture(context.Background(), TextInput{path, "docx"}, func(c *TextCapture) error {
		called = true
		if c.Evidence.Size != 64<<20 || len(c.SourceByteDigest) != 64 {
			t.Fatal("inclusive source bound lost")
		}
		return nil
	})
	if err != nil || !called {
		t.Fatalf("inclusive resident source refused: %v", err)
	}
}
