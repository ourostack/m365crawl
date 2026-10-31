package syncer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/workiq"
)

// WorkIQMailWindow selects a range-bounded mail sync of [Since, Until) on the received clock.
// Coverage is "" (choose per folder), "proof" (delta everywhere) or "fast" (listing everywhere).
type WorkIQMailWindow struct {
	Since, Until time.Time
	Coverage     string
}

// WorkIQWindowReport is what a window run fetched and proved.
type WorkIQWindowReport struct {
	Since   time.Time            `json:"since"`
	Until   time.Time            `json:"until"`
	Clock   string               `json:"clock"`
	Covered bool                 `json:"covered"`
	Folders []WorkIQWindowFolder `json:"folders"`
}

// WorkIQWindowFolder is one folder of a window run.
type WorkIQWindowFolder struct {
	FolderID      string `json:"folder_id"`
	Mode          string `json:"mode"`
	ItemsInWindow int64  `json:"items_in_window"`
	ItemsOpen     int64  `json:"items_open"`
	Covered       bool   `json:"covered"`
}

// Window run limits and disk defaults. An out-of-window item keeps only its member row; 16 KB
// covers that row, its page row and the write-ahead log of its cleared page.
const (
	workIQWindowRounds          = 5
	workIQWindowOutBytesPerItem = 16 << 10
	workIQWindowDeltaFloor      = 1000
)

var workIQWellKnown = []struct{ path, kind string }{
	{"inbox", "inbox"}, {"sentitems", "sent"}, {"drafts", "drafts"}, {"deleteditems", "deleted"}, {"junkemail", "junk"}, {"archive", "archive"},
}

// workIQWindowFailure is a coded window stop. workiq_window_unstable and workiq_disk_reserve keep
// every position, so the same command run again continues.
func workIQWindowFailure(code, fix string) error {
	exit := errs.ExitRuntime
	if code == "workiq_disk_space" {
		exit = errs.ExitEnvironment
	}
	return &errs.Coded{Code: code, Exit: exit, Message: "the explicit WorkIQ source could not complete (" + code + ")", Fix: fix}
}

type workIQWindowFolderRun struct {
	folder       workiq.Folder
	mode         string
	resource     string
	nWindow      int64
	nOpen        int64
	facts        store.WorkIQWindowFacts
	expiredOnce  bool
	unknownClock bool
}

type workIQWindowRun struct {
	options   WorkIQOptions
	session   *workIQSession
	attempt   int64
	account   string
	window    WorkIQMailWindow
	report    *WorkIQAccountReport
	out       *WorkIQWindowReport
	pages     int
	folders   []*workIQWindowFolderRun
	baseBytes int64
	predicted int64
	staged    int64
	factor    float64
	largest   int64
}

