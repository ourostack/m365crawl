// Package cli implements the teamscrawl command line.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/alecthomas/kong"

	"github.com/ourostack/teamscrawl/internal/errs"
)

// version is set at build time with
// -ldflags "-X github.com/ourostack/teamscrawl/internal/cli.version=...".
var version = "dev"

const exitOK = 0

// Globals are the flags every command accepts.
type Globals struct {
	Format    string `help:"Output format: text, json or log. Default: text on a terminal, json otherwise." placeholder:"text|json|log"`
	JSON      bool   `name:"json" help:"Alias for --format json."`
	DB        string `name:"db" env:"TEAMSCRAWL_DB" help:"Archive database path (default ~/.teamscrawl/teamscrawl.db)." placeholder:"PATH"`
	TeamsRoot string `name:"teams-root" env:"TEAMSCRAWL_TEAMS_ROOT" help:"Teams EBWebView directory (default: the new Teams container)." placeholder:"DIR"`
	Account   string `help:"Only this account, as <tenantId>/<userId>. Default: every account." placeholder:"TENANT/USER"`
	NoColor   bool   `name:"no-color" help:"Disable colored output."`
	MaxAge    string `name:"max-age" env:"TEAMSCRAWL_MAX_AGE" default:"15m" help:"Read commands sync first when the last successful sync is older than this (for example 15m, 2h, 1d). 0 disables the implicit sync." placeholder:"DURATION"`
	Fields    string `help:"Keep only these top-level keys of each item, comma separated." placeholder:"a,b,c"`
	MaxText   int    `name:"max-text" help:"Truncate each item's text to N characters and set text_truncated. 0 keeps all of it." placeholder:"N"`
}

type cliApp struct {
	Globals

	Doctor        doctorCmd        `cmd:"" help:"Check that Teams, Full Disk Access and the archive are ready."`
	Sync          syncCmd          `cmd:"" help:"Copy the Teams cache into the archive once and print what changed."`
	Status        statusCmd        `cmd:"" help:"Show archive counts per account, the last sync and other Teams origins."`
	Search        searchCmd        `cmd:"" help:"Full-text search over message text, newest first."`
	Messages      messagesCmd      `cmd:"" help:"List messages in chronological order."`
	Conversations conversationsCmd `cmd:"" help:"List conversations by latest activity."`
	People        peopleCmd        `cmd:"" help:"List people seen as senders or members."`
	Activity      activityCmd      `cmd:"" help:"List activity-feed items (mentions, replies, reactions) with their messages."`
	Unread        unreadCmd        `cmd:"" help:"List unread messages, newest first."`
	Thread        threadCmd        `cmd:"" help:"Show one thread: <conversation> <root-message-id>, or a Teams message link."`
	Whoami        whoamiCmd        `cmd:"" help:"Show the accounts in the archive and the archive's state."`
	SQL           sqlCmd           `cmd:"" name:"sql" help:"Run a read-only SQL query against the archive."`
	Version       versionCmd       `cmd:"" help:"Print the teamscrawl version."`
}

type versionCmd struct{}

func (versionCmd) Run(rt *runtime) error {
	_, err := fmt.Fprintln(rt.stdout, version)
	return err
}

// Main runs the CLI and returns the process exit code. It owns signal handling: SIGINT and
// SIGTERM cancel the context every lower layer cleans up on.
func Main(args []string, stdout, stderr io.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runCLI(ctx, args, stdout, stderr)
}

func runCLI(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	var app cliApp
	exited := false
	parser, err := kong.New(&app,
		kong.Name("teamscrawl"),
		kong.Description("Mirror the Microsoft Teams desktop cache into local SQLite so agents can read Teams offline."),
		kong.Writers(stdout, stderr),
		kong.Exit(func(int) { exited = true }),
	)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return errs.ExitRuntime
	}
	rt := newRuntime(ctx, &app.Globals, stdout, stderr)
	rt.format = guessFormat(args, rt.stdoutTTY) // until the real flags are validated
	kctx, err := parser.Parse(args)
	if exited {
		return exitOK
	}
	if err != nil {
		return rt.fail(err)
	}
	if err := rt.setup(); err != nil {
		return rt.fail(err)
	}
	kctx.Bind(rt)
	if err := kctx.Run(); err != nil {
		return rt.fail(err)
	}
	return exitOK
}

// fail prints err as a coded error and returns its exit status.
func (rt *runtime) fail(err error) int {
	var coded *errs.Coded
	if !errors.As(err, &coded) {
		var pe *kong.ParseError
		switch {
		case errors.As(err, &pe):
			coded = errs.Usage(pe.Error())
		case errors.Is(err, context.Canceled):
			coded = errs.Internal(errors.New("interrupted"))
		default:
			coded = errs.Internal(err)
		}
	}
	rt.printError(coded)
	return coded.Exit
}
