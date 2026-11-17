package ouroactions

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
)

// The vendored generator and claims are the files the ouro-ui-core release named in upstream-sources.lock.json publishes
// under these names in its dist-manifest.
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

// lockedRelease is the ouro-ui-core release upstream-sources.lock.json records for the vendored generator.
func lockedRelease(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("../../upstream-sources.lock.json")
	if err != nil {
		t.Fatal(err)
	}
	var lock struct {
		Sources []struct {
			ID      string `json:"id"`
			Release string `json:"release"`
		} `json:"sources"`
	}
	if err := json.Unmarshal(raw, &lock); err != nil {
		t.Fatal(err)
	}
	for _, source := range lock.Sources {
		if source.ID == "ourostack-ouro-ui-core-actions" {
			return source.Release
		}
	}
	t.Fatal("upstream-sources.lock.json has no ourostack-ouro-ui-core-actions source")
	return ""
}
