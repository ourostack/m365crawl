package cli

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/ourostack/m365crawl/internal/store"
)

// The states of a coverage entry. The vocabulary is open: anything but covered is unknown, never
// none.
const (
	covCovered    = "covered"
	covPartial    = "partial"
	covNotCovered = "not_covered"
)

// The bases of a covered part.
const (
	basisCache  = "desktop_cache"
	basisServer = "workiq_server"
	basisBoth   = "both"
)

// The reason codes of a coverage entry.
const (
	reasonFuture             = "future"
	reasonInProgress         = "in_progress"
	reasonNeverRead          = "never_read"
	reasonSourceOff          = "source_off"
	reasonNoProfile          = "no_profile"
	reasonFolderNotSynced    = "folder_not_synced"
	reasonNoCompleteRead     = "no_complete_read"
	reasonBeforeCache        = "before_cache"
	reasonOnlyOlderCopy      = "only_older_cache_copy"
	reasonCopiesDisagree     = "folder_copies_disagree"
	reasonSourceFailing      = "source_failing"
	reasonAfterCacheFresh    = "after_cache_fresh"
	reasonAfterNewestMail    = "after_newest_mail"
	reasonAfterLastRead      = "after_last_read"
	reasonBeforeArchiveStart = "before_archive_start"
	reasonReadGap            = "read_gap"
	reasonBehind             = "conversation_behind"
	reasonSequenceGap        = "sequence_gap"
	reasonDayNotShown        = "day_not_shown"
	reasonNotArchived        = "conversation_not_archived"
)

// teamsReadGap is how long after a range the archive may first read Teams and still call the
// range covered: Teams may drop a conversation's older messages from its cache before a read
// sees them. A stated heuristic; no Teams retention rule is known.
const teamsReadGap = 7 * 24 * time.Hour

// coverageFixes is the plain fix of each reason, for an entry whose first reason it is.
var coverageFixes = map[string]string{
	reasonFuture:             "This range has not started yet.",
	reasonInProgress:         "The range has not ended yet: read it again after it ends.",
	reasonNeverRead:          "Run `m365crawl sync`.",
	reasonSourceOff:          "Read with the Outlook source on: drop --outlook-root none, and give --outlook-root when --teams-root is set.",
	reasonNoProfile:          "No new Outlook profile is on this machine; mail is read from new Outlook's local cache.",
	reasonFolderNotSynced:    "Run `m365crawl sync`; `m365crawl mail folders` lists the folders the archive holds.",
	reasonNoCompleteRead:     "Run `m365crawl sync` while Outlook is open; `m365crawl doctor` says why reads are incomplete.",
	reasonBeforeCache:        "Outlook keeps only recent mail on this machine; WorkIQ can fetch the account's server mail into the archive.",
	reasonOnlyOlderCopy:      "Outlook keeps only recent mail on this machine; WorkIQ can fetch the account's server mail into the archive.",
	reasonCopiesDisagree:     "WorkIQ can fetch the account's server mail into the archive; until then count the archive's messages as a floor.",
	reasonSourceFailing:      "Run `m365crawl doctor`.",
	reasonAfterCacheFresh:    "Open the app and let it sync, then run `m365crawl sync`.",
	reasonAfterNewestMail:    "Run `m365crawl sync` again after more mail has arrived.",
	reasonAfterLastRead:      "Run `m365crawl sync`.",
	reasonBeforeArchiveStart: "Open those chats in Teams so it loads their older messages, then run `m365crawl sync`; WorkIQ can count the server's messages.",
	reasonReadGap:            "Open those chats in Teams so it loads their messages, then run `m365crawl sync`.",
	reasonBehind:             "Open those chats in Teams so it loads their messages, then run `m365crawl sync`.",
	reasonSequenceGap:        "Open those chats in Teams so it loads their messages, then run `m365crawl sync`.",
	reasonDayNotShown:        "Open that day in the Teams or Outlook calendar, then run `m365crawl sync`.",
	reasonNotArchived:        "Open the conversation in Teams so it is cached, then run `m365crawl sync`; `m365crawl conversations --query <name>` lists what is archived.",
}

// coverageReason is one reason an entry is not covered.
type coverageReason struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

