// Package syncer turns a snapshot of the Teams desktop cache into archive updates: for every
// Teams origin it fingerprints the files, skips an unchanged one, otherwise copies, decodes and
// maps the records and applies them to the SQLite archive.
package syncer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/ourostack/teamscrawl/internal/errs"
	"github.com/ourostack/teamscrawl/internal/store"
	"github.com/ourostack/teamscrawl/internal/teamsdesktop"
)

// Options configure one sync.
type Options struct {
	Root    string                // EBWebView directory; empty means teamsdesktop.DefaultRoot()
	DBPath  string                // archive file
	Account *teamsdesktop.Account // only this account's databases; nil means every account
	// Progress receives one human-readable line per source; nil discards them.
	Progress io.Writer
}

// Run syncs every Teams origin under o.Root into the archive at o.DBPath while holding the
// archive's run lock. Each source commits on its own, and a source that fails does not stop the
// others. The report and the changes of the sources that committed come back even when the run
// returns an error: with at least one committed source and one failed source the report's status
// is "partial" and the error has code partial_sync; when no source committed the status is
// "failed" and the error is the first failure's. Whatever happens, the run is recorded in
// sync_runs, and only a run in which every source succeeded counts as a fresh sync (for every
// account, or for the filtered account). Only runs without an account filter record a fingerprint,
// and a source is skipped ("unchanged") only when an unfiltered run's stored fingerprint for it
// equals the current one, so a filtered run can never hide another account's data from a later
// run. changes lists the messages and activity items the store inserted or updated.
func Run(ctx context.Context, o Options) (rep Report, changes []Change, err error) {
	started := time.Now().UTC()
	release, err := store.AcquireLock(o.DBPath)
	if err != nil {
		return Report{}, nil, err
	}
	defer release()
	teamsdesktop.SweepStaleSnapshots(os.TempDir(), staleSnapshotAfter)

	st, err := store.Open(ctx, o.DBPath)
	if err != nil {
		return Report{}, nil, asCoded(err)
	}
	defer func() { _ = st.Close() }()

	r := &runner{o: o, st: st}
	// The attempt is recorded even when ctx is cancelled.
	record := func(status string) error {
		return st.RecordRun(context.WithoutCancel(ctx), store.Run{StartedAt: started, FinishedAt: time.Now().UTC(), Status: status, Accounts: r.scope()})
	}
	// An archive written by an older build gets its derived fields recomputed first, so that this
	// sync's content hashes compare against current ones and the upgrade is not read as edits.
	var migrated *store.Migration
	err = contained(func() (e error) { migrated, e = rederiveArchive(ctx, st); return })
	var coded *errs.Coded
	if errors.As(err, &coded) && coded.Code == errs.CodeArchiveNewer {
		return Report{}, nil, err // before any write: not even a failed-run record
	}
	if err != nil {
		_ = record(StatusFailed)
		return Report{}, nil, errs.DBError(err)
	}
	rep, changes, err = r.run(ctx, started)
	if rep.Status == "" { // failed before any source ran
		_ = record(StatusFailed)
		return rep, changes, err
	}
	rep.Migrated = migrated
	if rerr := record(rep.Status); rerr != nil && err == nil {
		// The sources committed, but the run could not be recorded: it is not a fresh sync.
		return rep, changes, errs.DBError(rerr)
	}
	return rep, changes, err
}

type runner struct {
	o  Options
	st *store.Store
}

// scope is the accounts a run-level sync_runs row covers: every account, or the filtered one.
func (r *runner) scope() []string {
	if a := r.o.Account; a != nil {
		return []string{a.TenantID + "/" + a.UserID}
	}
	return []string{"*"}
}

func (r *runner) progress(format string, args ...any) {
	if r.o.Progress != nil {
		_, _ = r.o.Progress.Write([]byte(sprintf(format, args...) + "\n"))
	}
}

