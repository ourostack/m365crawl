package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/browser"
	"github.com/ourostack/m365crawl/internal/browser/browsertest"
	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/transcripts"
)

const trHost = "tenant.sharepoint.example.invalid"

// trPage is a fake browser tab: navigation lands on the host asked for (or landOn), and Eval
// answers the in-page script per item id, by default with one synthetic entry.
type trPage struct {
	mu         sync.Mutex
	host       string
	landOn     string
	hostErr    error
	hostFails  []error  // errors of the first Host calls; errHang blocks until the call's context ends
	hosts      []string // successive answers of Host, then host
	paths      []string // successive answers of location.pathname, then "/"
	probes     []bool   // successive answers of the sign-in probe, then true
	nPaths     int
	nProbes    int
	noDeadline int // Host calls whose context had no deadline
	answer     map[string]func(ctx context.Context) (transcripts.ScriptResult, error)
	items      []string
}

func (p *trPage) Navigate(ctx context.Context, raw string) error {
	u, _ := url.Parse(raw)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.host = u.Host
	if p.landOn != "" {
		p.host = p.landOn
	}
	return ctx.Err()
}

func (p *trPage) Host(ctx context.Context) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := ctx.Deadline(); !ok {
		p.noDeadline++
	}
	if len(p.hostFails) > 0 {
		err := p.hostFails[0]
		p.hostFails = p.hostFails[1:]
		if errors.Is(err, errHang) {
			p.mu.Unlock()
			<-ctx.Done()
			p.mu.Lock()
			return "", ctx.Err()
		}
		return "", err
	}
	if len(p.hosts) > 0 {
		h := p.hosts[0]
		p.hosts = p.hosts[1:]
		return h, nil
	}
	return p.host, p.hostErr
}

func (p *trPage) Eval(ctx context.Context, expr string, out any) error {
	p.mu.Lock()
	switch expr {
	case "location.pathname":
		p.nPaths++
		v := "/"
		if len(p.paths) > 0 {
			v, p.paths = p.paths[0], p.paths[1:]
		}
		p.mu.Unlock()
		b, _ := json.Marshal(v)
		return json.Unmarshal(b, out)
	case signinProbeJS:
		p.nProbes++
		v := true
		if len(p.probes) > 0 {
			v, p.probes = p.probes[0], p.probes[1:]
		}
		p.mu.Unlock()
		b, _ := json.Marshal(v)
		return json.Unmarshal(b, out)
	}
	p.mu.Unlock()
	var args transcripts.ScriptArgs
	body := strings.TrimSuffix(strings.TrimPrefix(expr, "("+transcripts.Script()+")("), ")")
	if err := json.Unmarshal([]byte(body), &args); err != nil {
		return err
	}
	item := path.Base(args.Base)
	p.mu.Lock()
	p.items = append(p.items, item)
	h := p.answer[item]
	p.mu.Unlock()
	res := transcripts.ScriptResult{State: "ok", Status: 200, Entries: []transcripts.RawEntry{
		{S: "Dee Example", B: "00:00:01.5", E: "00:00:03", T: "Fetched line one."},
		{S: "Eli Example", B: "00:00:04", E: "00:00:05", T: "Fetched line two."}}}
	if h != nil {
		var err error
		if res, err = h(ctx); err != nil {
			return err
		}
	}
	b, _ := json.Marshal(res)
	return json.Unmarshal(b, out)
}

func (p *trPage) fetched() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return strings.Join(p.items, ",")
}

type countCloser struct{ n atomic.Int32 }

func (c *countCloser) Close() error { c.n.Add(1); return nil }

// fakeBrowser replaces the browser seams: Find answers a fake path and openBrowser hands out page.
// It returns the closer and the options the last launch got.
func fakeBrowser(t *testing.T, page *trPage) (*countCloser, *browser.LaunchOptions) {
	t.Helper()
	closer := &countCloser{}
	got := &browser.LaunchOptions{}
	oldFind, oldOpen, oldNow, oldPoll := findBrowser, openBrowser, transcriptsNow, signinPoll
	findBrowser = func(pref string) (string, browser.Kind, error) {
		if pref == "missing" {
			return "", "", errs.NoBrowser()
		}
		return "/fake/msedge", browser.KindEdge, nil
	}
	openBrowser = func(_ context.Context, o browser.LaunchOptions) (transcripts.PageDriver, io.Closer, error) {
		*got = o
		if page == nil {
			t.Fatal("no browser may start in this test")
		}
		if o.StartURL != "" {
			_ = page.Navigate(context.Background(), o.StartURL)
		}
		return page, closer, nil
	}
	transcriptsNow = func() time.Time { return trFetchedAt.Add(time.Hour) }
	signinPoll = time.Millisecond
	t.Cleanup(func() { findBrowser, openBrowser, transcriptsNow, signinPoll = oldFind, oldOpen, oldNow, oldPoll })
	return closer, got
}

func newTrPage() *trPage {
	return &trPage{answer: map[string]func(context.Context) (transcripts.ScriptResult, error){}}
}

func fetchParts(t *testing.T, m map[string]any, call int) []map[string]any {
	t.Helper()
	calls, _ := m["calls"].([]any)
	if len(calls) <= call {
		t.Fatalf("calls %v", m["calls"])
	}
	var out []map[string]any
	for _, p := range calls[call].(map[string]any)["parts"].([]any) {
		out = append(out, p.(map[string]any))
	}
	return out
}

func partStates(parts []map[string]any) string {
	var s []string
	for _, p := range parts {
		s = append(s, p["state"].(string))
	}
	return strings.Join(s, ",")
}

