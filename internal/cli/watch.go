package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/openclaw/crawlkit/output"

	"github.com/ourostack/teamscrawl/internal/errs"
	"github.com/ourostack/teamscrawl/internal/store"
	"github.com/ourostack/teamscrawl/internal/syncer"
	"github.com/ourostack/teamscrawl/internal/teamsdesktop"
)

// Test seams.
var (
	// watchQuiet is how long the cache must stay quiet before a sync starts. Teams writes in
	// bursts. The least time between the end of one sync and the start of the next is --min-interval.
	watchQuiet = 2 * time.Second
	// watchMaxWait caps the debounce: a sync starts at most this long after a burst's first event.
	watchMaxWait = 10 * time.Second
	// watchLockedRetry is the wait before retrying a sync that found the archive locked.
	watchLockedRetry = 5 * time.Second
	runSync          = syncer.Run
	discover         = teamsdesktop.Discover
	fingerprintOf    = teamsdesktop.FingerprintOf
	newFSWatcher     = fsnotify.NewWatcher
	// watchEvents reports (coalesced) file-system events under dirs on the returned channel until
	// ctx is cancelled or the returned function is called.
	watchEvents = fileEvents
)

type watchCmd struct {
	Every       time.Duration `default:"60s" help:"Poll interval: the safety net when file events are missed. Syncs run only when the cache changed." placeholder:"DURATION"`
	MinInterval time.Duration `name:"min-interval" env:"TEAMSCRAWL_WATCH_MIN_INTERVAL" default:"60s" help:"Least time between the end of one sync and the start of the next. A busy Teams cache changes constantly, so without a pause watch would sync back to back. Changes that arrive meanwhile are coalesced into one sync. 0 disables the pause." placeholder:"DURATION"`
	EmitInitial bool          `name:"emit-initial" help:"Also emit the first sync's changes (by default that sync is a silent baseline and only later changes are emitted)."`
}

// changeLine is one message or activity change; item has the shape of the `messages` or `activity`
// items, shaped by --fields and --max-text.
type changeLine struct {
	Kind   string `json:"kind"`   // message | activity
	Change string `json:"change"` // new | edited | deleted
	Item   any    `json:"item"`
}

type syncLine struct {
	Kind   string        `json:"kind"` // sync
	Report syncer.Report `json:"report"`
}

// migratedLine says the archive's derived fields (text, names) were recomputed from the stored
// records because this build derives them differently. It is not a change: no edited lines follow.
type migratedLine struct {
	Kind string `json:"kind"` // migrated
	store.Migration
}

type errorLine struct {
	Kind  string    `json:"kind"` // error
	Error errorBody `json:"error"`
}

func (c *watchCmd) Run(rt *runtime) error {
	if c.Every <= 0 {
		return errs.Usage("--every must be greater than 0")
	}
	if c.MinInterval < 0 {
		return errs.Usage("--min-interval must not be negative (0 disables the pause)")
	}
	if err := checkWatchFields(rt); err != nil {
		return err
	}
	w := &watcher{rt: rt, every: c.Every, minInterval: c.MinInterval, emitInitial: c.EmitInitial}
	return w.run()
}

// checkWatchFields accepts any key that messages or activity items have.
func checkWatchFields(rt *runtime) error {
	if len(rt.fields) == 0 {
		return nil
	}
	valid := append(jsonKeys(reflect.TypeFor[messageItem]()), jsonKeys(reflect.TypeFor[activityItem]())...)
	valid = removeKey(valid, "text_truncated")
	for _, f := range rt.fields {
		if !contains(valid, f) {
			c := errs.Usage(fmt.Sprintf("unknown --fields key %q; valid keys: %s", f, strings.Join(dedupe(valid), ", ")))
			c.Fix = "Pick keys from the list in the message."
			return c
		}
	}
	return nil
}

