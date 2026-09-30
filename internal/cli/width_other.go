//go:build !unix

package cli

import "os"

func fileWidth(*os.File) int { return 0 }
