package transcripts

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/errs"
)

var update = flag.Bool("update", false, "rewrite golden files")

// fakePage is a PageDriver that runs no JavaScript: Eval decodes the script's arguments and
// answers with the result the test scripted for the part's item id.
type fakePage struct {
	mu          sync.Mutex
	host        string
	landOn      map[string]string // navigated host -> host the tab ends on; default: itself
	landOnURL   map[string]string // navigated URL -> host the tab ends on; checked before landOn
	navErr      map[string]error
	onNavigate  func()
	onHost      func()
	hostErr     error
	results     map[string]func(ctx context.Context) (ScriptResult, error)
	navigations []string
	evals       []ScriptArgs
	exprs       []string
}

func newFakePage() *fakePage {
	return &fakePage{landOn: map[string]string{}, landOnURL: map[string]string{}, navErr: map[string]error{}, results: map[string]func(context.Context) (ScriptResult, error){}}
}

func (f *fakePage) Navigate(ctx context.Context, raw string) error {
	if f.onNavigate != nil {
		f.onNavigate()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.navigations = append(f.navigations, raw)
	u, _ := url.Parse(raw)
	f.host = u.Host
	if to, ok := f.landOnURL[raw]; ok {
		f.host = to
	} else if to, ok := f.landOn[u.Host]; ok {
		f.host = to
	}
	if err := f.navErr[u.Host]; err != nil {
		return err
	}
	return ctx.Err()
}

func (f *fakePage) Host(ctx context.Context) (string, error) {
	if f.onHost != nil {
		f.onHost()
		return "", ctx.Err()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.host, f.hostErr
}

func (f *fakePage) Eval(ctx context.Context, expr string, out any) error {
	prefix := "(" + Script() + ")("
	if !strings.HasPrefix(expr, prefix) || !strings.HasSuffix(expr, ")") {
		return fmt.Errorf("unexpected expression %.40q", expr)
	}
	var args ScriptArgs
	if err := json.Unmarshal([]byte(strings.TrimSuffix(strings.TrimPrefix(expr, prefix), ")")), &args); err != nil {
		return err
	}
	f.mu.Lock()
	f.evals = append(f.evals, args)
	f.exprs = append(f.exprs, expr)
	h := f.results[path.Base(args.Base)]
	f.mu.Unlock()
	res := ScriptResult{State: "ok", Status: 200, Entries: []RawEntry{{S: "Ada Example", B: "00:00:01", E: "00:00:02", T: "Hello."}}}
	if h != nil {
		var err error
		if res, err = h(ctx); err != nil {
			return err
		}
	}
	b, _ := json.Marshal(res)
	return json.Unmarshal(b, out)
}

func (f *fakePage) evalCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.evals)
}

func answer(r ScriptResult) func(context.Context) (ScriptResult, error) {
	return func(context.Context) (ScriptResult, error) { return r, nil }
}

// tpart is a fetchable part on host, numbered by its item id.
func tpart(host, item string, ordinal int) Part {
	return Part{AccountID: "t/u", CallID: "call-" + host[:1], PartKey: "d:b!d1/" + item, Ordinal: ordinal, Host: host, SiteRoot: "/teams/site-a",
		DriveID: "b!d1", ItemID: item, TranscriptID: "tr-" + item, RefQuality: RefDriveItem}
}

type saved struct {
	mu    sync.Mutex
	parts []string
	res   map[string]FetchResult
}

func (s *saved) save(p Part, r FetchResult) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.res == nil {
		s.res = map[string]FetchResult{}
	}
	s.parts = append(s.parts, p.ItemID)
	s.res[p.ItemID] = r
	return nil
}

var fixedNow = time.Date(2026, 11, 9, 8, 0, 0, 0, time.UTC)

func newFetcher(p PageDriver) *Fetcher {
	return &Fetcher{Page: p, Browser: "edge", Now: func() time.Time { return fixedNow }, LandTimeout: 200 * time.Millisecond, PartTimeout: time.Second}
}

func codeOf(err error) string {
	var c *errs.Coded
	if errors.As(err, &c) {
		return c.Code
	}
	return ""
}

