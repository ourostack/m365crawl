// Package cli implements the teamscrawl command line.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"syscall"

	"github.com/alecthomas/kong"

	"github.com/openclaw/crawlkit/output"

	"github.com/ourostack/teamscrawl"
	"github.com/ourostack/teamscrawl/internal/errs"
	"github.com/ourostack/teamscrawl/internal/render"
)

// version, commit and date are set at build time with
// -ldflags "-X github.com/ourostack/teamscrawl/internal/cli.version=..." (and .commit, .date).
var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

// exitForced is the exit status of a run that a second SIGINT or SIGTERM cut short: 128 plus
// SIGINT's number, what a shell reports for a process killed by Ctrl-C.
const exitForced = 130

const exitOK = 0

// panicHook lets tests inject a panic into the run path.
var panicHook func()

// Globals are the flags every command accepts.
type Globals struct {
	Format    string `help:"Output format: text, json or log. Default: text on a terminal, json otherwise." placeholder:"text|json|log"`
	JSON      bool   `name:"json" help:"Alias for --format json."`
	DB        string `name:"db" env:"TEAMSCRAWL_DB" help:"Archive database path (default ~/.teamscrawl/teamscrawl.db)." placeholder:"PATH"`
	TeamsRoot string `name:"teams-root" env:"TEAMSCRAWL_TEAMS_ROOT" help:"Teams EBWebView directory (default: the new Teams container)." placeholder:"DIR"`
	Account   string `help:"Only this account, as <tenantId>/<userId>. Default: every account." placeholder:"TENANT/USER"`
	NoColor   bool   `name:"no-color" help:"Disable colored output (also: NO_COLOR). CLICOLOR_FORCE=1 forces color."`
	MaxAge    string `name:"max-age" env:"TEAMSCRAWL_MAX_AGE" default:"15m" help:"Read commands sync first when the last successful sync is older than this (for example 15m, 2h, 1d). 0 disables the implicit sync." placeholder:"DURATION"`
	Fields    string `help:"List commands only: keep only these top-level keys of each item, comma separated." placeholder:"a,b,c"`
	MaxText   int    `name:"max-text" help:"List commands only: truncate each item's text to N characters and set text_truncated. 0 keeps all of it." placeholder:"N"`
}

type cliApp struct {
	Globals
	Version versionFlag `name:"version" help:"Print the version, commit and build date, then exit."`

	Doctor        doctorCmd        `cmd:"" help:"Check that Teams, Full Disk Access and the archive are ready."`
	Sync          syncCmd          `cmd:"" help:"Copy the Teams cache into the archive once and print what changed."`
	Status        statusCmd        `cmd:"" help:"Show archive counts per account, the last sync and other Teams origins."`
	Search        searchCmd        "cmd:\"\" help:\"Full-text search over message text, sorted newest first; default --limit 50 (check `truncated`).\""
	Messages      messagesCmd      "cmd:\"\" help:\"List messages in chronological order (oldest first; with --limit, the newest matches); default --limit 50 (check `truncated`).\""
	Conversations conversationsCmd "cmd:\"\" help:\"List conversations, sorted by last activity, newest first; default --limit 50 (check `truncated`).\""
	Teams         teamsCmd         `cmd:"" help:"List teams with their channel count, last activity and unread count; the team_id or display_name is what --team takes."`
	People        peopleCmd        `cmd:"" help:"List people seen as senders or members."`
	Activity      activityCmd      `cmd:"" help:"List activity-feed items (mentions, replies, reactions) with their messages."`
	Stores        storesCmd        `cmd:"" help:"List every database and object store archived without a typed table, with record counts; the database name is what records --database takes."`
	Records       recordsCmd       `cmd:"" help:"List archived records of one database (or a prefix of its name), newest change first; value_json and key_json are parsed JSON; default --limit 50 (check truncated)."`
	Unread        unreadCmd        `cmd:"" help:"List unread messages (chats and meetings unless --include-channels), newest first; --by-conversation gives per-conversation counts."`
	Thread        threadCmd        `cmd:"" help:"Show one thread: <conversation> <root-message-id>, or a Teams message link."`
	Watch         watchCmd         `cmd:"" help:"Stream one JSON line per new, edited or deleted message or activity item as Teams writes its cache (runs until interrupted)."`
	Whoami        whoamiCmd        `cmd:"" help:"Show the accounts in the archive and the archive's state."`
	SQL           sqlCmd           `cmd:"" name:"sql" help:"Run a read-only SQL query against the archive."`
	Skill         skillCmd         `cmd:"" help:"Print the agent guide (SKILL.md) for this version, as Markdown in every output mode."`
	VersionCmd    versionCmd       `cmd:"" name:"version" help:"Print the teamscrawl version, commit and build date."`
}