func TestFetchCommandSkipsLocal(t *testing.T) {
	e := trEnv(t)
	page := newTrPage()
	closer, opts := fakeBrowser(t, page)
	m := trJSON(t, e, "transcripts", "fetch", "call-1")
	if page.fetched() != "P3" || closer.n.Load() != 1 {
		t.Fatalf("fetched %q, closed %d", page.fetched(), closer.n.Load())
	}
	if !opts.Headless || opts.Kind != browser.KindEdge || opts.Exe != "/fake/msedge" || opts.Profile != browser.ProfileDir(e.db) || opts.StartURL != "" {
		t.Fatalf("launch options %+v", opts)
	}
	parts := fetchParts(t, m, 0)
	if partStates(parts) != "local,local,ok" || m["source"] != "network" || m["browser"] != "edge" || m["fetched"] != float64(1) || m["local"] != float64(2) {
		t.Fatalf("result %v", m)
	}
	if parts[0]["fetched_at"] != "2026-11-09T08:00:00Z" || parts[0]["entries"] != float64(3) || parts[2]["fetched_at"] != "2026-11-09T09:00:00Z" || parts[2]["entries"] != float64(2) {
		t.Fatalf("parts %v", parts)
	}
	if m["next"] != "m365crawl transcripts show call-1" || m["fetched_at"] != "2026-11-09T09:00:00Z" {
		t.Fatalf("next %v, fetched_at %v", m["next"], m["fetched_at"])
	}
}

func TestFetchCommandRefetch(t *testing.T) {
	e := trEnv(t)
	page := newTrPage()
	fakeBrowser(t, page)
	m := trJSON(t, e, "transcripts", "fetch", "call-1", "--refetch")
	if page.fetched() != "P1,P2,P3" || partStates(fetchParts(t, m, 0)) != "ok,ok,ok" || m["fetched"] != float64(3) {
		t.Fatalf("fetched %q, %v", page.fetched(), m)
	}
}

func TestFetchCommandNothingToFetch(t *testing.T) {
	e := trEnv(t)
	fakeBrowser(t, nil)
	m := trJSON(t, e, "transcripts", "fetch", "call-2")
	if m["source"] != "archive" || m["browser"] != nil || m["fetched_at"] != nil || m["local"] != float64(1) || m["fetched"] != float64(0) {
		t.Fatalf("result %v", m)
	}
	if !strings.Contains(m["note"].(string), "nothing to fetch") {
		t.Fatalf("note %v", m["note"])
	}
}

func TestFetchCommandUnfetchableListed(t *testing.T) {
	e := trEnv(t)
	fakeBrowser(t, nil)
	m := trJSON(t, e, "transcripts", "fetch", "call-3")
	parts := fetchParts(t, m, 0)
	if partStates(parts) != "unfetchable" || m["unfetchable"] != float64(1) || !strings.Contains(parts[0]["reason"].(string), "sharing link") {
		t.Fatalf("result %v", m)
	}
}

func TestFetchCommandSinceXorMeeting(t *testing.T) {
	e := trEnv(t)
	fakeBrowser(t, nil)
	for _, args := range [][]string{{"transcripts", "fetch"}, {"transcripts", "fetch", "call-1", "--since", "7d"}} {
		er := trFails(t, e, errs.CodeUsage, errs.ExitUsage, args...)
		if !strings.Contains(er["message"].(string), "exactly one of <meeting> or --since") {
			t.Fatalf("%v: %v", args, er)
		}
	}
}

func TestFetchCommandUsageErrors(t *testing.T) {
	e := trEnv(t)
	fakeBrowser(t, nil)
	for _, args := range [][]string{
		{"transcripts", "fetch", "call-1", "--limit", "0"},
		{"transcripts", "fetch", "call-1", "--limit", "201"},
		{"transcripts", "fetch", "call-1", "--timeout", "0s"},
		{"transcripts", "fetch", "--since", "yesterday-ish"},
		{"transcripts", "fetch", "https://example.invalid/not-a-chat"},
		{"--fields", "calls", "transcripts", "fetch", "call-1"},
	} {
		trFails(t, e, errs.CodeUsage, errs.ExitUsage, args...)
	}
	trFails(t, e, errs.CodeUnknownMeeting, errs.ExitUsage, "transcripts", "fetch", "call-none")
}

func TestFetchCommandSince(t *testing.T) {
	e := trEnv(t)
	page := newTrPage()
	fakeBrowser(t, page)
	m := trJSON(t, e, "transcripts", "fetch", "--since", "2026-11-01")
	if page.fetched() != "R1,P3" || m["truncated"] != false || m["fetched"] != float64(2) || m["local"] != float64(2) {
		t.Fatalf("fetched %q, %v", page.fetched(), m)
	}
	if m["next"] != "m365crawl transcripts show <call-id>" {
		t.Fatalf("next %v", m["next"])
	}
	// Everything is fetched now: nothing is left, and the newest call alone is fetched again with --refetch --limit 1.
	page2 := newTrPage()
	fakeBrowser(t, page2)
	m = trJSON(t, e, "transcripts", "fetch", "--since", "2026-11-01")
	if len(m["calls"].([]any)) != 0 || !strings.Contains(m["note"].(string), "nothing to fetch") {
		t.Fatalf("second run %v", m)
	}
	m = trJSON(t, e, "transcripts", "fetch", "--since", "2026-11-01", "--refetch", "--limit", "1")
	if page2.fetched() != "R1" || m["truncated"] != true || m["next"] != "m365crawl transcripts show call-4" {
		t.Fatalf("fetched %q, %v", page2.fetched(), m)
	}
}

func TestFetchCommandNoBrowser(t *testing.T) {
	e := trEnv(t)
	fakeBrowser(t, nil)
	trFails(t, e, errs.CodeNoBrowser, errs.ExitEnvironment, "transcripts", "fetch", "call-4", "--browser", "missing")
	// The real lookup, through the environment variable.
	findBrowser = browser.Find
	t.Setenv("M365CRAWL_BROWSER", filepath.Join(t.TempDir(), "no-such-browser"))
	trFails(t, e, errs.CodeNoBrowser, errs.ExitEnvironment, "transcripts", "fetch", "call-4")
}

