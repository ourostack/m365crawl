package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/syncer"
	"github.com/ourostack/m365crawl/internal/workiq"
)

// Test seams of the route's archive reads.
var (
	workIQMailComplete     = (*store.Store).WorkIQMailComplete
	workIQMailBindable     = (*store.Store).WorkIQMailBindable
	mailCoverageOf         = (*store.Store).MailCoverage
	workIQMailDiskEstimate = (*store.Store).WorkIQMailDiskEstimate
	workIQRouteFreeBytes   = store.FreeBytes
	workIQMailWindowRoute  = (*store.Store).WorkIQMailWindowRoute
)

// Reasons a read routes to WorkIQ.
const (
	routeRangeBeforeCache  = "range_before_cache"
	routeParentNotArchived = "parent_not_archived"
	// routeRangeNotCovered: the read's range_coverage says the archive does not hold all of the
	// range, inside what the apps cached as well as before it.
	routeRangeNotCovered = "range_not_covered"
)

// workIQRoute tells an agent which explicit WorkIQ commands reach history the apps did not cache.
// The read itself stays cache-only: building a route never starts WorkIQ.
type workIQRoute struct {
	Source           string        `json:"source"`
	Account          string        `json:"account,omitempty"`
	Reason           string        `json:"reason"`
	CacheCoversSince *time.Time    `json:"cache_covers_since,omitempty"`
	Folders          []routeFolder `json:"folders,omitempty"`
	WorkIQ           string        `json:"workiq"`
	Fix              string        `json:"fix"`
	Next             []startStep   `json:"next"`
}

// routeFolder is a mail folder whose cached mail starts after the read's start.
type routeFolder struct {
	Folder           string     `json:"folder"`
	Kind             string     `json:"kind"`
	CacheCoversSince *time.Time `json:"cache_covers_since"`
	key              uint32
}

// workIQSetup is what a route needs to know about WorkIQ on this machine and archive.
type workIQSetup struct{ installed, enabled bool }

func (rt *runtime) workIQSetup(st *store.Store) (workIQSetup, error) {
	enabled, err := st.WorkIQEnabled(rt.ctx)
	return workIQSetup{installed: workiq.Locate() == nil, enabled: enabled}, err
}

func (s workIQSetup) state() string {
	switch {
	case !s.installed:
		return "not_installed"
	case s.enabled:
		return "enabled"
	}
	return "disabled"
}

// newWorkIQRoute starts a route with the steps every source shares: check WorkIQ, then enable it
// for this archive when it is off.
func (rt *runtime) newWorkIQRoute(s workIQSetup, source, account, reason, why string, since time.Time) workIQRoute {
	r := workIQRoute{Source: source, Account: account, Reason: reason, CacheCoversSince: tp(since), WorkIQ: s.state(), Fix: why}
	if !s.installed {
		r.Fix += " WorkIQ is not installed: a person must install WorkIQ, sign in and accept its EULA through WorkIQ once; m365crawl never does this. Then run these steps."
	}
	r.Next = append(r.Next, startStep{Command: rt.recoveryCommand(false, account, "workiq", "status", "--source", source),
		Does: "check that WorkIQ is installed, signed in and can read this account"})
	if !s.enabled {
		r.Next = append(r.Next, startStep{Command: rt.recoveryCommand(false, "", "workiq", "enable"),
			Does: "turn on the optional WorkIQ source for this archive"})
	}
	return r
}

// repeatStep is the read that produced the route, run again once WorkIQ has added older history.
func (rt *runtime) repeatStep() startStep {
	words := []string{"m365crawl"}
	for _, arg := range rt.args {
		words = append(words, quoteArg(arg))
	}
	return startStep{Command: strings.Join(words, " "), Does: "repeat this read, now with the mail WorkIQ added"}
}

// mailRoute routes one Outlook account to a WorkIQ mail sync and then the same read, unless a
// complete WorkIQ mail sync already retains the account's server mail (ok is false then). situation
// says why the read needs server mail; the route adds what to do about it.
func (rt *runtime) mailRoute(st *store.Store, account, reason, situation string) (r workIQRoute, ok bool, err error) {
	setup, ok, err := rt.mailRouteSetup(st, account)
	if err != nil || !ok {
		return r, false, err
	}
	return rt.wholeAccountRoute(st, setup, account, reason, situation)
}

// mailRouteSetup says whether an account gets a mail route at all: not once a complete WorkIQ mail
// sync retains its server mail, and not when WorkIQ could never bind it.
func (rt *runtime) mailRouteSetup(st *store.Store, account string) (workIQSetup, bool, error) {
	done, err := workIQMailComplete(st, rt.ctx, account)
	if err != nil || done {
		return workIQSetup{}, false, err
	}
	// A WorkIQ sync of an account WorkIQ cannot bind would only refuse, so it gets no route.
	if bindable, err := workIQMailBindable(st, rt.ctx, account); err != nil || !bindable {
		return workIQSetup{}, false, err
	}
	setup, err := rt.workIQSetup(st)
	return setup, err == nil, err
}

