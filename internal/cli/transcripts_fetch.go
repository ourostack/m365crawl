package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/openclaw/crawlkit/output"

	"github.com/ourostack/m365crawl/internal/browser"
	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/render"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/transcripts"
)

// Test seams of the two commands that run a browser.
var (
	// launchBrowser starts the browser; tests replace it to reach the failures of the tab.
	launchBrowser = func(ctx context.Context, o browser.LaunchOptions) (launchedBrowser, error) {
		b, err := browser.Launch(ctx, o)
		if err != nil {
			return nil, err
		}
		return b, nil
	}
	// openBrowser launches the browser and attaches to its tab. The caller owns the Closer and
	// closes it on every path; on an error nothing is left running.
	openBrowser = func(ctx context.Context, o browser.LaunchOptions) (transcripts.PageDriver, io.Closer, error) {
		b, err := launchBrowser(ctx, o)
		if err != nil {
			return nil, nil, err
		}
		p, err := b.Page(ctx)
		if err != nil {
			_ = b.Close()
			return nil, nil, errs.BrowserFailed("could not open the browser tab: " + err.Error())
		}
		return p, b, nil
	}
	transcriptsNow              = time.Now
	fetchLandTimeout            = transcripts.DefaultLandTimeout
	fetchLockPoll               = time.Second
	fetchLockWait               = transcripts.DefaultLockWait
	signinHosts                 = (*store.Store).TranscriptHosts
	openArchive                 = store.Open
	stdinIsTTY                  = func() bool { return isTTY(os.Stdin) }
	stdinReader       io.Reader = os.Stdin
	signinWait                  = 5 * time.Minute
	signinPoll                  = time.Second
	signinCallTimeout           = 10 * time.Second // each question to the tab; a hung one is asked again
)

const (
	maxFetchLimit = 200
	sourceNetwork = "network"
	stateLocal    = "local"
)

// launchedBrowser is a running browser: *browser.Browser.
type launchedBrowser interface {
	Page(ctx context.Context) (*browser.Page, error)
	Close() error
}

// progressLine prints one step of a long-running command on stderr: a plain line in text mode, a
// JSON line otherwise, so every stderr line stays machine-readable. It never carries content.
func (rt *runtime) progressLine(msg string) {
	if rt.format == output.Text {
		_, _ = fmt.Fprintf(rt.stderr, "m365crawl: %s\n", msg)
		return
	}
	rt.writeJSONLine(struct {
		Progress string `json:"progress"`
	}{msg})
}

// browserName is how a step line names the browser.
func browserName(k browser.Kind) string {
	switch k {
	case browser.KindEdge:
		return "Edge"
	case browser.KindChrome:
		return "Chrome"
	}
	return "browser"
}

// ---- transcripts fetch ----

type transcriptsFetchCmd struct {
	Meeting string        `arg:"" optional:"" help:"The meeting to fetch: an event id or key, a meeting chat link or thread id, or a call id."`
	Since   string        `help:"Instead of <meeting>: every recorded meeting that started at or after this time (YYYY-MM-DD, RFC3339 or an age such as 7d) and has parts not fetched yet." placeholder:"DATE"`
	Limit   int           `default:"20" help:"Maximum meetings to fetch (at most 200); truncated says whether more were left." placeholder:"N"`
	Refetch bool          `help:"Also fetch parts whose text is already in the archive."`
	Browser string        `env:"M365CRAWL_BROWSER" help:"The browser to run: edge, chrome or the path of an Edge or Chrome executable. Default: Edge, then Chrome." placeholder:"edge|chrome|PATH"`
	Timeout time.Duration `default:"10m" help:"Stop the whole fetch after this long (for example 10m)." placeholder:"DURATION"`
}

// Help is the long help of transcripts fetch.
func (transcriptsFetchCmd) Help() string {
	return transcriptsSeeGroup + "\nPass exactly one of <meeting> or --since. Parts whose text is already in the archive are not fetched again (state local, with their fetched_at) unless --refetch; parts that cannot be fetched are listed with their reason and never sent. With nothing left to fetch no browser starts." +
		"\nThe browser runs headless with its own profile next to the archive, opens each SharePoint site once and fetches each part inside the page, so m365crawl never handles a password, token or download link. Progress lines on stderr carry counts and states only." +
		"\nA part SharePoint refuses (no_access, not_found, ...) is stored with its state and the others still fetch; the command exits 0 with a note. A profile that is not signed in stops with transcripts_signin_required. " + errs.SigninRequired().Fix
}