func dedupe(ss []string) []string {
	var out []string
	for _, s := range ss {
		if !contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

type watcher struct {
	rt          *runtime
	every       time.Duration
	minInterval time.Duration // least time from the end of a sync to the start of the next
	emitInitial bool

	fps            map[string]string // source -> fingerprint at the last successful sync
	baselined      bool
	baselineFailed bool   // a sync failed before the baseline succeeded
	lastErr        string // the last failure reported, so a repeated failure is reported once
}

// run is the watch loop. It returns nil when the context is cancelled and an error only for
// failures that end the watch (environment problems).
func (w *watcher) run() error {
	ctx := w.rt.ctx
	srcs, _, err := discover(w.rootDir())
	if err != nil {
		return err
	}
	var dirs []string
	for _, s := range srcs {
		dirs = append(dirs, s.LevelDBDir, s.BlobDir)
	}
	events, closeEvents, err := watchEvents(ctx, dirs)
	if err != nil { // polling still works
		w.rt.printWarning(errs.Internal(fmt.Errorf("file events unavailable, polling every %s: %w", w.every, err)))
		events, closeEvents = nil, func() {}
	}
	defer closeEvents()

	ticker := time.NewTicker(w.every)
	defer ticker.Stop()
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()

	var (
		want      = true // a sync is wanted; the first one is the baseline
		lastEvent time.Time
		burstFrom time.Time // the first event since the last sync started
		lastEnd   time.Time // when the last sync finished; zero until one has, so the first is immediate
		retryAt   time.Time
	)
	arm := func() {
		timer.Stop() // Go 1.23 timers never leave a stale value on C after Stop
		if !want {
			return
		}
		settled := lastEvent.Add(watchQuiet)
		if !burstFrom.IsZero() && burstFrom.Add(watchMaxWait).Before(settled) {
			settled = burstFrom.Add(watchMaxWait) // events keep coming: stop waiting for quiet
		}
		due := latest(settled, lastEnd.Add(w.minInterval), retryAt)
		timer.Reset(max(time.Until(due), 0))
	}
	arm()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-events:
			want, lastEvent = true, time.Now()
			if burstFrom.IsZero() {
				burstFrom = lastEvent
			}
			arm()
		case <-ticker.C:
			if want {
				continue // already scheduled (or backing off)
			}
			changed, err := w.changed()
			if err != nil {
				if fatal := w.report(err); fatal != nil {
					return fatal
				}
				continue
			}
			if changed {
				want, lastEvent = true, time.Now()
				if burstFrom.IsZero() {
					burstFrom = lastEvent
				}
				arm()
			}
		case <-timer.C: // armed only while a sync is wanted
			burstFrom = time.Time{}
			done, err := w.sync()
			if !isLocked(err) { // a held lock cost nothing, so it retries on its own short backoff
				lastEnd = time.Now()
			}
			switch {
			case ctx.Err() != nil:
				return nil
			case err != nil:
				if fatal := w.report(err); fatal != nil {
					return fatal
				}
				retryAt = time.Now().Add(w.retryAfter(err))
			default:
				want = !done
			}
			arm()
		}
	}
}

func latest(ts ...time.Time) time.Time {
	var out time.Time
	for _, t := range ts {
		if t.After(out) {
			out = t
		}
	}
	return out
}

func (w *watcher) rootDir() string {
	if w.rt.root != "" {
		return w.rt.root
	}
	return teamsdesktop.DefaultRoot()
}

// fingerprints fingerprints every Teams source now.
func (w *watcher) fingerprints() (map[string]string, error) {
	srcs, _, err := discover(w.rootDir())
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(srcs))
	for _, s := range srcs {
		fp, err := fingerprintOf(s)
		if err != nil {
			return nil, err
		}
		out[s.Key()] = fp
	}
	return out, nil
}

func (w *watcher) changed() (bool, error) {
	cur, err := w.fingerprints()
	if err != nil {
		return false, err
	}
	return !sameFingerprints(cur, w.fps), nil
}

