//go:build !darwin && !windows

package browser

// Only macOS and Windows have known install locations; elsewhere pass --browser with a path.
func platformCandidates(Kind) []string { return nil }