// wholeAccountRoute ends in the whole-account mail sync and then the same read.
func (rt *runtime) wholeAccountRoute(st *store.Store, setup workIQSetup, account, reason, situation string) (r workIQRoute, ok bool, err error) {
	disk, known, err := workIQMailDiskEstimate(st, rt.ctx, account)
	if err != nil {
		return r, false, err
	}
	// The cost comes from the last run's saved estimate and the free space now; the read never
	// lists folders itself. Without both, the sync's own disk check decides.
	cost := "its size is unknown until WorkIQ lists the folders, and it stops before fetching messages the archive's disk cannot hold (it needs about " +
		syncer.WorkIQMailDiskPerItem() + " free per mailbox item)."
	if free, freeErr := workIQRouteFreeBytes(rt.dbPath); known && freeErr == nil {
		need := disk.Need()
		cost = syncer.WorkIQMailDiskCost(disk, need, free)
		if free < need {
			steps := "check WorkIQ"
			if !setup.enabled {
				steps += " and turn it on"
			}
			r = rt.newWorkIQRoute(setup, "mail", account, reason, situation+" Whole-account WorkIQ mail sync, which would fetch it, is refused on this archive: "+
				cost+" Free at least "+syncer.WorkIQSize(need-free)+" on the archive's disk or move the archive, and this read's route will offer the sync step again. "+
				"A shorter date range may fit: run workiq sync --source mail with --since and --until. Until then the steps below only "+steps+".", time.Time{})
			return r, true, nil
		}
	}
	r = rt.newWorkIQRoute(setup, "mail", account, reason, situation+" WorkIQ can fetch the account's server mail into this archive: run these steps, then repeat the read.", time.Time{})
	r.Next = append(r.Next, startStep{Command: rt.recoveryCommand(false, account, "workiq", "sync", "--source", "mail"),
		Does: "fetch every mail folder of this account from the server into the archive: " + cost +
			" Run it again only while its result says resumable: true, otherwise follow the error's fix"}, rt.repeatStep())
	return r, true, nil
}

// sinceRoutes routes each account with a folder being read whose cached mail starts after since.
// Each folder is judged by its own range, so an old folder never hides a newer one's start. A
// range inside a folder's coverage gets no route: gaps inside that range are not detectable here.
// Coverage rows come sorted by account; none without since. until is the read's end (zero for
// now), and named says the read names its folders, so coverage is the named folders'.
func (rt *runtime) sinceRoutes(st *store.Store, coverage []store.CoverageRow, since, until time.Time, named bool) ([]workIQRoute, error) {
	if since.IsZero() {
		return nil, nil
	}
	if until.IsZero() {
		until = workIQRouteNow()
	}
	var accounts []string
	reached := map[string][]routeFolder{}
	for _, c := range coverage {
		if c.OldestAt.IsZero() || !since.Before(c.OldestAt) {
			continue
		}
		if reached[c.Account] == nil {
			accounts = append(accounts, c.Account)
		}
		reached[c.Account] = append(reached[c.Account], routeFolder{Folder: c.Folder, Kind: c.Kind, CacheCoversSince: tp(c.OldestAt), key: c.FolderKey})
	}
	var out []workIQRoute
	for _, account := range accounts {
		var starts []string
		for _, f := range reached[account] {
			starts = append(starts, f.Folder+" from "+f.CacheCoversSince.In(displayZone).Format("2006-01-02"))
		}
		r, ok, err := rt.windowRoute(st, account, "This read starts before the cached mail of "+account+" in "+
			strings.Join(starts, ", ")+".", since, until, reached[account], named)
		if err != nil {
			return nil, err
		}
		if ok {
			r.Folders = reached[account]
			out = append(out, r)
		}
	}
	return out, nil
}

// matchCoverage is the coverage of the folders a --folder value names, as matchFolders picks them:
// by name first, then by kind, ignoring case.
func matchCoverage(rows []store.CoverageRow, name string) []store.CoverageRow {
	var byName, byKind []store.CoverageRow
	for _, c := range rows {
		if strings.EqualFold(c.Folder, name) {
			byName = append(byName, c)
		}
		if strings.EqualFold(c.Kind, name) {
			byKind = append(byKind, c)
		}
	}
	if len(byName) > 0 {
		return byName
	}
	return byKind
}

// threadReachesBack says a reply in rows answers a message the archive does not hold.
func threadReachesBack(rows []store.MailRow) bool {
	held := map[string]bool{}
	for _, r := range rows {
		held[r.InternetMessageID] = true
	}
	for _, r := range rows {
		if r.InReplyTo != "" && !held[r.InReplyTo] {
			return true
		}
	}
	return false
}