func TestFetchCommandBusy(t *testing.T) {
	// The real launch: the profile lock is held, so nothing starts.
	e := trEnv(t)
	release, err := store.AcquireLock(browser.ProfileDir(e.db))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	exe, _ := os.Executable()
	er := trFails(t, e, errs.CodeBrowserBusy, errs.ExitLocked, "transcripts", "fetch", "call-4", "--browser", exe)
	if !strings.Contains(er["fix"].(string), "sign-in window") {
		t.Fatalf("fix %v", er)
	}
}

func TestFetchCommandSavesThenShowIsLocal(t *testing.T) {
	e := trEnv(t)
	fakeBrowser(t, newTrPage())
	trJSON(t, e, "transcripts", "fetch", "call-4")
	show := trJSON(t, e, "transcripts", "show", "call-4")
	segs := show["segments"].([]any)
	seg := segs[0].(map[string]any)
	entries := seg["entries"].([]any)
	if show["source"] != "archive" || show["complete"] != true || seg["fetched_at"] != "2026-11-09T09:00:00Z" || len(entries) != 2 {
		t.Fatalf("show %v", show)
	}
	if en := entries[0].(map[string]any); en["speaker"] != "Dee Example" || en["offset"] != "0:00:01" || en["text"] != "Fetched line one." {
		t.Fatalf("entry %v", en)
	}
	// A second fetch finds the part local and launches nothing.
	fakeBrowser(t, nil)
	m := trJSON(t, e, "transcripts", "fetch", "call-4")
	if partStates(fetchParts(t, m, 0)) != "local" {
		t.Fatalf("second fetch %v", m)
	}
}

func TestFetchCommandPartialExitZeroNote(t *testing.T) {
	e := trEnv(t)
	page := newTrPage()
	page.answer["P3"] = func(context.Context) (transcripts.ScriptResult, error) {
		return transcripts.ScriptResult{State: "no_access", Status: 403}, nil
	}
	fakeBrowser(t, page)
	m := trJSON(t, e, "transcripts", "fetch", "call-1")
	parts := fetchParts(t, m, 0)
	if parts[2]["state"] != "no_access" || parts[2]["http_status"] != float64(403) || !strings.Contains(parts[2]["reason"].(string), "403") || m["failed"] != float64(1) {
		t.Fatalf("result %v", m)
	}
	if m["note"] != "1 part could not be fetched; its reason is listed with it" {
		t.Fatalf("note %v", m["note"])
	}
	show := trJSON(t, e, "transcripts", "show", "call-1")
	if seg := show["segments"].([]any)[2].(map[string]any); seg["state"] != "no_access" || show["complete"] != false {
		t.Fatalf("show %v", show)
	}
}

func TestFetchCommandSigninRequired(t *testing.T) {
	e := trEnv(t)
	page := newTrPage()
	page.landOn = "login.microsoftonline.com"
	closer, _ := fakeBrowser(t, page)
	old := fetchLandTimeout
	fetchLandTimeout = 20 * time.Millisecond
	t.Cleanup(func() { fetchLandTimeout = old })
	er := trFails(t, e, errs.CodeSigninRequired, errs.ExitEnvironment, "transcripts", "fetch", "call-4")
	if !strings.HasPrefix(er["fix"].(string), "Ask the user before running `m365crawl transcripts signin`") || closer.n.Load() != 1 {
		t.Fatalf("error %v, closed %d", er, closer.n.Load())
	}
	m := trJSON(t, e, "transcripts", "call-4")
	if s := items(t, m)[0]["parts"].([]any)[0].(map[string]any)["state"]; s != "not_fetched" {
		t.Fatalf("the part must stay not_fetched, got %v", s)
	}
}

func TestFetchCommandCancelledClosesBrowser(t *testing.T) {
	e := trEnv(t)
	page := newTrPage()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	page.answer["R1"] = func(c context.Context) (transcripts.ScriptResult, error) {
		cancel()
		<-c.Done()
		return transcripts.ScriptResult{}, c.Err()
	}
	closer, _ := fakeBrowser(t, page)
	var out, errb bytes.Buffer
	code := runCLI(ctx, []string{"--db", e.db, "--teams-root", e.root, "--max-age", "0", "--json", "transcripts", "fetch", "call-4"}, &out, &errb)
	if code != errs.ExitRuntime || errorOf(t, errb.String())["code"] != errs.CodeInterrupted || closer.n.Load() != 1 {
		t.Fatalf("exit %d, closed %d: %s", code, closer.n.Load(), errb.String())
	}
}

func TestFetchCommandTimeoutClosesBrowser(t *testing.T) {
	e := trEnv(t)
	page := newTrPage()
	page.answer["R1"] = func(c context.Context) (transcripts.ScriptResult, error) {
		<-c.Done()
		return transcripts.ScriptResult{}, c.Err()
	}
	closer, _ := fakeBrowser(t, page)
	er := trFails(t, e, errs.CodeBrowserFailed, errs.ExitRuntime, "transcripts", "fetch", "call-4", "--timeout", "50ms")
	if !strings.Contains(er["message"].(string), "did not finish within 50ms") || !strings.Contains(er["fix"].(string), "--timeout") || closer.n.Load() != 1 {
		t.Fatalf("error %v, closed %d", er, closer.n.Load())
	}
	// A launch that runs out of time is reported the same way.
	openBrowser = func(ctx context.Context, _ browser.LaunchOptions) (transcripts.PageDriver, io.Closer, error) {
		<-ctx.Done()
		return nil, nil, ctx.Err()
	}
	trFails(t, e, errs.CodeBrowserFailed, errs.ExitRuntime, "transcripts", "fetch", "call-4", "--timeout", "50ms")
}

