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
	// watchQuiet is how long the cache must stay quiet before a sync starts, and watchMinGap the
	// least time between the starts of two syncs. Teams writes in bursts.
	watchQuiet  = 2 * time.Second
	watchMinGap = 5 * time.Second
	runSync     = syncer.Run
	// watchEvents reports (coalesced) file-system events under dirs on the returned channel until
	// ctx is cancelled or the returned function is called.
	watchEvents = fileEvents
)

type watchCmd struct {
	Every       time.Duration `default:"60s" help:"Poll interval: the safety net when file events are missed. Syncs run only when the cache changed." placeholder:"DURATION"`
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

type errorLine struct {
	Kind  string    `json:"kind"` // error
	Error errorBody `json:"error"`
}

func (c *watchCmd) Run(rt *runtime) error {
	if c.Every <= 0 {
		return errs.Usage("--every must be greater than 0")
	}
	if err := checkWatchFields(rt); err != nil {
		return err
	}
	w := &watcher{rt: rt, every: c.Every, emitInitial: c.EmitInitial}
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
	emitInitial bool

	fps       map[string]string // source -> fingerprint at the last successful sync
	baselined bool
	lastErr   string // the last failure reported, so a repeated failure is reported once
}

// run is the watch loop. It returns nil when the context is cancelled and an error only for
// failures that end the watch (environment problems).
func (w *watcher) run() error {
	ctx := w.rt.ctx
	srcs, _, err := teamsdesktop.Discover(w.rootDir())
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
		lastStart time.Time
		retryAt   time.Time
	)
	arm := func() {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		if !want {
			return
		}
		due := latest(lastEvent.Add(watchQuiet), lastStart.Add(watchMinGap), retryAt)
		timer.Reset(max(time.Until(due), 0))
	}
	arm()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-events:
			want, lastEvent = true, time.Now()
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
				arm()
			}
		case <-timer.C:
			if !want {
				continue
			}
			lastStart = time.Now()
			done, err := w.sync()
			switch {
			case ctx.Err() != nil:
				return nil
			case err != nil:
				if fatal := w.report(err); fatal != nil {
					return fatal
				}
				retryAt = time.Now().Add(w.every)
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
	srcs, _, err := teamsdesktop.Discover(w.rootDir())
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(srcs))
	for _, s := range srcs {
		fp, err := teamsdesktop.FingerprintOf(s)
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
	if err != nil {
		return false, err
	}
	w.fps, w.lastErr = cur, ""
	first := !w.baselined
	w.baselined = true
	if first && !w.emitInitial {
		return true, nil
	}
	if err := w.emit(rep, changes); err != nil {
		return true, w.reportErr(err)
	}
	return true, nil
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
		shaped, err := shape(w.rt, []any{it.item})
		if err != nil {
			return err
		}
		w.line(changeLine{Kind: c.Kind, Change: c.Change, Item: shaped[0]})
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
	for i, it := range messageItems(mrows, w.rt.g.MaxText, nil) {
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
	fw, err := fsnotify.NewWatcher()
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
		for {
			select {
			case <-ctx.Done():
				return
			case <-stop:
				return
			case ev, ok := <-fw.Events:
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
			case _, ok := <-fw.Errors: // an overflow loses events; the poll covers it
				if !ok {
					return
				}
			}
		}
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
