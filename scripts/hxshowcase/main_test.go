package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/hxstore/hxshowcase"
)

func TestWrite(t *testing.T) {
	dir := t.TempDir()
	var errOut bytes.Buffer
	if code := run([]string{"-out", dir, "-now", "2026-10-07T18:04:00Z"}, &errOut); code != 0 {
		t.Fatalf("%d %s", code, errOut.String())
	}
	got, err := os.ReadFile(filepath.Join(dir, hxshowcase.Profile, "HxStore.hxd")) //nolint:gosec // a test temp dir
	if err != nil || !bytes.Equal(got, hxshowcase.Build(time.Date(2026, 10, 7, 18, 4, 0, 0, time.UTC))) {
		t.Fatalf("store differs: %v", err)
	}
}

func TestWriteUsesTheClock(t *testing.T) {
	at := time.Date(2030, 1, 2, 3, 4, 0, 0, time.UTC)
	old := clock
	clock = func() time.Time { return at }
	t.Cleanup(func() { clock = old })
	dir := t.TempDir()
	if code := run([]string{"-out", dir}, &bytes.Buffer{}); code != 0 {
		t.Fatal(code)
	}
	got, _ := os.ReadFile(filepath.Join(dir, hxshowcase.Profile, "HxStore.hxd")) //nolint:gosec // a test temp dir
	if !bytes.Equal(got, hxshowcase.Build(at)) {
		t.Fatal("store is not built for the clock's now")
	}
}

func TestUsage(t *testing.T) {
	for _, args := range [][]string{{"-nope"}, {}, {"-out", t.TempDir(), "-now", "yesterday"}} {
		var errOut bytes.Buffer
		if code := run(args, &errOut); code != 2 {
			t.Fatalf("%v: %d %s", args, code, errOut.String())
		}
	}
}

func TestWriteFailures(t *testing.T) {
	file := filepath.Join(t.TempDir(), "a-file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var errOut bytes.Buffer
	if code := run([]string{"-out", file}, &errOut); code != 1 || !strings.Contains(errOut.String(), "hxshowcase:") {
		t.Fatalf("%d %s", code, errOut.String())
	}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, hxshowcase.Profile, "HxStore.hxd"), 0o750); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"-out", dir}, &bytes.Buffer{}); code != 1 {
		t.Fatalf("%d", code)
	}
}

func TestMainExits(t *testing.T) {
	old, oldArgs := exit, os.Args
	t.Cleanup(func() { exit, os.Args = old, oldArgs })
	got := -1
	exit = func(c int) { got = c }
	os.Args = []string{"hxshowcase", "-out", t.TempDir()}
	main()
	if got != 0 {
		t.Fatal(got)
	}
}
