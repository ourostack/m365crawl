//go:build !unix

package hxstore

import "os"

// openFlags opens read-only.
const openFlags = os.O_RDONLY
