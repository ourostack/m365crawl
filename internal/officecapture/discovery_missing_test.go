package officecapture

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestJSONDiscoveryAbsentOtherAppsDoesNotDiscardHealthyCache(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("native default discovery is Mac only")
	}
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	recent := filepath.Join(home, "Library", "Containers", "com.microsoft.Word", "Data", "Library",
		"Application Support", "Microsoft", "Office", "16.0", "aggmru", "account")
	if err := os.MkdirAll(recent, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(recent, "w-mru4-en-US-sr.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	getHome := discoveryHome
	t.Cleanup(func() { discoveryHome = getHome })
	discoveryHome = func() (string, error) { return home, nil }
	inputs, err := DiscoverJSON(context.Background(), "")
	if err != nil || len(inputs) != 1 || inputs[0].Surface != "recent" || inputs[0].Application != "Word" {
		t.Fatalf("absent app discarded healthy descriptor: %v", err)
	}
}