// run syncs every source. It returns an empty-status report only when it fails before any source
// is tried (discovery); afterwards the report is always filled in.
func (r *runner) run(ctx context.Context, started time.Time) (Report, []Change, error) {
	root := r.o.Root
	if root == "" {
		root = teamsdesktop.DefaultRoot()
	}
	var sources []teamsdesktop.Source
	var other []string
	err := contained(func() (e error) { sources, other, e = discoverSources(root); return })
	if err != nil {
		return Report{}, nil, err
	}
	rep := Report{Omissions: map[string]int{}, OtherOrigins: other, StartedAt: started}
	if rep.OtherOrigins == nil {
		rep.OtherOrigins = []string{}
	}
	var (
		changes   []Change
		failures  []sourceFailure
		committed int
		decoded   bool
		stopped   error // cancellation: the sources after it are not tried
	)
	for _, src := range sources {
		if stopped = ctx.Err(); stopped != nil {
			break
		}
		sr, didDecode, err := r.safeSource(ctx, src, &rep, &changes)
		if err != nil {
			// Cancelling removes the snapshot a source is still reading, so its failure can look like
			// a damaged cache: the cancellation is the real reason.
			if stopped = ctx.Err(); stopped != nil {
				break
			}
			coded := codedOf(err)
			failures = append(failures, sourceFailure{src.Key(), err, coded})
			rep.Sources = append(rep.Sources, SourceReport{Source: src.Key(), Status: StatusFailed, Error: &SourceError{Code: coded.Code, Message: bodyMessage(coded)}})
			_ = r.st.RecordRun(context.WithoutCancel(ctx), store.Run{StartedAt: time.Now().UTC(), FinishedAt: time.Now().UTC(), Source: src.Key(), Status: StatusFailed})
			r.progress("%s: failed (%s)", src.Key(), coded.Code)
			continue
		}
		committed++
		decoded = decoded || didDecode
		rep.Sources = append(rep.Sources, sr)
		for k, v := range sr.Omissions {
			rep.Omissions[k] += v
		}
	}
	rep.FinishedAt = time.Now().UTC()
	switch {
	case stopped != nil && committed > 0:
		rep.Status = StatusPartial
	case stopped != nil || (len(failures) > 0 && committed == 0):
		rep.Status = StatusFailed
	case len(failures) > 0:
		rep.Status = StatusPartial
	case !decoded:
		rep.Status = StatusUnchanged
	case lost(rep.Omissions) > 0:
		rep.Status = StatusOmissions
	default:
		rep.Status = StatusOK
	}
	switch {
	case stopped != nil:
		return rep, changes, stopped
	case len(failures) == 0:
		return rep, changes, nil
	case committed == 0:
		return rep, changes, failures[0].err
	}
	return rep, changes, errs.PartialSync(describe(failures))
}

// sourceFailure is a source that failed and why.
type sourceFailure struct {
	source string
	err    error
	coded  *errs.Coded
}

func describe(fs []sourceFailure) string {
	parts := make([]string, len(fs))
	for i, f := range fs {
		parts[i] = fmt.Sprintf("%s (%s)", f.source, f.coded.Code)
	}
	return strings.Join(parts, ", ")
}

// codedOf is err as a coded error, wrapping anything else as an internal error.
func codedOf(err error) *errs.Coded {
	var coded *errs.Coded
	if errors.As(err, &coded) {
		return coded
	}
	return errs.Internal(err)
}

// bodyMessage is a coded error's message with its cause, as the CLI prints it.
func bodyMessage(c *errs.Coded) string {
	if c.Code == errs.CodeDBError && c.Unwrap() != nil {
		return c.Message + ": " + c.Unwrap().Error()
	}
	return c.Message
}

// contained runs fn and turns a panic into an internal error, so a bug never crashes the caller.
func contained(fn func() error) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = errs.Internal(fmt.Errorf("panic while syncing: %v", p))
		}
	}()
	return fn()
}

// safeSource is source with a bug that panics while decoding contained: the source's transaction
// and snapshot are already unwound by their own defers, so the panic becomes that source's
// internal error and the other sources still run.
func (r *runner) safeSource(ctx context.Context, src teamsdesktop.Source, rep *Report, changes *[]Change) (sr SourceReport, decoded bool, err error) {
	defer func() {
		if p := recover(); p != nil {
			sr, decoded, err = SourceReport{}, false, errs.Internal(fmt.Errorf("panic while syncing: %v", p))
		}
	}()
	return r.source(ctx, src, rep, changes)
}

// runCounts is the counts_json of a sync_runs row.
type runCounts struct {
	Conversations store.Counts `json:"conversations"`
	Messages      store.Counts `json:"messages"`
	People        store.Counts `json:"people"`
	Activity      store.Counts `json:"activity"`
	Records       store.Counts `json:"records"`
}

