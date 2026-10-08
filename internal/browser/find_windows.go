//go:build windows

package browser

import (
	"os"
	"path/filepath"
)

func platformCandidates(k Kind) []string {
	var out []string
	if k == KindEdge {
		for _, env := range []string{"ProgramFiles(x86)", "ProgramFiles"} {
			if dir := os.Getenv(env); dir != "" {
				out = append(out, filepath.Join(dir, "Microsoft", "Edge", "Application", "msedge.exe"))
			}
		}
		return out
	}
	for _, env := range []string{"ProgramFiles", "LOCALAPPDATA"} {
		if dir := os.Getenv(env); dir != "" {
			out = append(out, filepath.Join(dir, "Google", "Chrome", "Application", "chrome.exe"))
		}
	}
	return out
}