// fetchPartItem is one part of a fetch result. state is local for text already in the archive,
// unfetchable for a part no fetch can ask for, and otherwise this run's outcome.
type fetchPartItem struct {
	Ordinal    int        `json:"ordinal"`
	State      string     `json:"state"`
	HTTPStatus *int       `json:"http_status"`
	Entries    int        `json:"entries"`
	FetchedAt  *time.Time `json:"fetched_at"`
	Reason     *string    `json:"reason"`
}

type fetchCallItem struct {
	CallID string          `json:"call_id"`
	Parts  []fetchPartItem `json:"parts"`
}

// transcriptFetchResult is the document of transcripts fetch. source is network when a browser
// ran and archive when everything came from the archive.
type transcriptFetchResult struct {
	Source      string          `json:"source"`
	FetchedAt   *time.Time      `json:"fetched_at"`
	Browser     *string         `json:"browser"`
	Calls       []fetchCallItem `json:"calls"`
	Fetched     int             `json:"fetched"`
	Local       int             `json:"local"`
	Failed      int             `json:"failed"`
	Unfetchable int             `json:"unfetchable"`
	Truncated   bool            `json:"truncated"`
	Next        string          `json:"next"`
	meta
}

func (c *transcriptsFetchCmd) check(rt *runtime) (since time.Time, ref string, err error) {
	if (c.Meeting == "") == (c.Since == "") {
		u := errs.Usage("transcripts fetch takes exactly one of <meeting> or --since")
		u.Fix = "Run `m365crawl transcripts fetch <meeting>` for one meeting, or `m365crawl transcripts fetch --since 7d` for every recent one."
		return since, "", u
	}
	if err := checkLimit(c.Limit); err != nil {
		return since, "", err
	}
	if c.Limit > maxFetchLimit {
		u := errs.Usage(fmt.Sprintf("--limit %d is more than transcripts fetch takes at once", c.Limit))
		u.Fix = fmt.Sprintf("Use --limit %d or less, and run the fetch again for the rest.", maxFetchLimit)
		return since, "", u
	}
	if c.Timeout <= 0 {
		return since, "", errs.Usage("--timeout must be more than 0, for example 10m")
	}
	if since, err = rt.when("--since", c.Since); err != nil {
		return since, "", err
	}
	if c.Meeting == "" {
		return since, "", nil
	}
	ref, err = meetingRef(c.Meeting)
	return since, ref, err
}

func (c *transcriptsFetchCmd) Run(rt *runtime) error {
	since, ref, err := c.check(rt)
	if err != nil {
		return err
	}
	rt.maxAge = 0 // a fetch reads the parts the archive holds; it never syncs first
	return rt.read("transcripts fetch", func(st *store.Store) (result, error) {
		res := &transcriptFetchResult{Source: sourceArchive, Calls: []fetchCallItem{}}
		if st == nil {
			res.Note = "no archive yet: run m365crawl sync"
			return res, nil
		}
		if ok, err := st.HasTranscriptTables(rt.ctx); err != nil || !ok {
			res.Note = noTranscriptTables
			res.setNeedsSync(noTranscriptTables)
			return res, err
		}
		calls, err := c.calls(rt, st, since, ref, res)
		if err != nil {
			return nil, err
		}
		var todo []transcripts.Part
		for _, call := range calls {
			for _, p := range call.Parts {
				if p.Fetchable && (c.Refetch || !p.HasText()) {
					todo = append(todo, p.Part)
				}
			}
		}
		var sum transcripts.FetchSummary
		if len(todo) > 0 {
			if sum, err = c.fetch(rt, todo, res); err != nil {
				return nil, err
			}
		}
		res.fill(calls, sum, c.Refetch)
		c.finish(res, len(todo))
		return res, nil
	})
}