func (r *runner) source(ctx context.Context, src teamsdesktop.Source, rep *Report, changes *[]Change) (SourceReport, bool, error) {
	begun := time.Now().UTC()
	fp, err := teamsdesktop.FingerprintOf(src)
	if err != nil {
		return SourceReport{}, false, err
	}
	if r.o.Account == nil {
		last, err := r.st.LastFingerprint(ctx, src.Key())
		if err != nil {
			return SourceReport{}, false, errs.DBError(err)
		}
		if last == fp {
			if err := r.st.RecordRun(ctx, store.Run{StartedAt: begun, FinishedAt: time.Now().UTC(), Source: src.Key(), Fingerprint: fp, Status: StatusUnchanged}); err != nil {
				return SourceReport{}, false, errs.DBError(err)
			}
			r.progress("%s: unchanged", src.Key())
			return SourceReport{Source: src.Key(), Status: StatusUnchanged}, false, nil
		}
	} else {
		fp = "" // a filtered run says nothing about the other accounts
	}

	snap, cleanup, err := teamsdesktop.Snapshot(ctx, src)
	defer cleanup()
	if err != nil {
		return SourceReport{}, false, err
	}
	if d, _ := time.ParseDuration(os.Getenv(testPauseEnv)); d > 0 { // test hook for the e2e tests
		_, _ = fmt.Fprintln(os.Stderr, testPauseMarker) // lets a test signal from inside the pause
		if os.Getenv(testPauseStubbornEnv) == "1" {
			time.Sleep(d) // a stop that does not finish, so a test can send the second signal
		} else {
			select {
			case <-time.After(d):
			case <-ctx.Done():
				return SourceReport{}, false, ctx.Err()
			}
		}
	}

	afterSnapshot()

	sess, err := r.st.Begin(ctx)
	if err != nil {
		return SourceReport{}, false, errs.DBError(err)
	}
	defer sess.Rollback() // a no-op after Commit; on any failure nothing of this source is kept
	w := &writer{ctx: ctx, sess: sess, seenAcct: map[[2]string]bool{}, people: map[[2]string]teamsdesktop.Person{}}
	omissions, err := teamsdesktop.Read(ctx, snap, r.o.Account, w.add)
	if err != nil {
		return SourceReport{}, false, err
	}
	generic, redacted, err := w.readGeneric(ctx, snap, src.Key(), r.o.Account, begun)
	if err != nil {
		return SourceReport{}, false, err
	}
	if err := w.finish(); err != nil {
		return SourceReport{}, false, err
	}
	mergeOmissions(&omissions, generic)
	status := StatusOK
	if lost(omissions) > 0 {
		status = StatusOmissions
	}
	if len(omissions) == 0 {
		omissions = nil
	}
	if err := sess.RecordRun(ctx, store.Run{StartedAt: begun, FinishedAt: time.Now().UTC(), Source: src.Key(), Fingerprint: fp, Status: status, Counts: w.counts, Omissions: omissions}); err != nil {
		return SourceReport{}, false, errs.DBError(err)
	}
	if err := sess.Commit(); err != nil {
		return SourceReport{}, false, errs.DBError(err)
	}
	add(&rep.Conversations, w.counts.Conversations)
	add(&rep.Messages, w.counts.Messages)
	add(&rep.People, w.counts.People)
	add(&rep.Activity, w.counts.Activity)
	add(&rep.Records, w.counts.Records)
	rep.Redacted += redacted
	*changes = append(*changes, w.changes...)
	r.progress("%s: %s (%d messages, %d conversations, %d activity items, %d records)", src.Key(), status, w.counts.Messages.Seen, w.counts.Conversations.Seen, w.counts.Activity.Seen, w.counts.Records.Seen)
	counts := SourceCounts(w.counts)
	return SourceReport{Source: src.Key(), Status: status, Omissions: omissions, Redacted: redacted, Accounts: w.accounts(), Counts: &counts}, true, nil
}

// writer maps records as Read decodes them and hands them to the source's transaction in batches
// of at most batchSize, so memory stays bounded. People are merged per (tenant, id) and applied
// once at the end: the name of the newest sighting wins and SeenAt is the newest sighting, so the
// result does not depend on record order and a repeated sync changes nothing. Account rows are
// applied before the first record of that account.
type writer struct {
	ctx      context.Context
	sess     *store.Session
	seenAcct map[[2]string]bool
	convs    []teamsdesktop.Conversation
	msgs     []teamsdesktop.Message
	acts     []teamsdesktop.Activity
	recs     []teamsdesktop.GenericRecord
	recBytes int
	people   map[[2]string]teamsdesktop.Person
	counts   runCounts
	changes  []Change // held until the source commits
}

