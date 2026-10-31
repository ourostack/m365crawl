package workiq

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLocateFindsTheExecutableWithoutStartingIt(t *testing.T) {
	t.Setenv("M365CRAWL_WORKIQ_BIN", "")
	requireCode(t, Locate(), "workiq_hermetic_required")
	dir := t.TempDir()
	t.Setenv("M365CRAWL_WORKIQ_BIN", filepath.Join(dir, "absent"))
	requireCode(t, Locate(), "workiq_not_installed")
	marker := filepath.Join(dir, "started")
	present := filepath.Join(dir, "workiq")
	if err := os.WriteFile(present, []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0o700); err != nil { //nolint:gosec // G306: private synthetic test executable.
		t.Fatal(err)
	}
	t.Setenv("M365CRAWL_WORKIQ_BIN", present)
	if err := Locate(); err != nil {
		t.Fatal("an existing executable was not found", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("locating WorkIQ started it")
	}
}
