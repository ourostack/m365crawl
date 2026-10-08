package browsertest

import "os"

// stdioReport says whether the fake's standard streams are the null device.
func stdioReport() string {
	null, _ := os.Stat(os.DevNull)
	for _, f := range []*os.File{os.Stdin, os.Stdout, os.Stderr} {
		fi, _ := f.Stat()
		if !os.SameFile(fi, null) {
			return "not-null"
		}
	}
	return "null"
}