// coverageEntry says whether the archive holds every item of one source, scope and account over
// a range, and what it holds.
type coverageEntry struct {
	Source string `json:"source"`
	Scope  string `json:"scope,omitempty"`
	// NarrowedTo (chats) says the entry judges only the conversations a read's --conversation
	// (conversation) or --team (team) selects.
	NarrowedTo string  `json:"narrowed_to,omitempty"`
	Account    *string `json:"account"`
	State      string  `json:"state"`
	// Items is what the archive holds in the range, exact and independent of --limit.
	Items int `json:"items"`
	// MarkedItems (mail) is how many of Items a sync marked gone or evicted.
	MarkedItems *int `json:"marked_items,omitempty"`
	// MyItems (chats) is how many of Items the account sent; DeletedItems the deleted ones,
	// which Items leaves out.
	MyItems      *int `json:"my_items,omitempty"`
	DeletedItems *int `json:"deleted_items,omitempty"`
	// PossiblyMissing (chats) is how many Teams sequence numbers are absent between archived
	// messages around the range.
	PossiblyMissing int              `json:"possibly_missing,omitempty"`
	CoveredFrom     *time.Time       `json:"covered_from"`
	CoveredUntil    *time.Time       `json:"covered_until"`
	Basis           string           `json:"basis,omitempty"`
	Reasons         []coverageReason `json:"reasons"`
	Fix             string           `json:"fix,omitempty"`
	Next            []startStep      `json:"next,omitempty"`
}

func newCoverageEntry(source, scope, account string) coverageEntry {
	e := coverageEntry{Source: source, Scope: scope, State: covNotCovered, Reasons: []coverageReason{}}
	if account != "" {
		e.Account = &account
	}
	return e
}

// reason adds a reason once per code.
func (e *coverageEntry) reason(code, detail string) {
	if !slices.ContainsFunc(e.Reasons, func(r coverageReason) bool { return r.Code == code }) {
		e.Reasons = append(e.Reasons, coverageReason{code, detail})
	}
}

// has reports whether the entry has a reason of one of the codes.
func (e *coverageEntry) has(codes ...string) bool {
	return slices.ContainsFunc(e.Reasons, func(r coverageReason) bool { return slices.Contains(codes, r.Code) })
}

// settle sets the state and the covered bounds from the covered pieces of [a, b).
func (e *coverageEntry) settle(pieces []store.TimeSpan, a, b time.Time) {
	switch {
	case len(pieces) == 0:
		e.State = covNotCovered
		return
	case len(pieces) == 1 && pieces[0].From.Equal(a) && pieces[0].To.Equal(b):
		e.State = covCovered
	default:
		e.State = covPartial
	}
	e.CoveredFrom, e.CoveredUntil = tp(pieces[0].From), tp(pieces[len(pieces)-1].To)
}

// downgrade turns a covered entry partial: something in the covered range may be missing.
func (e *coverageEntry) downgrade() {
	if e.State == covCovered {
		e.State = covPartial
	}
}

// finish sets the fix from the first reason.
func (e *coverageEntry) finish() {
	if len(e.Reasons) > 0 {
		e.Fix = coverageFixes[e.Reasons[0].Code]
	}
}

// clip is the part of the spans inside [a, b), merged.
func clip(spans []store.TimeSpan, a, b time.Time) []store.TimeSpan {
	var out []store.TimeSpan
	for _, s := range spans {
		from, to := s.From, s.To
		if from.Before(a) {
			from = a
		}
		if to.After(b) {
			to = b
		}
		out = append(out, store.TimeSpan{From: from, To: to})
	}
	return store.MergeSpans(out)
}

// holes is the part of [a, b) that pieces (clipped and merged) leave uncovered.
func holes(pieces []store.TimeSpan, a, b time.Time) []store.TimeSpan {
	var out []store.TimeSpan
	at := a
	for _, p := range pieces {
		if p.From.After(at) {
			out = append(out, store.TimeSpan{From: at, To: p.From})
		}
		at = p.To
	}
	if b.After(at) {
		out = append(out, store.TimeSpan{From: at, To: b})
	}
	return out
}

func sameSpans(x, y []store.TimeSpan) bool { return slices.Equal(x, y) }

// stamp of a time in the display zone, for reason details.
func covStamp(t time.Time) string { return t.In(displayZone).Format(time.RFC3339) }

// coverageRun is one coverage computation's view of the archive, shared by every range it judges.
type coverageRun struct {
	rt     *runtime
	st     *store.Store
	now    time.Time
	health []store.SourceHealth
	mail   map[string]*mailCoverageEvidence
	teams  map[string]store.TeamsReadLog
	routes map[string]*workIQRoute // WorkIQ routes by source and account, nil when none applies
	// legacy names the mail accounts whose verdict rests on the last read's ranges.
	legacy      []string
	mailFolders []store.MailFolderRow
}

