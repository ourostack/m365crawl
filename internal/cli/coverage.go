package cli

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/render"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/teamsdesktop"
)

// maxCoverageDays caps one coverage read.
const maxCoverageDays = 31

// coverageSourceNames are the sources coverage judges, in output order.
var coverageSourceNames = []string{"mail:sent", "mail:inbox", "chats", "meetings", "channels", "calendar"}

type coverageCmd struct {
	Day    string `help:"One local day: YYYY-MM-DD, today or yesterday. Default: today." placeholder:"DATE"`
	Since  string `help:"First day of a range of days (YYYY-MM-DD, today or yesterday); with --until, at most 31 days." placeholder:"DATE"`
	Until  string `help:"Last day of the range, included (YYYY-MM-DD, today or yesterday). Default: today." placeholder:"DATE"`
	TZ     string `name:"tz" help:"IANA time zone the days are read in, such as America/Los_Angeles. Default: this machine's zone." placeholder:"ZONE"`
	Source string `default:"mail,chats,meetings,calendar" help:"Comma-separated sources: mail (mail:sent and mail:inbox), mail:sent, mail:inbox, chats (1:1 and group chats and your notes), meetings (meeting chats), channels (channels and teams; unopened channels are never cached), calendar." placeholder:"LIST"`
}

func (coverageCmd) Help() string {
	return `coverage says, for each day and source, whether the archive holds everything the app cached that day, so a real zero can be told apart from a day the archive does not cover. It answers from the archive only, like every read: the implicit sync follows --max-age and --no-sync, and WorkIQ never starts.

Each entry has state covered (the desktop cache held the whole day and the archive read it, so items: 0 means none), partial (covered_from and covered_until bound the covered part) or not_covered; treat anything but covered as unknown, never as none. items is exact and independent of any limit: mail counts each message once across folder copies, gone and evicted ones included (marked_items says how many of them mail list hides by default); chats count authored messages that are not deleted (my_items: the ones you sent; deleted_items apart), never call events or thread activity. reasons say why a part is not covered, most important first; fix is the first reason's fix and next lists WorkIQ steps when WorkIQ can help. basis desktop_cache is not a server total; workiq_server means a complete WorkIQ mail sync holds the day.

Mail judges Sent Items (mail:sent, dated by when a message was sent) and the Inbox folder with To Me (mail:inbox; mail a rule filed elsewhere is not in it). A day is covered when a complete read showed the cache held every message from before that day until a message that arrived after it: Outlook keeps writing its store while offline, so only arriving mail proves it was in sync. For mail and chats today is never covered: it is partial until it ends. Chats judge Teams conversations from the archive's first to its last successful Teams read, partial when Teams shows later activity than the archive holds, when Teams sequence numbers around the day are missing (possibly_missing: possibly events Teams never caches) or when part of the day waited more than a week for the next read. Meeting chats are their own source because their numbers skip more often. The calendar is covered on a day, today and coming days included, that a Teams or Outlook calendar cache showed; Outlook shows only days that hold an event, and an event created after the cache last refreshed is still missing.

Examples:
  m365crawl coverage
  m365crawl coverage --day 2026-10-25 --source mail:sent,chats
  m365crawl coverage --since 2026-10-19 --until 2026-10-25 --tz America/Los_Angeles`
}

// coverageDay is one day's entries.
type coverageDay struct {
	Day     string          `json:"day"`
	From    time.Time       `json:"from"`
	To      time.Time       `json:"to"`
	Entries []coverageEntry `json:"entries"`
}

// coverageSummary is one source and scope over every day and account: the worst state and the
// summed items.
type coverageSummary struct {
	Source  string `json:"source"`
	Scope   string `json:"scope,omitempty"`
	State   string `json:"state"`
	Items   int    `json:"items"`
	MyItems *int   `json:"my_items,omitempty"`
}

type coverageResult struct {
	Range   rangeInfo         `json:"range"`
	Days    []coverageDay     `json:"days"`
	Summary []coverageSummary `json:"summary"`
	meta
}

// parseCoverageDay reads a day in loc: YYYY-MM-DD, today or yesterday.
func parseCoverageDay(flag, v string, now time.Time, loc *time.Location) (time.Time, error) {
	today := now.In(loc)
	today = time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, loc)
	switch v {
	case "today":
		return today, nil
	case "yesterday":
		return today.AddDate(0, 0, -1), nil
	}
	d, err := time.ParseInLocation("2006-01-02", v, loc)
	if err != nil {
		return d, errs.Usage(flag + " takes a day: YYYY-MM-DD, today or yesterday")
	}
	return d, nil
}