// calls are the recorded calls this fetch covers: the meeting's, or those since --since that have
// a part to fetch, newest first, at most --limit.
func (c *transcriptsFetchCmd) calls(rt *runtime, st *store.Store, since time.Time, ref string, res *transcriptFetchResult) ([]store.TranscriptCall, error) {
	f := store.TranscriptFilter{Account: rt.account, Since: since}
	if ref != "" {
		ids, _, err := st.ResolveMeeting(rt.ctx, rt.account, ref)
		if err != nil {
			return nil, err
		}
		f.Calls = ids
	}
	all, _, err := transcriptCallsOf(st, rt.ctx, f)
	if err != nil {
		return nil, err
	}
	var out []store.TranscriptCall
	for _, call := range all {
		if ref == "" && !c.hasWork(call) {
			continue
		}
		if len(out) == c.Limit {
			res.Truncated = true
			break
		}
		out = append(out, call)
	}
	return out, nil
}

func (c *transcriptsFetchCmd) hasWork(call store.TranscriptCall) bool {
	for _, p := range call.Parts {
		if p.Fetchable && (c.Refetch || !p.HasText()) {
			return true
		}
	}
	return false
}

// fetch runs the browser over the parts. It owns the browser: Close runs on every way out.
func (c *transcriptsFetchCmd) fetch(rt *runtime, parts []transcripts.Part, res *transcriptFetchResult) (transcripts.FetchSummary, error) {
	exe, kind, err := findBrowser(c.Browser)
	if err != nil {
		return transcripts.FetchSummary{}, err
	}
	ctx, cancel := context.WithTimeout(rt.ctx, c.Timeout)
	defer cancel()
	now := transcriptsNow()
	rt.progressLine(fmt.Sprintf("starting a headless %s to fetch %s", browserName(kind), plural(len(parts), "part", "parts")))
	page, closer, err := openBrowser(ctx, browser.LaunchOptions{Exe: exe, Kind: kind, Profile: browser.ProfileDir(rt.dbPath), Headless: true})
	if err != nil {
		return transcripts.FetchSummary{}, c.runErr(rt, ctx, err)
	}
	closed := false
	defer func() {
		if !closed { // a panic: the browser still ends
			_ = closer.Close()
		}
	}()
	f := &transcripts.Fetcher{Page: page, Browser: string(kind), Now: transcriptsNow, LandTimeout: fetchLandTimeout, LockWait: fetchLockWait, LockPoll: fetchLockPoll,
		Progress: rt.progressLine, Gone: func(err error) bool { return errors.Is(err, browser.ErrDisconnected) }}
	sum, ferr := f.Fetch(ctx, parts, rt.saveFetched)
	closed = true
	_ = closer.Close()
	if rt.ctx.Err() != nil {
		return sum, rt.ctx.Err()
	}
	// Results that met a busy archive are saved now, with the browser already gone.
	sum, serr := f.FinishSaves(rt.ctx)
	if serr != nil {
		return sum, serr
	}
	if ferr != nil {
		return sum, c.runErr(rt, ctx, ferr)
	}
	b := string(kind)
	res.Source, res.FetchedAt, res.Browser = sourceNetwork, &now, &b
	return sum, nil
}

// runErr turns a stopped run into its error: a signal is interrupted, the end of --timeout is a
// browser failure that says to raise it.
func (c *transcriptsFetchCmd) runErr(rt *runtime, ctx context.Context, err error) error {
	if rt.ctx.Err() != nil {
		return rt.ctx.Err()
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		b := errs.BrowserFailed("the fetch did not finish within " + compactDuration(c.Timeout))
		b.Fix = "Run the fetch again: parts already fetched are kept. Raise --timeout, or fetch fewer meetings at once with --limit."
		return b
	}
	return err
}

// saveFetched stores one part's result under the archive's run lock, taken for this save only, so
// a sync is never blocked for the length of a fetch.
func (rt *runtime) saveFetched(p transcripts.Part, r transcripts.FetchResult) error {
	release, err := store.AcquireLock(rt.dbPath)
	if err != nil {
		return err
	}
	defer release()
	st, err := openArchive(rt.ctx, rt.dbPath)
	if err != nil {
		return asCoded(err)
	}
	defer func() { _ = st.Close() }()
	if err := st.SaveTranscript(rt.ctx, p.AccountID, p.PartKey, r); err != nil {
		return asCoded(err)
	}
	return nil
}

