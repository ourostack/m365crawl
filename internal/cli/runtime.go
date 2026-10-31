package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/openclaw/crawlkit/output"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/syncer"
	"github.com/ourostack/m365crawl/internal/teamsdesktop"
)

const defaultMaxAge = 15 * time.Minute

// runtime is what commands share: the validated globals, the output streams and the clock.
type runtime struct {
	ctx              context.Context
	g                *Globals
	args             []string // the command line, for the typo suggestion of an unknown command
	stdout, stderr   io.Writer
	stdoutTTY        bool
	stderrTTY        bool
	color            bool // ANSI color in text output
	format           output.Format
	cmd              string // the running command, for command-specific fixes
	maxAge           time.Duration
	syncOff          string // what turned the implicit sync off for this read, such as "--no-sync" or "--max-age 0"
	explicitMaxAge   bool   // --max-age was given on the command line
	explicitNoSync   bool   // --no-sync was given on the command line, maybe as --no-sync=false
	syncNever        bool   // the command never syncs first by design, so its result reports no implicit_sync
	fields           []string
	dbPath           string
	root             string
	outlookRoot      string // the Outlook profiles directory; empty means the default
	outlookOn        bool
	outlookDefault   bool   // on because it is the default, not because a root was named
	outlookLink      string // --outlook-account: a Teams account as <tenantId>/<userId>, "none", or empty
	outlookProfile   string // --outlook-profile
	account          *teamsdesktop.Account
	now              func() time.Time
	exitErr          error       // a failure while printing for a flag that ends the run (--version)
	team             string      // --team of the running read command, validated before any implicit sync
	synced           *syncedInfo // the implicit sync this read ran, if any
	implicitSync     string      // implicit_sync: ran, skipped_fresh, disabled, waited_for_other_sync, other_sync_in_progress, foreign_archive or no_archive
	feedChecked      bool        // checkFeed has run for this read
	notFed           string      // foreign_archive or no_archive: this machine's sources do not feed the archive, so the read does not sync
	lockBudget       time.Duration
	lockUntil        time.Time // when this read stops waiting for other runs; set by its first wait
	query            listQuery // what the running list command asked for, to explain an empty result
	link             *convLink // the Teams link the running read's conversation came from, if any
	windowNotice     string    // set by window when the running read's time window is empty
	noteCoverage     *noteCoverage
	documentCoverage *noteCoverage
	loopCoverage     *loopCoverage
	healthSources    []string // resolved search families, independent of returned hits
	healthAccount    string   // mail ID account or calendar event principal
	healthUnknown    bool     // this implicit refresh could not persist or derive its source health

	// healthUntil is the read's upper time bound: a stale Outlook cache written at or after it hides
	// nothing the read asks for.
	healthUntil time.Time
}

// runtimeClock is every command's clock; tests pin it to make clock-dependent output repeatable.
var runtimeClock = time.Now

func newRuntime(ctx context.Context, g *Globals, stdout, stderr io.Writer) *runtime {
	return &runtime{ctx: ctx, g: g, stdout: stdout, stderr: stderr, stdoutTTY: isTTY(stdout), stderrTTY: isTTY(stderr), now: runtimeClock}
}

