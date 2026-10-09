//go:build !windows

package outlookowa

import "github.com/ourostack/m365crawl/internal/outlookdesktop"

func DefaultRoot() (string, error) {
	return "", outlookdesktop.ErrNotSupported
}