func (rt *runtime) newCoverageRun(st *store.Store) (*coverageRun, error) {
	health, err := readSourceHealth(st, rt.ctx)
	if err != nil {
		return nil, err
	}
	return &coverageRun{rt: rt, st: st, now: rt.now(), health: health, mail: map[string]*mailCoverageEvidence{},
		teams: map[string]store.TeamsReadLog{}, routes: map[string]*workIQRoute{}}, nil
}

// healthOf finds the source's health entry of the account: failing (or never read) or stale.
func (c *coverageRun) healthOf(source, account string) (failing, stale bool) {
	for _, h := range c.health {
		if h.Source != source || h.AccountID != nil && *h.AccountID != account {
			continue
		}
		switch h.State {
		case store.SourceStale:
			stale = true
		default:
			failing = true
		}
	}
	return failing, stale
}

// notices are the sentences a result with coverage entries adds.
func (c *coverageRun) notices() []string {
	var out []string
	for _, a := range c.legacy {
		out = append(out, "mail coverage of "+a+" rests on the last read's ranges, because no sync by this version has recorded which ranges a complete read held; run `m365crawl sync` to record them")
	}
	return out
}

// ---- mail ----

// mailCoverageEvidence is what the archive knows about one mail account's coverage.
type mailCoverageEvidence struct {
	spans  store.MailSpans
	server time.Time
	roles  map[string]bool // the roles the account has an archived folder of
}

// mailCoverageSpans and mailServerThrough are test seams of the archive's mail evidence.
var (
	mailCoverageSpans = (*store.Store).MailCoverageSpans
	mailServerThrough = (*store.Store).MailServerThrough
	mailRoleCount     = (*store.Store).MailRoleCount
)

func (c *coverageRun) mailEvidence(account string, folders []store.MailFolderRow) (*mailCoverageEvidence, error) {
	if ev := c.mail[account]; ev != nil {
		return ev, nil
	}
	ev := &mailCoverageEvidence{roles: map[string]bool{}}
	for _, f := range folders {
		if f.Account == account {
			ev.roles[mailRoleOf(f.Kind)] = true
		}
	}
	var err error
	if ev.spans, err = mailCoverageSpans(c.st, c.rt.ctx, account); err != nil {
		return nil, err
	}
	if ev.server, err = mailServerThrough(c.st, c.rt.ctx, account); err != nil {
		return nil, err
	}
	if ev.spans.Legacy {
		c.legacy = append(c.legacy, account)
	}
	c.mail[account] = ev
	return ev, nil
}

// mailRoleOf is the coverage role of a folder kind: To Me is part of the Inbox.
func mailRoleOf(kind string) string {
	if kind == "to_me" {
		return store.MailRoleInbox
	}
	return kind
}