func runWorkIQMailWindow(ctx context.Context, options WorkIQOptions) (WorkIQReport, error) {
	options.Kind = "mail_window"
	report := WorkIQReport{Kind: "sync", Counts: map[string]int{}, Gaps: map[string]int{}}
	window := *options.Window
	var enabled bool
	var facts []workiq.AccountFact
	if err := workIQRead(ctx, options.DBPath, func(s *store.Store) (err error) {
		enabled, err = s.WorkIQEnabled(ctx)
		if err == nil && enabled {
			facts, err = s.WorkIQAccounts(ctx)
		}
		return err
	}); err != nil {
		return report, err
	}
	report.Enabled = enabled
	if !enabled {
		return report, workIQFailure("workiq_disabled")
	}
	user, err := workIQBootstrap(ctx, options)
	if err != nil {
		return failedWorkIQStartupReport(ctx, options, report, facts, "mail", options.Account, err)
	}
	binding, err := workiq.SelectMailAccount(user, facts, options.Account)
	if err != nil {
		return failedWorkIQStartupReport(ctx, options, report, facts, "mail", options.Account, workIQAccountChoice(err, facts))
	}
	options.Account = binding.Fact.ID
	if err := holdWorkIQScope(ctx, options, store.WorkIQScope{Family: "mail", Account: binding.Fact.ID, Kind: "mail_window"}); err != nil {
		return report, err
	}
	client, pinned, err := workIQPinned(ctx, options, binding)
	if err != nil {
		return failedWorkIQStartupReport(ctx, options, report, facts, "mail", binding.Fact.ID, err)
	}
	session := &workIQSession{options: options, binding: binding, client: client}
	var attempt int64
	started := workIQNow()
	err = workIQWrite(ctx, options.DBPath, func(writeCtx context.Context, s *store.Store) (err error) {
		attempt, err = s.BeginWorkIQAttempt(writeCtx, store.WorkIQScope{Family: "mail", Account: binding.Fact.ID, Kind: "mail_window",
			WindowStart: window.Since, WindowEnd: window.Until}, started)
		if err != nil {
			return err
		}
		return s.VerifyWorkIQAttempt(writeCtx, attempt, binding, pinned, started)
	})
	account := WorkIQAccountReport{Family: "mail", Account: binding.Fact.ID, Counts: map[string]int{}, Gaps: map[string]int{}}
	out := &WorkIQWindowReport{Since: window.Since, Until: window.Until, Clock: "received", Folders: []WorkIQWindowFolder{}}
	report.Window = out
	if err == nil {
		r := &workIQWindowRun{options: options, session: session, attempt: attempt, account: binding.Fact.ID, window: window,
			report: &account, out: out, pages: options.pageWork, factor: 1}
		err = r.acquire(ctx)
	}
	cleanupErr := session.close()
	err = errors.Join(err, cleanupErr)
	account.Status = "ok"
	code := ""
	err = workIQStuck(ctx, options, "mail", account.Account, err)
	account.Diagnostic = workIQDiagnostic(err)
	if err != nil {
		account.Status, code = "failed", workIQOutcomeCode(err)
		gap := code
		if !strings.HasPrefix(gap, "workiq_") {
			gap = "workiq_failed"
		}
		account.Gaps[gap]++
	} else if len(account.Gaps) > 0 {
		account.Status = "ok_with_omissions"
	}
	var persistErr error
	if attempt != 0 {
		persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), workIQPersistBudget)
		persistErr = workIQWrite(persistCtx, options.DBPath, func(writeCtx context.Context, s *store.Store) error {
			return s.CompleteWorkIQAttempt(writeCtx, attempt, store.WorkIQSummary{
				Status: account.Status, ErrorCode: code, FinishedAt: workIQNow(), Folders: account.Counts["folders"],
				Counts: account.Counts, Gaps: account.Gaps, Diagnostic: account.Diagnostic, StopAt: workIQStopAt(err),
			})
		})
		cancel()
		err = errors.Join(err, persistErr)
		if persistErr != nil {
			account.Status = "failed"
			account.Gaps["workiq_persistence_failed"]++
		}
	}
	report.Accounts = append(report.Accounts, account)
	for name, n := range account.Counts {
		report.Counts[name] += n
	}
	for name, n := range account.Gaps {
		report.Gaps[name] += n
	}
	report.Resumable = workIQWindowResumable[code] && account.Gaps[code] > 0 && cleanupErr == nil && persistErr == nil
	return report, err
}

// workIQWindowResumable are the window stops that keep every position.
var workIQWindowResumable = map[string]bool{"workiq_page_limit": true, "workiq_archive_busy": true, "workiq_retry_later": true,
	"workiq_window_unstable": true, "workiq_disk_reserve": true}

func (r *workIQWindowRun) acquire(ctx context.Context) error {
	for after, done := int64(0), false; !done; {
		if err := workIQWrite(ctx, r.options.DBPath, func(writeCtx context.Context, s *store.Store) (err error) {
			after, done, err = s.IndexWorkIQMailCandidates(writeCtx, r.attempt, after, 128)
			return err
		}); err != nil {
			return err
		}
	}
	tree, digest, err := r.tree(ctx)
	if err != nil {
		return err
	}
	r.report.Counts["folders"] = len(tree)
	if err := workIQWrite(ctx, r.options.DBPath, func(writeCtx context.Context, s *store.Store) error {
		return s.EnsureWorkIQWindowFolders(writeCtx, r.attempt, tree)
	}); err != nil {
		return err
	}
	for _, folder := range tree {
		f, err := r.choose(ctx, folder)
		if err != nil {
			return err
		}
		r.folders = append(r.folders, f)
	}
	if err := r.refreshFacts(ctx); err != nil {
		return err
	}
	r.baseBytes = workIQArchiveBytes(r.options.DBPath)
	if err := r.checkDisk(true, false); err != nil {
		return err
	}
	for _, f := range r.folders {
		if err := r.walk(ctx, f); err != nil {
			return err
		}
	}
	snapshot, err := r.rounds(ctx)
	if err != nil {
		return err
	}
	if _, again, err := r.tree(ctx); err != nil {
		return err
	} else if again != digest {
		return workIQWindowFailure("workiq_window_unstable", "The mail folder tree changed while the window was fetched, so its completeness is not proven. Positions are kept: run the same workiq sync again.")
	}
	if err := r.refreshFacts(ctx); err != nil {
		return err
	}
	if err := r.checkDisk(false, true); err != nil {
		return err
	}
	return r.finish(ctx, snapshot)
}

