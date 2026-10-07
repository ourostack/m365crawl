//go:build !windows

package errs

func noFullDiskAccessMessage(path string) (string, string) {
	return "macOS denied access to " + path,
		"Grant Full Disk Access to the app that runs m365crawl in System Settings › Privacy & Security › Full Disk Access, then restart that app."
}