// skillCmd prints the agent guide embedded in the binary. It is the documented exception to the
// JSON default (like --help): the output is raw Markdown whatever --format says.
type skillCmd struct{}

func (skillCmd) Run(rt *runtime) error {
	_, err := io.WriteString(rt.stdout, teamscrawl.Skill)
	return err
}

type versionCmd struct{}

func (versionCmd) Run(rt *runtime) error { return rt.printVersion() }

// versionFlag is --version: it prints what the version command prints and ends the parse, so it
// works without a command.
type versionFlag bool

func (versionFlag) BeforeReset(app *kong.Kong, rt *runtime) error {
	rt.exitErr = rt.printVersion() // reported by runCLI as a runtime error, not a usage error
	app.Exit(exitOK)
	return nil
}

// versionInfo is the version command's JSON document.
type versionInfo struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Date    string `json:"date"`
}

// printVersion prints one JSON document, or the human line in text mode.
func (rt *runtime) printVersion() error {
	if rt.format == output.Text {
		_, err := fmt.Fprintf(rt.stdout, "teamscrawl %s (commit %s, built %s)\n", version, commit, date)
		return err
	}
	return rt.write("version", versionInfo{Version: version, Commit: commit, Date: date})
}

// Main runs the CLI and returns the process exit code. It owns signal handling: the first SIGINT
// or SIGTERM cancels the context every lower layer cleans up on; a second one force-quits at once
// with status 130, in case that cleanup is stuck.
func Main(args []string, stdout, stderr io.Writer) int {
	ctx, cancel := context.WithCancel(context.Background())
	sigs := make(chan os.Signal, 2)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		defer close(done)
		watchSignals(sigs, cancel, os.Exit)
	}()
	defer func() {
		signal.Stop(sigs)
		close(sigs)
		<-done
		cancel()
	}()
	return runCLI(ctx, args, stdout, stderr)
}

// watchSignals cancels on the first signal and calls exit(exitForced) on the second. It returns
// when sigs is closed.
func watchSignals(sigs <-chan os.Signal, cancel func(), exit func(int)) {
	if _, ok := <-sigs; !ok {
		return
	}
	cancel()
	if _, ok := <-sigs; !ok {
		return
	}
	exit(exitForced)
}

// listCommands are the commands whose results are item lists; --fields and --max-text apply to them.
var listCommands = []string{"search", "messages", "conversations", "teams", "people", "activity", "stores", "records", "unread", "thread", "watch"}

const issuesURL = "https://github.com/ourostack/teamscrawl/issues"

// newParser builds the kong parser; a test seam for its construction error.
var newParser = kong.New