func TestFetchScriptGolden(t *testing.T) {
	golden := filepath.Join("testdata", "fetch.js.golden")
	if *update {
		if err := os.WriteFile(golden, []byte(Script()+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden) //nolint:gosec // fixed testdata name
	if err != nil {
		t.Fatal(err)
	}
	if Script()+"\n" != string(want) {
		t.Fatalf("the in-page script changed; review it and run with -update\n%s", Script())
	}
	// The script never hands the temporary download URL back: it returns only states, counts and entries.
	for _, s := range []string{"temporaryDownloadUrl}", "url:", "u}", "href"} {
		if strings.Contains(Script(), s) {
			t.Errorf("the script may return a URL (%q)", s)
		}
	}
}

func TestFetchGroupsByHostNavigatesOnce(t *testing.T) {
	page := newFakePage()
	var parts []Part
	for i := 1; i <= 6; i++ {
		host := "a.sharepoint.example.invalid"
		if i%2 == 0 {
			host = "B.sharepoint.example.invalid" // hosts compare without case
		}
		parts = append(parts, tpart(host, fmt.Sprintf("I%d", i), i))
	}
	var s saved
	sum, err := newFetcher(page).Fetch(context.Background(), parts, s.save)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"https://a.sharepoint.example.invalid/teams/site-a/", "https://b.sharepoint.example.invalid/teams/site-a/"}
	if strings.Join(page.navigations, " ") != strings.Join(want, " ") {
		t.Fatalf("navigations %v", page.navigations)
	}
	if sum.Fetched != 6 || len(s.parts) != 6 || len(sum.Parts) != 6 {
		t.Fatalf("summary %+v, saved %v", sum, s.parts)
	}
	r := s.res["I2"]
	if r.State != StateOK || r.HTTPStatus != 200 || r.Browser != "edge" || !r.At.Equal(fixedNow) || len(r.Entries) != 1 || *r.Entries[0].StartMS != 1000 {
		t.Fatalf("result %+v", r)
	}
	for _, o := range sum.Parts {
		if !o.Saved || o.State != StateOK || o.Entries != 1 || !o.FetchedAt.Equal(fixedNow) || o.Reason != "" {
			t.Fatalf("outcome %+v", o)
		}
	}
	if a := page.evals[3]; a.Base != "https://b.sharepoint.example.invalid/teams/site-a/_api/v2.1/drives/b!d1/items/I2" || a.TranscriptID != "tr-I2" ||
		a.MaxBytes != DefaultMaxBytes || strings.Join(a.LoginHosts, ",") != strings.Join(LoginHosts, ",") {
		t.Fatalf("args %+v", a)
	}
}

func TestFetchLandsOnThePartsSite(t *testing.T) {
	// A OneDrive host's bare root sends the tab off the host (to the OneDrive web app or the
	// Microsoft 365 home); its personal site keeps the tab on it. The host is opened once, on the
	// first part's site, and every part on the host is fetched, whatever its own site.
	const host = "contoso-my.sharepoint.example.invalid"
	page := newFakePage()
	page.landOn[host] = "www.office.example.invalid"
	page.landOnURL["https://"+host+"/personal/ada_contoso_example/"] = host
	first := tpart(host, "I1", 1)
	first.SiteRoot, first.StorageKind = "/personal/ada_contoso_example", "MeetingOrganizerOneDrive"
	second := tpart(host, "I2", 2)
	second.SiteRoot, second.StorageKind = "/personal/bob_contoso_example", "MeetingCoOrganizerOneDrive"
	var s saved
	sum, err := newFetcher(page).Fetch(context.Background(), []Part{first, second}, s.save)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(page.navigations, " ") != "https://"+host+"/personal/ada_contoso_example/" {
		t.Fatalf("navigations %v", page.navigations)
	}
	if sum.Fetched != 2 || sum.Failed != 0 || s.res["I1"].State != StateOK || s.res["I2"].State != StateOK {
		t.Fatalf("summary %+v, saved %+v", sum, s.res)
	}
	if b := page.evals[1].Base; b != "https://"+host+"/personal/bob_contoso_example/_api/v2.1/drives/b!d1/items/I2" {
		t.Fatalf("base %q", b)
	}
	// Opening the bare root, as v0.6.0 did, would have left the tab elsewhere.
	if page.landOn[host] == host {
		t.Fatal("the fake must send the bare root off the host")
	}
}

func TestFetchLandsOnLogin(t *testing.T) {
	for _, login := range LoginHosts {
		page := newFakePage()
		page.landOn["a.sharepoint.example.invalid"] = login
		var s saved
		sum, err := newFetcher(page).Fetch(context.Background(), []Part{tpart("a.sharepoint.example.invalid", "I1", 1)}, s.save)
		if codeOf(err) != errs.CodeSigninRequired {
			t.Fatalf("%s: err %v", login, err)
		}
		if len(s.parts) != 0 || page.evalCount() != 0 || sum.Fetched != 0 {
			t.Fatalf("%s: saved %v, evals %d", login, s.parts, page.evalCount())
		}
	}
}

func TestFetchLandingWaitsForTheHost(t *testing.T) {
	// The tab passes through the login host and comes back: silent sign-in worked.
	page := newFakePage()
	page.landOn["a.sharepoint.example.invalid"] = "login.microsoftonline.com"
	f := newFetcher(page)
	f.LandTimeout = 5 * time.Second
	go func() {
		time.Sleep(100 * time.Millisecond)
		page.mu.Lock()
		page.host = "a.sharepoint.example.invalid"
		page.mu.Unlock()
	}()
	var s saved
	if _, err := f.Fetch(context.Background(), []Part{tpart("a.sharepoint.example.invalid", "I1", 1)}, s.save); err != nil || len(s.parts) != 1 {
		t.Fatalf("err %v, saved %v", err, s.parts)
	}
}

func TestFetchHostRedirectedElsewhere(t *testing.T) {
	// The part's own site sends the tab off the host: its parts fail without running the script.
	page := newFakePage()
	page.landOnURL["https://a.sharepoint.example.invalid/teams/site-a/"] = "other.example.invalid"
	page.navErr["c.sharepoint.example.invalid"] = errors.New("navigation failed: net::ERR_NAME_NOT_RESOLVED")
	page.landOn["c.sharepoint.example.invalid"] = "" // the tab stays on its blank page
	var s saved
	parts := []Part{tpart("a.sharepoint.example.invalid", "I1", 1), tpart("b.sharepoint.example.invalid", "I2", 2), tpart("c.sharepoint.example.invalid", "I3", 3)}
	sum, err := newFetcher(page).Fetch(context.Background(), parts, s.save)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Failed != 2 || sum.Fetched != 1 || s.res["I1"].State != StateFailed || s.res["I2"].State != StateOK || s.res["I3"].State != StateFailed {
		t.Fatalf("summary %+v, saved %v", sum, s.res)
	}
	if r := sum.Parts[0].Reason; r != "the SharePoint host redirected elsewhere" {
		t.Errorf("reason %q", r)
	}
	if r := sum.Parts[2].Reason; r != "the SharePoint host could not be reached" {
		t.Errorf("reason %q", r)
	}
	if page.evalCount() != 1 {
		t.Errorf("%d evals", page.evalCount())
	}
}

func TestFetchHostUnreadable(t *testing.T) {
	// The tab never reports a host (a page that keeps failing): the host's parts fail, nothing is signed out.
	page := newFakePage()
	page.hostErr = errors.New("no page")
	var s saved
	sum, err := newFetcher(page).Fetch(context.Background(), []Part{tpart("a.sharepoint.example.invalid", "I1", 1)}, s.save)
	if err != nil || sum.Failed != 1 || s.res["I1"].State != StateFailed {
		t.Fatalf("err %v, summary %+v", err, sum)
	}
}

func TestFetchRedirectedToLoginMidRun(t *testing.T) {
	page := newFakePage()
	page.results["I2"] = answer(ScriptResult{State: "signin", Status: 401})
	var s saved
	parts := []Part{tpart("a.sharepoint.example.invalid", "I1", 1), tpart("a.sharepoint.example.invalid", "I2", 2), tpart("a.sharepoint.example.invalid", "I3", 3)}
	sum, err := newFetcher(page).Fetch(context.Background(), parts, s.save)
	if codeOf(err) != errs.CodeSigninRequired {
		t.Fatalf("err %v", err)
	}
	if strings.Join(s.parts, ",") != "I1" || s.res["I1"].State != StateOK || sum.Fetched != 1 || page.evalCount() != 2 {
		t.Fatalf("saved %v, summary %+v, evals %d", s.parts, sum, page.evalCount())
	}
}

func TestFetchSigninProbe(t *testing.T) {
	// A metadata request that threw: the tab's host decides between a sign-in page and a failure.
	for _, c := range []struct {
		host, code string
		state      string
	}{
		{"login.microsoftonline.com", errs.CodeSigninRequired, ""},
		{"a.sharepoint.example.invalid", "", StateFailed},
	} {
		page := newFakePage()
		page.results["I1"] = func(context.Context) (ScriptResult, error) {
			page.mu.Lock()
			page.host = c.host
			page.mu.Unlock()
			return ScriptResult{State: "signin_probe"}, nil
		}
		var s saved
		_, err := newFetcher(page).Fetch(context.Background(), []Part{tpart("a.sharepoint.example.invalid", "I1", 1)}, s.save)
		if codeOf(err) != c.code || s.res["I1"].State != c.state {
			t.Fatalf("%s: err %v, saved %+v", c.host, err, s.res)
		}
	}
}

func TestFetchPartForbiddenContinues(t *testing.T) {
	page := newFakePage()
	page.results["I2"] = answer(ScriptResult{State: "no_access", Status: 403})
	var s saved
	parts := []Part{tpart("a.sharepoint.example.invalid", "I1", 1), tpart("a.sharepoint.example.invalid", "I2", 2), tpart("a.sharepoint.example.invalid", "I3", 3)}
	sum, err := newFetcher(page).Fetch(context.Background(), parts, s.save)
	if err != nil {
		t.Fatal(err)
	}
	if s.res["I2"].State != StateNoAccess || s.res["I2"].HTTPStatus != 403 || s.res["I3"].State != StateOK || sum.NoAccess != 1 || sum.Fetched != 2 {
		t.Fatalf("saved %+v, summary %+v", s.res, sum)
	}
	if o := sum.Parts[1]; o.Reason != Reason(o.Part, StateNoAccess) || !o.FetchedAt.IsZero() || o.HTTPStatus != 403 {
		t.Fatalf("outcome %+v", o)
	}
}

// fetchOne fetches one part whose script answers r and returns what was saved and the summary.
func fetchOne(t *testing.T, r ScriptResult) (FetchResult, FetchSummary) {
	t.Helper()
	page := newFakePage()
	page.results["I1"] = answer(r)
	var s saved
	sum, err := newFetcher(page).Fetch(context.Background(), []Part{tpart("a.sharepoint.example.invalid", "I1", 1)}, s.save)
	if err != nil {
		t.Fatal(err)
	}
	return s.res["I1"], sum
}

func TestFetchNotFound(t *testing.T) {
	if r, sum := fetchOne(t, ScriptResult{State: "not_found", Status: 404}); r.State != StateNotFound || sum.NotFound != 1 {
		t.Fatalf("%+v %+v", r, sum)
	}
}

func TestFetchNoTranscript(t *testing.T) {
	if r, sum := fetchOne(t, ScriptResult{State: "no_transcript", Status: 200}); r.State != StateNoTranscript || sum.NoTranscript != 1 {
		t.Fatalf("%+v %+v", r, sum)
	}
}

func TestFetchTooLarge(t *testing.T) {
	r, sum := fetchOne(t, ScriptResult{State: "too_large", Status: 200, Bytes: 40 << 20})
	if r.State != StateTooLarge || sum.TooLarge != 1 || len(r.Entries) != 0 {
		t.Fatalf("%+v %+v", r, sum)
	}
}

func TestFetchResultTable(t *testing.T) {
	for _, c := range []struct {
		res    ScriptResult
		state  string
		status int
		count  func(FetchSummary) int
	}{
		{ScriptResult{State: "ok", Status: 200, Entries: []RawEntry{{S: "Ada Example", B: "00:00:01", T: "x"}}}, StateOK, 200, func(s FetchSummary) int { return s.Fetched }},
		{ScriptResult{State: "no_access", Status: 403}, StateNoAccess, 403, func(s FetchSummary) int { return s.NoAccess }},
		{ScriptResult{State: "not_found", Status: 404}, StateNotFound, 404, func(s FetchSummary) int { return s.NotFound }},
		{ScriptResult{State: "failed", Status: 500}, StateFailed, 500, func(s FetchSummary) int { return s.Failed }},
		{ScriptResult{State: "failed", Error: "TypeError"}, StateFailed, 0, func(s FetchSummary) int { return s.Failed }},
		{ScriptResult{State: "no_transcript", Status: 200}, StateNoTranscript, 200, func(s FetchSummary) int { return s.NoTranscript }},
		{ScriptResult{State: "too_large", Status: 200, Bytes: 1}, StateTooLarge, 200, func(s FetchSummary) int { return s.TooLarge }},
		{ScriptResult{State: "something_new", Status: 200}, StateFailed, 200, func(s FetchSummary) int { return s.Failed }},
	} {
		r, sum := fetchOne(t, c.res)
		if r.State != c.state || r.HTTPStatus != c.status || c.count(sum) != 1 {
			t.Errorf("%+v: saved %+v, summary %+v", c.res, r, sum)
		}
	}
	// 401 and an HTML answer are sign-in pages: nothing is stored.
	for _, res := range []ScriptResult{{State: "signin", Status: 401}, {State: "signin", Status: 200}} {
		page := newFakePage()
		page.results["I1"] = answer(res)
		var s saved
		if _, err := newFetcher(page).Fetch(context.Background(), []Part{tpart("a.sharepoint.example.invalid", "I1", 1)}, s.save); codeOf(err) != errs.CodeSigninRequired || len(s.parts) != 0 {
			t.Errorf("%+v: err %v, saved %v", res, err, s.parts)
		}
	}
}

func TestFetchEvalFailure(t *testing.T) {
	page := newFakePage()
	page.results["I1"] = func(context.Context) (ScriptResult, error) {
		return ScriptResult{}, errors.New("the page script threw TypeError")
	}
	var s saved
	sum, err := newFetcher(page).Fetch(context.Background(), []Part{tpart("a.sharepoint.example.invalid", "I1", 1), tpart("a.sharepoint.example.invalid", "I2", 2)}, s.save)
	if err != nil || s.res["I1"].State != StateFailed || s.res["I2"].State != StateOK || sum.Failed != 1 {
		t.Fatalf("err %v, saved %+v", err, s.res)
	}
	if r := sum.Parts[0].Reason; r != "the page script failed" {
		t.Errorf("reason %q", r)
	}
}

func TestFetchPicksTranscriptByID(t *testing.T) {
	page := newFakePage()
	var s saved
	p := tpart("a.sharepoint.example.invalid", "I1", 1)
	p.TranscriptID = "tr-chosen"
	if _, err := newFetcher(page).Fetch(context.Background(), []Part{p}, s.save); err != nil {
		t.Fatal(err)
	}
	if page.evals[0].TranscriptID != "tr-chosen" {
		t.Fatalf("args %+v", page.evals[0])
	}
	// The script picks the transcript with that id, and the only one only when there is one.
	if !strings.Contains(Script(), "ts.find(t => t.id === transcriptId) || (ts.length === 1 ? ts[0] : null)") {
		t.Fatal("the script no longer picks the transcript by id")
	}
}

func TestFetchArgsJSONEncoded(t *testing.T) {
	page := newFakePage()
	p := tpart("a.sharepoint.example.invalid", "01AB!x_y.z%2D-9", 1)
	p.TranscriptID = "tr!%2F_.-"
	var s saved
	if _, err := newFetcher(page).Fetch(context.Background(), []Part{p}, s.save); err != nil {
		t.Fatal(err)
	}
	want, _ := json.Marshal(ScriptArgs{Base: "https://a.sharepoint.example.invalid/teams/site-a/_api/v2.1/drives/b!d1/items/01AB!x_y.z%2D-9",
		TranscriptID: "tr!%2F_.-", MaxBytes: DefaultMaxBytes, LoginHosts: LoginHosts})
	if page.exprs[0] != "("+Script()+")("+string(want)+")" {
		t.Fatalf("expression %q", page.exprs[0])
	}
	if ScriptExpr(ScriptArgs{Base: "\"); alert(1); (\""}) == "("+Script()+")({\"base\":\"\"); alert(1); (\"\"" {
		t.Fatal("an argument escaped its JSON string")
	}
	if e := ScriptExpr(ScriptArgs{Base: "</script>\u2028\""}); !strings.Contains(e, `"base":"\u003c/script\u003e\u2028\""`) {
		t.Fatalf("not JSON-encoded: %s", e)
	}
}

func TestFetchSkipsMalformedRef(t *testing.T) {
	page := newFakePage()
	bad := tpart("a.sharepoint.example.invalid", "I1", 1)
	bad.Host = `a.example.invalid"/x`
	share := tpart("a.sharepoint.example.invalid", "I2", 2)
	share.RefQuality = RefShareOnly
	var s saved
	sum, err := newFetcher(page).Fetch(context.Background(), []Part{bad, share}, s.save)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.navigations) != 0 || len(s.parts) != 0 || sum.Unfetchable != 2 {
		t.Fatalf("navigations %v, saved %v, summary %+v", page.navigations, s.parts, sum)
	}
	if o := sum.Parts[0]; o.State != StateUnfetchable || o.Reason != "the cached file reference is malformed" {
		t.Fatalf("outcome %+v", o)
	}
}

