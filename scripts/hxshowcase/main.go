// Command hxshowcase writes the synthetic showcase Outlook store that `make screenshot` syncs:
// OUT/Main/HxStore.hxd, with every time an offset from -now. Everything in it is invented; see
// internal/hxstore/hxshowcase.
//
// Usage: go run ./scripts/hxshowcase -out DIR [-now RFC3339]
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/ourostack/m365crawl/internal/hxstore/hxshowcase"
)

var (
	exit  = os.Exit
	clock = time.Now
)

func main() { exit(run(os.Args[1:], os.Stderr)) }

func run(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("hxshowcase", flag.ContinueOnError)
	fs.SetOutput(stderr)
	out := fs.String("out", "", "Outlook root directory to write (one profile directory is made in it)")
	nowText := fs.String("now", "", "the present, RFC 3339 (default: the clock)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *out == "" {
		_, _ = fmt.Fprintln(stderr, "hxshowcase: -out is required")
		return 2
	}
	now := clock()
	if *nowText != "" {
		t, err := time.Parse(time.RFC3339, *nowText)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "hxshowcase: -now: %v\n", err)
			return 2
		}
		now = t
	}
	dir := filepath.Join(*out, hxshowcase.Profile)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		_, _ = fmt.Fprintf(stderr, "hxshowcase: %v\n", err)
		return 1
	}
	if err := os.WriteFile(filepath.Join(dir, "HxStore.hxd"), hxshowcase.Build(now), 0o600); err != nil {
		_, _ = fmt.Fprintf(stderr, "hxshowcase: %v\n", err)
		return 1
	}
	return 0
}
