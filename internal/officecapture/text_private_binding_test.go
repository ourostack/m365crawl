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
