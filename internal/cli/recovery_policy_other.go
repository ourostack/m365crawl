//go:build !windows

package cli

func recoveryPlatformCommand(db string, flags, args []string) string {
	return archiveScopedCommand(db, append(flags, args...)...)
}