func (w *writer) add(acct teamsdesktop.Account, kind string, v any) error {
	if k := [2]string{acct.TenantID, acct.UserID}; !w.seenAcct[k] {
		if err := w.sess.ApplyAccount(w.ctx, acct); err != nil {
			return asCoded(err)
		}
		w.seenAcct[k] = true
	}
	switch kind {
	case teamsdesktop.KindReplyChain:
		ms, ps, err := teamsdesktop.MapReplyChain(acct, v)
		if err != nil {
			return err
		}
		w.addPeople(ps)
		for _, m := range ms {
			w.msgs = append(w.msgs, m)
			if len(w.msgs) >= batchSize {
				if err := w.flushMessages(); err != nil {
					return err
				}
			}
		}
	case teamsdesktop.KindConversation:
		c, ps, err := teamsdesktop.MapConversation(acct, v)
		if err != nil {
			return err
		}
		w.addPeople(ps)
		if w.convs = append(w.convs, c); len(w.convs) >= batchSize {
			return w.flushConversations()
		}
	case teamsdesktop.KindActivity:
		a, err := teamsdesktop.MapActivity(acct, v)
		if err != nil {
			return err
		}
		if w.acts = append(w.acts, a); len(w.acts) >= batchSize {
			return w.flushActivity()
		}
	}
	return nil
}

func (w *writer) flushConversations() error {
	if len(w.convs) == 0 {
		return nil
	}
	if err := beforeFlush("conversation", len(w.convs)); err != nil {
		return err
	}
	n, err := w.sess.ApplyConversations(w.ctx, w.convs)
	if err != nil {
		return asCoded(err)
	}
	add(&w.counts.Conversations, n)
	w.convs = nil
	return nil
}

func (w *writer) flushMessages() error {
	if len(w.msgs) == 0 {
		return nil
	}
	if err := beforeFlush("message", len(w.msgs)); err != nil {
		return err
	}
	n, ch, err := w.sess.ApplyMessagesChanges(w.ctx, w.msgs)
	if err != nil {
		return asCoded(err)
	}
	add(&w.counts.Messages, n)
	for _, x := range ch {
		w.changes = append(w.changes, Change{Kind: kindMessage, Change: x.Change, Key: x.Key})
	}
	w.msgs = nil
	return nil
}

func (w *writer) flushActivity() error {
	if len(w.acts) == 0 {
		return nil
	}
	if err := beforeFlush("activity", len(w.acts)); err != nil {
		return err
	}
	n, ch, err := w.sess.ApplyActivityChanges(w.ctx, w.acts)
	if err != nil {
		return asCoded(err)
	}
	add(&w.counts.Activity, n)
	for _, x := range ch {
		w.changes = append(w.changes, Change{Kind: kindActivity, Change: x.Change, Key: x.Key})
	}
	w.acts = nil
	return nil
}

// recordBatchBytes caps the decoded bytes of one batch of generic records, so a few very large
// values flush sooner than batchSize records would (a variable so a test can lower it).
var recordBatchBytes = 4 << 20

