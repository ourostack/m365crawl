package cli

import (
	"strings"
	"time"

	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/syncer"
	"github.com/ourostack/m365crawl/internal/workiq"
)

// Test seams of the route's archive reads.
var (
	workIQMailComplete     = (*store.Store).WorkIQMailComplete
	mailCoverageOf         = (*store.Store).MailCoverage
	workIQMailDiskEstimate = (*store.Store).WorkIQMailDiskEstimate
	workIQRouteFreeBytes   = store.FreeBytes
)

// Reasons a read routes to WorkIQ.
const (
	routeRangeBeforeCache  = "range_before_cache"
	routeParentNotArchived = "parent_not_archived"
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
// complete WorkIQ mail sync already retains the account's server mail (ok is false then).
func (rt *runtime) mailRoute(st *store.Store, account, reason, why string) (r workIQRoute, ok bool, err error) {
	done, err := workIQMailComplete(st, rt.ctx, account)
	if err != nil || done {
		return r, false, err
	}
	setup, err := rt.workIQSetup(st)
	if err != nil {
		return r, false, err
	}
	disk, known, err := workIQMailDiskEstimate(st, rt.ctx, account)
	if err != nil {
		return r, false, err
	}
	r = rt.newWorkIQRoute(setup, "mail", account, reason, why, time.Time{})
	// The cost comes from the last run's saved estimate and the free space now; the read never
	// lists folders itself. Without both, the sync's own disk check decides.
	cost := "its size is unknown until WorkIQ lists the folders, and it stops before fetching messages the archive's disk cannot hold (it needs about " +
		syncer.WorkIQMailDiskPerItem() + " free per mailbox item)."
	if free, freeErr := workIQRouteFreeBytes(rt.dbPath); known && freeErr == nil {
		need := disk.Need()
		cost = syncer.WorkIQMailDiskCost(disk, need, free)
		if free < need {
			r.Fix += " Whole-account WorkIQ mail sync is refused on this archive: " + cost +
				" Free space or move the archive to run it. Fetching only a date range is tracked in ourostack/m365crawl#262."
			return r, true, nil
		}
	}
	r.Next = append(r.Next, startStep{Command: rt.recoveryCommand(false, account, "workiq", "sync", "--source", "mail"),
		Does: "fetch every mail folder of this account from the server into the archive: " + cost +
			" Run it again only while its result says resumable: true, otherwise follow the error's fix"}, rt.repeatStep())
	return r, true, nil
}

// sinceRoutes routes each account with a folder being read whose cached mail starts after since.
// Each folder is judged by its own range, so an old folder never hides a newer one's start. A
// range inside a folder's coverage gets no route: gaps inside that range are not detectable here.
// Coverage rows come sorted by account; none without since.
func (rt *runtime) sinceRoutes(st *store.Store, coverage []store.CoverageRow, since time.Time) ([]workIQRoute, error) {
	if since.IsZero() {
		return nil, nil
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
		reached[c.Account] = append(reached[c.Account], routeFolder{Folder: c.Folder, Kind: c.Kind, CacheCoversSince: tp(c.OldestAt)})
	}
	var out []workIQRoute
	for _, account := range accounts {
		var starts []string
		for _, f := range reached[account] {
			starts = append(starts, f.Folder+" from "+f.CacheCoversSince.In(displayZone).Format("2006-01-02"))
		}
		r, ok, err := rt.mailRoute(st, account, routeRangeBeforeCache, "This read starts before the cached mail of "+account+" in "+
			strings.Join(starts, ", ")+". WorkIQ can fetch the older mail from the server into this archive: run these steps, then repeat the read.")
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
