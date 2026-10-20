//go:build windows

package cli

import "strings"

func recoveryPlatformCommand(db string, flags, args []string) string {
	for _, word := range append([]string{db}, flags...) {
		if strings.Trim(word, windowsQuotable) != "" {
			return "no executable recovery command: selected context requires literal arguments on Windows"
		}
	}
	// Link values are concrete acquisition context, not item selectors or help placeholders.
	for i, word := range args {
		if (word == "--outlook-account" || word == "--outlook-profile") && i+1 < len(args) {
			if strings.Trim(args[i+1], windowsQuotable) != "" && args[i+1] != "<tenantId>/<userId>" {
				return "no executable recovery command: selected link requires literal arguments on Windows"
			}
		}
	}
	return archiveScopedCommand(db, append(flags, args...)...)
}