// tree lists the whole folder tree, hidden and child folders included, with the six well-known
// folders identified by their fixed lookups, and its digest over (id, parent, hidden).
func (r *workIQWindowRun) tree(ctx context.Context) ([]workiq.Folder, string, error) {
	kinds := map[string]string{}
	for _, known := range workIQWellKnown {
		values, err := r.collection(ctx, "/me/mailFolders/"+known.path, true)
		if err != nil {
			return nil, "", err
		}
		f, err := workiq.DecodeFolder(ctx, values[0])
		if err != nil {
			return nil, "", err
		}
		kinds[f.ID] = known.kind
	}
	queue, err := r.folderList(ctx, "/me/mailFolders?includeHiddenFolders=true", "")
	if err != nil {
		return nil, "", err
	}
	seen := map[string]bool{}
	for _, f := range queue {
		seen[f.ID] = true
	}
	for i := 0; i < len(queue); i++ {
		if queue[i].ChildCount != nil && *queue[i].ChildCount == 0 {
			continue
		}
		children, err := r.folderList(ctx, "/me/mailFolders/"+url.PathEscape(queue[i].ID)+"/childFolders?includeHiddenFolders=true", queue[i].ID)
		if err != nil {
			return nil, "", err
		}
		if queue, err = appendWorkIQMailQueue(queue, children, seen); err != nil {
			return nil, "", err
		}
	}
	lines := make([]string, 0, len(queue))
	for i := range queue {
		queue[i].WellKnown = kinds[queue[i].ID]
		hidden := queue[i].Hidden != nil && *queue[i].Hidden
		lines = append(lines, fmt.Sprintf("%q %q %t", queue[i].ID, queue[i].ParentID, hidden))
	}
	sort.Strings(lines)
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return queue, hex.EncodeToString(sum[:]), nil
}

func (r *workIQWindowRun) folderList(ctx context.Context, path, parent string) ([]workiq.Folder, error) {
	values, err := r.collection(ctx, path, false)
	if err != nil {
		return nil, err
	}
	var out []workiq.Folder
	for _, value := range values {
		f, err := workiq.DecodeFolder(ctx, value)
		if err != nil {
			return nil, err
		}
		if parent != "" {
			if f.ParentID != "" && f.ParentID != parent {
				return nil, workIQFailure("workiq_folder_inventory_ambiguous")
			}
			f.ParentID = parent
		}
		out = append(out, f)
	}
	return out, nil
}

// collection reads a whole collection, following its next links, or one entity.
func (r *workIQWindowRun) collection(ctx context.Context, path string, entity bool) ([]json.RawMessage, error) {
	var out []json.RawMessage
	scope, _ := url.ParseRequestURI(path)
	for next := path; next != ""; {
		if _, err := workiq.RelativePath(next, scope.Path); err != nil {
			return nil, err
		}
		page, err := r.fetch(ctx, next)
		if err != nil {
			return nil, err
		}
		if entity {
			return []json.RawMessage{page.Data}, nil
		}
		values, err := workIQCollection(page)
		if err != nil {
			return nil, err
		}
		if len(out)+len(values) > 100000 {
			return nil, workIQFailure("workiq_folder_limit")
		}
		out = append(out, values...)
		next = page.NextCursor
	}
	return out, nil
}

// fetch counts every page against the run's budget. A delta link the server expired comes back as
// workIQExpiredStatus, so the walk can restart that folder.
func (r *workIQWindowRun) fetch(ctx context.Context, path string) (workiq.Page, error) {
	if r.pages >= workIQMailPageLimit {
		r.report.Gaps["workiq_page_limit"]++
		return workiq.Page{}, workIQFailure("workiq_page_limit")
	}
	page, err := r.session.fetch(ctx, path)
	if err != nil {
		if page.StatusCode == 410 {
			return page, &workIQExpiredStatus{err}
		}
		return page, err
	}
	r.pages++
	r.report.Counts["pages"] = r.pages
	return page, nil
}