// coverageDays turns the flags into the days to judge, in loc.
func (c *coverageCmd) days(now time.Time, loc *time.Location) ([]time.Time, error) {
	if c.Day != "" && (c.Since != "" || c.Until != "") {
		return nil, errs.Usage("--day is one day; give either --day or --since and --until")
	}
	if c.Until != "" && c.Since == "" {
		return nil, errs.Usage("--until needs --since")
	}
	first, err := parseCoverageDay("--day", firstOf(c.Day, c.Since, "today"), now, loc)
	if err != nil {
		if c.Since != "" {
			err = errs.Usage("--since takes a day: YYYY-MM-DD, today or yesterday")
		}
		return nil, err
	}
	last := first
	if c.Since != "" {
		if last, err = parseCoverageDay("--until", firstOf(c.Until, "today"), now, loc); err != nil {
			return nil, err
		}
	}
	if last.Before(first) {
		return nil, errs.Usage("--until is before --since")
	}
	var out []time.Time
	for d := first; !d.After(last); d = d.AddDate(0, 0, 1) {
		if len(out) == maxCoverageDays {
			return nil, errs.Usage(fmt.Sprintf("coverage reads at most %d days at once; split the range", maxCoverageDays))
		}
		out = append(out, d)
	}
	return out, nil
}

// coverageSourceList parses --source into the sources of coverageSourceNames, in their order.
func coverageSourceList(v string) ([]string, error) {
	want := map[string]bool{}
	for _, s := range strings.Split(v, ",") {
		s = strings.TrimSpace(strings.ToLower(s))
		switch {
		case s == "mail":
			want["mail:sent"], want["mail:inbox"] = true, true
		case slices.Contains(coverageSourceNames, s):
			want[s] = true
		default:
			return nil, errs.Usage(fmt.Sprintf("--source: %q is not a coverage source; use mail, mail:sent, mail:inbox, chats, meetings, channels or calendar", s))
		}
	}
	var out []string
	for _, s := range coverageSourceNames {
		if want[s] {
			out = append(out, s)
		}
	}
	return out, nil
}

func (c *coverageCmd) Run(rt *runtime) error {
	loc := time.Local
	if c.TZ != "" {
		var err error
		if loc, err = time.LoadLocation(c.TZ); err != nil {
			return errs.Usage("--tz: " + c.TZ + " is not an IANA time zone such as America/Los_Angeles")
		}
	}
	days, err := c.days(rt.now(), loc)
	if err != nil {
		return err
	}
	sources, err := coverageSourceList(c.Source)
	if err != nil {
		return err
	}
	end := days[len(days)-1].AddDate(0, 0, 1)
	return rt.read("coverage", func(st *store.Store) (result, error) {
		res := &coverageResult{Range: *rangeOf(days[0], end), Days: []coverageDay{}, Summary: []coverageSummary{}}
		var run *coverageRun
		if st != nil {
			if run, err = rt.newCoverageRun(st); err != nil {
				return nil, err
			}
		}
		for _, d := range days {
			next := d.AddDate(0, 0, 1)
			day := coverageDay{Day: d.Format("2006-01-02"), From: d, To: next, Entries: []coverageEntry{}}
			for _, src := range sources {
				entries, err := rt.coverageEntries(run, src, d, next)
				if err != nil {
					return nil, err
				}
				for i := range entries {
					entries[i].inZone(loc)
				}
				day.Entries = append(day.Entries, entries...)
			}
			res.Days = append(res.Days, day)
		}
		res.summarize()
		if run != nil {
			for _, n := range run.notices() {
				res.addNotice(n)
			}
		}
		return res, nil
	})
}

// inZone shows the covered bounds in the zone the days are read in.
func (e *coverageEntry) inZone(loc *time.Location) {
	for _, t := range []*time.Time{e.CoveredFrom, e.CoveredUntil} {
		if t != nil {
			*t = t.In(loc)
		}
	}
}

// coverageEntries judges one source over [a, b): one entry per account in scope.
func (rt *runtime) coverageEntries(run *coverageRun, src string, a, b time.Time) ([]coverageEntry, error) {
	source, scope, _ := strings.Cut(src, ":")
	if source == "mail" {
		return rt.mailCoverageEntries(run, []string{scope}, a, b)
	}
	if source == "calendar" {
		return rt.calendarCoverageEntries(run, a, b)
	}
	return rt.chatCoverageEntries(run, []string{source}, a, b, store.ChatNarrow{})
}