// fill lists every part of the calls: local, unfetchable, or what this run did.
func (res *transcriptFetchResult) fill(calls []store.TranscriptCall, sum transcripts.FetchSummary, refetch bool) {
	ran := map[string]transcripts.PartOutcome{}
	for _, o := range sum.Parts {
		ran[o.Part.AccountID+"\x00"+o.Part.PartKey] = o
	}
	for _, call := range calls {
		item := fetchCallItem{CallID: call.CallID, Parts: []fetchPartItem{}}
		for _, p := range call.Parts {
			out := fetchPartItem{Ordinal: p.Ordinal}
			o, did := ran[p.AccountID+"\x00"+p.PartKey]
			switch {
			case !p.Fetchable:
				out.State, out.Reason = transcripts.StateUnfetchable, nz(p.Reason)
				res.Unfetchable++
			case did:
				out.State, out.Entries, out.Reason = o.State, o.Entries, nz(o.Reason)
				if o.HTTPStatus != 0 {
					out.HTTPStatus = &o.HTTPStatus
				}
				if o.State == transcripts.StateOK {
					out.FetchedAt = tp(o.FetchedAt)
					res.Fetched++
				} else {
					res.Failed++
				}
			case p.HasText() && !refetch:
				out.State, out.Entries, out.FetchedAt = stateLocal, p.Fetch.EntryCount, p.Fetch.FetchedAt
				if p.Fetch.HTTPStatus != 0 {
					out.HTTPStatus = &p.Fetch.HTTPStatus
				}
				res.Local++
			}
			item.Parts = append(item.Parts, out)
		}
		res.Calls = append(res.Calls, item)
	}
}

// finish sets the note and the next command.
func (c *transcriptsFetchCmd) finish(res *transcriptFetchResult, sent int) {
	switch {
	case sent == 0 && len(res.Calls) == 0:
		res.Note = "nothing to fetch: no recorded meeting since then has a part that is not fetched yet"
	case sent == 0 && res.Local == 0:
		res.Note = "nothing to fetch: no part of this meeting can be fetched; each part's reason says why"
	case sent == 0:
		res.Note = "nothing to fetch: every part that can be fetched is already in the archive"
	case res.Failed == 1:
		res.Note = "1 part could not be fetched; its reason is listed with it"
	case res.Failed > 1:
		res.Note = fmt.Sprintf("%d parts could not be fetched; each reason is listed with its part", res.Failed)
	}
	switch {
	case c.Meeting != "":
		res.Next = "m365crawl transcripts show " + shellWord(c.Meeting)
	case len(res.Calls) == 1:
		res.Next = "m365crawl transcripts show " + res.Calls[0].CallID
	default:
		res.Next = "m365crawl transcripts show <call-id>"
	}
}

func (res *transcriptFetchResult) renderTranscripts(rt *runtime) {
	w := rt.stdout
	var rows [][]string
	for _, call := range res.Calls {
		for _, p := range call.Parts {
			entries, when := "", ""
			if p.State == transcripts.StateOK || p.State == stateLocal {
				entries = strconv.Itoa(p.Entries)
			}
			if p.FetchedAt != nil {
				when = stamp(*p.FetchedAt)
			}
			rows = append(rows, []string{call.CallID, strconv.Itoa(p.Ordinal), p.State, entries, when, sv(p.Reason)})
		}
	}
	if len(rows) > 0 {
		render.Table(w, []string{"call", "part", "state", "entries", "fetched", "why no text"}, rows, rt.color)
		_, _ = fmt.Fprintln(w)
	}
	more := ""
	if res.Truncated {
		more = " (more meetings were left; raise --limit)"
	}
	_, _ = fmt.Fprintf(w, "%s\n", render.Dim(fmt.Sprintf("%d fetched · %d local · %d failed · %d unfetchable%s", res.Fetched, res.Local, res.Failed, res.Unfetchable, more), rt.color))
	metaLines(w, res.meta, rt.color)
	_, _ = fmt.Fprintf(w, "next: %s\n", res.Next)
	src := "source: " + res.Source
	if res.FetchedAt != nil {
		src += " · fetched " + stamp(*res.FetchedAt)
	}
	_, _ = fmt.Fprintf(w, "%s\n", render.Dim(src, rt.color))
}

