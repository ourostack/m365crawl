package syncer

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/indexeddb"
	"github.com/ourostack/m365crawl/internal/outlookmail"
	"github.com/ourostack/m365crawl/internal/outlookowa"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/teamsdesktop"
)

func TestOWAMailSnapshotDisposalRequiresReaderRelease(t *testing.T) {
	for _, mode := range []string{"success", "open-error", "open-unproved", "open-unproved-deadline", "panic-open", "close-error", "layout-close-error", "layout-close-panic", "panic-close", "cancel-collect", "cancel-collect-close"} {
		t.Run(mode, func(t *testing.T) {
			isolateTmp(t)
			root, db := owaRoot(t, "calendar"), newDB(t)
			opts := outlookOpts(db, root)
			run(t, opts)
			opts.FullRead = true
			if mode == "layout-close-error" || mode == "layout-close-panic" {
				opts.OutlookRoot = owaRoot(t, "empty")
			}
			st, err := store.Open(context.Background(), db)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Close() })
			open, closeOrigin, copySource := outlookOWAOpen, outlookOWAClose, outlookOWAPrivateSnapshot
			t.Cleanup(func() { outlookOWAOpen, outlookOWAClose, outlookOWAPrivateSnapshot = open, closeOrigin, copySource })
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var snap string
			var release func()
			var reader *indexeddb.Origin
			cleaned, closed := 0, false
			outlookOWAPrivateSnapshot = func(ctx context.Context, source teamsdesktop.Source) (string, func(), error) {
				var err error
				snap, release, err = copySource(ctx, source)
				return snap, func() { cleaned++; release() }, err
			}
			t.Cleanup(func() {
				// The fixture proves its real reader closed, or that it never opened one.
				if release != nil && cleaned == 0 && (reader == nil || closed) {
					release()
				}
			})
			outlookOWAOpen = func(ctx context.Context, dir, blob string) (*indexeddb.Origin, error) {
				switch mode {
				case "open-error":
					return nil, errors.New("private opening cause")
				case "open-unproved":
					return nil, errors.Join(context.Canceled, outlookowa.ErrReaderClose)
				case "open-unproved-deadline":
					return nil, errors.Join(context.DeadlineExceeded, outlookowa.ErrReaderClose)
				case "panic-open":
					panic("private opening panic")
				}
				o, err := open(ctx, dir, blob)
				reader = o
				if mode == "cancel-collect" || mode == "cancel-collect-close" {
					cancel()
				}
				return o, err
			}
			outlookOWAClose = func(o *indexeddb.Origin) error {
				if err := closeOrigin(o); err != nil {
					return err
				}
				closed = true
				switch mode {
				case "close-error", "layout-close-error", "cancel-collect-close":
					return errors.New("private closing cause")
				case "panic-close", "layout-close-panic":
					panic("private closing panic")
				}
				return nil
			}
			r := runner{o: opts, st: st}
			out := r.outlookOWASources(ctx, &Report{})
			if len(out) != 2 {
				t.Fatal("calendar integration lost a public profile lane")
			}
			mail := out[1]
			wantCleanup := mode == "success" || mode == "open-error" || mode == "cancel-collect"
			if cleaned != 0 && !wantCleanup || cleaned != 1 && wantCleanup {
				t.Fatal("snapshot disposal preceded reader-release proof")
			}
			if !wantCleanup {
				if _, err := os.Stat(snap); err != nil {
					t.Fatal("unproved reader's snapshot is not retained")
				}
				if mail.err == nil || !strings.Contains(mail.err.Error(), "retained") {
					t.Fatal("snapshot retention was not reported")
				}
			}
			if mode == "success" {
				if mail.err != nil || !mail.decoded {
					t.Fatal("healthy mail read regressed")
				}
			} else if mail.err == nil || strings.Contains(mail.err.Error(), "private") ||
				count(t, db, `select count(*) from meta where key='outlook_mail_read:outlook/Default'`) != 0 {
				t.Fatal("failed reader was successful, exposed its cause, or vouched for freshness")
			}
			if (mode == "open-unproved" || mode == "cancel-collect" || mode == "cancel-collect-close") && !errors.Is(mail.err, context.Canceled) {
				t.Fatal("reader-close failure replaced cancellation")
			}
			if mode == "open-unproved-deadline" && !errors.Is(mail.err, context.DeadlineExceeded) {
				t.Fatal("reader-close failure replaced its deadline")
			}
			if (mode == "layout-close-error" || mode == "layout-close-panic") && codedOf(mail.err).Code != outlookmail.CodeMailLayoutUnsupported {
				t.Fatal("reader-close failure replaced the primary layout refusal")
			}
			if count(t, db, `select count(*) from mail_messages`) != 5 || count(t, db, `select count(*) from mail_absent`) != 0 {
				t.Fatal("reader failure damaged prior mail or inferred absence")
			}
		})
	}
}

func TestOWAMailCanceledBeforeCollectorOwnsNoReader(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	released := true
	_, err := collectOWAMail(ctx, "unused", "outlook/Default", false, &released)
	if !errors.Is(err, context.Canceled) || !released {
		t.Fatal("an unstarted collector acquired ownership or lost cancellation")
	}
}

func TestOWAMailCanceledRunReportsRetainedSnapshot(t *testing.T) {
	isolateTmp(t)
	root, db := owaRoot(t, "calendar"), newDB(t)
	opts := outlookOpts(db, root)
	run(t, opts)
	opts.FullRead = true
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	open, closeOrigin, copySource := outlookOWAOpen, outlookOWAClose, outlookOWAPrivateSnapshot
	t.Cleanup(func() { outlookOWAOpen, outlookOWAClose, outlookOWAPrivateSnapshot = open, closeOrigin, copySource })
	var release func()
	closed := false
	outlookOWAPrivateSnapshot = func(ctx context.Context, source teamsdesktop.Source) (string, func(), error) {
		var snap string
		var err error
		snap, release, err = copySource(ctx, source)
		return snap, release, err
	}
	t.Cleanup(func() {
		if closed && release != nil {
			release()
		}
	})
	outlookOWAOpen = func(ctx context.Context, dir, blob string) (*indexeddb.Origin, error) {
		o, err := open(ctx, dir, blob)
		cancel()
		return o, err
	}
	outlookOWAClose = func(o *indexeddb.Origin) error {
		if err := closeOrigin(o); err != nil {
			return err
		}
		closed = true
		return errors.New("private close cause")
	}
	rep, _, err := Run(ctx, opts)
	if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "retained") {
		t.Fatal("public canceled Run discarded the retained-snapshot notice")
	}
	source := sourceKeyed(t, rep, "outlook|Default|mail")
	if source.Error == nil || !strings.Contains(source.Error.Message, "retained") {
		t.Fatal("public canceled report discarded its retained source outcome")
	}
}