// setup validates the global flags and resolves their defaults.
func (rt *runtime) setup() error {
	g := rt.g
	// Validate --format even when --json wins over it.
	format, err := output.Resolve(g.Format, false)
	if err != nil {
		return errs.Usage(err.Error() + " (use text, json or log)")
	}
	if g.JSON || (g.Format == "" && !rt.stdoutTTY) {
		format = output.JSON
	}
	rt.format = format
	rt.color = rt.colorEnabled()
	maxAge := g.MaxAge
	if strings.TrimSpace(maxAge) == "" {
		rt.maxAge = defaultMaxAge
	} else if rt.maxAge, err = parseMaxAge(maxAge); err != nil {
		return errs.Usage(err.Error())
	} else if rt.maxAge == 0 {
		rt.syncOff = syncOffMaxAgeEnv
		if rt.explicitMaxAge {
			rt.syncOff = syncOffMaxAge
		}
	}
	// The flag (even --no-sync=false) beats M365CRAWL_NO_SYNC, and so does an explicit --max-age.
	if bool(g.NoSync) && (rt.explicitNoSync || !rt.explicitMaxAge) {
		rt.maxAge, rt.syncOff = 0, syncOffNoSyncEnv
		if rt.explicitNoSync {
			rt.syncOff = syncOffNoSync
		}
	}
	rt.fields = splitFields(g.Fields)
	if g.MaxText < 0 {
		return errs.Usage("--max-text must be 0 or more")
	}
	rt.dbPath = g.DB
	if rt.dbPath == "" {
		var err error
		rt.dbPath, err = defaultArchivePath()
		if err != nil {
			return err
		}
	}
	rt.root = g.TeamsRoot
	rt.outlookRoot, rt.outlookOn, rt.outlookDefault = outlookChoice(g.OutlookRoot, g.TeamsRoot)
	// A mail account is outlook/<profile>, not a Teams account: the mail commands and search take one.
	if g.Account != "" && !strings.HasPrefix(rt.cmd, "mail ") && (rt.cmd != "search" || !isSearchMailAccount(g.Account)) {
		if rt.account, err = parseAccount(g.Account); err != nil {
			return err
		}
	}
	return nil
}

// mailAccount is the Outlook account (outlook/<profile>) --account names, or "" for a Teams account
// or none. Commands other than mail and search keep it as an account with tenant "outlook", which
// is how the calendar names an Outlook profile.
func (rt *runtime) mailAccount() string {
	if rt.account == nil || rt.account.TenantID+"/" != outlookAccountPrefix {
		return ""
	}
	return outlookAccountPrefix + rt.account.UserID
}

func parseAccount(s string) (*teamsdesktop.Account, error) {
	tenant, user, ok := strings.Cut(s, "/")
	user = strings.TrimPrefix(user, "8:orgid:")
	if !ok || tenant == "" || user == "" || strings.Contains(user, "/") {
		return nil, errs.Usage("--account must look like <tenantId>/<userId>; `m365crawl whoami` lists them")
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
	if err := rt.checkFeed(); err != nil {
		return nil, err
	}
	if rt.maxAge <= 0 {
		rt.implicitSync = implicitSyncDisabled
		if rt.notFed != "" {
			rt.implicitSync = rt.notFed
		}
	}
	if rt.maxAge <= 0 && rt.outlookLink == "" {
		return nil, nil
	}
	last, pending, err := rt.lastSuccess()
	if err != nil {
		return nil, err
	}
	if rt.maxAge <= 0 {
		if !pending {
			return nil, nil
		}
		c := errs.Usage("--outlook-account is not applied while the implicit sync is off (" + rt.syncOff + ")")
		args := []string{"sync"}
		if rt.outlookProfile != "" {
			args = append(args, "--outlook-profile", rt.outlookProfile)
		}
		args = append(args, "--outlook-account", rt.outlookLink)
		c.Fix = "Run `" + rt.recoveryCommand(true, "", args...) + "` to apply it."
		return nil, c
	}
	if !last.IsZero() && rt.now().Sub(last) <= rt.maxAge && !pending {
		rt.implicitSync = implicitSyncSkippedFresh
		return nil, nil
	}
	rt.implicitSync = implicitSyncRan
	age := time.Duration(0) // zero: no complete sync yet
	if !last.IsZero() {
		age = rt.now().Sub(last)
	}
	rt.printSyncNotice(age) // stderr only; stdout stays the result
	began := rt.now()
	// The implicit sync always covers every account, so --account can never hide data from a later run.
	o := rt.linkOptions(rt.syncOptions(syncer.Options{Root: rt.root, DBPath: rt.dbPath, Progress: rt.progress()}))
	rep, _, err := runSync(rt.ctx, o)
	if isLocked(err) {
		// Another run holds the archive: wait for it rather than report `locked`.
		var ran bool
		var waitErr error
		if rep, err, ran, waitErr = rt.syncAfterOtherRun(o); waitErr != nil {
			if rt.ctx.Err() != nil || errors.Is(waitErr, context.Canceled) {
				return nil, errors.Join(rt.ctx.Err(), waitErr)
			}
			return nil, asCoded(waitErr)
		}
		if !ran {
			return nil, nil
		}
		began = rt.now()
	}
	rt.synced = &syncedInfo{Seconds: math.Round(rt.now().Sub(began).Seconds()*10) / 10, Status: rep.Status}
	rt.warnScoutRescrub(rep)
	if err == nil {
		return nil, nil
	}
	if rt.synced.Status == "" {
		rt.synced.Status = syncer.StatusFailed
	}
	if rt.ctx.Err() != nil || errors.Is(err, context.Canceled) {
		return nil, errors.Join(rt.ctx.Err(), err)
	}
	var coded *errs.Coded
	if !errors.As(err, &coded) {
		coded = errs.Internal(err)
	}
	rt.healthUnknown = coded.Code == errs.CodeDBError && rep.FailedSourceHealth == nil
	coded = withSnapshotRetention(err, coded)
	if coded.Exit == errs.ExitUsage {
		return nil, coded // the link the operator asked for was refused: that is theirs to fix, not a warning
	}
	rt.printWarning(coded)
	b := bodyOf(coded)
	return &syncError{Code: b.Code, Message: b.Message}, nil
}

// lastSuccess is when the archive was last fully synced for this read, and whether an
// --outlook-account link is still waiting for a sync to apply it.
func (rt *runtime) lastSuccess() (last time.Time, pending bool, err error) {
	st, err := store.OpenReadOnly(rt.ctx, rt.dbPath)
	if errors.Is(err, store.ErrNoArchive) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, errs.DBError(err)
	}
	defer func() { _ = st.Close() }()
	last, err = rt.freshness(st)
	return last, rt.linkPending(st), err
}

