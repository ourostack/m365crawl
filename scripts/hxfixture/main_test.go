package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ourostack/teamscrawl/internal/hxstore/hxfixture"
)

func TestWriteThenCheck(t *testing.T) {
	dir := t.TempDir()
	var errOut bytes.Buffer
	if code := run([]string{"-out", dir}, &errOut); code != 0 {
		t.Fatalf("%d %s", code, errOut.String())
	}
	for _, f := range hxfixture.Build() {
		got, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(f.Name))) //nolint:gosec // a path in a test temp dir
		if err != nil || !bytes.Equal(got, f.Data) {
			t.Fatalf("%s: %v", f.Name, err)
		}
	}
	if code := run([]string{"-out", dir, "-check"}, &errOut); code != 0 {
		t.Fatalf("check of fresh output: %d %s", code, errOut.String())
	}
	if err := os.WriteFile(filepath.Join(dir, "HxStore.hxd"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "PROVENANCE.json")); err != nil {
		t.Fatal(err)
	}
	errOut.Reset()
	if code := run([]string{"-out", dir, "-check"}, &errOut); code != 1 ||
		!strings.Contains(errOut.String(), "HxStore.hxd differs") || !strings.Contains(errOut.String(), "PROVENANCE.json differs") {
		t.Fatalf("%d %s", code, errOut.String())
	}
}

func TestBadFlag(t *testing.T) {
	var errOut bytes.Buffer
	if code := run([]string{"-nope"}, &errOut); code != 2 {
		t.Fatal(code)
	}
}

func TestWriteFailures(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "a-file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var errOut bytes.Buffer
	// The output directory is a file: the directory cannot be made.
	if code := run([]string{"-out", filepath.Join(file, "sub")}, &errOut); code != 1 {
		t.Fatalf("%d", code)
	}
	// A directory stands where a file must go: the write fails.
	d2 := t.TempDir()
	if err := os.MkdirAll(filepath.Join(d2, "HxStore.hxd"), 0o750); err != nil {
		t.Fatal(err)
	}
	errOut.Reset()
	if code := run([]string{"-out", d2}, &errOut); code != 1 || !strings.Contains(errOut.String(), "hxfixture:") {
		t.Fatalf("%d %s", code, errOut.String())
	}
}

func TestMainExits(t *testing.T) {
	old := exit
	defer func() { exit = old }()
	got := -1
	exit = func(c int) { got = c }
	oldArgs := os.Args
	defer func() { os.Args = oldArgs }()
	os.Args = []string{"hxfixture", "-out", t.TempDir()}
	main()
	if got != 0 {
		t.Fatal(got)
	}
}
