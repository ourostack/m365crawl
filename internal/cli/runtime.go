package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/openclaw/crawlkit/output"

	"github.com/ourostack/teamscrawl/internal/errs"
	"github.com/ourostack/teamscrawl/internal/store"
	"github.com/ourostack/teamscrawl/internal/syncer"
	"github.com/ourostack/teamscrawl/internal/teamsdesktop"
)

const defaultMaxAge = 15 * time.Minute

// runtime is what commands share: the validated globals, the output streams and the clock.
type runtime struct {
	ctx            context.Context
	g              *Globals
	stdout, stderr io.Writer
	stdoutTTY      bool
	stderrTTY      bool
	color          bool // ANSI color in text output
	format         output.Format
	cmd            string // the running command, for command-specific fixes
	maxAge         time.Duration
	fields         []string
	dbPath         string
	root           string
	account        *teamsdesktop.Account
	now            func() time.Time
}

func newRuntime(ctx context.Context, g *Globals, stdout, stderr io.Writer) *runtime {
	return &runtime{ctx: ctx, g: g, stdout: stdout, stderr: stderr, stdoutTTY: isTTY(stdout), stderrTTY: isTTY(stderr), now: time.Now}
}

// setup validates the global flags and resolves their defaults.
func (rt *runtime) setup() error {
	g := rt.g
	f := g.Format
	if f == "" && !g.JSON {
		if rt.stdoutTTY {
			f = string(output.Text)
		} else {
			f = string(output.JSON)
		}
	}
	if _, err := output.Resolve(g.Format, false); err != nil {
		return errs.Usage(err.Error() + " (use text, json or log)")
	}
	format, err := output.Resolve(f, g.JSON)
	if err != nil {
		return errs.Usage(err.Error() + " (use text, json or log)")
	}
	rt.format = format
	rt.color = rt.colorEnabled()
	maxAge := g.MaxAge
	if strings.TrimSpace(maxAge) == "" {
		rt.maxAge = defaultMaxAge
	} else if rt.maxAge, err = parseMaxAge(maxAge); err != nil {
		return errs.Usage(err.Error())
	}
	rt.fields = splitFields(g.Fields)
	if g.MaxText < 0 {
		return errs.Usage("--max-text must be 0 or more")
	}
	rt.dbPath = g.DB
	if rt.dbPath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return errs.Usage("cannot find the home directory; pass --db")
		}
		rt.dbPath = filepath.Join(home, ".teamscrawl", "teamscrawl.db")
	}
	rt.root = g.TeamsRoot
	if g.Account != "" {
		if rt.account, err = parseAccount(g.Account); err != nil {
			return err
		}
	}
	return nil
}

func parseAccount(s string) (*teamsdesktop.Account, error) {
	tenant, user, ok := strings.Cut(s, "/")
	user = strings.TrimPrefix(user, "8:orgid:")
	if !ok || tenant == "" || user == "" || strings.Contains(user, "/") {
		return nil, errs.Usage("--account must look like <tenantId>/<userId>; `teamscrawl whoami` lists them")
	}
	return &teamsdesktop.Account{TenantID: tenant, UserID: user}, nil
}

// progress is where sync progress lines go: stderr, and only when it is a terminal.
func (rt *runtime) progress() io.Writer {
	if rt.stderrTTY {
		return rt.stderr
	}
	return nil
}

// ensureFresh runs a sync first when the archive has never synced or its last successful sync is
// older than --max-age. A failed sync does not stop the read: it comes back as a warning. Only
// cancellation is returned as an error.
func (rt *runtime) ensureFresh() (*syncError, error) {
	if rt.maxAge <= 0 {
		return nil, nil
	}
	last, err := rt.lastSuccess()
	if err != nil {
		return nil, err
	}
	if !last.IsZero() && rt.now().Sub(last) <= rt.maxAge {
		return nil, nil
	}
	// The implicit sync always covers every account, so --account can never hide data from a later run.
	_, _, err = syncer.Run(rt.ctx, syncer.Options{Root: rt.root, DBPath: rt.dbPath, Progress: rt.progress()})
	if err == nil {
		return nil, nil
	}
	if rt.ctx.Err() != nil {
		return nil, rt.ctx.Err()
	}
	var coded *errs.Coded
	if !errors.As(err, &coded) {
		coded = errs.Internal(err)
	}
	rt.printWarning(coded)
	b := bodyOf(coded)
	return &syncError{Code: b.Code, Message: b.Message}, nil
}

func (rt *runtime) lastSuccess() (time.Time, error) {
	st, err := store.OpenReadOnly(rt.ctx, rt.dbPath)
	if errors.Is(err, store.ErrNoArchive) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, errs.DBError(err)
	}
	defer func() { _ = st.Close() }()
	t, err := st.LastSuccess(rt.ctx)
	if err != nil {
		return time.Time{}, errs.DBError(err)
	}
	return t, nil
}

// read runs a read command: implicit sync, open the archive read-only (nil when there is none),
// run fn, stamp the result with the archive's age and any sync error, and print it.
func (rt *runtime) read(label string, fn func(st *store.Store) (result, error)) error {
	syncErr, err := rt.ensureFresh()
	if err != nil {
		return err
	}
	st, err := store.OpenReadOnly(rt.ctx, rt.dbPath)
	switch {
	case errors.Is(err, store.ErrNoArchive):
		st = nil
	case err != nil:
		return errs.DBError(err)
	default:
		defer func() { _ = st.Close() }()
	}
	var age *int64
	if st != nil {
		last, err := st.LastSuccess(rt.ctx)
		if err != nil {
			return errs.DBError(err)
		}
		if !last.IsZero() {
			s := max(int64(rt.now().Sub(last).Seconds()), 0)
			age = &s
		}
	}
	const syncHint = "run teamscrawl sync"
	if age == nil {
		rt.hint(syncHint)
	}
	res, err := fn(st)
	if err != nil {
		return asCoded(err)
	}
	res.setMeta(age, syncErr)
	if age == nil {
		res.setNeedsSync(syncHint)
	}
	return rt.write(label, res)
}

// asCoded leaves coded and cancellation errors alone and wraps everything else as an archive error.
func asCoded(err error) error {
	var coded *errs.Coded
	if errors.As(err, &coded) || errors.Is(err, context.Canceled) {
		return err
	}
	return errs.DBError(err)
}

// since parses an optional --since/--until value.
func (rt *runtime) when(flag, val string) (time.Time, error) {
	if val == "" {
		return time.Time{}, nil
	}
	t, err := parseWhen(val, rt.now(), time.Local)
	if err != nil {
		return time.Time{}, errs.Usage(flag + ": " + err.Error())
	}
	return t, nil
}

func checkLimit(n int) error {
	if n < 1 {
		return errs.Usage("--limit must be at least 1")
	}
	return nil
}