func TestFetchCommandPanicClosesBrowser(t *testing.T) {
	e := trEnv(t)
	page := newTrPage()
	page.answer["R1"] = func(context.Context) (transcripts.ScriptResult, error) { panic("synthetic failure") }
	closer, _ := fakeBrowser(t, page)
	trFails(t, e, errs.CodeInternal, errs.ExitRuntime, "transcripts", "fetch", "call-4")
	if closer.n.Load() != 1 {
		t.Fatalf("closed %d times", closer.n.Load())
	}
}

func TestFetchCommandSaveWaitsForTheLock(t *testing.T) {
	e := trEnv(t)
	release, err := store.AcquireLock(e.db)
	if err != nil {
		t.Fatal(err)
	}
	old := fetchLockPoll
	fetchLockPoll = 10 * time.Millisecond
	t.Cleanup(func() { fetchLockPoll = old })
	time.AfterFunc(100*time.Millisecond, release)
	fakeBrowser(t, newTrPage())
	m := trJSON(t, e, "transcripts", "fetch", "call-4")
	if m["fetched"] != float64(1) {
		t.Fatalf("result %v", m)
	}
	if s := trJSON(t, e, "transcripts", "call-4"); items(t, s)[0]["state"] != "fetched" {
		t.Fatalf("not saved: %v", s)
	}
}

func TestFetchCommandSaveFailure(t *testing.T) {
	e := trEnv(t)
	fakeBrowser(t, newTrPage())
	old := openArchive
	openArchive = func(context.Context, string) (*store.Store, error) { return nil, errors.New("synthetic open failure") }
	t.Cleanup(func() { openArchive = old })
	trFails(t, e, errs.CodeDBError, errs.ExitRuntime, "transcripts", "fetch", "call-4")
	openArchive = old
	e.exec(`drop table transcript_entries`)
	e.exec(`create table transcript_entries(x)`)
	trFails(t, e, errs.CodeDBError, errs.ExitRuntime, "transcripts", "fetch", "call-4")
}

func TestFetchCommandArchiveStates(t *testing.T) {
	e := textEnv(t)
	fakeBrowser(t, nil)
	m := trJSON(t, e, "transcripts", "fetch", "call-1")
	if m["note"] != "no archive yet: run m365crawl sync" || m["needs_sync"] != true {
		t.Fatalf("no archive: %v", m)
	}
	e.emptyArchive()
	e.exec(`drop table transcript_entries`)
	m = trJSON(t, e, "transcripts", "fetch", "call-1")
	if m["note"] != noTranscriptTables {
		t.Fatalf("no tables: %v", m)
	}
}

func TestFetchProgressHasNoContent(t *testing.T) {
	e := trEnv(t)
	page := newTrPage()
	page.answer["P3"] = func(context.Context) (transcripts.ScriptResult, error) {
		return transcripts.ScriptResult{State: "no_access", Status: 403}, nil
	}
	fakeBrowser(t, page)
	for _, format := range []string{"json", "text"} {
		code, _, errOut := e.tr("--format", format, "transcripts", "fetch", "--since", "2026-11-01", "--refetch")
		if code != 0 {
			t.Fatalf("exit %d: %s", code, errOut)
		}
		if !strings.Contains(errOut, "part 5 of 5 fetched") || !strings.Contains(errOut, "starting a headless Edge to fetch 5 parts") {
			t.Fatalf("%s: no progress: %q", format, errOut)
		}
		for _, leak := range []string{"Example", "Fetched line", "sharepoint", trHost, "call-", "P1", "P3", "R1", "b!d1", "meeting_", "tr-", "Weekly"} {
			if strings.Contains(errOut, leak) {
				t.Errorf("%s: progress carries %q: %q", format, leak, errOut)
			}
		}
		if format == "json" {
			for _, line := range strings.Split(strings.TrimSpace(errOut), "\n") {
				var doc map[string]string
				if err := json.Unmarshal([]byte(line), &doc); err != nil || doc["progress"] == "" {
					t.Errorf("not a progress line: %q", line)
				}
			}
		}
	}
}

func TestFetchGolden(t *testing.T) {
	// Every state a fetch prints: local, ok and no_access from --since, then ok's part read back
	// as local beside an unfetchable one. Each output format starts from a fresh archive.
	page := newTrPage()
	page.answer["P3"] = func(context.Context) (transcripts.ScriptResult, error) {
		return transcripts.ScriptResult{State: "no_access", Status: 403}, nil
	}
	fakeBrowser(t, page)
	checkFetchGoldens(t, "transcripts_fetch", [][]string{{"fetch", "--since", "2026-11-01"}})
	checkFetchGoldens(t, "transcripts_fetch_local", [][]string{{"fetch", "call-4"}, {"fetch", trAdhoc}})
}

// checkFetchGoldens runs the commands on a fresh archive for each of plain, color and JSON output
// and compares the last command's output with its goldens.
func checkFetchGoldens(t *testing.T, name string, runs [][]string) {
	t.Helper()
	run := func(format string) string {
		e := trEnv(t)
		var out string
		for _, args := range runs {
			code, o, errOut := e.tr(append([]string{format, "transcripts"}, args...)...)
			if code != 0 {
				t.Fatalf("%s %v: exit %d: %s", name, args, code, errOut)
			}
			out = o
		}
		if format == "--json" {
			return jsonGolden(t, out)
		}
		return e.scrub(out)
	}
	for _, color := range []bool{false, true} {
		suffix := "plain"
		t.Setenv("CLICOLOR_FORCE", "")
		if color {
			suffix = "color"
			t.Setenv("CLICOLOR_FORCE", "1")
		}
		out := run("--format=text")
		if !color && strings.Contains(out, "\x1b") {
			t.Errorf("%s: escape in plain output", name)
		}
		lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
		if last := lines[len(lines)-1]; !strings.HasPrefix(last, "source: ") && !strings.Contains(last, "source: ") {
			t.Errorf("%s: the last line says where the result came from: %q", name, last)
		}
		checkGolden(t, name+"."+suffix, out)
	}
	t.Setenv("CLICOLOR_FORCE", "")
	checkGolden(t, name+".json", run("--json"))
}