// ---- transcripts signin ----

type transcriptsSigninCmd struct {
	Host       string `help:"The SharePoint host to sign in to, such as <tenant>.sharepoint.com. Default: the host that holds most of the archive's transcript parts." placeholder:"HOST"`
	Browser    string `env:"M365CRAWL_BROWSER" help:"The browser to run: edge, chrome or the path of an Edge or Chrome executable. Default: Edge, then Chrome." placeholder:"edge|chrome|PATH"`
	UserAgreed bool   `name:"user-agreed" help:"The user has agreed to the visible window. Required when stdin is not a terminal (an agent runs the command); on a terminal the command asks and waits for Enter instead."`
}

// Help is the long help of transcripts signin.
func (transcriptsSigninCmd) Help() string {
	return errs.SigninRequired().Fix +
		"\nThe window uses m365crawl's own browser profile, next to the archive, never your everyday browser profile. Sign in there once (choose to stay signed in); the command waits up to 5 minutes, then closes the window. Each step is printed on stderr."
}

// signinResult is the document of transcripts signin.
type signinResult struct {
	SignedIn bool   `json:"signed_in"`
	Browser  string `json:"browser"`
	Next     string `json:"next"`
}

func (r *signinResult) renderTranscripts(rt *runtime) {
	_, _ = fmt.Fprintf(rt.stdout, "signed in; run %s\n", r.Next)
}

var hostName = regexp.MustCompile(`^[a-z0-9-]+(\.[a-z0-9-]+)+$`)

// signinHostOK reports whether host is a plain SharePoint host name, safe to open signed in.
func signinHostOK(host string) bool {
	return hostName.MatchString(host) && transcripts.IsSharePointHost(host)
}

// signinPagePath is a SharePoint page on the way to sign-in, not past it.
var signinPagePath = regexp.MustCompile(`(?i)^/_layouts/15/(Authenticate|AccessDenied)\.aspx|^/_forms/`)

// signinProbeJS asks the site's own API whether the session is signed in. It returns a boolean
// and nothing else.
const signinProbeJS = `(async () => { try { const r = await fetch("/_api/web?$select=Id", {headers: {accept: "application/json"}, credentials: "include", cache: "no-store"}); return r.status === 200 && (r.headers.get("content-type") || "").includes("json"); } catch (e) { return false; } })()`

func (c *transcriptsSigninCmd) Run(rt *runtime) error {
	host := strings.ToLower(strings.TrimSpace(c.Host))
	if host != "" && !signinHostOK(host) {
		u := errs.Usage(fmt.Sprintf("--host %q is not a SharePoint host name", c.Host))
		u.Fix = "Pass the SharePoint host alone, such as <tenant>.sharepoint.com, with no scheme or path."
		return u
	}
	if host == "" {
		var err error
		if host, err = rt.defaultSigninHost(); err != nil {
			return err
		}
	}
	exe, kind, err := findBrowser(c.Browser)
	if err != nil {
		return err
	}
	if err := c.agree(rt, kind); err != nil {
		return err
	}
	rt.progressLine(fmt.Sprintf("opening a visible %s window with m365crawl's own browser profile", browserName(kind)))
	page, closer, err := openBrowser(rt.ctx, browser.LaunchOptions{Exe: exe, Kind: kind, Profile: browser.ProfileDir(rt.dbPath), StartURL: "https://" + host + "/"})
	if err != nil {
		return err
	}
	defer func() { _ = closer.Close() }()
	rt.progressLine(fmt.Sprintf("waiting for you to sign in to SharePoint in that window (up to %s)", compactDuration(signinWait)))
	if err := waitForHost(rt.ctx, page, host); err != nil {
		return err
	}
	rt.progressLine("signed in; closing the window")
	return rt.write("transcripts signin", &signinResult{SignedIn: true, Browser: string(kind), Next: "m365crawl transcripts fetch <meeting>"})
}

