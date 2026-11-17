package ouroactions

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
)

// The vendored generator and claims are the files the ouro-ui-core release in dist-manifest.json publishes under these
// names. That release is the one upstream-sources.lock.json names for the vendored source, so an update that moves
// the files, the manifest and the lock together passes, and one that moves only some of them fails.
var vendored = map[string]string{
	"ouro-actions.mts":    "dist/actions/ouro-actions.mts",
	"actions.claims.json": "dist/ax/actions.claims.json",
}

func TestVendoredMatchesCoreManifest(t *testing.T) {
	raw, err := os.ReadFile("dist-manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		CoreTag string            `json:"coreTag"`
		Files   map[string]string `json:"files"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	if release := lockedRelease(t); manifest.CoreTag != release {
		t.Errorf("dist-manifest.json is for %s; upstream-sources.lock.json vendors %s", manifest.CoreTag, release)
	}
	for file, distPath := range vendored {
		body, err := os.ReadFile(file) //nolint:gosec // a vendored file named in this test, read from the repository
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(body)
		if got, want := hex.EncodeToString(sum[:]), manifest.Files[distPath]; got != want {
			t.Errorf("%s: sha256 %s, core's manifest says %s for %s", file, got, want, distPath)
		}
	}
}

// lockedRelease is the ouro-ui-core release that upstream-sources.lock.json names for the files vendored here.
func lockedRelease(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("../../upstream-sources.lock.json")
	if err != nil {
		t.Fatal(err)
	}
	var lock struct {
		Sources []struct {
			Repository string `json:"repository"`
			Release    string `json:"release"`
			Files      []struct {
				GeneratedPath string `json:"generatedPath"`
			} `json:"files"`
		} `json:"sources"`
	}
	if err := json.Unmarshal(raw, &lock); err != nil {
		t.Fatal(err)
	}
	for _, source := range lock.Sources {
		for _, file := range source.Files {
			if source.Repository == "ourostack/ouro-ui-core" && file.GeneratedPath == "tools/ouro-actions/dist-manifest.json" {
				return source.Release
			}
		}
	}
	t.Fatal("upstream-sources.lock.json vendors no ourostack/ouro-ui-core source to tools/ouro-actions/dist-manifest.json")
	return ""
}