// mailCoverageEntries judges the roles of every mail account in scope (--account).
func (rt *runtime) mailCoverageEntries(run *coverageRun, roles []string, a, b time.Time) ([]coverageEntry, error) {
	if run == nil {
		return noMailAccount(rt, roles), nil
	}
	all, err := run.folders()
	if err != nil {
		return nil, err
	}
	want := rt.mailAccount()
	if rt.account != nil && want == "" {
		return nil, nil // a Teams account names no mail
	}
	var accounts []string
	for _, f := range all {
		if (want == "" || f.Account == want) && !slices.Contains(accounts, f.Account) {
			accounts = append(accounts, f.Account)
		}
	}
	return run.mailEntries(accounts, roles, a, b)
}

// mailEntries judges each role of each account, in account order; no account gives the entries
// that say why there is none.
func (c *coverageRun) mailEntries(accounts, roles []string, a, b time.Time) ([]coverageEntry, error) {
	if len(accounts) == 0 {
		return noMailAccount(c.rt, roles), nil
	}
	all, err := c.folders()
	if err != nil {
		return nil, err
	}
	slices.Sort(accounts)
	var out []coverageEntry
	for _, account := range accounts {
		for _, role := range roles {
			e, err := c.mailEntry(account, role, all, a, b)
			if err != nil {
				return nil, err
			}
			out = append(out, e)
		}
	}
	return out, nil
}

// mailFoldersOf is the test seam of the folders coverage reads.
var mailFoldersOf = (*store.Store).MailFolders

// folders is every archived mail folder, read once.
func (c *coverageRun) folders() ([]store.MailFolderRow, error) {
	if c.mailFolders == nil {
		all, err := mailFoldersOf(c.st, c.rt.ctx)
		if err != nil {
			return nil, err
		}
		c.mailFolders = append([]store.MailFolderRow{}, all...)
	}
	return c.mailFolders, nil
}

// mailRangeCoverage adds range_coverage to a mail list bounded by --since or --until: Sent Items
// and the Inbox of each account among the selected folders, over [since, until) with an open end
// read as now. Other folders hold only part of their mail locally and are never judged.
func (rt *runtime) mailRangeCoverage(st *store.Store, sc mailScope, since, until time.Time, m *meta) error {
	run, err := rt.newCoverageRun(st)
	if err != nil {
		return err
	}
	return run.mailRangeCoverage(sc.folders, since, until, m)
}

// mailRangeCoverage adds the mail entries of the judged roles among folders over [since, until),
// an open end read as now, with the WorkIQ routes and notices they need. No folder at all gives
// the entries that say why there is no mail.
func (c *coverageRun) mailRangeCoverage(folders []store.MailFolderRow, since, until time.Time, m *meta) error {
	if until.IsZero() {
		until = c.now
	}
	var roles, accounts []string
	for _, f := range folders {
		role := mailRoleOf(f.Kind)
		if role != store.MailRoleSent && role != store.MailRoleInbox {
			continue
		}
		if !slices.Contains(roles, role) {
			roles = append(roles, role)
		}
		if !slices.Contains(accounts, f.Account) {
			accounts = append(accounts, f.Account)
		}
	}
	slices.SortFunc(roles, func(x, y string) int { return strings.Compare(y, x) }) // sent before inbox
	if len(folders) == 0 {
		roles = []string{store.MailRoleSent, store.MailRoleInbox}
	}
	if len(roles) == 0 {
		return nil
	}
	entries, err := c.mailEntries(accounts, roles, since, until)
	if err != nil {
		return err
	}
	m.RangeCoverage = append(m.RangeCoverage, entries...)
	m.WorkIQRoutes = c.addRoutes(m.WorkIQRoutes, entries)
	for _, n := range c.notices() {
		m.addNotice(n)
	}
	return nil
}

// searchRangeCoverage adds range_coverage to a search bounded by --since or --until, for the
// sources it planned: chats narrowed like messages, and mail across the accounts and folders mail
// search reads (every Outlook account unless --account names one, the folders --folder matches).
// Transcripts, notes, documents, Loop and content are never judged.
func (rt *runtime) searchRangeCoverage(st *store.Store, p searchPlan, f store.Filter, folder string, m *meta) error {
	run, err := rt.newCoverageRun(st)
	if err != nil {
		return err
	}
	if p.chats {
		if err := run.chatRangeCoverage(f.Since, f.Until, store.ChatNarrow{Conversation: f.Conversation, Team: f.Team}, m); err != nil {
			return err
		}
	}
	if !p.mail {
		return nil
	}
	all, err := run.folders()
	if err != nil {
		return err
	}
	account := mailAccountOf(rt.g.Account)
	var folders []store.MailFolderRow
	for _, x := range all {
		if account == "" || x.Account == account {
			folders = append(folders, x)
		}
	}
	if folder != "" {
		// --folder unknown (messages without a folder row) and other folders no row names hold
		// no judged role; only an archive without any mail folder says why there is no mail.
		if folders = matchFolders(folders, folder); len(folders) == 0 {
			return nil
		}
	}
	return run.mailRangeCoverage(folders, f.Since, f.Until, m)
}