// freshness is when the accounts this read covers were last fully synced: --account's own time, or
// the stalest account's for a read of every account. A partial or failed sync does not count.
func (rt *runtime) freshness(st *store.Store) (time.Time, error) {
	var t time.Time
	var err error
	if a := rt.account; a != nil {
		t, err = st.LastSuccessFor(rt.ctx, a.TenantID+"/"+a.UserID)
	} else {
		t, err = st.LastSuccess(rt.ctx)
	}
	if err != nil {
		return time.Time{}, errs.DBError(err)
	}
	return t, nil
}

// checkTeam rejects an unknown or ambiguous --team before the implicit sync spends time on it. An
// archive that does not exist yet cannot say, so the check is repeated after the sync.
func (rt *runtime) checkTeam() error {
	if rt.team == "" {
		return nil
	}
	st, err := store.OpenReadOnly(rt.ctx, rt.dbPath)
	if errors.Is(err, store.ErrNoArchive) {
		return nil
	}
	if err != nil {
		return errs.DBError(err)
	}
	defer func() { _ = st.Close() }()
	if err := st.CheckTeam(rt.ctx, rt.account, rt.team); err != nil {
		return asCoded(err)
	}
	return nil
}

// read runs a read command: implicit sync, open the archive read-only (nil when there is none),
// run fn, stamp the result with the archive's age and any sync error, and print it.
func (rt *runtime) read(label string, fn func(st *store.Store) (result, error)) error {
	// whoami and status are how a bad setup is looked at, so a stale root never stops them; status
	// reports it as data.
	bad := rt.checkOutlookRoot()
	if bad != nil && label != "status" && label != "whoami" {
		return bad
	}
	if err := rt.checkTeam(); err != nil {
		return err
	}
	syncErr, err := rt.ensureFresh()
	if err != nil {
		return err
	}
	st, err := store.OpenReadOnly(rt.ctx, rt.dbPath)
	switch {
	case errors.Is(err, store.ErrNoArchive):
		if rt.team != "" {
			return store.NoTeam(rt.team)
		}
		st = nil
	case err != nil:
		return errs.DBError(err)
	default:
		defer func() { _ = st.Close() }()
	}
	var age *int64
	if st != nil {
		last, err := rt.freshness(st)
		if err != nil {
			return err
		}
		if !last.IsZero() {
			s := max(int64(rt.now().Sub(last).Seconds()), 0)
			age = &s
		}
	}
	syncHint := "run " + rt.recoverySyncCommand(st)
	if age == nil && rt.format == output.Text {
		rt.hint(syncHint) // JSON mode carries the same hint in the result (needs_sync, hint)
	}
	res, err := fn(st)
	if err != nil {
		return asCoded(err)
	}
	if err := rt.noteEmpty(st, res); err != nil {
		return asCoded(err)
	}
	if sr, ok := res.(*statusResult); ok && bad != nil {
		b := bodyOf(bad)
		sr.Outlook = &b
	}
	res.setMeta(age, syncErr)
	if rt.syncNever {
		rt.implicitSync = ""
	}
	res.setSynced(rt.synced)
	res.setImplicitSync(rt.implicitSync)
	if rt.implicitSync == implicitSyncOtherInProgress {
		res.addNotice(rt.otherSyncNotice())
	}
	rt.noteNotFed(res)
	if age == nil {
		res.setNeedsSync(syncHint)
	} else if rt.implicitSync == implicitSyncDisabled {
		rt.noteSyncOff(res, time.Duration(*age)*time.Second, syncHint)
	}
	if err := rt.noteSourceHealth(st, label, res, age == nil); err != nil {
		return asCoded(err)
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

// The values of implicit_sync: what the implicit sync did before this read answered.
const (
	implicitSyncRan          = "ran"           // the archive was older than --max-age, so the read synced first
	implicitSyncSkippedFresh = "skipped_fresh" // the archive was younger than --max-age
	implicitSyncDisabled     = "disabled"      // --no-sync or --max-age 0: the archive was read as it is
)

// What can turn the implicit sync off. The notice names the one in effect, so an agent is told to
// undo what it (or its environment) actually set.
const (
	syncOffNoSync    = "--no-sync"
	syncOffNoSyncEnv = "M365CRAWL_NO_SYNC"
	syncOffMaxAge    = "--max-age 0"
	syncOffMaxAgeEnv = "M365CRAWL_MAX_AGE=0"
)

// syncOffUndo is how to turn the implicit sync back on, and whether the cause is a zero max age,
// which agents read as "nothing cached is fresh enough".
func (rt *runtime) syncOffUndo() (undo string, zeroAge bool) {
	switch rt.syncOff {
	case syncOffMaxAge:
		return "drop --max-age 0", true
	case syncOffMaxAgeEnv:
		return "unset M365CRAWL_MAX_AGE or pass a positive --max-age", true
	case syncOffNoSyncEnv:
		return "unset M365CRAWL_NO_SYNC or pass --no-sync=false", false
	}
	return "drop --no-sync", false
}

// noteSyncOff cautions a read that skipped its implicit sync, so an empty or short answer from an
// old archive cannot pass as fresh. --no-sync says so once the archive is older than the default
// max age; a zero max age always does, because agents read it as "nothing cached is fresh enough".
// syncHint is "run <the sync command>".
func (rt *runtime) noteSyncOff(res result, age time.Duration, syncHint string) {
	undo, zeroAge := rt.syncOffUndo()
	if !zeroAge && age <= defaultMaxAge {
		return
	}
	run := strings.TrimPrefix(syncHint, "run ")
	if strings.HasPrefix(run, "no executable") {
		run = "m365crawl sync"
	}
	old := "the archive is " + compactDuration(roundAge(age)) + " old"
	if zeroAge {
		res.addNotice("This read did not sync: " + rt.syncOff + " turns the implicit sync off, it does not mean fresh, and " + old +
			", so newer items may be missing. For fresh data run `" + run + "` first or " + undo + "; pass --no-sync to read the archive as it is.")
	} else {
		res.addNotice("This read did not sync (" + rt.syncOff + ") and " + old + ", so newer items may be missing. For fresh data run `" + run + "` first or " + undo + ".")
	}
	if rt.format != output.Text {
		return // JSON and log results carry the notice; their stderr stays for warnings
	}
	hint := "this read did not sync; " + old + ". Run `" + run + "` first for fresh data, or " + undo
	if zeroAge {
		hint += " (" + rt.syncOff + " turns syncing off; --no-sync reads as is)"
	}
	rt.hint(hint)
}

// noteRegisteredSyncOff marks a registered-source read (notes, documents, Loop, native people)
// whose refresh --no-sync or a zero max age skipped: its coverage object cannot tell skipped from
// not due, so the result always says implicit_sync: disabled. The notice is added only when it can
// matter: never with no registered source; always for a zero max age, which agents read as "fresh";
// and for --no-sync when the oldest selected source was last attempted more than the default max
// age ago (a source never attempted counts as old as the Teams archive, or stale when that is
// unknown). attempts are the selected sources' attempted_at.
func (rt *runtime) noteRegisteredSyncOff(res result, what string, attempts []*time.Time) {
	if rt.notFed != "" {
		rt.noteNotFed(res)
		return
	}
	if rt.syncOff == "" {
		return
	}
	res.setImplicitSync(implicitSyncDisabled)
	if len(attempts) == 0 {
		return
	}
	undo, zeroAge := rt.syncOffUndo()
	notice := "This read did not refresh its registered " + what + " sources: " + rt.syncOff +
		" turns the refresh off, it does not mean fresh, so newer items may be missing. For fresh data " + undo + "; pass --no-sync to read the archive as it is."
	if !zeroAge {
		oldest, known := rt.oldestAttempt(attempts)
		if known && rt.now().Sub(oldest) <= defaultMaxAge {
			return
		}
		// An absolute time, not an age: two reads of the same archive print the same caution.
		read := "the oldest has not been read since the archive's last complete sync"
		if known {
			read = "the oldest was last read at " + oldest.UTC().Format(time.RFC3339)
		}
		notice = "This read did not refresh its registered " + what + " sources (" + rt.syncOff + ") and " + read + ", so newer items may be missing. For fresh data " + undo + "."
	}
	res.addNotice(notice)
	if rt.format == output.Text {
		rt.hint("this read did not refresh its registered " + what + " sources (" + rt.syncOff + "); for fresh data " + undo)
	}
}

// oldestAttempt is when the stalest of attempts was made. A source never attempted counts as
// read at the archive's last complete sync; known is false when that is unknown too.
func (rt *runtime) oldestAttempt(attempts []*time.Time) (oldest time.Time, known bool) {
	for _, at := range attempts {
		if at == nil {
			last := rt.lastCompleteSync()
			if last.IsZero() {
				return time.Time{}, false
			}
			at = &last
		}
		if oldest.IsZero() || at.Before(oldest) {
			oldest = *at
		}
	}
	return oldest, true
}

// lastCompleteSync is when the accounts this read covers were last fully synced, or zero when the
// archive is missing or cannot say.
func (rt *runtime) lastCompleteSync() time.Time {
	st, err := store.OpenReadOnly(rt.ctx, rt.dbPath)
	if err != nil {
		return time.Time{}
	}
	defer func() { _ = st.Close() }()
	last, _ := rt.freshness(st)
	return last
}

// sourceAttempts are the attempted_at of a coverage object's sources.
func sourceAttempts(coverage *noteCoverage) []*time.Time {
	out := []*time.Time{}
	for _, source := range coverage.Sources {
		out = append(out, source.AttemptedAt)
	}
	return out
}

// roundAge drops the parts of an archive age a person does not need: seconds once it is a minute.
func roundAge(age time.Duration) time.Duration {
	if age >= time.Minute {
		return age.Truncate(time.Minute)
	}
	return age.Truncate(time.Second)
}

// syncNotice is the line a read prints to stderr before its implicit sync, so a wait of several
// seconds is not silent. An age of zero means the archive has no complete sync yet.
func syncNotice(age, maxAge time.Duration) string {
	why := "no complete sync yet"
	if age > 0 {
		why = "archive is " + compactDuration(roundAge(age)) + " old"
	}
	return "m365crawl: syncing — " + why + " (max-age " + compactDuration(maxAge) + ")"
}

// compactDuration is d without the zero parts Go's String keeps: 15m, 2h14m, 1m30s.
func compactDuration(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = s[:len(s)-2]
	}
	if strings.HasSuffix(s, "h0m") {
		s = s[:len(s)-2]
	}
	return s
}

// syncNoticeDoc is the JSON form of the notice: one line on stderr in json and log modes.
type syncNoticeDoc struct {
	Notice            string `json:"notice"`
	Reason            string `json:"reason"` // stale or never_synced
	ArchiveAgeSeconds *int64 `json:"archive_age_seconds,omitempty"`
	MaxAgeSeconds     int64  `json:"max_age_seconds"`
}

// printSyncNotice tells stderr an implicit sync is starting: a plain line in text mode, one JSON
// line otherwise so every stderr line of a JSON run stays machine-readable. age is zero when the
// archive has no complete sync yet.
func (rt *runtime) printSyncNotice(age time.Duration) {
	if rt.format == output.Text {
		_, _ = fmt.Fprintln(rt.stderr, syncNotice(age, rt.maxAge))
		return
	}
	doc := syncNoticeDoc{Notice: "syncing", Reason: "never_synced", MaxAgeSeconds: int64(rt.maxAge.Seconds())}
	if age > 0 {
		secs := int64(age.Seconds())
		doc.Reason, doc.ArchiveAgeSeconds = "stale", &secs
	}
	rt.writeJSONLine(doc)
}

// outlookChoice says whether the Outlook source runs, where it reads and whether it is on only by
// default. An explicit root turns it on and "none" turns it off. With neither it is on and reads the
// default directory, unless a Teams root is set: a run pointed at a Teams fixture never reads a real
// Outlook profile. M365CRAWL_OUTLOOK=1, which used to turn Outlook on, is read by nothing now: it is accepted and has no effect, so a machine that set it keeps working. A default Outlook is a best effort that never fails a sync (syncer.Options.OutlookImplicit).
func outlookChoice(root, teamsRoot string) (dir string, on, byDefault bool) {
	switch {
	case root == "none":
		return "", false, false
	case root != "":
		return root, true, false
	}
	return "", teamsRoot == "", teamsRoot == ""
}

// CodeOutlookRootMissing is the failure of an Outlook root that is not a directory.
const CodeOutlookRootMissing = "outlook_root_missing"

// checkOutlookRoot refuses an Outlook root that is not a directory, the same way on every command
// that takes --outlook-root, before any sync starts. It does nothing while the Outlook source is
// off, and nothing while it is on only by default: a default Outlook that is not there is not an
// error. A root that exists but holds no profiles is the sync's own failure (no_outlook_profiles).
func (rt *runtime) checkOutlookRoot() *errs.Coded {
	if !rt.outlookOn || rt.outlookDefault {
		return nil
	}
	root, how := rt.outlookRoot, "--outlook-root "
	info, err := os.Stat(root)
	if err == nil && info.IsDir() {
		return nil
	}
	c := &errs.Coded{Code: CodeOutlookRootMissing, Exit: errs.ExitEnvironment}
	c.Message = how + root + " is not a directory"
	if err != nil {
		c.Message = how + root + " cannot be read: " + oneLine(err.Error())
	}
	c.Fix = "Point --outlook-root at the Windows Olk EBWebView directory containing Outlook IndexedDB profiles, or the Mac profiles directory containing HxStore.hxd; turn the Outlook source off with --outlook-root none."
	return c
}

// noteOutlookOff adds the notice that Outlook events in a result are archived data this run did not
// refresh, with the age of the last read of each Outlook account. has says the result holds an
// Outlook event; with the Outlook source on the sync keeps them fresh and there is nothing to say.
// A read time that cannot be loaded leaves the ages out; the notice is still given.
func (rt *runtime) noteOutlookOff(st *store.Store, res result, has bool) {
	if !has || rt.outlookOn {
		return
	}
	reads, _ := outlookReadTimes(st, rt.ctx)
	accounts := make([]string, 0, len(reads))
	for a := range reads {
		accounts = append(accounts, a)
	}
	sort.Strings(accounts)
	var ages []string
	for _, a := range accounts {
		ages = append(ages, a+" "+ageText(rt.now().Sub(reads[a])))
	}
	msg := "the Outlook source is off for this run (--outlook-root none, or --teams-root without --outlook-root), so the Outlook events in this result are archived data that is not being refreshed"
	if len(ages) > 0 {
		msg += "; last read " + strings.Join(ages, ", ")
	}
	res.addNotice(msg + ". Name --outlook-root DIR (and not none) to read Outlook again.")
}

// outlookReadTimes is the test seam of the read times.
var outlookReadTimes = (*store.Store).OutlookReadTimes

// ageText is a coarse age: minutes under an hour, hours under two days, days after that.
func ageText(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return strconv.Itoa(int(d/time.Minute)) + " min ago"
	case d < 48*time.Hour:
		return strconv.Itoa(int(d/time.Hour)) + " h ago"
	}
	return strconv.Itoa(int(d/(24*time.Hour))) + " days ago"
}

