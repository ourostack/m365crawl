package transcripts

import (
	"context"
	_ "embed" // the in-page script
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ourostack/m365crawl/internal/errs"
)

// fetchJS is the in-page script, the only code that touches the network. It runs inside the
// SharePoint page, so the session and the temporary download URL never leave the browser: it
// returns a state, an HTTP status, a size or the entries, and nothing else.
//
//go:embed fetch.js
var fetchJS string

// Script is the in-page script as it is evaluated.
func Script() string { return strings.TrimSpace(fetchJS) }

// LoginHosts are the Microsoft sign-in hosts. A tab that ends on one of them is not signed in.
// The script gets the same list as an argument, so Go and the page agree.
var LoginHosts = []string{"login.microsoftonline.com", "login.microsoft.com", "login.live.com", "device.login.microsoftonline.com"}

// IsLoginHost reports whether host is one of LoginHosts, ignoring case.
func IsLoginHost(host string) bool {
	for _, h := range LoginHosts {
		if strings.EqualFold(h, host) {
			return true
		}
	}
	return false
}

// Fetch defaults.
const (
	DefaultLandTimeout = 30 * time.Second
	DefaultPartTimeout = 60 * time.Second
	DefaultMaxBytes    = 32 << 20
	DefaultLockWait    = 2 * time.Minute

	// hostCallTimeout bounds one question for the tab's host; the page retries while a navigation
	// replaces the document.
	hostCallTimeout = 10 * time.Second
)

// PageDriver is the browser tab a fetch runs in; *browser.Page satisfies it.
type PageDriver interface {
	Navigate(ctx context.Context, url string) error
	Host(ctx context.Context) (string, error)
	Eval(ctx context.Context, expr string, out any) error
}

// ScriptArgs are the in-page script's arguments. They reach the page only as JSON.
type ScriptArgs struct {
	Base         string   `json:"base"`
	TranscriptID string   `json:"transcriptId"`
	MaxBytes     int64    `json:"maxBytes"`
	LoginHosts   []string `json:"loginHosts"`
}

// ScriptResult is what the in-page script returns. Error is an exception's class name only.
type ScriptResult struct {
	State   string     `json:"state"`
	Status  int        `json:"status,omitempty"`
	Bytes   int64      `json:"bytes,omitempty"`
	Error   string     `json:"error,omitempty"`
	Entries []RawEntry `json:"entries,omitempty"`
}

// ScriptExpr is the expression that runs the script with args: the function applied to its
// arguments encoded by encoding/json, never pasted into the source as text.
func ScriptExpr(a ScriptArgs) string {
	b, _ := json.Marshal(a) // strings, an int and a string slice always encode
	return "(" + Script() + ")(" + string(b) + ")"
}

// The script's states that are not stored.
const (
	scriptSignin      = "signin"
	scriptSigninProbe = "signin_probe"
)

// Reasons a fetch gives for a part beyond the stored state's own.
const (
	reasonElsewhere   = "the SharePoint host redirected elsewhere"
	reasonUnreachable = "the SharePoint host could not be reached"
	reasonTimeout     = "the fetch of this part timed out"
	reasonEval        = "the page script failed"
	reasonRequest     = "the request to SharePoint failed"
)

// Fetcher fetches parts' transcripts through one browser tab.
type Fetcher struct {
	Page PageDriver
	// LandTimeout bounds the wait for the tab to settle on a host after navigating there;
	// PartTimeout bounds one part's script.
	LandTimeout, PartTimeout time.Duration
	// MaxBytes is the largest transcript the script reads.
	MaxBytes int64
	// Browser is stored with each result: edge, chrome or custom.
	Browser string
	Now     func() time.Time
	// LockWait is how long results wait for a busy archive before the fetch gives up on saving them.
	LockWait time.Duration
	// Progress, when set, gets one line per step: counts, ordinals and states only.
	Progress func(string)
	// LockPoll is how often a waiting result tries the archive again (default one second).
	LockPoll time.Duration

	hostPoll time.Duration
}

// PartOutcome is what happened to one part in this run. FetchedAt is set when the text was
// fetched; Saved says the result reached the archive.
type PartOutcome struct {
	Part       Part
	State      string
	HTTPStatus int
	Entries    int
	FetchedAt  time.Time
	Reason     string
	Saved      bool
}

