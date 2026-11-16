package ouroax

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The lock names each vendored file of the Ouro AX kit with the sha256 it was copied with.
const lockPath = "../../upstream-sources.lock.json"

type lockFile struct {
	Sources []struct {
		Files []struct {
			GeneratedPath string `json:"generatedPath"`
			SHA256        string `json:"sha256"`
		} `json:"files"`
	} `json:"sources"`
}

func TestVendoredFilesMatchLock(t *testing.T) {
	raw, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	var lock lockFile
	if err := json.Unmarshal(raw, &lock); err != nil {
		t.Fatal(err)
	}
	listed := map[string]bool{}
	for _, source := range lock.Sources {
		for _, file := range source.Files {
			listed[filepath.Base(file.GeneratedPath)] = true
			body, err := os.ReadFile(filepath.Join("../..", file.GeneratedPath))
			if err != nil {
				t.Fatalf("%s: %v", file.GeneratedPath, err)
			}
			sum := sha256.Sum256(body)
			if got := hex.EncodeToString(sum[:]); got != file.SHA256 {
				t.Errorf("%s: sha256 %s, lock says %s; re-copy it from the pinned release", file.GeneratedPath, got, file.SHA256)
			}
		}
	}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasSuffix(name, ".ts") || name == "LICENSE" {
			if !listed[name] {
				t.Errorf("%s is vendored but not listed in %s", name, lockPath)
			}
		}
	}
}
