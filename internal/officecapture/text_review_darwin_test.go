//go:build darwin

package officecapture

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ourostack/m365crawl/internal/officedocuments"
	"golang.org/x/sys/unix"
)

func TestTextUnknownAncestorFlagsRefuseBeforeContent(t *testing.T) {
	for _, late := range []bool{false, true} {
		t.Run(map[bool]string{false: "initial", true: "after-copy"}[late], func(t *testing.T) {
			path := textFixture(t)
			stat, read := inputStat, registryReadAt
			t.Cleanup(func() { inputStat, registryReadAt = stat, read })
			unsafe := !late
			inputStat = func(fd int, st *unix.Stat_t) error {
				err := stat(fd, st)
				if unsafe && st.Mode&unix.S_IFMT == unix.S_IFDIR {
					st.Flags |= 0x1000
				}
				return err
			}
			reads := 0
			registryReadAt = func(f *os.File, b []byte, offset int64) (int, error) { reads++; return read(f, b, offset) }
			beforeRead := 0
			err := WithTextCapture(context.Background(), TextInput{path, "docx"}, func(c *TextCapture) error {
				beforeRead = reads
				unsafe = true
				_, err := c.ReadText(context.Background())
				return err
			})
			if err == nil || (!late && reads != 0) || (late && reads != beforeRead) {
				t.Fatalf("unknown ancestry reached content: late=%t reads=%d before=%d err=%v", late, reads, beforeRead, err)
			}
		})
	}
}

func TestTextDarwinYearOneModifiedClockIsUnavailable(t *testing.T) {
	path := textFixture(t)
	file, err := os.OpenInRoot(filepath.Dir(path), filepath.Base(path))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	stat := inputStat
	t.Cleanup(func() { inputStat = stat })
	inputStat = func(fd int, st *unix.Stat_t) error {
		err := stat(fd, st)
		st.Mtim.Sec, st.Mtim.Nsec = -62135596800, 0
		return err
	}
	e, err := heldTextEvidence(file)
	if err != nil || e.ModificationRaw == nil || *e.ModificationRaw != "-62135596800:0" || e.ModifiedAt != nil {
		t.Fatalf("sentinel raw/display disagreement: %#v %v", e, err)
	}
}

func TestSharedOfficeGuardsRejectUnknownDirectoryFlags(t *testing.T) {
	path := registrySource(t)
	stat, readAt, read := inputStat, registryReadAt, jsonRead
	t.Cleanup(func() { inputStat, registryReadAt, jsonRead = stat, readAt, read })
	inputStat = func(fd int, st *unix.Stat_t) error {
		err := stat(fd, st)
		if st.Mode&unix.S_IFMT == unix.S_IFDIR {
			st.Flags |= 0x1000
		}
		return err
	}
	reads := 0
	registryReadAt = func(f *os.File, b []byte, off int64) (int, error) { reads++; return readAt(f, b, off) }
	jsonRead = func(f *os.File, b []byte) (int, error) { reads++; return read(f, b) }
	for _, capture := range []func() error{
		func() error {
			return WithRegistryCapture(context.Background(), RegistryInput{path}, func(*RegistryCapture) error { t.Fatal("unsafe registry callback"); return nil })
		},
		func() error {
			return WithOneDriveUsageCapture(context.Background(), UsageInput{path}, func(*UsageCapture) error { t.Fatal("unsafe usage callback"); return nil })
		},
		func() error {
			return WithJSONCapture(context.Background(), DocumentInput{Path: path, Surface: officedocuments.Recent, Application: "supplied"}, func(*JSONCapture) error { t.Fatal("unsafe JSON callback"); return nil })
		},
	} {
		if err := capture(); err == nil {
			t.Fatal("shared capture admitted unknown directory flags")
		}
	}
	if reads != 0 {
		t.Fatal("shared consumer read bytes under unknown ancestry")
	}
}

func TestTextHeldEvidenceRejectsUnknownLeafFlagPolicy(t *testing.T) {
	path := textFixture(t)
	file, err := os.OpenInRoot(filepath.Dir(path), filepath.Base(path))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	stat := inputStat
	t.Cleanup(func() { inputStat = stat })
	inputStat = func(fd int, st *unix.Stat_t) error { err := stat(fd, st); st.Flags |= 0x1000; return err }
	if _, err := heldTextEvidence(file); err == nil {
		t.Fatal("unknown held native leaf flags qualified")
	}
}

func TestTextEvidenceFailureAfterHeldChainValidationRefusesBytes(t *testing.T) {
	path := textFixture(t)
	stat, native, read := registryStat, inputStat, registryReadAt
	t.Cleanup(func() { registryStat, inputStat, registryReadAt = stat, native, read })
	registryStat = func(f *os.File) (os.FileInfo, error) {
		info, err := stat(f)
		inputStat = func(int, *unix.Stat_t) error { return os.ErrPermission }
		return info, err
	}
	registryReadAt = func(*os.File, []byte, int64) (int, error) {
		t.Fatal("native evidence failure read bytes")
		return 0, nil
	}
	if err := WithTextCapture(context.Background(), TextInput{path, "docx"}, func(*TextCapture) error {
		t.Fatal("native evidence failure reached consumer")
		return nil
	}); err == nil {
		t.Fatal("native evidence failure qualified")
	}
}