// readGeneric archives every record of the object stores that no typed mapper consumes. Right
// after each database is read it flushes that database's records and, when the database was read
// completely, marks the rows that are not seen any more as removed, so the keys seen are dropped
// before the next database is read. Then it marks the rows of databases the origin no longer
// holds, and clears the rows whose database or store name is denied now (credential material is
// cleared, not kept). Marking is skipped for an incomplete or unreadable database, and entirely
// when ReadGeneric fails (the source then fails and nothing of it is kept). It returns the
// omissions the generic read counted and how many values it redacted.
func (w *writer) readGeneric(ctx context.Context, snap, source string, account *teamsdesktop.Account, at time.Time) (map[string]int, int, error) {
	opts := teamsdesktop.GenericOptions{OnDatabase: func(db string, complete bool, seen map[string]map[string]struct{}) error {
		if !complete {
			return nil
		}
		if err := w.flushRecords(source, at); err != nil {
			return err
		}
		if _, err := w.sess.MarkRecordsRemoved(source, db, seen, at); err != nil {
			return asCoded(err)
		}
		return nil
	}}
	res, err := readGenericFn(ctx, snap, account, genericBudget, opts, func(rec teamsdesktop.GenericRecord) error {
		w.recs = append(w.recs, rec)
		if w.recBytes += len(rec.KeyJSON) + len(rec.ValueJSON); len(w.recs) >= batchSize || w.recBytes >= recordBatchBytes {
			return w.flushRecords(source, at)
		}
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	if err := w.flushRecords(source, at); err != nil {
		return nil, 0, err
	}
	if _, err := w.sess.MarkDatabasesRemoved(source, res.Present, account, at); err != nil {
		return nil, 0, asCoded(err)
	}
	if _, err := w.sess.PurgeDenied(source, deniedFn, at); err != nil {
		return nil, 0, asCoded(err)
	}
	return res.Omissions, res.Redacted, nil
}

func (w *writer) flushRecords(source string, at time.Time) error {
	if len(w.recs) == 0 {
		return nil
	}
	if err := beforeFlush("record", len(w.recs)); err != nil {
		return err
	}
	n, err := w.sess.UpsertRecords(source, w.recs, at)
	if err != nil {
		return asCoded(err)
	}
	add(&w.counts.Records, n)
	w.recs, w.recBytes = nil, 0
	return nil
}

// mergeOmissions adds the generic read's omission counts to the typed read's. A truncated log
// tail is one fact both reads see, so it takes the larger count instead of the sum.
func mergeOmissions(into *map[string]int, from map[string]int) {
	if *into == nil {
		*into = map[string]int{}
	}
	for k, v := range from {
		if k == "truncated_log_tail" {
			(*into)[k] = max((*into)[k], v)
		} else {
			(*into)[k] += v
		}
	}
}

// finish flushes what is left (conversations first) and applies the merged people.
func (w *writer) finish() error {
	if err := w.flushConversations(); err != nil {
		return err
	}
	if err := w.flushMessages(); err != nil {
		return err
	}
	if err := w.flushActivity(); err != nil {
		return err
	}
	n, err := w.sess.ApplyPeople(w.ctx, w.peopleList())
	if err != nil {
		return asCoded(err)
	}
	add(&w.counts.People, n)
	return nil
}

// accounts lists the accounts the source had records for, as "<tenantId>/<userId>".
func (w *writer) accounts() []string {
	out := make([]string, 0, len(w.seenAcct))
	for k := range w.seenAcct {
		out = append(out, k[0]+"/"+k[1])
	}
	sort.Strings(out)
	return out
}

func (w *writer) addPeople(ps []teamsdesktop.Person) {
	for _, p := range ps {
		k := [2]string{p.TenantID, p.ID}
		old, ok := w.people[k]
		if !ok {
			w.people[k] = p
			continue
		}
		if (!p.SeenAt.Before(old.SeenAt) && p.DisplayName != "") || old.DisplayName == "" {
			old.DisplayName = p.DisplayName
		}
		if p.SeenAt.After(old.SeenAt) {
			old.SeenAt = p.SeenAt
		}
		w.people[k] = old
	}
}

func (w *writer) peopleList() []teamsdesktop.Person {
	out := make([]teamsdesktop.Person, 0, len(w.people))
	for _, p := range w.people {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].TenantID != out[j].TenantID {
			return out[i].TenantID < out[j].TenantID
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// asCoded leaves coded and cancellation errors alone and wraps everything else as an archive error.
func asCoded(err error) error {
	var coded *errs.Coded
	if errors.As(err, &coded) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return errs.DBError(err)
}

// Test seams: the most records the syncer hands the store at once, a hook called before each
// batch is applied, and a hook called once a source's snapshot is taken, before its transaction
// begins.
var (
	discoverSources = teamsdesktop.Discover
	genericBudget   = teamsdesktop.DefaultGenericBudget
	deniedFn        = teamsdesktop.Denied
	readGenericFn   = teamsdesktop.ReadGeneric
	rederiveArchive = func(ctx context.Context, st *store.Store) (*store.Migration, error) { return st.Rederive(ctx) }
	batchSize       = 2000
	beforeFlush     = func(kind string, n int) error { return nil }
	afterSnapshot   = func() {}
)