func sameFingerprints(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// sync runs one sync if the cache changed since the last one and emits what it found. done is
// true when the wanted sync is satisfied (it ran, or the cache had not changed).
func (w *watcher) sync() (done bool, err error) {
	cur, err := w.fingerprints() // taken before the sync: a write during it triggers the next one
	if err != nil {
		return false, err
	}
	if w.baselined && sameFingerprints(cur, w.fps) {
		return true, nil
	}
	// The sync covers every account so --account only filters what is emitted, never the archive.
	rep, changes, err := runSync(w.rt.ctx, syncer.Options{Root: w.rt.root, DBPath: w.rt.dbPath})
	var coded *errs.Coded
	partial := errors.As(err, &coded) && coded.Code == errs.CodePartialSync && w.rt.ctx.Err() == nil
	// Sources committed but the run could not be recorded: their changes are in the archive too.
	unrecorded := err != nil && !partial && (rep.Status == syncer.StatusOK || rep.Status == syncer.StatusOmissions || rep.Status == syncer.StatusUnchanged)
	if err != nil && !partial && !unrecorded {
		return false, err
	}
	if m := rep.Migrated; m != nil {
		w.migrated(*m)
	}
	if partial || unrecorded {
		// Some sources committed: their changes are in the archive and the next sync will skip
		// them as unchanged, so they are emitted now. The fingerprints stay as they were, which
		// makes the next pass retry the failed sources (or the record).
		w.emitCommitted(rep, changes)
		return false, err
	}
	w.fps, w.lastErr = cur, ""
	first := !w.baselined
	w.baselined = true
	if first && !w.emitInitial {
		if w.baselineFailed {
			// The first attempts failed, so this sync's changes are not emitted.
			w.rt.printWarning(&errs.Coded{Code: "baseline_delayed", Exit: errs.ExitRuntime,
				Message: "the first sync failed, so the sync that finally succeeded was taken as the baseline and its changes are not emitted",
				Fix:     "Read the archive (for example `teamscrawl messages --since 1h`) to catch up on what arrived meanwhile, or restart watch with --emit-initial."})
		}
		return true, nil
	}
	if err := w.emit(rep, changes); err != nil {
		return true, w.reportErr(lostChanges(err, len(changes)))
	}
	return true, nil
}

// lostChanges is the error for changes that were synced but could not be read back to emit.
func lostChanges(err error, n int) error {
	var coded *errs.Coded
	if errors.As(err, &coded) {
		return &errs.Coded{Code: coded.Code, Exit: coded.Exit, Message: fmt.Sprintf("%d change(s) from the last sync could not be emitted: %s", n, coded.Message), Fix: coded.Fix}
	}
	return errs.Internal(fmt.Errorf("%d change(s) from the last sync could not be emitted: %w", n, err))
}

// emitCommitted emits the changes of the sources a partial (or unrecorded) sync committed. The
// first sync that commits anything is the baseline, as for a normal first sync: those changes are
// not emitted unless --emit-initial. Afterwards they are emitted. A failure to read them back is
// reported as an error line; the caller still reports the sync's own error. (A read-back failure
// is a database error, never an environment error that would end the watch, so the result of
// reportErr is nil here.)
func (w *watcher) emitCommitted(rep syncer.Report, changes []syncer.Change) {
	first := !w.baselined
	w.baselined = true
	if first && !w.emitInitial {
		return
	}
	if err := w.emit(rep, changes); err != nil {
		_ = w.reportErr(lostChanges(err, len(changes)))
	}
}

// migrated reports an archive upgrade once, whether or not this sync's changes are emitted.
func (w *watcher) migrated(m store.Migration) {
	if w.rt.format == output.Text {
		_, _ = fmt.Fprintf(w.rt.stdout, "%s archive upgraded: %d rows re-derived (derivation %d to %d); not changes\n", time.Now().Format("15:04:05"), m.Rows, m.From, m.To)
		return
	}
	w.line(migratedLine{Kind: "migrated", Migration: m})
}

// retryAfter is how long to wait before retrying after err: a held lock is usually brief.
func (w *watcher) retryAfter(err error) time.Duration {
	if isLocked(err) {
		return min(watchLockedRetry, w.every)
	}
	return w.every
}

// isLocked reports whether err is the coded "archive locked" failure.
func isLocked(err error) bool {
	var coded *errs.Coded
	return errors.As(err, &coded) && coded.Code == errs.CodeLocked
}

// reportErr keeps a failure to read changes back from ending the watch.
func (w *watcher) reportErr(err error) error {
	if fatal := w.report(err); fatal != nil {
		return fatal
	}
	return nil
}

// report handles a failed sync or check. Locked is a warning on stderr, environment errors end
// the watch (returned), everything else is an error line and the watch continues. A failure
// repeated unchanged is reported once.
func (w *watcher) report(err error) error {
	if w.rt.ctx.Err() != nil {
		return nil
	}
	var coded *errs.Coded
	if !errors.As(err, &coded) {
		coded = errs.Internal(err)
	}
	if coded.Exit == errs.ExitEnvironment {
		return coded
	}
	if !w.baselined {
		w.baselineFailed = true
	}
	sig := coded.Code + ": " + coded.Message
	if sig == w.lastErr {
		return nil
	}
	w.lastErr = sig
	switch {
	case coded.Code == errs.CodeLocked:
		w.rt.printWarning(coded)
	case w.rt.format == output.Text:
		w.rt.printError(coded)
	default:
		w.line(errorLine{Kind: "error", Error: bodyOf(coded)})
	}
	return nil
}

// line writes one compact JSON line to stdout.
func (w *watcher) line(v any) {
	enc := json.NewEncoder(w.rt.stdout)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

// emit prints one line per change (messages and activity items in the sync's order), then the
// sync's report line.
func (w *watcher) emit(rep syncer.Report, changes []syncer.Change) error {
	items, err := w.items(changes)
	if err != nil {
		return err
	}
	text := w.rt.format == output.Text
	for _, c := range changes {
		it, ok := items[c.Kind+"\x00"+c.Key]
		if !ok {
			continue // filtered out (another account, system conversation) or gone
		}
		if text {
			w.textLine(c, it)
			continue
		}
		w.line(changeLine{Kind: c.Kind, Change: c.Change, Item: shape(w.rt, []any{it.item})[0]})
	}
	if text {
		_, _ = fmt.Fprintf(w.rt.stdout, "%s sync %s: %d new, %d updated messages; %d new, %d updated activity items\n",
			time.Now().Format("15:04:05"), rep.Status, rep.Messages.Inserted, rep.Messages.Updated, rep.Activity.Inserted, rep.Activity.Updated)
		return nil
	}
	w.line(syncLine{Kind: "sync", Report: rep})
	return nil
}

type watchItem struct {
	item   any // messageItem or activityItem, before shaping
	at     time.Time
	conv   string
	sender string
	text   string
	label  string // activity type
}

// items looks the changed rows up in the archive and keeps the ones the watch reports.
func (w *watcher) items(changes []syncer.Change) (map[string]watchItem, error) {
	out := map[string]watchItem{}
	var msgKeys, actKeys []string
	for _, c := range changes {
		if c.Kind == "activity" {
			actKeys = append(actKeys, c.Key)
		} else {
			msgKeys = append(msgKeys, c.Key)
		}
	}
	if len(changes) == 0 {
		return out, nil
	}
	st, err := store.OpenReadOnly(w.rt.ctx, w.rt.dbPath)
	if err != nil {
		return nil, errs.DBError(err)
	}
	defer func() { _ = st.Close() }()
	system := store.SystemConversationIDs()
	keep := func(tenant, user, conv string) bool {
		if a := w.rt.account; a != nil && (a.TenantID != tenant || a.UserID != user) {
			return false
		}
		return !contains(system, conv)
	}
	mrows, err := st.MessagesByKey(w.rt.ctx, msgKeys)
	if err != nil {
		return nil, errs.DBError(err)
	}
	var html map[store.MessageKey]string
	if contains(w.rt.fields, "html") { // as messages --html: fill the body the field asks for
		keys := make([]store.MessageKey, len(mrows))
		for i, r := range mrows {
			keys[i] = store.MessageKey{TenantID: r.TenantID, UserID: r.UserID, ConversationID: r.ConversationID, ID: r.ID}
		}
		if html, err = st.MessageHTML(w.rt.ctx, keys); err != nil {
			return nil, errs.DBError(err)
		}
	}
	for i, it := range messageItems(mrows, w.rt.g.MaxText, html) {
		r := mrows[i]
		if keep(r.TenantID, r.UserID, r.ConversationID) {
			out["message\x00"+r.TenantID+"|"+r.UserID+"|"+r.ConversationID+"|"+r.ID] = watchItem{item: it, at: r.SentAt, conv: r.ConversationDisplayName, sender: r.SenderName, text: it.Text}
		}
	}
	arows, err := st.ActivityByKey(w.rt.ctx, actKeys)
	if err != nil {
		return nil, errs.DBError(err)
	}
	for i, it := range activityItems(arows, w.rt.g.MaxText) {
		r := arows[i]
		if keep(r.TenantID, r.UserID, r.ConversationID) {
			out["activity\x00"+r.TenantID+"|"+r.UserID+"|"+r.ID] = watchItem{item: it, at: r.At, conv: r.ConversationDisplayName, sender: r.SenderName, text: it.Text, label: r.Type}
		}
	}
	return out, nil
}

// textLine prints one change for a person: time, change, kind, where, who and a one-line text.
func (w *watcher) textLine(c syncer.Change, it watchItem) {
	what := c.Kind
	if it.label != "" {
		what += " " + it.label
	}
	text, _ := truncateRunes(oneLine(it.text), 200)
	_, _ = fmt.Fprintf(w.rt.stdout, "%s %s %s  %s | %s: %s\n", time.Now().Format("15:04:05"), c.Change, what, it.conv, it.sender, text)
}

// fileEvents watches dirs with fsnotify (kqueue on macOS, inotify on Linux; no CGO) and sends on
// the returned channel when something in them is created, written, removed or renamed. Events
// coalesce: the channel holds at most one pending signal.
func fileEvents(ctx context.Context, dirs []string) (<-chan struct{}, func(), error) {
	fw, err := newFSWatcher()
	if err != nil {
		return nil, nil, err
	}
	added := 0
	var firstErr error
	for _, d := range dirs {
		if d == "" {
			continue
		}
		if err := fw.Add(filepath.Clean(d)); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		added++
	}
	if added == 0 {
		_ = fw.Close()
		if firstErr == nil {
			firstErr = errors.New("no directories to watch")
		}
		return nil, nil, firstErr
	}
	out := make(chan struct{}, 1)
	stop := make(chan struct{})
	go func() {
		defer func() { _ = fw.Close() }()
		pumpEvents(ctx, stop, fw.Events, fw.Errors, out)
	}()
	var once = make(chan struct{}, 1)
	return out, func() {
		select {
		case once <- struct{}{}:
			close(stop)
		default:
		}
	}, nil
}

// pumpEvents forwards file events to out until ctx is cancelled, stop is closed or a source
// channel closes. Chmod events are ignored, errors are dropped (an overflow loses events and the
// poll covers it) and a signal is dropped when one is already pending.
func pumpEvents(ctx context.Context, stop <-chan struct{}, events <-chan fsnotify.Event, errc <-chan error, out chan<- struct{}) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-stop:
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			if ev.Op == fsnotify.Chmod {
				continue
			}
			select {
			case out <- struct{}{}:
			default:
			}
		case _, ok := <-errc:
			if !ok {
				return
			}
		}
	}
}