// messagesRangeCoverage adds range_coverage to messages bounded by --since or --until: chats,
// meeting chats and channels of each Teams account in scope, narrowed to --conversation and
// --team.
func (rt *runtime) messagesRangeCoverage(st *store.Store, since, until time.Time, narrow store.ChatNarrow, m *meta) error {
	run, err := rt.newCoverageRun(st)
	if err != nil {
		return err
	}
	return run.chatRangeCoverage(since, until, narrow, m)
}

// chatRangeCoverage adds the chat entries of [since, until) to a result's range_coverage, an
// open end read as now, and the WorkIQ routes they need.
func (c *coverageRun) chatRangeCoverage(since, until time.Time, narrow store.ChatNarrow, m *meta) error {
	if until.IsZero() {
		until = c.now
	}
	entries, err := c.rt.chatCoverageEntries(c, []string{store.ChatScopeChats, store.ChatScopeMeetings, store.ChatScopeChannels}, since, until, narrow)
	if err != nil {
		return err
	}
	m.RangeCoverage = append(m.RangeCoverage, entries...)
	m.WorkIQRoutes = c.addRoutes(m.WorkIQRoutes, entries)
	return nil
}

func noMailAccount(rt *runtime, roles []string) []coverageEntry {
	run := &coverageRun{rt: rt}
	var out []coverageEntry
	for _, role := range roles {
		out = append(out, run.mailNoAccount(role))
	}
	return out
}

// teamsAccountsOf is the test seam of the archive's Teams accounts.
var teamsAccountsOf = (*store.Store).Whoami

// chatCoverageEntries judges the scopes of every Teams account in scope (--account), narrowed to
// what narrow selects: a scope without a selected conversation gets no entry, and an account
// without any gets one conversation_not_archived entry.
func (rt *runtime) chatCoverageEntries(run *coverageRun, scopes []string, a, b time.Time, narrow store.ChatNarrow) ([]coverageEntry, error) {
	if rt.mailAccount() != "" {
		return nil, nil // an Outlook account names no Teams chats
	}
	var accounts []teamsdesktop.Account
	if run != nil {
		rows, err := teamsAccountsOf(run.st, rt.ctx)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			if rt.account == nil || *rt.account == (teamsdesktop.Account{TenantID: r.TenantID, UserID: r.UserID}) {
				accounts = append(accounts, teamsdesktop.Account{TenantID: r.TenantID, UserID: r.UserID})
			}
		}
	}
	var out []coverageEntry
	matchedAny := map[teamsdesktop.Account]bool{}
	for _, scope := range scopes {
		if len(accounts) == 0 {
			out = append(out, chatNoAccount(scope))
		}
		for _, acct := range accounts {
			e, matched, err := run.chatEntry(acct.TenantID, acct.UserID, scope, a, b, narrow)
			if err != nil {
				return nil, err
			}
			if matched {
				matchedAny[acct] = true
				out = append(out, e)
			}
		}
	}
	for _, acct := range accounts {
		if !matchedAny[acct] {
			out = append(out, notArchived(acct.TenantID+"/"+acct.UserID, narrow))
		}
	}
	return out, nil
}

// calendarCoverageEntries judges one day of every calendar principal in scope.
func (rt *runtime) calendarCoverageEntries(run *coverageRun, a, b time.Time) ([]coverageEntry, error) {
	none := newCoverageEntry("calendar", "", "")
	none.reason(reasonNeverRead, "no calendar is in the archive")
	none.finish()
	if run == nil {
		return []coverageEntry{none}, nil
	}
	agenda, err := calendarAgendaOf(run.st, rt.ctx, store.CalendarFilter{Account: rt.account, From: a, To: b, Limit: 1 << 30})
	if err != nil {
		return nil, err
	}
	if len(agenda.Accounts) == 0 {
		return []coverageEntry{none}, nil
	}
	var out []coverageEntry
	for _, acct := range agenda.Accounts {
		e := newCoverageEntry("calendar", "", acct.AccountID)
		for _, r := range agenda.Rows {
			if r.Principal == acct.AccountID {
				e.Items++
			}
		}
		if len(acct.Uncovered) == 0 {
			e.settle([]store.TimeSpan{{From: a, To: b}}, a, b)
			e.Basis = basisCache
		} else {
			e.reason(reasonDayNotShown, "neither the Teams nor the Outlook calendar cache showed this day; Outlook shows only days that hold an event")
		}
		e.finish()
		out = append(out, e)
	}
	return out, nil
}

