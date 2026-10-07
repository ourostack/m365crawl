package syncer

import (
	"context"
	"errors"
	"os"
	"runtime"
	"runtime/debug"
	"time"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/hxstore"
	"github.com/ourostack/m365crawl/internal/outlookdesktop"
	"github.com/ourostack/m365crawl/internal/outlookmail"
	"github.com/ourostack/m365crawl/internal/store"
)

// outlookMailSupported says whether this operating system reads Outlook mail (a test seam). Mail
// follows the Outlook source: where Outlook is read and mail is not, a sync says so and goes on.
var outlookMailSupported = func() bool { return runtime.GOOS != "windows" }

// mailKey is the source key of a profile's mail in sync_runs and in the report.
func mailKey(profileKey string) string { return profileKey + "|mail" }

// mailRowOf is the mail outcome of a profile whose store was not read this run. Where mail is not
// read at all it is a note carrying the platform error, not a failure.
func (r *runner) mailRowOf(on bool, row SourceReport, profileKey string) []outlookOutcome {
	if !on {
		c := errs.MailUnsupportedPlatform()
		row = SourceReport{Status: StatusUnavailable, Error: &SourceError{Code: c.Code, Message: c.Message, Fix: c.Fix}}
		row.Source = mailKey(profileKey)
		return []outlookOutcome{{key: row.Source, report: row, note: true}}
	}
	row.Source = mailKey(profileKey)
	return []outlookOutcome{{key: row.Source, report: row}}
}

// mailRunCounts is the counts_json of a mail source's sync_runs row.
type mailRunCounts struct {
	Mail store.MailResult `json:"mail"`
}

// outlookMail reads the profile's mail from the private copy and commits it in one transaction. A
// panic or failure here is the mail source's alone.
func (r *runner) outlookMail(ctx context.Context, p outlookdesktop.Profile, info outlookdesktop.Info, fp string, begun time.Time) (out outlookOutcome) {
	key := mailKey(outlookKey(p.Name))
	out.key = key
	err := contained(func() (e error) { out.report, out.decoded, e = r.readMail(ctx, p, info, key, fp, begun); return })
	if err != nil {
		out.err = err
		if ctx.Err() == nil {
			c := codedOf(err)
			// Forgets the read marker, so the next sync reads the mail again.
			_ = r.st.SetMailFailure(ctx, outlookAccount(p.Name), store.OutlookFailure{Code: c.Code, Message: bodyMessage(c), Fix: c.Fix, Exit: c.Exit})
		}
	}
	return out
}

func (r *runner) readMail(ctx context.Context, p outlookdesktop.Profile, info outlookdesktop.Info, key, fp string, begun time.Time) (SourceReport, bool, error) {
	account := outlookAccount(p.Name)
	expect, err := r.st.MailHolds(ctx, account)
	if err != nil {
		return SourceReport{}, false, errs.DBError(err)
	}
	need, err := r.st.MailNeedBody(ctx, account)
	if err != nil {
		return SourceReport{}, false, errs.DBError(err)
	}
	res, err := collectMail(ctx, info, p.Dir, account, outlookmail.Options{ReadBodies: true, NeedBody: need, ExpectMail: expect})
	if err != nil {
		return SourceReport{}, false, err
	}
	omissions := map[string]int{}
	for _, l := range res.Losses {
		omissions[l.Code] += l.Count
	}
	status := StatusOK
	if lost(omissions) > 0 {
		status = StatusOmissions
	} else {
		omissions = nil
	}
	count := len(res.Messages)
	mail, err := r.st.CommitMail(ctx, store.MailBatch{Account: account, ReadAt: begun, FreshAt: info.ModTime, Result: res, Trusted: len(res.Losses) == 0})
	if err != nil {
		return SourceReport{}, false, asCoded(err)
	}
	if err := r.st.SetMailRead(ctx, account, begun); err != nil {
		return SourceReport{}, false, errs.DBError(err)
	}
	if err := r.st.RecordRun(ctx, store.Run{StartedAt: begun, FinishedAt: outlookNow(), Source: key, Fingerprint: fp, Status: status, Counts: mailRunCounts{Mail: mail}, Omissions: omissions}); err != nil {
		return SourceReport{}, false, errs.DBError(err)
	}
	r.progress("%s: %s (%d messages)", key, status, count)
	return SourceReport{Source: key, Status: status, Omissions: omissions, Accounts: []string{account}, Counts: &SourceCounts{Mail: &mail}}, true, nil
}

// collectMail opens the private copy and reads its mail, with the same memory care as the
// calendar read. A guard refusal comes back as a coded error that carries the guard's code.
func collectMail(ctx context.Context, info outlookdesktop.Info, profileDir, account string, opt outlookmail.Options) (outlookmail.Result, error) {
	f, err := os.Open(info.Path)
	if err != nil {
		return outlookmail.Result{}, errs.Internal(err)
	}
	defer func() { _ = f.Close() }()
	var res outlookmail.Result
	s, err := hxstore.OpenStore(f, info.Size)
	if err == nil {
		debug.FreeOSMemory()
		defer debug.FreeOSMemory()
		defer debug.SetGCPercent(debug.SetGCPercent(outlookGCPercent))
		if res, err = outlookmail.Collect(ctx, s, profileDir, account, opt); err == nil {
			return res, nil
		}
	}
	var guard *hxstore.GuardError
	if errors.As(err, &guard) {
		return outlookmail.Result{}, &errs.Coded{Code: guard.Code, Exit: errs.ExitEnvironment, Message: "the Outlook mail cannot be read by this version of m365crawl: " + guard.Error(),
			Fix: "Update m365crawl: this version reads mail of Outlook store version i, header 0x4f, detail 0xc9 and body 0xca. Nothing of Outlook mail was applied."}
	}
	return outlookmail.Result{}, err
}
