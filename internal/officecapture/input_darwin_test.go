//go:build darwin

package officecapture

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestInputGuardNativePolicyAndInitialPanic(t *testing.T) {
	for range 2 {
		if err := withInputGuard(context.Background(), func() error {
			if policyGet(3, 1) != 1 {
				return errors.New("source operation escaped native OFF policy")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	get := policyGet
	t.Cleanup(func() { policyGet = get })
	policyGet = func(int32, int32) int32 { panic("private policy payload") }
	done := make(chan error, 1)
	go func() { done <- withInputGuard(context.Background(), func() error { return nil }) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("policy panic admitted")
		}
	case <-time.After(time.Second):
		t.Fatal("initial policy failure stranded caller")
	}
}

func TestInputGuardRefusalsAndRestoreBarrier(t *testing.T) {
	if err := withInputGuard(context.Background(), func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	for _, fault := range []string{"save", "set", "verify", "restore", "restore-readback", "cancel", "operation", "panic"} {
		t.Run(fault, func(t *testing.T) {
			get, set := policyGet, policySet
			t.Cleanup(func() { policyGet, policySet = get, set })
			calls := 0
			policyGet = func(int32, int32) int32 {
				calls++
				switch {
				case fault == "save":
					return -1
				case fault == "verify" && calls == 2:
					return 0
				case fault == "restore-readback" && calls == 3:
					return 1
				case calls == 2:
					return 1
				default:
					return 0
				}
			}
			sets := 0
			policySet = func(_, _ int32, _ int32) int32 {
				sets++
				if (fault == "set" && sets == 1) || (fault == "restore" && sets == 2) {
					return -1
				}
				return 0
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if fault == "cancel" {
				cancel()
			}
			called := false
			err := withInputGuard(ctx, func() error {
				called = true
				if fault == "operation" {
					return errors.New("refused operation")
				}
				if fault == "panic" {
					panic("private payload")
				}
				return nil
			})
			if err == nil {
				t.Fatal("guard failure admitted")
			}
			if (fault == "save" || fault == "set" || fault == "verify" || fault == "cancel") && called {
				t.Fatal("unqualified guard performed operation")
			}
			if fault == "restore" || fault == "restore-readback" {
				var coded *ReadError
				if !errors.As(err, &coded) || coded.Code != "office_json_policy_restore_failed" {
					t.Fatal("restore failure not surfaced")
				}
			}
		})
	}
}

func TestInputAncestryAndResidencyRefusalBeforeDataIO(t *testing.T) {
	path, _ := ordinaryFixture(t)
	for _, fault := range []string{"dataless", "network", "unknown-fs", "fs-prefix", "stat", "fs", "root-open", "leaf-open", "directory-type", "leaf-type"} {
		t.Run(fault, func(t *testing.T) {
			open, statFile, fs := inputOpen, inputStat, inputFS
			t.Cleanup(func() { inputOpen, inputStat, inputFS = open, statFile, fs })
			calls := 0
			inputOpen = func(fd int, name string, flags int, mode uint32) (int, error) {
				calls++
				if (fault == "root-open" && calls == 1) || (fault == "leaf-open" && name == "recent.json") {
					return -1, os.ErrPermission
				}
				return open(fd, name, flags, mode)
			}
			inputStat = func(fd int, info *unix.Stat_t) error {
				if fault == "stat" {
					return os.ErrPermission
				}
				if err := statFile(fd, info); err != nil {
					return err
				}
				if fault == "dataless" {
					info.Flags |= unix.SF_DATALESS
				}
				if fault == "directory-type" && info.Mode&unix.S_IFMT == unix.S_IFDIR {
					info.Mode = unix.S_IFREG
				}
				if fault == "leaf-type" && info.Mode&unix.S_IFMT == unix.S_IFREG {
					info.Mode = unix.S_IFDIR
				}
				return nil
			}
			inputFS = func(fd int, info *unix.Statfs_t) error {
				if fault == "fs" {
					return os.ErrPermission
				}
				if err := fs(fd, info); err != nil {
					return err
				}
				if fault == "network" {
					info.Flags &^= unix.MNT_LOCAL
				}
				if fault == "unknown-fs" {
					copy(info.Fstypename[:], "fuse")
				}
				if fault == "fs-prefix" {
					copy(info.Fstypename[:], "apfs-other")
				}
				return nil
			}
			read := jsonRead
			t.Cleanup(func() { jsonRead = read })
			reads := 0
			jsonRead = func(f *os.File, b []byte) (int, error) {
				reads++
				return read(f, b)
			}
			err := WithJSONCapture(context.Background(), DocumentInput{Path: path, Surface: "recent", Application: "supplied"}, func(*JSONCapture) error {
				t.Fatal("unqualified input reached consumer")
				return nil
			})
			if err == nil || reads != 0 {
				t.Fatal("unqualified residency caused data IO")
			}
		})
	}
	for _, path := range []string{"/", "/private/../private", filepath.Join(filepath.Dir(path), "CloudStorage", "recent.json")} {
		input, err := openInput(path, false)
		if err == nil || input != nil {
			t.Fatal("unqualified path admitted")
		}
	}
}
