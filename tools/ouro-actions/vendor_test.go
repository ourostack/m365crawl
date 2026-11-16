package ouroactions

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
)

// The vendored generator and claims are the files ouro-ui-core v0.16.0 publishes under these names in its dist-manifest.
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
	if manifest.CoreTag != "v0.16.0" {
		t.Errorf("dist-manifest.json is for %s; the vendored generator is v0.16.0", manifest.CoreTag)
	}
	for file, distPath := range vendored {
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(body)
		if got, want := hex.EncodeToString(sum[:]), manifest.Files[distPath]; got != want {
			t.Errorf("%s: sha256 %s, core's manifest says %s for %s", file, got, want, distPath)
		}
	}
}
