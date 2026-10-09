//go:build !windows

package outlookowa

import "errors"

func DefaultRoot() (string, error) {
	return "", errors.New("outlookowa: default_root_not_supported")
}
