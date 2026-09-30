// Package syncer turns a snapshot of the Teams desktop cache into archive updates: for every
// Teams origin it fingerprints the files, skips an unchanged one, otherwise copies, decodes and
// maps the allowlisted records and applies them to the SQLite archive.
package syncer

import (
	"context"
	"errors"
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
func Run(ctx context.Context, o Options) (Report, []Change, error) {
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
	rep, changes, err := r.run(ctx, started)
	if err != nil {
		now := time.Now().UTC()
		// The attempt is recorded even when ctx is cancelled.
		_ = st.RecordRun(context.WithoutCancel(ctx), store.Run{StartedAt: started, FinishedAt: now, Source: r.current, Status: statusFailed})
		return Report{}, nil, err
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
	if d, _ := time.ParseDuration(os.Getenv(testPauseEnv)); d > 0 { // test hook for the SIGINT e2e test
		select {
		case <-time.After(d):
		case <-ctx.Done():
			return SourceReport{}, false, ctx.Err()
		}
	}

	b := newBatch()
	omissions, err := teamsdesktop.Read(ctx, snap, r.o.Account, b.add)
	if err != nil {
		return SourceReport{}, false, err
	}
	var counts runCounts
	if err := r.apply(ctx, b, &counts, changes); err != nil {
		return SourceReport{}, false, err
	}
	add(&rep.Conversations, counts.Conversations)
	add(&rep.Messages, counts.Messages)
	add(&rep.People, counts.People)
	add(&rep.Activity, counts.Activity)

	status := StatusOK
	if sum(omissions) > 0 {
		status = StatusOmissions
	}
	if len(omissions) == 0 {
		omissions = nil
	}
	if err := r.st.RecordRun(ctx, store.Run{StartedAt: begun, FinishedAt: time.Now().UTC(), Source: src.Key(), Fingerprint: fp, Status: status, Counts: counts, Omissions: omissions}); err != nil {
		return SourceReport{}, false, errs.DBError(err)
	}
	r.progress("%s: %s (%d messages, %d conversations, %d activity items)", src.Key(), status, counts.Messages.Seen, counts.Conversations.Seen, counts.Activity.Seen)
	return SourceReport{Source: src.Key(), Status: status, Omissions: omissions}, true, nil
}

// apply writes one source's mapped records: accounts, conversations, people, messages, activity.
func (r *runner) apply(ctx context.Context, b *batch, c *runCounts, changes *[]Change) error {
	var err error
	for _, a := range b.accounts {
		if err = r.st.ApplyAccount(ctx, a); err != nil {
			return asCoded(err)
		}
	}
	if c.Conversations, err = r.st.ApplyConversations(ctx, b.convs); err != nil {
		return asCoded(err)
	}
	if c.People, err = r.st.ApplyPeople(ctx, b.peopleList()); err != nil {
		return asCoded(err)
	}
	var mc, ac []store.Change
	if c.Messages, mc, err = r.st.ApplyMessagesChanges(ctx, b.msgs); err != nil {
		return asCoded(err)
	}
	if c.Activity, ac, err = r.st.ApplyActivityChanges(ctx, b.acts); err != nil {
		return asCoded(err)
	}
	for _, x := range mc {
		*changes = append(*changes, Change{Kind: kindMessage, Change: x.Change, Key: x.Key})
	}
	for _, x := range ac {
		*changes = append(*changes, Change{Kind: kindActivity, Change: x.Change, Key: x.Key})
	}
	return nil
}

// batch collects one source's mapped records in memory. People are merged per (tenant, id): the
// name of the newest sighting wins and SeenAt is the newest sighting, so the result does not
// depend on record order and a repeated sync changes nothing.
type batch struct {
	accounts []teamsdesktop.Account
	seenAcct map[[2]string]bool
	convs    []teamsdesktop.Conversation
	msgs     []teamsdesktop.Message
	acts     []teamsdesktop.Activity
	people   map[[2]string]teamsdesktop.Person
}

func newBatch() *batch {
	return &batch{seenAcct: map[[2]string]bool{}, people: map[[2]string]teamsdesktop.Person{}}
}

func (b *batch) add(acct teamsdesktop.Account, kind string, v any) error {
	if k := [2]string{acct.TenantID, acct.UserID}; !b.seenAcct[k] {
		b.seenAcct[k] = true
		b.accounts = append(b.accounts, acct)
	}
	switch kind {
	case teamsdesktop.KindReplyChain:
		ms, ps, err := teamsdesktop.MapReplyChain(acct, v)
		if err != nil {
			return err
		}
		b.msgs = append(b.msgs, ms...)
		b.addPeople(ps)
	case teamsdesktop.KindConversation:
		c, ps, err := teamsdesktop.MapConversation(acct, v)
		if err != nil {
			return err
		}
		b.convs = append(b.convs, c)
		b.addPeople(ps)
	case teamsdesktop.KindActivity:
		a, err := teamsdesktop.MapActivity(acct, v)
		if err != nil {
			return err
		}
		b.acts = append(b.acts, a)
	}
	return nil
}

func (b *batch) addPeople(ps []teamsdesktop.Person) {
	for _, p := range ps {
		k := [2]string{p.TenantID, p.ID}
		old, ok := b.people[k]
		if !ok {
			b.people[k] = p
			continue
		}
		if !p.SeenAt.Before(old.SeenAt) && p.DisplayName != "" || old.DisplayName == "" {
			old.DisplayName = p.DisplayName
		}
		if p.SeenAt.After(old.SeenAt) {
			old.SeenAt = p.SeenAt
		}
		b.people[k] = old
	}
}

func (b *batch) peopleList() []teamsdesktop.Person {
	out := make([]teamsdesktop.Person, 0, len(b.people))
	for _, p := range b.people {
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