func TestFetchPartTimeout(t *testing.T) {
	page := newFakePage()
	page.results["I1"] = func(ctx context.Context) (ScriptResult, error) {
		<-ctx.Done()
		return ScriptResult{}, ctx.Err()
	}
	f := newFetcher(page)
	f.PartTimeout = 50 * time.Millisecond
	var s saved
	sum, err := f.Fetch(context.Background(), []Part{tpart("a.sharepoint.example.invalid", "I1", 1), tpart("a.sharepoint.example.invalid", "I2", 2)}, s.save)
	if err != nil {
		t.Fatal(err)
	}
	if s.res["I1"].State != StateFailed || s.res["I2"].State != StateOK || sum.Failed != 1 || sum.Parts[0].Reason != "the fetch of this part timed out" {
		t.Fatalf("saved %+v, summary %+v", s.res, sum)
	}
}

func TestFetchCancelledClosesBrowser(t *testing.T) {
	// Cancelled during part 2: Fetch returns the cancellation at once and asks for nothing more,
	// so the caller's deferred Close ends the browser.
	page := newFakePage()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	page.results["I2"] = func(c context.Context) (ScriptResult, error) {
		cancel()
		<-c.Done()
		return ScriptResult{}, c.Err()
	}
	var s saved
	parts := []Part{tpart("a.sharepoint.example.invalid", "I1", 1), tpart("a.sharepoint.example.invalid", "I2", 2), tpart("a.sharepoint.example.invalid", "I3", 3)}
	_, err := newFetcher(page).Fetch(ctx, parts, s.save)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v", err)
	}
	if strings.Join(s.parts, ",") != "I1" || page.evalCount() != 2 {
		t.Fatalf("saved %v, evals %d", s.parts, page.evalCount())
	}
	// Cancelled before a host is opened, and while it is opened.
	if _, err := newFetcher(newFakePage()).Fetch(ctx, parts, s.save); !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v", err)
	}
	slow := newFakePage()
	slow.landOn["a.sharepoint.example.invalid"] = "login.microsoftonline.com"
	ctx2, cancel2 := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel2)
	f := newFetcher(slow)
	f.LandTimeout = 5 * time.Second
	if _, err := f.Fetch(ctx2, parts, s.save); !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v", err)
	}
}