// calendarRangeCoverage is the calendar agenda's range_coverage: one entry per principal in scope
// over [from, to), covered on the days a calendar cache showed. items counts the principal's
// events with the default filters, whatever the read's own filters and limit; a schedule, so a
// coming day can be covered. No calendar gives one never_read entry.
func (rt *runtime) calendarRangeCoverage(st *store.Store, from, to time.Time) ([]coverageEntry, error) {
	none := newCoverageEntry("calendar", "", "")
	none.reason(reasonNeverRead, "no calendar is in the archive")
	none.finish()
	if st == nil {
		return []coverageEntry{none}, nil
	}
	agenda, err := calendarAgendaOf(st, rt.ctx, store.CalendarFilter{Account: rt.account, From: from, To: to, Limit: 1 << 30})
	if err != nil {
		return nil, err
	}
	if len(agenda.Accounts) == 0 {
		return []coverageEntry{none}, nil
	}
	type day struct {
		name     string
		from, to time.Time
	}
	var days []day
	for d := time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, from.Location()); d.Before(to); d = d.AddDate(0, 0, 1) {
		days = append(days, day{d.Format("2006-01-02"), maxTime(d, from), minTime(d.AddDate(0, 0, 1), to)})
	}
	var out []coverageEntry
	for _, acct := range agenda.Accounts {
		e := newCoverageEntry("calendar", "", acct.AccountID)
		for _, r := range agenda.Rows {
			if r.Principal == acct.AccountID {
				e.Items++
			}
		}
		var covered []store.TimeSpan
		for _, d := range days {
			if !slices.Contains(acct.Uncovered, d.name) {
				covered = append(covered, store.TimeSpan{From: d.from, To: d.to})
			}
		}
		e.settle(store.MergeSpans(covered), from, to)
		if len(covered) > 0 {
			e.Basis = basisCache
		}
		if missing := len(days) - len(covered); missing > 0 {
			e.reason(reasonDayNotShown, fmt.Sprintf("%d of %s in this range no Teams or Outlook calendar cache showed, so an event missing on them is not evidence; Outlook shows only days that hold an event", missing, plural(len(days), "day", "days")))
		}
		e.finish()
		out = append(out, e)
	}
	return out, nil
}

func minTime(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}

// summarize adds one summary row per source and scope: the worst state and the summed items.
func (r *coverageResult) summarize() {
	rank := map[string]int{covCovered: 0, covPartial: 1, covNotCovered: 2}
	index := map[string]int{}
	for _, d := range r.Days {
		for _, e := range d.Entries {
			key := e.Source + ":" + e.Scope
			i, seen := index[key]
			if !seen {
				i = len(r.Summary)
				index[key] = i
				s := coverageSummary{Source: e.Source, Scope: e.Scope, State: e.State}
				if e.MyItems != nil {
					s.MyItems = new(int)
				}
				r.Summary = append(r.Summary, s)
			}
			s := &r.Summary[i]
			if rank[e.State] > rank[s.State] {
				s.State = e.State
			}
			s.Items += e.Items
			if e.MyItems != nil {
				*s.MyItems += *e.MyItems
			}
		}
	}
}

func (r *coverageResult) renderCoverage(rt *runtime) error {
	var rows [][]string
	for _, d := range r.Days {
		for _, e := range d.Entries {
			account, until, reasons := "", "", ""
			if e.Account != nil {
				account = *e.Account
			}
			if e.CoveredUntil != nil && e.State != covCovered {
				until = stamp(*e.CoveredUntil)
			}
			var codes []string
			for _, x := range e.Reasons {
				codes = append(codes, x.Code)
			}
			reasons = strings.Join(codes, ", ")
			rows = append(rows, []string{d.Day, e.Source, e.Scope, account, e.State, fmt.Sprint(e.Items), until, reasons})
		}
	}
	render.Table(rt.stdout, []string{"day", "source", "scope", "account", "state", "items", "covered until", "reasons"}, rows, rt.color)
	_, _ = fmt.Fprintln(rt.stdout)
	for _, d := range r.Days {
		for _, e := range d.Entries {
			if e.Fix != "" {
				_, _ = fmt.Fprintf(rt.stdout, "%s\n", render.Dim("fix: "+strings.Join(append([]string{d.Day}, entryName(e)...), " ")+": "+e.Fix, rt.color))
			}
		}
	}
	metaLines(rt.stdout, r.meta, rt.color)
	return nil
}
