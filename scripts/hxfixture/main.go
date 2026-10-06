// Command hxfixture writes the synthetic Outlook store fixture, or checks that
// the files on disk are what the generator makes.
//
// Usage: go run ./scripts/hxfixture [-out DIR] [-check]
//
// The default directory is testdata/outlook-fixture, relative to the working
// directory. Everything written is invented; see internal/hxstore/hxfixture.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/ourostack/teamscrawl/internal/hxstore/hxfixture"
)

var exit = os.Exit

func main() { exit(run(os.Args[1:], os.Stderr)) }

func run(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("hxfixture", flag.ContinueOnError)
	fs.SetOutput(stderr)
	out := fs.String("out", hxfixture.Dir, "directory to write")
	check := fs.Bool("check", false, "compare instead of writing")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	bad := 0
	for _, f := range hxfixture.Build() {
		path := filepath.Join(*out, filepath.FromSlash(f.Name))
		if *check {
			have, err := os.ReadFile(path) //nolint:gosec // the -out directory joined with a generated name
			if err != nil || !bytes.Equal(have, f.Data) {
				_, _ = fmt.Fprintf(stderr, "hxfixture: %s differs\n", f.Name)
				bad++
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			_, _ = fmt.Fprintf(stderr, "hxfixture: %v\n", err)
			return 1
		}
		if err := os.WriteFile(path, f.Data, 0o600); err != nil {
			_, _ = fmt.Fprintf(stderr, "hxfixture: %v\n", err)
			return 1
		}
	}
	if bad > 0 {
		return 1
	}
	return 0
}
