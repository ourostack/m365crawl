// Package cli implements the teamscrawl command line.
package cli

import (
	"errors"
	"fmt"
	"io"

	"github.com/alecthomas/kong"
)

// version is set at build time with
// -ldflags "-X github.com/ourostack/teamscrawl/internal/cli.version=...".
var version = "dev"

const (
	exitOK    = 0
	exitUsage = 2
)

type cliApp struct {
	Version versionCmd `cmd:"" help:"Print the teamscrawl version."`
}

type versionCmd struct{}

func (versionCmd) Run(w io.Writer) error {
	_, err := fmt.Fprintln(w, version)
	return err
}

// Main runs the CLI and returns the process exit code.
func Main(args []string, stdout, stderr io.Writer) int {
	var app cliApp
	exited := false
	parser, err := kong.New(&app,
		kong.Name("teamscrawl"),
		kong.Description("Mirror the Microsoft Teams desktop cache into local SQLite for agents."),
		kong.Writers(stdout, stderr),
		kong.Exit(func(int) { exited = true }),
		kong.BindTo(stdout, (*io.Writer)(nil)),
	)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	kctx, err := parser.Parse(args)
	if exited {
		return exitOK
	}
	if err != nil {
		var pe *kong.ParseError
		if errors.As(err, &pe) {
			_, _ = fmt.Fprintln(stderr, err)
			return exitUsage
		}
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	if err := kctx.Run(); err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	return exitOK
}
