//go:build darwin

package browser

import (
	"path/filepath"
	"testing"
)

func TestPlatformCandidatesDarwin(t *testing.T) {
	t.Setenv("HOME", "/Users/test")
	edge := platformCandidates(KindEdge)
	if len(edge) != 2 || edge[0] != "/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge" ||
		edge[1] != filepath.Join("/Users/test", "Applications", "Microsoft Edge.app/Contents/MacOS/Microsoft Edge") {
		t.Fatalf("edge candidates: %v", edge)
	}
	chrome := platformCandidates(KindChrome)
	if len(chrome) != 2 || chrome[0] != "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" {
		t.Fatalf("chrome candidates: %v", chrome)
	}
	t.Setenv("HOME", "")
	if got := platformCandidates(KindEdge); len(got) != 1 {
		t.Fatalf("without a home directory only the system location remains: %v", got)
	}
}