func TestFetchTextTruncated(t *testing.T) {
	e := trEnv(t)
	fakeBrowser(t, newTrPage())
	code, out, errOut := e.tr("--format", "text", "transcripts", "fetch", "--since", "2026-11-01", "--limit", "1")
	if code != 0 || !strings.Contains(out, "(more meetings were left; raise --limit)") || !strings.Contains(out, "next: m365crawl transcripts show call-4") {
		t.Fatalf("exit %d: %s%s", code, out, errOut)
	}
	// Nothing left: no table, the totals and the note.
	e.tr("--format", "text", "transcripts", "fetch", "--since", "2026-11-01")
	code, out, _ = e.tr("--format", "text", "transcripts", "fetch", "--since", "2026-11-01")
	if code != 0 || strings.Contains(out, "call ") || !strings.HasPrefix(out, "0 fetched · 0 local") {
		t.Fatalf("exit %d: %s", code, out)
	}
}

// ---- signin ----

func signinTTY(t *testing.T, tty bool, input string) {
	t.Helper()
	oldTTY, oldIn := stdinIsTTY, stdinReader
	stdinIsTTY = func() bool { return tty }
	stdinReader = strings.NewReader(input)
	t.Cleanup(func() { stdinIsTTY, stdinReader = oldTTY, oldIn })
}

func TestSigninRefusesWithoutAgreementWhenNotTTY(t *testing.T) {
	e := trEnv(t)
	fakeBrowser(t, nil)
	signinTTY(t, false, "")
	er := trFails(t, e, errs.CodeSigninNeedsAgreement, errs.ExitUsage, "transcripts", "signin")
	if !strings.HasPrefix(er["fix"].(string), "Ask the user before running `m365crawl transcripts signin`") {
		t.Fatalf("error %v", er)
	}
}

func TestSigninHeadedWaitsForHost(t *testing.T) {
	e := trEnv(t)
	page := newTrPage()
	page.hosts = []string{"", "login.microsoftonline.com", "login.microsoftonline.com", trHost}
	closer, opts := fakeBrowser(t, page)
	signinTTY(t, false, "")
	code, out, errOut := e.tr("--json", "transcripts", "signin", "--user-agreed")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if opts.Headless || opts.StartURL != "https://"+trHost+"/" || opts.Profile != browser.ProfileDir(e.db) || closer.n.Load() != 1 {
		t.Fatalf("launch %+v, closed %d", opts, closer.n.Load())
	}
	m := decode(t, out)
	if m["signed_in"] != true || m["browser"] != "edge" || m["next"] != "m365crawl transcripts fetch <meeting>" {
		t.Fatalf("result %v", m)
	}
	var steps []string
	for _, line := range strings.Split(strings.TrimSpace(errOut), "\n") {
		var doc map[string]string
		if err := json.Unmarshal([]byte(line), &doc); err != nil {
			t.Fatalf("stderr line %q", line)
		}
		steps = append(steps, doc["progress"])
	}
	want := []string{"opening a visible Edge window with m365crawl's own browser profile",
		"waiting for you to sign in to SharePoint in that window (up to 5m)", "signed in; closing the window"}
	if strings.Join(steps, "|") != strings.Join(want, "|") {
		t.Fatalf("steps %q", steps)
	}
	// Text mode prints the same line a person reads.
	page.hosts = []string{trHost}
	code, out, _ = e.tr("--format", "text", "transcripts", "signin", "--user-agreed", "--host", strings.ToUpper(trHost))
	if code != 0 || out != "signed in; run m365crawl transcripts fetch <meeting>\n" {
		t.Fatalf("exit %d: %q", code, out)
	}
}

func TestSigninWaitsForEnterOnATerminal(t *testing.T) {
	e := trEnv(t)
	page := newTrPage()
	page.hosts = []string{trHost}
	closer, _ := fakeBrowser(t, page)
	signinTTY(t, true, "\n")
	code, _, errOut := e.tr("--format", "text", "transcripts", "signin")
	if code != 0 || !strings.Contains(errOut, "Press Enter to open it") || closer.n.Load() != 1 {
		t.Fatalf("exit %d, closed %d: %s", code, closer.n.Load(), errOut)
	}
	// End of input instead of Enter: nothing opens.
	signinTTY(t, true, "")
	trFails(t, e, errs.CodeSigninNeedsAgreement, errs.ExitUsage, "transcripts", "signin")
	if closer.n.Load() != 1 {
		t.Fatal("a browser opened without Enter")
	}
}

