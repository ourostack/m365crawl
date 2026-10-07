package outlookdesktop

import "os"

// OpenReadOnly opens a file inside an Outlook profile for reading and nothing else: the same
// flags as the store open (read-only, and non-blocking where the platform has it). It is the one
// way other packages open Outlook's files, for example the downloaded message bodies under the
// profile's Files directory. The caller closes the file. Errors are the operating system's, so
// errors.Is(err, fs.ErrNotExist) tells a file that is gone.
func OpenReadOnly(path string) (*os.File, error) {
	return openFile(path, sourceOpenFlags, 0)
}