// count reads a folder's item count for a filter from a one-item page.
func (r *workIQWindowRun) count(ctx context.Context, folder, filter string) (int64, error) {
	page, err := r.fetch(ctx, "/me/mailFolders/"+url.PathEscape(folder)+"/messages?$filter="+filter+"&$top=1&$select=id&$count=true")
	if err != nil {
		return 0, err
	}
	var data struct {
		Count *int64 `json:"@odata.count"`
	}
	if json.Unmarshal(page.Data, &data) != nil || data.Count == nil || *data.Count < 0 {
		return 0, workIQFailure("workiq_protocol")
	}
	return *data.Count, nil
}

// choose counts a folder's window and open range and picks delta, which can prove the window, or
// listing, which only fetches it.
func (r *workIQWindowRun) choose(ctx context.Context, folder workiq.Folder) (*workIQWindowFolderRun, error) {
	since := "receivedDateTime%20ge%20" + r.window.Since.UTC().Format(time.RFC3339)
	f := &workIQWindowFolderRun{folder: folder}
	var err error
	if f.nWindow, err = r.count(ctx, folder.ID, since+"%20and%20receivedDateTime%20lt%20"+r.window.Until.UTC().Format(time.RFC3339)); err != nil {
		return nil, err
	}
	if f.nOpen, err = r.count(ctx, folder.ID, since); err != nil {
		return nil, err
	}
	r.report.Counts["window_items"] += int(f.nWindow)
	r.report.Counts["window_items_open"] += int(f.nOpen)
	f.mode = "delta"
	switch {
	case r.window.Coverage == "fast":
		f.mode = "listing"
	case r.window.Coverage == "proof":
	case f.nOpen > max(2*f.nWindow, workIQWindowDeltaFloor):
		f.mode = "listing"
	}
	f.resource = store.WorkIQWindowDeltaResource(folder.ID, r.window.Since)
	if f.mode == "listing" {
		f.resource = store.WorkIQWindowListingResource(folder.ID, r.window.Since, r.window.Until)
	}
	return f, nil
}

func (r *workIQWindowRun) progress(ctx context.Context, f *workIQWindowFolderRun) (store.WorkIQWindowProgress, error) {
	var p store.WorkIQWindowProgress
	err := workIQRead(ctx, r.options.DBPath, func(s *store.Store) (err error) {
		p, err = s.WorkIQWindowProgressFor(ctx, r.attempt, f.resource)
		return err
	})
	return p, err
}

func (r *workIQWindowRun) refreshFacts(ctx context.Context) error {
	return workIQRead(ctx, r.options.DBPath, func(s *store.Store) error {
		for _, f := range r.folders {
			facts, err := s.WorkIQWindowFactsFor(ctx, r.attempt, f.resource)
			if err != nil {
				return err
			}
			f.facts = facts
			r.largest = max(r.largest, facts.LargestPage)
		}
		return nil
	})
}

// walk fetches a folder's position to its end: a delta walk to its delta link, kept across runs,
// or a listing to its last page. A listing a previous run finished and admitted is walked again; one
// whose mail still waits for admission is kept, so a resumed run admits it instead of fetching it twice.
func (r *workIQWindowRun) walk(ctx context.Context, f *workIQWindowFolderRun) error {
	p, err := r.progress(ctx, f)
	if err != nil {
		return err
	}
	if p.Terminal && f.mode == "listing" && f.facts.Unadmitted == 0 {
		if err := workIQWrite(ctx, r.options.DBPath, func(writeCtx context.Context, s *store.Store) error {
			return s.RetireWorkIQWindowFrontier(writeCtx, r.attempt, f.resource, p.Revision)
		}); err != nil {
			return err
		}
		p = store.WorkIQWindowProgress{}
	}
	for !p.Terminal {
		path := f.resource
		if p.Started {
			path = p.PendingCursor
		}
		if _, err := r.stage(ctx, f, path, p.Revision); err != nil {
			if !workIQExpiredPage(err) {
				return err
			}
			if err := r.expire(ctx, f, p, err); err != nil {
				return err
			}
		}
		if p, err = r.progress(ctx, f); err != nil {
			return err
		}
	}
	return nil
}

// expire retires a position whose cursor the server expired, once per folder and run, so the
// folder is walked again from its start; a second expiry is a gap.
func (r *workIQWindowRun) expire(ctx context.Context, f *workIQWindowFolderRun, p store.WorkIQWindowProgress, cause error) error {
	if f.expiredOnce {
		r.report.Gaps["workiq_delta_expired"]++
		return cause
	}
	f.expiredOnce = true
	return workIQWrite(ctx, r.options.DBPath, func(writeCtx context.Context, s *store.Store) error {
		return s.RetireWorkIQWindowFrontier(writeCtx, r.attempt, f.resource, p.Revision)
	})
}