// defaultSigninHost is the host most fetchable parts in the archive live on.
func (rt *runtime) defaultSigninHost() (string, error) {
	u := errs.Usage("no transcript part in the archive names a SharePoint host, so transcripts signin needs --host")
	u.Fix = "Run `m365crawl sync` so the archive lists the meeting recordings, or pass --host <tenant>.sharepoint.com."
	st, err := store.OpenReadOnly(rt.ctx, rt.dbPath)
	if errors.Is(err, store.ErrNoArchive) {
		return "", u
	}
	if err != nil {
		return "", errs.DBError(err)
	}
	defer func() { _ = st.Close() }()
	if ok, err := st.HasTranscriptTables(rt.ctx); err != nil || !ok {
		return "", u
	}
	hosts, err := signinHosts(st, rt.ctx)
	if err != nil {
		return "", errs.DBError(err)
	}
	if len(hosts) == 0 || !signinHostOK(hosts[0]) {
		return "", u
	}
	return hosts[0], nil
}

// agree makes sure the user agreed to a visible window: --user-agreed, or Enter on a terminal.
func (c *transcriptsSigninCmd) agree(rt *runtime, kind browser.Kind) error {
	if c.UserAgreed {
		return nil
	}
	if !stdinIsTTY() {
		return errs.SigninNeedsAgreement()
	}
	_, _ = fmt.Fprintf(rt.stderr, "m365crawl will open a visible %s window with its own browser profile, where you sign in to Microsoft 365 once. Press Enter to open it, or Ctrl-C to stop.\n", browserName(kind))
	line := make(chan error, 1)
	in := stdinReader
	go func() {
		_, err := bufio.NewReader(in).ReadString('\n')
		line <- err
	}()
	select {
	case err := <-line:
		if err != nil {
			return errs.SigninNeedsAgreement()
		}
		return nil
	case <-rt.ctx.Done():
		return rt.ctx.Err()
	}
}

// waitForHost waits until the window is signed in: the tab is on host, past SharePoint's own
// sign-in pages, and the site's API answers as for a signed-in user.
func waitForHost(ctx context.Context, page transcripts.PageDriver, host string) error {
	deadline := time.Now().Add(signinWait)
	for {
		done, err := signedIn(ctx, page, host, deadline)
		switch {
		case ctx.Err() != nil:
			return ctx.Err()
		case done:
			return nil
		case !time.Now().Before(deadline):
			b := errs.BrowserFailed("sign-in did not finish within " + compactDuration(signinWait))
			b.Fix = "Ask the user, then run `m365crawl transcripts signin` again and finish signing in in the window it opens."
			return b
		case err != nil:
			b := errs.BrowserFailed("the sign-in window was closed before sign-in finished")
			b.Fix = "Ask the user, then run `m365crawl transcripts signin` again and leave the window open until it closes by itself."
			return b
		}
		pause(ctx, signinPoll) // a cancellation is seen at the next question
	}
}

// signedIn asks the tab once, each question bounded by the deadline and by signinCallTimeout. The
// error is a window that went away; a page still loading, or a question that failed or hung, is
// only not signed in yet.
func signedIn(ctx context.Context, page transcripts.PageDriver, host string, deadline time.Time) (bool, error) {
	qctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	ask := func(q func(context.Context) error) error {
		cctx, ccancel := context.WithTimeout(qctx, signinCallTimeout)
		defer ccancel()
		return q(cctx)
	}
	var h string
	err := ask(func(c context.Context) (e error) { h, e = page.Host(c); return e })
	if err != nil || !strings.EqualFold(h, host) {
		return false, gone(err)
	}
	var path string
	if err := ask(func(c context.Context) error { return page.Eval(c, "location.pathname", &path) }); err != nil {
		return false, gone(err)
	}
	if signinPagePath.MatchString(path) {
		return false, nil
	}
	var ok bool
	err = ask(func(c context.Context) error { return page.Eval(c, signinProbeJS, &ok) })
	return ok && err == nil, gone(err)
}

// gone keeps an error only when the browser's connection closed.
func gone(err error) error {
	if errors.Is(err, browser.ErrDisconnected) {
		return err
	}
	return nil
}

// pause waits d or until ctx ends.
func pause(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
	}
}
