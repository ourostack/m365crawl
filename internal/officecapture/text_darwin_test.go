//go:build darwin

package officecapture

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestTextDarwinHeldEvidenceFaultsAndZeros(t *testing.T) {
	path := textFixture(t)
	file, err := os.OpenInRoot(filepath.Dir(path), filepath.Base(path))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	stat := inputStat
	t.Cleanup(func() { inputStat = stat })
	inputStat = func(int, *unix.Stat_t) error { return os.ErrPermission }
	if _, err := heldTextEvidence(file); err == nil {
		t.Fatal("native stat failure")
	}
	for _, mode := range []string{"zero", "modified-invalid", "creation-invalid", "generation", "creation", "negative-device", "unsupported-clock"} {
		inputStat = func(_ int, st *unix.Stat_t) error {
			st.Dev, st.Ino, st.Size = 1, 2, 8
			switch mode {
			case "modified-invalid":
				st.Mtim.Nsec = -1
			case "creation-invalid":
				st.Btim.Nsec = -1
			case "generation":
				st.Gen = 7
				st.Btim.Sec = 3
				st.Mtim.Sec = 4
			case "creation":
				st.Btim.Sec = 3
			case "negative-device":
				st.Dev = -1
			case "unsupported-clock":
				st.Mtim.Sec = 1 << 62
			}
			return nil
		}
		e, err := heldTextEvidence(file)
		if mode == "modified-invalid" || mode == "creation-invalid" {
			if err == nil {
				t.Fatal("invalid native encoding")
			}
		} else if err != nil || (mode == "zero" && (e.ModifiedAt != nil || e.GenerationQualified || *e.NativeGenerationRaw != "0" || *e.CreationRaw != "0:0")) {
			t.Fatalf("observed zero evidence: %#v %v", e, err)
		}
	}
}

func TestTextDarwinUnknownFlagsRefuseBeforeBytes(t *testing.T) {
	path := textFixture(t)
	stat, statat, read := inputStat, registryStatat, registryReadAt
	t.Cleanup(func() { inputStat, registryStatat, registryReadAt = stat, statat, read })
	inputStat = func(fd int, st *unix.Stat_t) error {
		err := stat(fd, st)
		if st.Mode&unix.S_IFMT == unix.S_IFREG {
			st.Flags |= 0x1000
		}

		return err
	}
	registryStatat = func(fd int, name string, st *unix.Stat_t, flags int) error {
		err := statat(fd, name, st, flags)
		if st.Mode&unix.S_IFMT == unix.S_IFREG {
			st.Flags |= 0x1000
		}
		return err
	}
	registryReadAt = func(*os.File, []byte, int64) (int, error) {
		t.Fatal("unknown native flags read content")
		return 0, nil
	}
	if err := WithTextCapture(context.Background(), TextInput{path, "docx"}, func(*TextCapture) error { t.Fatal("unknown flags reached parser"); return nil }); err == nil {
		t.Fatal("unknown flags qualified")
	}
}

func TestTextDarwinLateGenerationAndSameClockByteMutation(t *testing.T) {
	for _, mode := range []string{"generation", "bytes", "copied-bytes"} {
		t.Run(mode, func(t *testing.T) {
			path := textFixture(t)
			stat, read := inputStat, registryReadAt
			t.Cleanup(func() { inputStat, registryReadAt = stat, read })
			if mode == "copied-bytes" {
				reads := 0
				registryReadAt = func(f *os.File, b []byte, off int64) (int, error) {
					n, err := read(f, b, off)
					if f.Name() == filepath.Base(path) {
						reads++
						if reads > 1 && n > 0 {
							b[0] ^= 1
						}
					}
					return n, err
				}
			}
			err := WithTextCapture(context.Background(), TextInput{path, "docx"}, func(c *TextCapture) error {
				switch mode {
				case "generation":
					inputStat = func(fd int, st *unix.Stat_t) error {
						err := stat(fd, st)
						if st.Mode&unix.S_IFMT == unix.S_IFREG {
							st.Gen++
						}
						return err
					}
				case "bytes":
					info, err := os.Stat(path)
					if err != nil {
						t.Fatal(err)
					}
					root, err := os.OpenRoot(filepath.Dir(path))
					if err != nil {
						t.Fatal(err)
					}
					f, err := root.OpenFile(filepath.Base(path), os.O_WRONLY, 0)
					if err != nil {
						t.Fatal(err)
					}
					_, writeErr := f.WriteAt([]byte("X"), 0)
					closeErr := f.Close()
					rootErr := root.Close()
					if writeErr != nil || closeErr != nil || rootErr != nil {
						t.Fatal("fixture mutation")
					}
					if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
						t.Fatal(err)
					}
				}
				_, err := c.ReadText(context.Background())
				return err
			})
			if err == nil {
				t.Fatal("late native change admitted")
			}
		})
	}
}
