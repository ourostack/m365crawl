//go:build windows

package browser

import (
	"path/filepath"
	"testing"
)

func TestPlatformCandidatesWindows(t *testing.T) {
	t.Setenv("ProgramFiles(x86)", `C:\PF86`)
	t.Setenv("ProgramFiles", `C:\PF`)
	t.Setenv("LOCALAPPDATA", `C:\Local`)
	edge := platformCandidates(KindEdge)
	if len(edge) != 2 || edge[0] != filepath.Join(`C:\PF86`, "Microsoft", "Edge", "Application", "msedge.exe") {
		t.Fatalf("edge candidates: %v", edge)
	}
	chrome := platformCandidates(KindChrome)
	if len(chrome) != 2 || chrome[1] != filepath.Join(`C:\Local`, "Google", "Chrome", "Application", "chrome.exe") {
		t.Fatalf("chrome candidates: %v", chrome)
	}
	t.Setenv("ProgramFiles(x86)", "")
	t.Setenv("ProgramFiles", "")
	t.Setenv("LOCALAPPDATA", "")
	if len(platformCandidates(KindEdge)) != 0 || len(platformCandidates(KindChrome)) != 0 {
		t.Fatal("unset environment variables give no candidates")
	}
}