// mailEntry judges one mail account and role over [a, b).
func (c *coverageRun) mailEntry(account, role string, folders []store.MailFolderRow, a, b time.Time) (coverageEntry, error) {
	e := newCoverageEntry("mail", role, account)
	ev, err := c.mailEvidence(account, folders)
	if err != nil {
		return e, err
	}
	count, err := mailRoleCount(c.st, c.rt.ctx, account, role, a, b)
	if err != nil {
		return e, err
	}
	e.Items, e.MarkedItems = count.Messages, &count.Marked
	switch {
	case !ev.roles[role]:
		e.reason(reasonFolderNotSynced, "the archive holds no "+role+" folder of this account")
		e.finish()
		return e, nil
	case !a.Before(c.now):
		e.reason(reasonFuture, "the range starts after now")
		e.finish()
		return e, nil
	}
	var cache []store.TimeSpan
	for _, s := range ev.spans.Spans[role] {
		if role == store.MailRoleSent {
			s.From = s.From.Add(ev.spans.SentShift)
		}
		cache = append(cache, s)
	}
	cache = store.MergeSpans(cache)
	var server []store.TimeSpan
	if !ev.server.IsZero() {
		server = []store.TimeSpan{{To: ev.server}}
	}
	pieces := clip(append(append([]store.TimeSpan{}, cache...), server...), a, b)
	e.settle(pieces, a, b)
	if len(pieces) > 0 {
		cacheAll, serverAll := sameSpans(clip(cache, a, b), pieces), sameSpans(clip(server, a, b), pieces)
		switch {
		case cacheAll && !serverAll:
			e.Basis = basisCache
		case serverAll && !cacheAll:
			e.Basis = basisServer
		default:
			e.Basis = basisBoth
		}
	}
	if b.After(c.now) {
		e.reason(reasonInProgress, "the range ends after now")
	}
	var cacheEnd time.Time
	if len(cache) > 0 {
		cacheEnd = cache[len(cache)-1].To
	}
	for _, h := range holes(pieces, a, b) {
		switch {
		case len(cache) == 0:
			e.reason(reasonNoCompleteRead, "no read of this account's mail was complete enough to show that the cache held every "+role+" message of a range")
		case h.From.Before(cacheEnd) && count.State == "older_copy_only":
			e.reason(reasonOnlyOlderCopy, "only an older copy of the folder, from a folder set Outlook replaced, reached these dates")
		case h.From.Before(cacheEnd):
			detail := "the cache did not hold all " + role + " mail before " + covStamp(h.To) + " when the archive read it"
			if role == store.MailRoleSent && ev.spans.SentShift > 0 {
				detail += "; Sent Items are dated by when they were sent, and a message drafted up to " + ev.spans.SentShift.Round(time.Minute).String() + " before it was sent may have left the cache before any read"
			}
			e.reason(reasonBeforeCache, detail)
		default:
			c.mailTail(&e, account, ev, cacheEnd, b)
		}
	}
	serverCovers := sameSpans(clip(server, a, b), []store.TimeSpan{{From: a, To: b}})
	if count.State == "partial" && !serverCovers {
		e.downgrade()
		e.reason(reasonCopiesDisagree, fmt.Sprintf("an older copy of the folder holds %d of %d messages in this range that the current cache lacks although its range covers them", count.LackingInCurrentRange, count.Messages))
	}
	if e.has(reasonBeforeCache, reasonOnlyOlderCopy, reasonCopiesDisagree) {
		if e.Next, err = c.mailSteps(account); err != nil {
			return e, err
		}
	}
	e.finish()
	return e, nil
}

// mailTail says why mail after the last covered time, up to the range's end b, is not covered.
// A range that reaches past the last read's store write time needs a sync first; one that ends
// before it lacks only proof that Outlook was in sync after its newest message.
func (c *coverageRun) mailTail(e *coverageEntry, account string, ev *mailCoverageEvidence, cacheEnd, b time.Time) {
	failing, stale := c.healthOf("outlook_mail", account)
	until := "after " + covStamp(cacheEnd)
	switch {
	case !c.rt.outlookOn:
		e.reason(reasonSourceOff, "the Outlook source is off in this run, so mail "+until+" was not read")
	case failing:
		e.reason(reasonSourceFailing, "the last read of this account's mail failed, so mail "+until+" is not in the archive")
	case stale:
		e.reason(reasonAfterCacheFresh, "Outlook had not updated its local cache for over an hour when the archive last checked it, so mail "+until+" is not in it")
	case b.After(ev.spans.FreshAt):
		e.reason(reasonAfterLastRead, "the archive's last read holds Outlook's cache as written at "+covStamp(ev.spans.FreshAt)+"; later mail is not in it, and no message arrived "+until)
	default:
		e.reason(reasonAfterNewestMail, "no message arrived "+until+", so the archive cannot tell a quiet mailbox from an Outlook that had stopped syncing")
	}
}

// mailSteps is the WorkIQ mail route's steps for the account, or none when no route applies.
func (c *coverageRun) mailSteps(account string) ([]startStep, error) {
	key := "mail\x00" + account
	if r, ok := c.routes[key]; ok {
		return routeSteps(r), nil
	}
	r, ok, err := c.rt.mailRoute(c.st, account, routeRangeNotCovered, "The archive does not hold all of this range's mail.")
	if err != nil {
		return nil, err
	}
	c.routes[key] = nil
	if ok {
		c.routes[key] = &r
	}
	return routeSteps(c.routes[key]), nil
}

func routeSteps(r *workIQRoute) []startStep {
	if r == nil {
		return nil
	}
	return r.Next
}

// addRoutes appends to routes the WorkIQ route of each source and account an entry needed that
// routes does not already have; a route without an account stands for every account.
func (c *coverageRun) addRoutes(routes []workIQRoute, entries []coverageEntry) []workIQRoute {
	for _, e := range entries {
		if e.Account == nil || len(e.Next) == 0 || slices.ContainsFunc(routes, func(r workIQRoute) bool {
			return r.Source == e.Source && (r.Account == "" || r.Account == *e.Account)
		}) {
			continue
		}
		routes = append(routes, *c.routes[e.Source+"\x00"+*e.Account])
	}
	return routes
}

