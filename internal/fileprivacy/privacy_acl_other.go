//go:build !windows && !darwin

package fileprivacy

import "os"

func privateACL(*os.File) error { return nil }