// FetchSummary counts a run's outcomes. SkippedLocal is left for the caller, which knows what was
// already in the archive.
type FetchSummary struct {
	Fetched, NoAccess, NotFound, NoTranscript, TooLarge, Failed, SkippedLocal, Unfetchable int
	Parts                                                                                  []PartOutcome
}

func (f *Fetcher) defaults() {
	if f.LandTimeout <= 0 {
		f.LandTimeout = DefaultLandTimeout
	}
	if f.PartTimeout <= 0 {
		f.PartTimeout = DefaultPartTimeout
	}
	if f.MaxBytes <= 0 {
		f.MaxBytes = DefaultMaxBytes
	}
	if f.LockWait <= 0 {
		f.LockWait = DefaultLockWait
	}
	if f.Now == nil {
		f.Now = time.Now
	}
	if f.LockPoll <= 0 {
		f.LockPoll = time.Second
	}
	if f.hostPoll <= 0 {
		f.hostPoll = 100 * time.Millisecond
	}
}

func (f *Fetcher) progress(format string, a ...any) {
	if f.Progress != nil {
		f.Progress(fmt.Sprintf(format, a...))
	}
}

// Fetch fetches every part, host by host: it opens each host once, runs the script per part and
// hands each result to save. A part that cannot be fetched is listed and never sent. A sign-in
// page stops the run with transcripts_signin_required; parts already saved keep their state.
func (f *Fetcher) Fetch(ctx context.Context, parts []Part, save func(Part, FetchResult) error) (FetchSummary, error) {
	f.defaults()
	var sum FetchSummary
	sv := &saver{save: save, sum: &sum}
	var hosts []string
	byHost := map[string][]int{}
	for _, p := range parts {
		if !p.Fetchable() {
			sum.Unfetchable++
			sum.Parts = append(sum.Parts, PartOutcome{Part: p, State: StateUnfetchable, Reason: Reason(p, StateUnfetchable)})
			continue
		}
		h := strings.ToLower(p.Host)
		if _, ok := byHost[h]; !ok {
			hosts = append(hosts, h)
		}
		sum.Parts = append(sum.Parts, PartOutcome{Part: p})
		byHost[h] = append(byHost[h], len(sum.Parts)-1)
	}
	total := len(sum.Parts) - sum.Unfetchable
	n := 0
	for hi, host := range hosts {
		if err := ctx.Err(); err != nil {
			return sum, err
		}
		f.progress("opening SharePoint site %d of %d", hi+1, len(hosts))
		reason, err := f.land(ctx, host)
		if err != nil {
			return sum, f.stop(ctx, sv, err)
		}
		for _, i := range byHost[host] {
			n++
			p := sum.Parts[i].Part
			r, why := FetchResult{State: StateFailed}, reason
			if why == "" {
				r, why, err = f.fetchPart(ctx, host, p)
				if err != nil {
					return sum, f.stop(ctx, sv, err)
				}
			}
			r.Browser, r.At = f.Browser, f.Now()
			sum.record(i, r, why)
			if r.State == StateOK {
				unit := "entries"
				if len(r.Entries) == 1 {
					unit = "entry"
				}
				f.progress("part %d of %d fetched: ok, %d %s", n, total, len(r.Entries), unit)
			} else {
				f.progress("part %d of %d fetched: %s", n, total, r.State)
			}
			if err := sv.put(i, r); err != nil {
				return sum, err
			}
		}
	}
	return sum, sv.drain(ctx, f.LockWait, f.LockPoll)
}

// stop ends a run early: a cancelled run returns at once; a sign-in stop first saves what is waiting.
func (f *Fetcher) stop(ctx context.Context, sv *saver, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if derr := sv.drain(ctx, f.LockWait, f.LockPoll); derr != nil {
		return derr
	}
	return err
}

// land opens https://<host>/ and waits until the tab settles on that host. It returns a reason
// when the host's parts cannot be fetched, and transcripts_signin_required when the tab is left
// on a sign-in page.
func (f *Fetcher) land(ctx context.Context, host string) (string, error) {
	deadline := time.Now().Add(f.LandTimeout)
	lctx, cancel := context.WithDeadline(ctx, deadline)
	navErr := f.Page.Navigate(lctx, "https://"+host+"/")
	cancel()
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if navErr != nil && time.Now().Before(deadline) {
		return reasonUnreachable, nil
	}
	// The host is asked at least once, even when loading the page took the whole wait.
	last := ""
	for {
		hctx, hcancel := context.WithTimeout(ctx, hostCallTimeout)
		h, err := f.Page.Host(hctx)
		hcancel()
		if err == nil {
			last = strings.ToLower(h)
			if last == host {
				return "", nil
			}
		}
		if !time.Now().Before(deadline) {
			break
		}
		if err := sleep(ctx, f.hostPoll); err != nil {
			return "", err
		}
	}
	if IsLoginHost(last) {
		return "", errs.SigninRequired()
	}
	return reasonElsewhere, nil
}