// mailNoAccount is the entry of a mail role when the archive holds no mail account in scope.
func (c *coverageRun) mailNoAccount(role string) coverageEntry {
	e := newCoverageEntry("mail", role, "")
	switch {
	case !c.rt.outlookOn:
		e.reason(reasonSourceOff, "the Outlook source is off in this run and the archive holds no mail")
	case c.profileCount() == 0:
		e.reason(reasonNoProfile, "no supported Outlook profile was found on this machine")
	default:
		e.reason(reasonNeverRead, "no mail has been read into the archive")
	}
	e.finish()
	return e
}

// profileCount counts the machine's Outlook profiles; a problem reading them counts as none.
func (c *coverageRun) profileCount() int {
	n, _ := mailProfileCount(c.rt)
	return n
}

// ---- chats ----

var (
	teamsReads   = (*store.Store).TeamsReads
	chatEvidence = (*store.Store).ChatEvidence
)

func (c *coverageRun) teamsLog(account string) (store.TeamsReadLog, error) {
	if log, ok := c.teams[account]; ok {
		return log, nil
	}
	log, err := teamsReads(c.st, c.rt.ctx, account)
	if err != nil {
		return log, err
	}
	c.teams[account] = log
	return log, nil
}

// chatEntry judges one Teams account's conversations of a scope over [a, b), narrowed to what
// narrow selects; matched is false when a narrowing selects no conversation of the scope.
func (c *coverageRun) chatEntry(tenantID, userID, scope string, a, b time.Time, narrow store.ChatNarrow) (e coverageEntry, matched bool, err error) {
	account := tenantID + "/" + userID
	e = newCoverageEntry("chats", scope, account)
	e.NarrowedTo = narrowName(narrow)
	ev, err := chatEvidence(c.st, c.rt.ctx, tenantID, userID, scope, a, b, narrow)
	if err != nil || !ev.Matched {
		return e, false, err
	}
	log, err := c.teamsLog(account)
	if err != nil {
		return e, false, err
	}
	e.Items, e.MyItems, e.DeletedItems = ev.Items, &ev.MyItems, &ev.Deleted
	switch {
	case len(log.Reads) == 0:
		e.reason(reasonNeverRead, "no read of this Teams account succeeded")
		e.finish()
		return e, true, nil
	case !a.Before(c.now):
		e.reason(reasonFuture, "the range starts after now")
		e.finish()
		return e, true, nil
	}
	first, last := log.Reads[0], log.Reads[len(log.Reads)-1]
	pieces := clip([]store.TimeSpan{{From: first, To: last}}, a, b)
	e.settle(pieces, a, b)
	if len(pieces) > 0 {
		e.Basis = basisCache
	}
	if b.After(c.now) {
		e.reason(reasonInProgress, "the range ends after now")
	}
	for _, h := range holes(pieces, a, b) {
		if h.From.Before(first) {
			e.reason(reasonBeforeArchiveStart, "the archive first read this Teams account at "+covStamp(first)+"; Teams had cached only some earlier messages")
			continue
		}
		failing, _ := c.healthOf("teams", account)
		switch {
		case failing:
			e.reason(reasonSourceFailing, "the last read of this Teams account failed, so messages after "+covStamp(last)+" are not in the archive")
		case log.LastUnchanged.After(last.Add(store.OutlookCacheStaleAfter)):
			e.reason(reasonAfterCacheFresh, "Teams had not written its cache since the read at "+covStamp(last)+" when the archive checked it at "+covStamp(log.LastUnchanged)+", so later messages are not in it")
		default:
			e.reason(reasonAfterLastRead, "the archive last read this Teams account at "+covStamp(last))
		}
	}
	if len(pieces) == 0 && e.has(reasonBeforeArchiveStart) && ev.Items+ev.Deleted > 0 {
		e.State = covPartial // Teams had cached some of it
	}
	if gap := worstReadGap(log.Reads, maxTime(a, first), b); len(pieces) > 0 && gap > teamsReadGap {
		e.downgrade()
		e.reason(reasonReadGap, fmt.Sprintf("part of this range waited %s for the next Teams read; Teams may have dropped messages before any read saw them", gap.Round(time.Hour)))
	}
	if ev.Behind > 0 {
		e.downgrade()
		e.reason(reasonBehind, fmt.Sprintf("Teams shows later activity in %s than the archive holds", plural(ev.Behind, "conversation", "conversations")))
	}
	if ev.PossiblyMissing > 0 {
		e.downgrade()
		e.PossiblyMissing = ev.PossiblyMissing
		e.reason(reasonSequenceGap, fmt.Sprintf("%s skip %d Teams sequence numbers around this range: possibly missing messages, since Teams also numbers events it never caches",
			plural(ev.GapConversations, "conversation", "conversations"), ev.PossiblyMissing))
	}
	if e.has(reasonBeforeArchiveStart) {
		if e.Next, err = c.chatSteps(account, a); err != nil {
			return e, false, err
		}
	}
	e.finish()
	return e, true, nil
}