// stage fetches one page of a folder's position and stores it, after the disk check.
// Staging checked every cursor it saved against the folder, so path is always the folder's own.
func (r *workIQWindowRun) stage(ctx context.Context, f *workIQWindowFolderRun, path string, revision int64) (store.WorkIQWindowPage, error) {
	if err := r.checkDisk(false, false); err != nil {
		return store.WorkIQWindowPage{}, err
	}
	page, err := r.fetch(ctx, path)
	if err != nil {
		return store.WorkIQWindowPage{}, err
	}
	var out store.WorkIQWindowPage
	if err := workIQWrite(workIQPageWrite(ctx, len(page.JSON)), r.options.DBPath, func(writeCtx context.Context, s *store.Store) (err error) {
		out, err = s.StageWorkIQWindowPage(writeCtx, r.attempt, f.folder.ID, f.resource, revision, page, workIQNow())
		return err
	}); err != nil {
		return store.WorkIQWindowPage{}, err
	}
	r.largest = max(r.largest, out.Bytes)
	r.predicted += int64(out.InWindow)*store.WorkIQAcquireBytesPerItem + int64(out.OutOfWindow+out.Removed+out.ClockUnknown)*workIQWindowOutBytesPerItem
	r.staged += int64(out.Items)
	if r.staged >= workIQMeasureAfter && r.predicted > 0 {
		r.factor = max(r.factor, float64(workIQArchiveBytes(r.options.DBPath)-r.baseBytes)/float64(r.predicted))
	}
	if out.Cleared {
		r.report.Counts["window_pages_cleared"]++
	}
	r.report.Counts["duplicates_skipped"] += out.Repeats
	if out.ClockUnknown > 0 {
		f.unknownClock = true
	}
	if err := workIQRead(ctx, r.options.DBPath, func(s *store.Store) (err error) {
		f.facts, err = s.WorkIQWindowFactsFor(ctx, r.attempt, f.resource)
		return err
	}); err != nil {
		return store.WorkIQWindowPage{}, err
	}
	return out, nil
}

// rounds follows every delta folder's latest delta link until one whole round changes nothing that
// can affect the window, and returns the clock read just before that round began.
func (r *workIQWindowRun) rounds(ctx context.Context) (time.Time, error) {
	delta := false
	for _, f := range r.folders {
		delta = delta || f.mode == "delta"
	}
	if !delta {
		return workIQNow(), nil
	}
	for round := 1; round <= workIQWindowRounds; round++ {
		snapshot := workIQNow()
		r.report.Counts["window_rounds"] = round
		affecting := 0
		for _, f := range r.folders {
			if f.mode != "delta" {
				continue
			}
			n, err := r.follow(ctx, f)
			if err != nil {
				return time.Time{}, err
			}
			affecting += n
		}
		if affecting == 0 {
			return snapshot, nil
		}
	}
	return time.Time{}, workIQWindowFailure("workiq_window_unstable", fmt.Sprintf(
		"Mail in this range kept changing for %d rounds, so its completeness is not proven. Positions are kept: run the same workiq sync again later.", workIQWindowRounds))
}

// follow fetches one folder's latest delta link to its next delta link and counts the changes
// that can affect the window. A delta link the server expired restarts the folder's walk once.
func (r *workIQWindowRun) follow(ctx context.Context, f *workIQWindowFolderRun) (int, error) {
	p, err := r.progress(ctx, f)
	if err != nil {
		return 0, err
	}
	affecting := 0
	for first := true; first || !p.Terminal; first = false {
		out, err := r.stage(ctx, f, p.PendingCursor, p.Revision)
		if workIQExpiredPage(err) {
			if err := r.expire(ctx, f, p, err); err != nil {
				return 0, err
			}
			// Every item of the new walk is new to it, so this round cannot be the last.
			return 1, r.walk(ctx, f)
		}
		if err != nil {
			return 0, err
		}
		affecting += out.Affecting
		if p, err = r.progress(ctx, f); err != nil {
			return 0, err
		}
	}
	return affecting, nil
}