// chatRoutes routes a chat read that reaches before the oldest archived Teams message to WorkIQ
// coverage. Chat history stays counts-only, so the route counts and never repeats the read.
func (rt *runtime) chatRoutes(st *store.Store, since time.Time) ([]workIQRoute, error) {
	if since.IsZero() {
		return nil, nil
	}
	oldest, _, err := messageWindowOf(st, rt.ctx, rt.account)
	if err != nil || oldest.IsZero() || !since.Before(oldest) {
		return nil, err
	}
	setup, err := rt.workIQSetup(st)
	if err != nil {
		return nil, err
	}
	account := ""
	if rt.account != nil {
		account = rt.account.TenantID + "/" + rt.account.UserID
	}
	r := rt.newWorkIQRoute(setup, "chats", account, routeRangeBeforeCache, "The archived Teams messages start on "+
		oldest.In(displayZone).Format("2006-01-02")+". WorkIQ chat history stays counts-only: coverage counts the server's messages in this range, but adds no messages to the archive.", oldest)
	r.Next = append(r.Next, startStep{Command: rt.recoveryCommand(false, account, "workiq", "coverage", "--source", "chats", "--since", since.In(time.Local).Format(time.RFC3339)),
		Does: "count the server's chat messages from this read's start, without adding them"})
	return []workIQRoute{r}, nil
}

// workIQRouteNow is the read's clock; a test seam.
var workIQRouteNow = time.Now

// workIQWindowOverlap is how far past a folder's cache start a routed window reaches.
const workIQWindowOverlap = time.Hour

// windowRoute routes a read that starts before the cache to a WorkIQ mail window of the range the
// cache does not hold: from the read's start to an hour past the latest cache start among the
// folders it reaches, or the read's end when that is earlier. The hour lets the window overlap the
// cache for Sent Items, which coverage dates by sent time. The route disappears once windows have
// proven that range on every folder.
func (rt *runtime) windowRoute(st *store.Store, account, situation string, since, until time.Time, folders []routeFolder, named bool) (r workIQRoute, ok bool, err error) {
	setup, ok, err := rt.mailRouteSetup(st, account)
	if err != nil || !ok {
		return r, false, err
	}
	since = since.UTC().Truncate(time.Second)
	end := time.Time{}
	var keys []uint32
	var kinds []string
	for _, f := range folders {
		end = laterTime(end, earlierTime(until, f.CacheCoversSince.Add(workIQWindowOverlap)))
		keys = append(keys, f.key)
		kinds = append(kinds, f.Kind)
	}
	if t := end.UTC().Truncate(time.Second); t.Before(end) {
		end = t.Add(time.Second)
	} else {
		end = t
	}
	if !named {
		keys, kinds = nil, nil
	}
	info, err := workIQMailWindowRoute(st, rt.ctx, account, since, end, keys, kinds)
	if err != nil {
		return r, false, err
	}
	switch {
	case info.Covered && info.Twins == 0:
		return r, false, nil
	case info.Covered:
		return rt.wholeAccountRoute(st, setup, account, routeRangeBeforeCache, situation+fmt.Sprintf(
			" WorkIQ mail windows proved this range, but %d server messages in it match mail cached in other folders; only a whole-account sync can tell them apart.", info.Twins))
	}
	bounds := []string{"workiq", "sync", "--source", "mail", "--since", since.Format(time.RFC3339), "--until", end.Format(time.RFC3339)}
	if info.Unproven {
		pages := (info.OpenItems + store.WorkIQMailItemsPerPage - 1) / store.WorkIQMailItemsPerPage
		r = rt.newWorkIQRoute(setup, "mail", account, routeRangeBeforeCache, situation+
			" Some matching server mail was fetched; completeness is not proven. Prove the range with one more sync, then repeat the read.", time.Time{})
		r.Next = append(r.Next, startStep{Command: rt.recoveryCommand(false, account, append(bounds, "--coverage", "proof")...),
			Does: fmt.Sprintf("prove this range complete by following every folder's changes from its start: about %d items in about %d pages (roughly %s). "+
				"Run it again only while its result says resumable: true, otherwise follow the error's fix", info.OpenItems, pages,
				workIQRouteDuration((pages+store.WorkIQMailPagesPerMinute-1)/store.WorkIQMailPagesPerMinute))}, rt.repeatStep())
		return r, true, nil
	}
	r = rt.newWorkIQRoute(setup, "mail", account, routeRangeBeforeCache, situation+
		" WorkIQ can fetch this range of the account's server mail into this archive: run these steps, then repeat the read.", time.Time{})
	r.Next = append(r.Next, startStep{Command: rt.recoveryCommand(false, account, bounds...),
		Does: "fetch this account's server mail received in this range and prove it complete: it lists the folders, counts each one, and stops before " +
			"fetching more than the archive's disk can hold. Run it again only while its result says resumable: true, otherwise follow the error's fix"}, rt.repeatStep())
	return r, true, nil
}

func workIQRouteDuration(minutes int64) string {
	if minutes < 120 {
		return fmt.Sprintf("%d minutes", minutes)
	}
	return fmt.Sprintf("%d hours", (minutes+59)/60)
}

func earlierTime(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}

func laterTime(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}