// narrowName is the narrowed_to of a narrowing: conversation, team or "" when there is none.
func narrowName(n store.ChatNarrow) string {
	switch {
	case n.Conversation != "":
		return "conversation"
	case n.Team != "":
		return "team"
	}
	return ""
}

// notArchived is the entry of an account none of whose conversations a narrowing selects.
func notArchived(account string, narrow store.ChatNarrow) coverageEntry {
	e := newCoverageEntry("chats", "", account)
	e.NarrowedTo = narrowName(narrow)
	e.reason(reasonNotArchived, "the archive holds no conversation of this account that the read selects, so it cannot say whether its messages are all there")
	e.finish()
	return e
}

// worstReadGap is the longest any moment of [from, b) waited for the next read: from each read
// in the range (and from the range's start) to the read after it, up to the first read at or
// after b. Time after the last read is not counted; it is not covered at all.
func worstReadGap(reads []time.Time, from, b time.Time) time.Duration {
	var worst time.Duration
	at := from
	for _, r := range reads {
		if !r.After(at) {
			continue
		}
		worst = max(worst, r.Sub(at))
		if !r.Before(b) {
			break
		}
		at = r
	}
	return worst
}

func maxTime(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// chatSteps is the WorkIQ chat counting steps from since: counts only, no messages.
func (c *coverageRun) chatSteps(account string, since time.Time) ([]startStep, error) {
	key := "chats\x00" + account
	if r, ok := c.routes[key]; ok {
		return routeSteps(r), nil
	}
	setup, err := c.rt.workIQSetup(c.st)
	if err != nil {
		return nil, err
	}
	r := c.rt.newWorkIQRoute(setup, "chats", account, routeRangeNotCovered, "The archive does not hold all of this range's chat messages. WorkIQ chat history stays counts-only: coverage counts the server's messages in this range, but adds no messages to the archive.", time.Time{})
	args, does := []string{"workiq", "coverage", "--source", "chats"}, "count the server's chat messages from this range's start, without adding them"
	if since.IsZero() {
		does = "count the server's chat messages over WorkIQ's default window (365 days), without adding them" // the range has no start
	} else {
		args = append(args, "--since", since.In(time.Local).Format(time.RFC3339))
	}
	r.Next = append(r.Next, startStep{Command: c.rt.recoveryCommand(false, account, args...), Does: does})
	c.routes[key] = &r
	return r.Next, nil
}

// chatNoAccount is the entry of a chat scope when the archive holds no Teams account in scope.
func chatNoAccount(scope string) coverageEntry {
	e := newCoverageEntry("chats", scope, "")
	e.reason(reasonNeverRead, "no Teams account is in the archive")
	e.finish()
	return e
}

// ---- shared ----

// entryName is the source, scope and account of an entry, the ones it has.
func entryName(e coverageEntry) []string {
	parts := []string{e.Source}
	if e.Scope != "" {
		parts = append(parts, e.Scope)
	}
	if e.Account != nil {
		parts = append(parts, *e.Account)
	}
	return parts
}

// coverageLine is one entry as a line of text output.
func coverageLine(e coverageEntry) string {
	line := strings.Join(entryName(e), " ") + ": " + e.State + " (" + plural(e.Items, "item", "items")
	if e.State == covPartial && e.CoveredUntil != nil {
		line += "; covered " + stamp(*e.CoveredFrom) + " to " + stamp(*e.CoveredUntil)
	}
	line += ")"
	var codes []string
	for _, r := range e.Reasons {
		codes = append(codes, r.Code)
	}
	if len(codes) > 0 {
		line += " " + strings.Join(codes, ", ")
	}
	return line
}