// checkDisk compares the archive volume's free space with what the rest of the window needs: the
// fetch of every item not yet staged, the admission of every in-window item not yet admitted,
// fetched or not, transactions and headroom. Nothing lowers the admission reserve before
// admission, and clearing a page body lowers nothing: freed space counts only through the run's
// measured growth.
func (r *workIQWindowRun) checkDisk(first, admission bool) error {
	var remainingIn, remainingOut, unadmitted, bytes int64
	for _, f := range r.folders {
		remainingIn += max(0, f.nWindow-f.facts.InWindow)
		if f.mode == "delta" {
			remainingOut += max(0, f.nOpen-f.nWindow-f.facts.OutOfWindow)
		}
		unadmitted += f.facts.StagedIn
		bytes += f.facts.StagedBytes
	}
	admit := int64(store.WorkIQAdmitBytesPerItem)
	if unadmitted > 0 {
		admit = max(admit, 2*bytes/unadmitted)
	}
	acquire := int64(0)
	if !admission {
		acquire = int64(float64(remainingIn*store.WorkIQAcquireBytesPerItem+remainingOut*workIQWindowOutBytesPerItem) * r.factor)
	} else {
		remainingIn = 0
	}
	need := (acquire+(unadmitted+remainingIn)*admit)*5/4 + 2*max(16<<20, r.largest) + 256<<20 + 1<<30
	free, err := workIQFreeBytes(r.options.DBPath)
	if err != nil {
		return err
	}
	r.report.Counts["disk_needed_bytes"] = int(need)
	r.report.Counts["disk_free_bytes"] = int(free)
	if free >= need {
		return nil
	}
	pages := (remainingIn + remainingOut + store.WorkIQMailItemsPerPage - 1) / store.WorkIQMailItemsPerPage
	cost := fmt.Sprintf("about %d more items in about %d pages need about %s free on the archive's disk, including admitting every in-window item, and %s is free.",
		remainingIn+remainingOut, pages, WorkIQSize(need), WorkIQSize(free))
	if first {
		return workIQWindowFailure("workiq_disk_space", "The WorkIQ mail window stopped before fetching messages: "+cost+
			" Free space or move the archive, or fetch a shorter date range.")
	}
	return workIQWindowFailure("workiq_disk_reserve", "The WorkIQ mail window stopped before the archive's disk ran short: "+cost+
		" Its positions are kept: free space, then run the same workiq sync again.")
}

// finish admits the window's in-window mail and records what it proved.
func (r *workIQWindowRun) finish(ctx context.Context, snapshot time.Time) error {
	resources := make([]string, 0, len(r.folders))
	for _, f := range r.folders {
		resources = append(resources, f.resource)
	}
	for done := false; !done; {
		var got store.WorkIQWindowAdmission
		if err := workIQWrite(ctx, r.options.DBPath, func(writeCtx context.Context, s *store.Store) (err error) {
			got, err = s.AdmitWorkIQWindow(writeCtx, r.attempt, resources, 128)
			return err
		}); err != nil {
			return err
		}
		r.report.Counts["window_admitted"] += got.Admitted
		r.report.Counts["window_updated"] += got.Updated
		r.report.Counts["window_deferred"] += got.Deferred
		r.report.Counts["window_resolved"] += got.Resolved
		r.report.Counts["window_pages_cleared"] += got.Cleared
		for code, n := range got.Gaps {
			r.report.Gaps[code] += n
		}
		done = got.Done
	}
	var proven []string
	all := true
	for _, f := range r.folders {
		covered := f.mode == "delta" && f.facts.ClockUnknown == 0
		if covered {
			proven = append(proven, f.folder.ID)
		} else {
			all = false
		}
		switch {
		case f.mode == "listing":
			r.report.Gaps["workiq_window_unproven"]++
		case f.facts.ClockUnknown > 0:
			r.report.Gaps["workiq_clock_unknown"] += int(f.facts.ClockUnknown)
		}
		r.out.Folders = append(r.out.Folders, WorkIQWindowFolder{FolderID: f.folder.ID, Mode: f.mode, ItemsInWindow: f.nWindow, ItemsOpen: f.nOpen, Covered: covered})
	}
	if err := workIQWrite(ctx, r.options.DBPath, func(writeCtx context.Context, s *store.Store) error {
		return s.RecordWorkIQWindowCoverage(writeCtx, r.attempt, proven, all, snapshot)
	}); err != nil {
		return err
	}
	r.out.Covered = all
	return nil
}

// workIQExpiredPage says a fetch failed because the server expired its delta link.
func workIQExpiredPage(err error) bool {
	var stop *workIQExpiredStatus
	return errors.As(err, &stop)
}

type workIQExpiredStatus struct{ error }

func (e *workIQExpiredStatus) Unwrap() error { return e.error }