func TestFetchCancelledDuringNavigation(t *testing.T) {
	page := newFakePage()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := newFetcher(page)
	page.onNavigate = cancel
	if _, err := f.Fetch(ctx, []Part{tpart("a.sharepoint.example.invalid", "I1", 1)}, (&saved{}).save); !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v", err)
	}
}

func TestFetchSavesAfterLockFreed(t *testing.T) {
	page := newFakePage()
	var s saved
	busy := 3
	save := func(p Part, r FetchResult) error {
		if busy > 0 {
			busy--
			return errs.Locked("another m365crawl run holds the archive lock")
		}
		return s.save(p, r)
	}
	f := newFetcher(page)
	f.LockWait = 5 * time.Second
	f.LockPoll = 10 * time.Millisecond
	parts := []Part{tpart("a.sharepoint.example.invalid", "I1", 1), tpart("a.sharepoint.example.invalid", "I2", 2)}
	sum, err := f.Fetch(context.Background(), parts, save)
	if err != nil || sum.Parts[0].Saved {
		t.Fatalf("err %v; the archive is still busy: %+v", err, sum)
	}
	sum, err = f.FinishSaves(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(s.parts, ",") != "I1,I2" || !sum.Parts[0].Saved || !sum.Parts[1].Saved {
		t.Fatalf("saved %v, summary %+v", s.parts, sum)
	}
}

func TestFetchLockNeverFreed(t *testing.T) {
	page := newFakePage()
	f := newFetcher(page)
	f.LockWait = 50 * time.Millisecond
	f.LockPoll = 10 * time.Millisecond
	locked := func(Part, FetchResult) error { return errs.Locked("held") }
	if _, err := f.Fetch(context.Background(), []Part{tpart("a.sharepoint.example.invalid", "I1", 1), tpart("a.sharepoint.example.invalid", "I2", 2)}, locked); err != nil {
		t.Fatal(err)
	}
	sum, err := f.FinishSaves(context.Background())
	var c *errs.Coded
	if !errors.As(err, &c) || c.Code != errs.CodeLocked || !strings.Contains(c.Message, "2 fetched parts were not saved") {
		t.Fatalf("err %v", err)
	}
	if sum.Parts[0].Saved || sum.Fetched != 2 {
		t.Fatalf("summary %+v", sum)
	}
	// Interrupted while waiting for the lock.
	ctx, cancel := context.WithCancel(context.Background())
	f.LockWait = 5 * time.Second
	time.AfterFunc(50*time.Millisecond, cancel)
	if _, err := f.Fetch(ctx, []Part{tpart("a.sharepoint.example.invalid", "I1", 1)}, locked); err != nil {
		t.Fatal(err)
	}
	if _, err := f.FinishSaves(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v", err)
	}
	// Nothing waiting: nothing to do.
	if _, err := (&Fetcher{}).FinishSaves(ctx); err != nil {
		t.Fatalf("err %v", err)
	}
}

func TestFetchSaveErrorStops(t *testing.T) {
	page := newFakePage()
	boom := errors.New("disk full")
	_, err := newFetcher(page).Fetch(context.Background(), []Part{tpart("a.sharepoint.example.invalid", "I1", 1), tpart("a.sharepoint.example.invalid", "I2", 2)},
		func(Part, FetchResult) error { return boom })
	if !errors.Is(err, boom) || page.evalCount() != 1 {
		t.Fatalf("err %v, evals %d", err, page.evalCount())
	}
	// A save that fails while the waiting results are saved is reported too.
	page = newFakePage()
	page.results["I2"] = answer(ScriptResult{State: "signin"})
	n := 0
	f := newFetcher(page)
	_, err = f.Fetch(context.Background(), []Part{tpart("a.sharepoint.example.invalid", "I1", 1), tpart("a.sharepoint.example.invalid", "I2", 2)},
		func(Part, FetchResult) error {
			n++
			if n == 1 {
				return errs.Locked("held")
			}
			return boom
		})
	if codeOf(err) != errs.CodeSigninRequired {
		t.Fatalf("err %v", err)
	}
	if _, err := f.FinishSaves(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("err %v", err)
	}
}

func TestFetchSigninKeepsPendingSaves(t *testing.T) {
	// A part fetched while the archive was locked is still saved when a later part meets sign-in.
	page := newFakePage()
	page.results["I2"] = answer(ScriptResult{State: "signin"})
	var s saved
	busy := 1
	save := func(p Part, r FetchResult) error {
		if busy > 0 {
			busy--
			return errs.Locked("held")
		}
		return s.save(p, r)
	}
	f := newFetcher(page)
	f.LockPoll = time.Millisecond
	_, err := f.Fetch(context.Background(), []Part{tpart("a.sharepoint.example.invalid", "I1", 1), tpart("a.sharepoint.example.invalid", "I2", 2)}, save)
	if codeOf(err) != errs.CodeSigninRequired || len(s.parts) != 0 {
		t.Fatalf("err %v, saved %v", err, s.parts)
	}
	if _, err := f.FinishSaves(context.Background()); err != nil || strings.Join(s.parts, ",") != "I1" {
		t.Fatalf("err %v, saved %v", err, s.parts)
	}
}

func TestFetchProgress(t *testing.T) {
	page := newFakePage()
	page.results["I2"] = answer(ScriptResult{State: "no_access", Status: 403})
	var lines []string
	f := newFetcher(page)
	f.Progress = func(s string) { lines = append(lines, s) }
	parts := []Part{tpart("a.sharepoint.example.invalid", "I1", 1), tpart("b.sharepoint.example.invalid", "I2", 2)}
	if _, err := f.Fetch(context.Background(), parts, (&saved{}).save); err != nil {
		t.Fatal(err)
	}
	want := []string{"opening SharePoint site 1 of 2", "part 1 of 2 fetched: ok, 1 entry", "opening SharePoint site 2 of 2", "part 2 of 2 fetched: no_access"}
	if strings.Join(lines, "|") != strings.Join(want, "|") {
		t.Fatalf("progress %q", lines)
	}
}

func TestFetcherDefaults(t *testing.T) {
	f := &Fetcher{}
	f.defaults()
	if f.LandTimeout != 30*time.Second || f.PartTimeout != 60*time.Second || f.MaxBytes != 32<<20 || f.LockWait != 2*time.Minute || f.Now == nil || f.LockPoll <= 0 {
		t.Fatalf("defaults %+v", f)
	}
	for _, h := range LoginHosts {
		if !IsLoginHost(strings.ToUpper(h)) {
			t.Errorf("%s is a login host", h)
		}
	}
	if IsLoginHost("a.sharepoint.example.invalid") {
		t.Error("a SharePoint host is not a login host")
	}
}

func TestFetchCancelledDuringSigninProbe(t *testing.T) {
	page := newFakePage()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	page.results["I1"] = func(context.Context) (ScriptResult, error) {
		page.onHost = cancel
		return ScriptResult{State: "signin_probe"}, nil
	}
	var s saved
	if _, err := newFetcher(page).Fetch(ctx, []Part{tpart("a.sharepoint.example.invalid", "I1", 1)}, s.save); !errors.Is(err, context.Canceled) || len(s.parts) != 0 {
		t.Fatalf("err %v, saved %v", err, s.parts)
	}
}

func TestFetchSlowLoadStillAsksTheHost(t *testing.T) {
	// Loading the page used the whole wait and failed, and the tab is on a sign-in page.
	page := newFakePage()
	page.landOn["a.sharepoint.example.invalid"] = "login.microsoftonline.com"
	f := newFetcher(page)
	f.LandTimeout = 30 * time.Millisecond
	page.onNavigate = func() { time.Sleep(50 * time.Millisecond) }
	page.navErr["a.sharepoint.example.invalid"] = context.DeadlineExceeded
	if _, err := f.Fetch(context.Background(), []Part{tpart("a.sharepoint.example.invalid", "I1", 1)}, (&saved{}).save); codeOf(err) != errs.CodeSigninRequired {
		t.Fatalf("err %v", err)
	}
}

func TestFetchNavigationErrorThenLogin(t *testing.T) {
	// The navigation reports an error, yet the tab ends on the sign-in page: that is a sign-in stop.
	page := newFakePage()
	page.landOn["a.sharepoint.example.invalid"] = "login.microsoftonline.com"
	page.navErr["a.sharepoint.example.invalid"] = errors.New("navigation failed: net::ERR_ABORTED")
	if _, err := newFetcher(page).Fetch(context.Background(), []Part{tpart("a.sharepoint.example.invalid", "I1", 1)}, (&saved{}).save); codeOf(err) != errs.CodeSigninRequired {
		t.Fatalf("err %v", err)
	}
}

func TestFetchNavigationErrorThenTarget(t *testing.T) {
	// The navigation reports an error, yet the tab ends on the host: the parts are fetched.
	page := newFakePage()
	page.navErr["a.sharepoint.example.invalid"] = errors.New("navigation failed: net::ERR_ABORTED")
	var s saved
	if _, err := newFetcher(page).Fetch(context.Background(), []Part{tpart("a.sharepoint.example.invalid", "I1", 1)}, s.save); err != nil || s.res["I1"].State != StateOK {
		t.Fatalf("err %v, saved %+v", err, s.res)
	}
}

var errGone = errors.New("the browser connection closed")

func TestFetchBrowserGoneIsABrowserFailure(t *testing.T) {
	gone := func(err error) bool { return errors.Is(err, errGone) }
	// While a part runs.
	page := newFakePage()
	page.results["I2"] = func(context.Context) (ScriptResult, error) { return ScriptResult{}, errGone }
	var s saved
	f := newFetcher(page)
	f.Gone = gone
	parts := []Part{tpart("a.sharepoint.example.invalid", "I1", 1), tpart("a.sharepoint.example.invalid", "I2", 2), tpart("a.sharepoint.example.invalid", "I3", 3)}
	if _, err := f.Fetch(context.Background(), parts, s.save); codeOf(err) != errs.CodeBrowserFailed || strings.Join(s.parts, ",") != "I1" {
		t.Fatalf("err %v, saved %v", err, s.parts)
	}
	// While a host is opened, and while the host is asked.
	page = newFakePage()
	page.navErr["a.sharepoint.example.invalid"] = errGone
	f = newFetcher(page)
	f.Gone = gone
	if _, err := f.Fetch(context.Background(), parts, s.save); codeOf(err) != errs.CodeBrowserFailed {
		t.Fatalf("err %v", err)
	}
	page = newFakePage()
	page.hostErr = errGone
	f = newFetcher(page)
	f.Gone = gone
	if _, err := f.Fetch(context.Background(), parts, s.save); codeOf(err) != errs.CodeBrowserFailed {
		t.Fatalf("err %v", err)
	}
	// While the sign-in probe asks the host.
	page = newFakePage()
	page.results["I1"] = func(context.Context) (ScriptResult, error) {
		page.mu.Lock()
		page.hostErr = errGone
		page.mu.Unlock()
		return ScriptResult{State: "signin_probe"}, nil
	}
	f = newFetcher(page)
	f.Gone = gone
	if _, err := f.Fetch(context.Background(), parts[:1], s.save); codeOf(err) != errs.CodeBrowserFailed {
		t.Fatalf("err %v", err)
	}
}

func TestFetchScriptChecksContentLength(t *testing.T) {
	if !strings.Contains(Script(), `Number(r.headers.get("content-length") || 0)`) || !strings.Contains(Script(), "buf.byteLength > maxBytes") {
		t.Fatal("the script must refuse an oversize download by its header and by its size")
	}
}
