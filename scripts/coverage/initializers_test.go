package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSourceInventoryDoesNotLoseSiblingInitializers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.go")
	text := "package sample\nvar first, second = func() int { return 1 }, func() int { return 2 }\n"
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	fns, err := sourceFunctions(path)
	if err != nil || len(fns) != 2 || fns[0].name != "initializer:first" || fns[1].name != "initializer:second" {
		t.Fatalf("sibling initializer inventory = %v %v", fns, err)
	}
}

func TestSourceInventoryIncludesWrappedInitializers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.go")
	text := "package sample\nvar wrapped = wrap(func() int { return 1 })\n"
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	fns, err := sourceFunctions(path)
	if err != nil || len(fns) != 1 {
		t.Fatalf("wrapped initializer missing = %v %v", fns, err)
	}
}
