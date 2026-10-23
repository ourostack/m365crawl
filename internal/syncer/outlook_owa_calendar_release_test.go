package syncer

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/hxstore"
	"github.com/ourostack/m365crawl/internal/indexeddb"
	"github.com/ourostack/m365crawl/internal/outlookcal"
	"github.com/ourostack/m365crawl/internal/outlookowacal"
	"github.com/ourostack/m365crawl/internal/teamsdesktop"
)

func TestOWACalendarClosePanicPreservesPrimaryAndMail(t *testing.T) {
	for _, mode := range []string{"layout", "canceled", "no-primary"} {
		t.Run(mode, func(t *testing.T) {
			db := newDB(t)
			r, profile, _ := owaCalendarRunner(t, db)
			closeOrigin, copySource := outlookOWACalendarClose, outlookOWAPrivateSnapshot
			t.Cleanup(func() { outlookOWACalendarClose, outlookOWAPrivateSnapshot = closeOrigin, copySource })
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var release func()
			closed, cleaned := false, 0
			outlookOWAPrivateSnapshot = func(ctx context.Context, source teamsdesktop.Source) (string, func(), error) {
				snap, cleanup, err := copySource(ctx, source)
				release = cleanup
				return snap, func() { cleaned++; cleanup() }, err
			}
			t.Cleanup(func() {
				if closed && cleaned == 0 && release != nil {
					release()
				}
			})
			collect := outlookOWACalendarCollect
			outlookOWACalendarCollect = func(ctx context.Context, o *indexeddb.Origin, account string, expect bool) (outlookowacal.Result, error) {
				switch mode {
				case "layout":
					return outlookowacal.Result{}, &hxstore.GuardError{Code: outlookcal.CodeLayoutUnsupported, Detail: "owa_calendar_layout"}
				case "canceled":
					cancel()
					return outlookowacal.Result{}, context.Canceled
				default:
					return collect(ctx, o, account, expect)
				}
			}
			outlookOWACalendarClose = func(o *indexeddb.Origin) error {
				if err := closeOrigin(o); err != nil {
					return err
				}
				closed = true
				panic("private calendar close cause")
			}
			cal, mail := owaKinds(t, r.outlookOWAProfile(ctx, profile))
			if cal.err == nil || !errors.Is(cal.err, ErrOWASnapshotRetained) || strings.Contains(cal.err.Error(), "private") ||
				!closed || cleaned != 0 || mail.err != nil || !mail.decoded {
				t.Fatal("calendar close panic disposed an unproved reader, exposed its cause or suppressed mail")
			}
			switch mode {
			case "layout":
				if codedOf(cal.err).Code != outlookcal.CodeLayoutUnsupported {
					t.Fatal("calendar close panic replaced its primary layout refusal")
				}
			case "canceled":
				if !errors.Is(cal.err, context.Canceled) {
					t.Fatal("calendar close panic replaced cancellation")
				}
			case "no-primary":
				if codedOf(cal.err).Code != errs.CodeInternal {
					t.Fatal("an otherwise-unexplained close panic lost its internal classification")
				}
			}
			if count(t, db, `select count(*) from mail_messages`) != 5 ||
				count(t, db, `select count(*) from calendar_source_events`) != 0 {
				t.Fatal("calendar failure changed mail or committed an unproved read")
			}
		})
	}
}