// outlookAccountEnv is the environment variable of --outlook-account.
const outlookAccountEnv = "M365CRAWL_OUTLOOK_ACCOUNT"

// checkLink validates --outlook-account and --outlook-profile and, when they are good, turns the
// link on for this run. Only a command that syncs or reads the calendar calls it, so a stray
// variable never breaks whoami, status or version. The account is a Teams account in the form of
// --account, or "none". A link needs the Outlook source on: with the source off, a value that came
// from the environment alone is a warning (it is the ambient setting of a machine that sometimes
// runs without Outlook) and a flag is a usage error. A profile name means nothing without a link.
func (rt *runtime) checkLink() error {
	account, profile := rt.g.OutlookAccount, rt.g.OutlookProfile
	switch {
	case account == "" && profile == "":
		return nil
	case account == "":
		return errs.Usage("--outlook-profile names the profile --outlook-account applies to; give --outlook-account too")
	case !rt.outlookOn:
		c := errs.Usage("--outlook-account needs the Outlook source, which is off")
		c.Fix = "Add --outlook-root DIR (Outlook is off here because of --outlook-root none, or because --teams-root is set without --outlook-root)."
		if os.Getenv(outlookAccountEnv) == account {
			rt.printWarning(c) // from the environment only: ignored
			return nil
		}
		return c
	case account == syncer.OutlookLinkNone:
		rt.outlookLink, rt.outlookProfile = account, profile
		return nil
	}
	a, err := parseAccount(account)
	if err != nil {
		c := errs.Usage("--outlook-account must be <tenantId>/<userId> of a Teams account, or none")
		c.Fix = "`m365crawl whoami` lists the Teams accounts."
		return c
	}
	rt.outlookLink, rt.outlookProfile = a.TenantID+"/"+a.UserID, profile
	return nil
}

// linkOptions adds the explicit Outlook link to the options of a sync that applies it.
func (rt *runtime) linkOptions(o syncer.Options) syncer.Options {
	o.OutlookLink, o.OutlookLinkProfile = rt.outlookLink, rt.outlookProfile
	return o
}

// linkPending says whether --outlook-account still has to be applied by a sync: the archive does
// not show the link as asked for. An archive that cannot say (no calendar tables) counts as
// pending.
func (rt *runtime) linkPending(st *store.Store) bool {
	if rt.outlookLink == "" {
		return false
	}
	account, principal := "", rt.outlookLink
	if rt.outlookProfile != "" {
		account = "outlook/" + rt.outlookProfile
	}
	if principal == syncer.OutlookLinkNone {
		principal = ""
	}
	ok, err := st.OutlookLinkInEffect(rt.ctx, account, principal)
	return err != nil || !ok
}

// syncOptions adds the Outlook choice to a run's options.
func (rt *runtime) syncOptions(o syncer.Options) syncer.Options {
	o.OutlookEnabled, o.OutlookRoot, o.OutlookImplicit = rt.outlookOn, rt.outlookRoot, rt.outlookDefault
	o.SourceIdentity = rt.sourceIdentity(true)
	return o
}