// fetchPart runs the script for one part. The error is a cancellation or a sign-in stop.
func (f *Fetcher) fetchPart(ctx context.Context, host string, p Part) (FetchResult, string, error) {
	pctx, cancel := context.WithTimeout(ctx, f.PartTimeout)
	defer cancel()
	args := ScriptArgs{Base: "https://" + host + p.SiteRoot + "/_api/v2.1/drives/" + p.DriveID + "/items/" + p.ItemID,
		TranscriptID: p.TranscriptID, MaxBytes: f.MaxBytes, LoginHosts: LoginHosts}
	var res ScriptResult
	err := f.Page.Eval(pctx, ScriptExpr(args), &res)
	switch {
	case ctx.Err() != nil:
		return FetchResult{}, "", ctx.Err()
	case pctx.Err() != nil:
		return FetchResult{State: StateFailed}, reasonTimeout, nil
	case err != nil:
		return FetchResult{State: StateFailed}, reasonEval, nil
	}
	r := FetchResult{State: res.State, HTTPStatus: res.Status}
	switch res.State {
	case scriptSignin:
		return r, "", errs.SigninRequired()
	case scriptSigninProbe:
		hctx, hcancel := context.WithTimeout(ctx, f.LandTimeout)
		defer hcancel()
		h, _ := f.Page.Host(hctx)
		if ctx.Err() != nil {
			return FetchResult{}, "", ctx.Err()
		}
		if IsLoginHost(h) {
			return r, "", errs.SigninRequired()
		}
		return FetchResult{State: StateFailed}, reasonRequest, nil
	case StateOK:
		r.Entries, _ = DecodeEntries(res.Entries)
	case StateNoAccess, StateNotFound, StateNoTranscript, StateTooLarge, StateFailed:
	default:
		r.State = StateFailed
	}
	return r, "", nil
}

// record notes one part's result in the summary.
func (s *FetchSummary) record(i int, r FetchResult, reason string) {
	o := &s.Parts[i]
	o.State, o.HTTPStatus = r.State, r.HTTPStatus
	if reason == "" {
		reason = Reason(o.Part, r.State)
	}
	o.Reason = reason
	switch r.State {
	case StateOK:
		s.Fetched++
		o.Entries, o.FetchedAt = len(r.Entries), r.At
	case StateNoAccess:
		s.NoAccess++
	case StateNotFound:
		s.NotFound++
	case StateNoTranscript:
		s.NoTranscript++
	case StateTooLarge:
		s.TooLarge++
	default:
		s.Failed++
	}
}

// saver hands results to save in order. A result that meets a busy archive waits in memory and is
// tried again after each later part and at the end.
type saver struct {
	save    func(Part, FetchResult) error
	sum     *FetchSummary
	pending []pendingSave
}

type pendingSave struct {
	i int
	r FetchResult
}

func (s *saver) put(i int, r FetchResult) error {
	s.pending = append(s.pending, pendingSave{i, r})
	return s.flush()
}

// flush saves what is waiting, in order, until the archive is busy.
func (s *saver) flush() error {
	for len(s.pending) > 0 {
		p := s.pending[0]
		err := s.save(s.sum.Parts[p.i].Part, p.r)
		var c *errs.Coded
		if errors.As(err, &c) && c.Code == errs.CodeLocked {
			return nil
		}
		if err != nil {
			return err
		}
		s.sum.Parts[p.i].Saved = true
		s.pending = s.pending[1:]
	}
	return nil
}

// drain keeps trying the waiting results for up to wait, then gives up with `locked`.
func (s *saver) drain(ctx context.Context, wait, poll time.Duration) error {
	deadline := time.Now().Add(wait)
	for {
		if err := s.flush(); err != nil {
			return err
		}
		if len(s.pending) == 0 {
			return nil
		}
		if !time.Now().Before(deadline) {
			return errs.Locked(fmt.Sprintf("another m365crawl run held the archive lock for %s; %d fetched parts were not saved", wait, len(s.pending)))
		}
		if err := sleep(ctx, poll); err != nil {
			return err
		}
	}
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