func TestSigninInterruptedAtThePrompt(t *testing.T) {
	e := trEnv(t)
	fakeBrowser(t, nil)
	r, w := io.Pipe()
	defer func() { _ = w.Close() }()
	oldTTY, oldIn := stdinIsTTY, stdinReader
	stdinIsTTY, stdinReader = func() bool { return true }, r
	t.Cleanup(func() { stdinIsTTY, stdinReader = oldTTY, oldIn })
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	var out, errb bytes.Buffer
	code := runCLI(ctx, []string{"--db", e.db, "--teams-root", e.root, "--max-age", "0", "--json", "transcripts", "signin"}, &out, &errb)
	if code != errs.ExitRuntime || !strings.Contains(errb.String(), errs.CodeInterrupted) {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
}

var errHang = errors.New("hang")

func TestSigninWindowClosed(t *testing.T) {
	e := trEnv(t)
	page := newTrPage()
	page.hostErr = fmt.Errorf("%w: websocket closed", browser.ErrDisconnected)
	closer, _ := fakeBrowser(t, page)
	er := trFails(t, e, errs.CodeBrowserFailed, errs.ExitRuntime, "transcripts", "signin", "--user-agreed")
	if er["message"] != "the browser failed: the sign-in window was closed before sign-in finished" || !strings.Contains(er["fix"].(string), "signin") || closer.n.Load() != 1 {
		t.Fatalf("error %v", er)
	}
}

func TestSigninRetriesAFailedOrHungHostCall(t *testing.T) {
	e := trEnv(t)
	page := newTrPage()
	// A Host error that is not a dropped connection, then a call that never answers: both are
	// asked again, and the hung one gives up after its own short timeout, not the whole wait.
	page.hostFails = []error{errors.New("target busy"), errHang}
	page.hosts = []string{trHost}
	closer, _ := fakeBrowser(t, page)
	oldWait, oldCall, oldPoll := signinWait, signinCallTimeout, signinPoll
	signinWait, signinCallTimeout, signinPoll = 3*time.Second, 20*time.Millisecond, time.Millisecond
	t.Cleanup(func() { signinWait, signinCallTimeout, signinPoll = oldWait, oldCall, oldPoll })
	start := time.Now()
	code, out, errOut := e.tr("transcripts", "signin", "--user-agreed")
	if code != 0 || decode(t, out)["signed_in"] != true || closer.n.Load() != 1 {
		t.Fatalf("exit %d: %s %s", code, out, errOut)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("a hung call held the wait for %s", d)
	}
}

func TestSigninTimesOut(t *testing.T) {
	e := trEnv(t)
	page := newTrPage()
	page.landOn = "login.microsoftonline.com"
	fakeBrowser(t, page)
	old := signinWait
	signinWait = 30 * time.Millisecond
	t.Cleanup(func() { signinWait = old })
	er := trFails(t, e, errs.CodeBrowserFailed, errs.ExitRuntime, "transcripts", "signin", "--user-agreed")
	if !strings.Contains(er["message"].(string), "sign-in did not finish within 30ms") {
		t.Fatalf("error %v", er)
	}
}

func TestSigninFailures(t *testing.T) {
	e := trEnv(t)
	fakeBrowser(t, nil)
	trFails(t, e, errs.CodeNoBrowser, errs.ExitEnvironment, "transcripts", "signin", "--user-agreed", "--browser", "missing")
	trFails(t, e, errs.CodeUsage, errs.ExitUsage, "transcripts", "signin", "--user-agreed", "--host", "bad host/")
	openBrowser = func(context.Context, browser.LaunchOptions) (transcripts.PageDriver, io.Closer, error) {
		return nil, nil, errs.BrowserBusy()
	}
	trFails(t, e, errs.CodeBrowserBusy, errs.ExitLocked, "transcripts", "signin", "--user-agreed")
	ctx, cancel := context.WithCancel(context.Background())
	page := newTrPage()
	page.landOn = "login.microsoftonline.com"
	fakeBrowser(t, page)
	time.AfterFunc(30*time.Millisecond, cancel)
	var out, errb bytes.Buffer
	code := runCLI(ctx, []string{"--db", e.db, "--teams-root", e.root, "--max-age", "0", "--json", "transcripts", "signin", "--user-agreed"}, &out, &errb)
	if code != errs.ExitRuntime || !strings.Contains(errb.String(), errs.CodeInterrupted) {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
}

func TestSigninNeedsHost(t *testing.T) {
	e := textEnv(t)
	fakeBrowser(t, nil)
	for _, prep := range []func(){func() {}, e.emptyArchive} {
		prep()
		er := trFails(t, e, errs.CodeUsage, errs.ExitUsage, "transcripts", "signin", "--user-agreed")
		if !strings.Contains(er["message"].(string), "--host") {
			t.Fatalf("error %v", er)
		}
	}
	e.exec(`drop table transcript_parts`)
	trFails(t, e, errs.CodeUsage, errs.ExitUsage, "transcripts", "signin", "--user-agreed")
	// A parts table the query cannot read, and an archive file that is not a database.
	e.exec(`create table transcript_parts(x)`)
	trFails(t, e, errs.CodeDBError, errs.ExitRuntime, "transcripts", "signin", "--user-agreed")
	if err := os.WriteFile(e.db, []byte("not a database, long enough to be read as one"), 0o600); err != nil {
		t.Fatal(err)
	}
	trFails(t, e, errs.CodeDBError, errs.ExitRuntime, "transcripts", "signin", "--user-agreed")
	_ = stdinIsTTY() // the real check runs; what it says depends on how the tests were started
}

func TestBrowserName(t *testing.T) {
	for k, want := range map[browser.Kind]string{browser.KindEdge: "Edge", browser.KindChrome: "Chrome", browser.KindCustom: "browser"} {
		if got := browserName(k); got != want {
			t.Errorf("%s: %q", k, got)
		}
	}
}

func TestFetchCommandManyFailuresAndReadError(t *testing.T) {
	e := trEnv(t)
	page := newTrPage()
	refuse := func(context.Context) (transcripts.ScriptResult, error) {
		return transcripts.ScriptResult{State: "not_found", Status: 404}, nil
	}
	page.answer["P3"], page.answer["R1"] = refuse, refuse
	fakeBrowser(t, page)
	m := trJSON(t, e, "transcripts", "fetch", "--since", "2026-11-01")
	if m["note"] != "2 parts could not be fetched; each reason is listed with its part" || m["failed"] != float64(2) {
		t.Fatalf("result %v", m)
	}
	old := transcriptCallsOf
	transcriptCallsOf = func(*store.Store, context.Context, store.TranscriptFilter) ([]store.TranscriptCall, bool, error) {
		return nil, false, errors.New("synthetic read failure")
	}
	t.Cleanup(func() { transcriptCallsOf = old })
	trFails(t, e, errs.CodeDBError, errs.ExitRuntime, "transcripts", "fetch", "call-1")
}

func TestSigninGolden(t *testing.T) {
	e := trEnv(t)
	page := newTrPage()
	fakeBrowser(t, page)
	signinTTY(t, false, "")
	for _, color := range []bool{false, true} {
		suffix := "plain"
		t.Setenv("CLICOLOR_FORCE", "")
		if color {
			suffix = "color"
			t.Setenv("CLICOLOR_FORCE", "1")
		}
		code, out, errOut := e.tr("--format", "text", "transcripts", "signin", "--user-agreed")
		if code != 0 {
			t.Fatalf("exit %d: %s", code, errOut)
		}
		checkGolden(t, "transcripts_signin."+suffix, out+"--- stderr ---\n"+errOut)
	}
	code, out, errOut := e.tr("--json", "transcripts", "signin", "--user-agreed")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	checkGolden(t, "transcripts_signin.json", jsonGolden(t, out))
}

func TestOpenBrowserFakeExecutable(t *testing.T) {
	// The real launch path with the fake browser: a page comes back, and Close ends it.
	exe := browsertest.FakeBrowser(t)
	profile := filepath.Join(t.TempDir(), "archive", "browser")
	page, closer, err := openBrowser(context.Background(), browser.LaunchOptions{Exe: exe, Kind: browser.KindCustom, Profile: profile, Headless: true, Timeout: 20 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if h, err := page.Host(context.Background()); err != nil || h != "complete" {
		t.Fatalf("host %q, %v", h, err)
	}
	if err := closer.Close(); err != nil {
		t.Fatal(err)
	}
	// The tab cannot be reached: the browser is closed, and the error says so.
	tab := &tabless{}
	old := launchBrowser
	launchBrowser = func(context.Context, browser.LaunchOptions) (launchedBrowser, error) { return tab, nil }
	t.Cleanup(func() { launchBrowser = old })
	_, _, err = openBrowser(context.Background(), browser.LaunchOptions{})
	var c *errs.Coded
	if !errors.As(err, &c) || c.Code != errs.CodeBrowserFailed || tab.closed != 1 {
		t.Fatalf("err %v, closed %d", err, tab.closed)
	}
}

type tabless struct{ closed int }

func (b *tabless) Page(context.Context) (*browser.Page, error) {
	return nil, errors.New("connection refused")
}
func (b *tabless) Close() error { b.closed++; return nil }

func TestTranscriptsFetchHelp(t *testing.T) {
	texts := helpTexts(t)
	for where, want := range map[string]string{
		"transcripts fetch":  "Fetch the transcripts of a meeting's parts from SharePoint and store them, so later reads are offline.",
		"transcripts signin": "Open m365crawl's browser profile in a visible window once, so you can sign in to SharePoint; transcripts fetch then signs in silently.",
	} {
		if !strings.HasPrefix(texts[where], want) {
			t.Errorf("%s: %q", where, texts[where])
		}
	}
	for _, flag := range []string{"transcripts fetch --since", "transcripts fetch --limit", "transcripts fetch --refetch", "transcripts fetch --browser", "transcripts fetch --timeout",
		"transcripts signin --host", "transcripts signin --browser", "transcripts signin --user-agreed"} {
		if texts[flag] == "" {
			t.Errorf("no %s", flag)
		}
	}
	if texts["transcripts --browser"] != "" || texts["transcripts show --browser"] != "" {
		t.Error("--browser belongs to fetch and signin only")
	}
	e := textEnv(t)
	code, out, _ := e.run("transcripts", "signin", "--help", "--format", "text")
	flat := strings.Join(strings.Fields(out), " ")
	if code != 0 || !strings.Contains(flat, "Ask the user before running `m365crawl transcripts signin`: it opens a visible Edge window where they sign in to Microsoft 365 once. Run it only after they say yes.") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

func TestSigninHostMustBeSharePoint(t *testing.T) {
	e := trEnv(t)
	fakeBrowser(t, nil)
	er := trFails(t, e, errs.CodeUsage, errs.ExitUsage, "transcripts", "signin", "--user-agreed", "--host", "attacker.example")
	if !strings.Contains(er["message"].(string), "not a SharePoint host") {
		t.Fatalf("error %v", er)
	}
	// A host the archive names is used only when it is a SharePoint host.
	e.exec(`update transcript_parts set host='attacker.example'`)
	er = trFails(t, e, errs.CodeUsage, errs.ExitUsage, "transcripts", "signin", "--user-agreed")
	if !strings.Contains(er["message"].(string), "--host") {
		t.Fatalf("error %v", er)
	}
}

func TestSigninDefaultHostIsValidated(t *testing.T) {
	old := signinHosts
	signinHosts = func(*store.Store, context.Context) ([]string, error) { return []string{"bad host/"}, nil }
	t.Cleanup(func() { signinHosts = old })
	e := trEnv(t)
	fakeBrowser(t, nil)
	trFails(t, e, errs.CodeUsage, errs.ExitUsage, "transcripts", "signin", "--user-agreed")
}

func TestSigninWaitsPastTheSignInPages(t *testing.T) {
	e := trEnv(t)
	page := newTrPage()
	page.paths = []string{"/_layouts/15/Authenticate.aspx", "/_forms/default.aspx", "/_layouts/15/AccessDenied.aspx", "/"}
	page.probes = []bool{false, true}
	closer, _ := fakeBrowser(t, page)
	code, _, errOut := e.tr("--json", "transcripts", "signin", "--user-agreed")
	if code != 0 || closer.n.Load() != 1 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if page.nPaths != 5 || page.nProbes != 2 {
		t.Fatalf("pathname asked %d times, probe %d times", page.nPaths, page.nProbes)
	}
	if page.noDeadline != 0 {
		t.Fatalf("%d host questions had no deadline", page.noDeadline)
	}
	// The probe itself returns a boolean only, from the page's own API.
	for _, want := range []string{`fetch("/_api/web?$select=Id"`, `credentials: "include"`, `cache: "no-store"`, "r.status === 200"} {
		if !strings.Contains(signinProbeJS, want) {
			t.Errorf("probe lacks %s", want)
		}
	}
}

func TestSigninPageQuestionsFail(t *testing.T) {
	// The browser goes away between the host and the probe: the same closed-window failure.
	e := trEnv(t)
	page := &evalFails{trPage: newTrPage(), err: fmt.Errorf("%w: closed", browser.ErrDisconnected)}
	fakeBrowserDriver(t, page)
	er := trFails(t, e, errs.CodeBrowserFailed, errs.ExitRuntime, "transcripts", "signin", "--user-agreed")
	if !strings.Contains(er["message"].(string), "closed before sign-in finished") {
		t.Fatalf("error %v", er)
	}
	page.failProbe = true
	trFails(t, e, errs.CodeBrowserFailed, errs.ExitRuntime, "transcripts", "signin", "--user-agreed")
	// A page that is still loading only keeps the wait going.
	old := signinWait
	signinWait = 30 * time.Millisecond
	t.Cleanup(func() { signinWait = old })
	page.err = errors.New("Execution context was destroyed")
	er = trFails(t, e, errs.CodeBrowserFailed, errs.ExitRuntime, "transcripts", "signin", "--user-agreed")
	if !strings.Contains(er["message"].(string), "did not finish within") {
		t.Fatalf("error %v", er)
	}
}

// evalFails answers the host, then fails the pathname question (or the probe).
type evalFails struct {
	*trPage
	failProbe bool
	err       error
}

func (p *evalFails) Eval(ctx context.Context, expr string, out any) error {
	if expr == "location.pathname" && !p.failProbe || expr == signinProbeJS {
		return p.err
	}
	return p.trPage.Eval(ctx, expr, out)
}

func fakeBrowserDriver(t *testing.T, page transcripts.PageDriver) {
	t.Helper()
	fakeBrowser(t, newTrPage())
	openBrowser = func(ctx context.Context, o browser.LaunchOptions) (transcripts.PageDriver, io.Closer, error) {
		_ = page.Navigate(ctx, o.StartURL)
		return page, &countCloser{}, nil
	}
}

func TestFetchCommandBrowserGone(t *testing.T) {
	e := trEnv(t)
	page := newTrPage()
	page.answer["P3"] = func(context.Context) (transcripts.ScriptResult, error) {
		return transcripts.ScriptResult{}, fmt.Errorf("%w: websocket closed", browser.ErrDisconnected)
	}
	closer, _ := fakeBrowser(t, page)
	er := trFails(t, e, errs.CodeBrowserFailed, errs.ExitRuntime, "transcripts", "fetch", "call-1")
	if closer.n.Load() != 1 || !strings.Contains(er["message"].(string), "stopped answering") {
		t.Fatalf("error %v, closed %d", er, closer.n.Load())
	}
}

func TestFetchClosesBrowserBeforeWaitingForTheLock(t *testing.T) {
	// The archive is busy for the whole fetch and frees up only when the browser closes: the
	// results must still be saved, so the wait for the lock comes after the browser is closed.
	e := trEnv(t)
	release, err := store.AcquireLock(e.db)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	oldPoll, oldWait := fetchLockPoll, fetchLockWait
	fetchLockPoll, fetchLockWait = 10*time.Millisecond, 2*time.Second
	t.Cleanup(func() { fetchLockPoll, fetchLockWait = oldPoll, oldWait })
	fakeBrowser(t, newTrPage())
	inner := openBrowser
	openBrowser = func(ctx context.Context, o browser.LaunchOptions) (transcripts.PageDriver, io.Closer, error) {
		p, _, err := inner(ctx, o)
		return p, closerFunc(func() error { release(); return nil }), err
	}
	m := trJSON(t, e, "transcripts", "fetch", "call-4")
	if m["fetched"] != float64(1) {
		t.Fatalf("result %v", m)
	}
	if s := trJSON(t, e, "transcripts", "call-4"); items(t, s)[0]["state"] != "fetched" {
		t.Fatalf("not saved: %v", s)
	}
}

type closerFunc func() error

func (f closerFunc) Close() error { return f() }

func TestFetchLockNeverFreedAfterClose(t *testing.T) {
	e := trEnv(t)
	release, err := store.AcquireLock(e.db)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	oldPoll, oldWait := fetchLockPoll, fetchLockWait
	fetchLockPoll, fetchLockWait = 10*time.Millisecond, 50*time.Millisecond
	t.Cleanup(func() { fetchLockPoll, fetchLockWait = oldPoll, oldWait })
	closer, _ := fakeBrowser(t, newTrPage())
	er := trFails(t, e, errs.CodeLocked, errs.ExitLocked, "transcripts", "fetch", "call-4")
	if !strings.Contains(er["message"].(string), "1 fetched parts were not saved") || closer.n.Load() != 1 {
		t.Fatalf("error %v, closed %d", er, closer.n.Load())
	}
}

func TestFetchCommandCancelledWhileLaunching(t *testing.T) {
	e := trEnv(t)
	fakeBrowser(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	openBrowser = func(c context.Context, _ browser.LaunchOptions) (transcripts.PageDriver, io.Closer, error) {
		cancel()
		return nil, nil, c.Err()
	}
	var out, errb bytes.Buffer
	code := runCLI(ctx, []string{"--db", e.db, "--teams-root", e.root, "--max-age", "0", "--json", "transcripts", "fetch", "call-4"}, &out, &errb)
	if code != errs.ExitRuntime || errorOf(t, errb.String())["code"] != errs.CodeInterrupted {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
}