func runCLI(ctx context.Context, args []string, stdout, stderr io.Writer) (code int) {
	var app cliApp
	rt := newRuntime(ctx, &app.Globals, stdout, stderr)
	rt.format = guessFormat(args, rt.stdoutTTY) // until the real flags are validated
	// Defense in depth: no path may print a Go stack trace unasked.
	defer func() {
		if r := recover(); r != nil {
			c := errs.Internal(fmt.Errorf("unexpected failure: %v", r))
			c.Fix = "Re-run with TEAMSCRAWL_DEBUG=1 and report the output at " + issuesURL
			rt.printError(c)
			if os.Getenv("TEAMSCRAWL_DEBUG") == "1" {
				_, _ = stderr.Write(debug.Stack())
			}
			code = c.Exit
		}
	}()
	exited := false
	parser, err := newParser(&app,
		kong.Name("teamscrawl"),
		kong.Description("Mirror the Microsoft Teams desktop cache into local SQLite so agents can read Teams offline."),
		kong.Writers(stdout, stderr),
		kong.Bind(rt),
		kong.Exit(func(int) { exited = true }),
		kong.Help(func(opts kong.HelpOptions, kctx *kong.Context) error {
			// Text mode opens help with the wordmark, like every other text screen.
			if rt.format == output.Text {
				// The flag is not parsed yet when help runs, so read it from the raw args.
				rt.g.NoColor = rt.g.NoColor || contains(args, "--no-color")
				rt.color = rt.colorEnabled()
				render.Banner(kctx.Stdout, "help", rt.color)
			}
			return kong.DefaultHelpPrinter(opts, kctx)
		}),
	)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return errs.ExitRuntime
	}
	kctx, err := parser.Parse(args)
	if exited {
		if rt.exitErr != nil {
			return rt.fail(rt.exitErr)
		}
		return exitOK
	}
	if err != nil {
		var pe *kong.ParseError
		if errors.As(err, &pe) && pe.Context != nil {
			rt.cmd = commandName(pe.Context)
		}
		return rt.fail(err)
	}
	rt.cmd = commandName(kctx)
	if err := rt.setup(); err != nil {
		return rt.fail(err)
	}
	if err := rt.checkListOnly(); err != nil {
		return rt.fail(err)
	}
	if panicHook != nil {
		panicHook()
	}
	kctx.Bind(rt)
	if err := kctx.Run(); err != nil {
		return rt.fail(err)
	}
	return exitOK
}

// commandName is the command path without its argument placeholders: "search <query>" -> "search".
func commandName(k *kong.Context) string {
	var words []string
	for _, w := range strings.Fields(k.Command()) {
		if strings.HasPrefix(w, "<") || strings.HasPrefix(w, "[") {
			break
		}
		words = append(words, w)
	}
	return strings.Join(words, " ")
}

// checkListOnly rejects --fields and --max-text on commands that do not return item lists.
func (rt *runtime) checkListOnly() error {
	if (rt.g.Fields == "" && rt.g.MaxText == 0) || contains(listCommands, rt.cmd) {
		return nil
	}
	c := errs.Usage(fmt.Sprintf("--fields and --max-text apply to list commands only (%s), not %q", strings.Join(listCommands, ", "), rt.cmd))
	c.Fix = "Drop the flag, or use it with a list command, for example `teamscrawl search <query> --fields id,text`."
	return c
}

// fail prints err as a coded error and returns its exit status.
func (rt *runtime) fail(err error) int {
	var coded *errs.Coded
	if errors.Is(err, context.Canceled) {
		// SIGINT or SIGTERM cancelled the context, possibly under a wrapping coded error.
		coded = errs.Interrupted()
	} else if !errors.As(err, &coded) {
		var pe *kong.ParseError
		switch {
		case errors.As(err, &pe):
			coded = errs.Usage(pe.Error())
		default:
			coded = errs.Internal(err)
		}
	}
	if coded.Code == errs.CodeUsage && strings.HasPrefix(coded.Fix, "Run the command with --help") {
		// The generic fix becomes command-specific: name the command whose help to read.
		if rt.cmd != "" {
			coded.Fix = fmt.Sprintf("Run `teamscrawl %s --help` to see the accepted arguments and flags.", rt.cmd)
		} else {
			coded.Fix = "Run `teamscrawl --help` to see the commands and global flags."
		}
	}
	rt.printError(coded)
	return coded.Exit
}
