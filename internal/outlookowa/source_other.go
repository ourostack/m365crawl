//go:build !windows

package outlookowa

import (
	"fmt"
	"os"
	"syscall"
)

func unsafeEntry(info os.FileInfo) bool { return info.Mode()&os.ModeSymlink != 0 }

func sourceIdentity(_ string, info os.FileInfo) (string, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", sourceFailure("source_unreadable", nil)
	}
	return fmt.Sprintf("%d:%d", stat.Dev, stat.Ino), nil
}
