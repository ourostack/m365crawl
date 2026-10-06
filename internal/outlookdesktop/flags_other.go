//go:build !unix

package outlookdesktop

import "os"

// sourceOpenFlags opens Outlook's store read-only.
const sourceOpenFlags = os.O_RDONLY
