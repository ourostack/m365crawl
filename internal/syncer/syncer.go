// Package syncer turns a snapshot of the Teams desktop cache into archive updates: for every
// Teams origin it fingerprints the files, skips an unchanged one, otherwise copies, decodes and
// maps the allowlisted records and applies them to the SQLite archive.
package syncer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
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
// archive's run lock. Failures return an error and no report; the attempt is still recorded in
// sync_runs with status "failed". Only runs without an account filter record a fingerprint, and
// a source is skipped ("unchanged") only when an unfiltered run's stored fingerprint for it
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
	fail := func(err error) (Report, []Change, error) {
		now := time.Now().UTC()
		// The attempt is recorded even when ctx is cancelled.
		_ = st.RecordRun(context.WithoutCancel(ctx), store.Run{StartedAt: started, FinishedAt: now, Source: r.current, Status: statusFailed})
		return Report{}, nil, err
	}
	// A bug that panics while decoding must not crash the caller: the source's transaction and
	// snapshot are already unwound by their own defers, so report it as an internal error.
	defer func() {
		if p := recover(); p != nil {
			rep, changes, err = fail(errs.Internal(fmt.Errorf("panic while syncing: %v", p)))
		}
	}()
	rep, changes, err = r.run(ctx, started)
	if err != nil {
		return fail(err)
	}
	return rep, changes, nil
}

type runner struct {
	o       Options
	st      *store.Store
	current string // source being processed, for the failed-run record
}

func (r *runner) progress(format string, args ...any) {
	if r.o.Progress != nil {
		_, _ = r.o.Progress.Write([]byte(sprintf(format, args...) + "\n"))
	}
}

func (r *runner) run(ctx context.Context, started time.Time) (Report, []Change, error) {
	root := r.o.Root
	if root == "" {
		root = teamsdesktop.DefaultRoot()
	}
	sources, other, err := teamsdesktop.Discover(root)
	if err != nil {
		return Report{}, nil, err
	}
	rep := Report{Omissions: map[string]int{}, OtherOrigins: other, StartedAt: started}
	if rep.OtherOrigins == nil {
		rep.OtherOrigins = []string{}
	}
	var changes []Change
	decoded := false
	for _, src := range sources {
		if err := ctx.Err(); err != nil {
			return Report{}, nil, err
		}
		r.current = src.Key()
		sr, didDecode, err := r.source(ctx, src, &rep, &changes)
		if err != nil {
			return Report{}, nil, err
		}
		decoded = decoded || didDecode
		rep.Sources = append(rep.Sources, sr)
		for k, v := range sr.Omissions {
			rep.Omissions[k] += v
		}
	}
	r.current = ""
	switch {
	case !decoded:
		rep.Status = StatusUnchanged
	case sum(rep.Omissions) > 0:
		rep.Status = StatusOmissions
	default:
		rep.Status = StatusOK
	}
	rep.FinishedAt = time.Now().UTC()
	return rep, changes, nil
}

// runCounts is the counts_json of a sync_runs row.
type runCounts struct {
	Conversations store.Counts `json:"conversations"`
	Messages      store.Counts `json:"messages"`
	People        store.Counts `json:"people"`
	Activity      store.Counts `json:"activity"`
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
		select {
		case <-time.After(d):
		case <-ctx.Done():
			return SourceReport{}, false, ctx.Err()
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
	if err := w.finish(); err != nil {
		return SourceReport{}, false, err
	}
	status := StatusOK
	if sum(omissions) > 0 {
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
	*changes = append(*changes, w.changes...)
	r.progress("%s: %s (%d messages, %d conversations, %d activity items)", src.Key(), status, w.counts.Messages.Seen, w.counts.Conversations.Seen, w.counts.Activity.Seen)
	return SourceReport{Source: src.Key(), Status: status, Omissions: omissions}, true, nil
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
	batchSize     = 2000
	beforeFlush   = func(kind string, n int) error { return nil }
	afterSnapshot = func() {}
)
