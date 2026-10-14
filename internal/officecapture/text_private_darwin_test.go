//go:build darwin

package officecapture

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestTextDarwinPrivateGuardsRefuseBeforeContent(t *testing.T) {
	for _, mode := range []string{"ancestry", "native-evidence"} {
		t.Run(mode, func(t *testing.T) {
			path := textFixture(t)
			open, stat, read := textOpenPrivate, inputStat, registryReadAt
			t.Cleanup(func() { textOpenPrivate, inputStat, registryReadAt = open, stat, read })
			textOpenPrivate = func(path string, directory bool) (*guardedInput, error) {
				input, err := open(path, directory)
				if err != nil {
					return nil, err
				}
				if mode == "ancestry" {
					input.stamps[len(input.stamps)-1] = "changed"
				} else {
					inputStat = func(fd int, st *unix.Stat_t) error {
						err := stat(fd, st)
						if st.Mode&unix.S_IFMT == unix.S_IFREG {
							st.Btim.Nsec = -1
						}
						return err
					}
				}
				return input, nil
			}
			privateReads := 0
			registryReadAt = func(f *os.File, b []byte, off int64) (int, error) {
				if filepath.Base(f.Name()) == "resident-ooxml" {
					privateReads++
				}
				return read(f, b, off)
			}
			if err := WithTextCapture(context.Background(), TextInput{path, "docx"}, func(c *TextCapture) error { _, err := c.ReadText(context.Background()); return err }); err == nil {
				t.Fatal("unsafe private reader")
			}
			if privateReads != 0 {
				t.Fatal("unsafe private evidence read bytes")
			}
		})
	}
}
