package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"time"

	"github.com/ourostack/m365crawl/internal/browser"
	"github.com/ourostack/m365crawl/internal/engagecontent"
	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/transcripts"
)

type contentFetchEngageHomeCmd struct {
	Browser string        `env:"M365CRAWL_BROWSER" help:"edge, chrome or executable path; only the tool-owned archive profile."`
	Timeout time.Duration `default:"10m" help:"Positive overall timeout; never raises the native observer's 45s maximum."`
}
type EngageFetchReport struct {
	RegistrationID int64                `json:"registration_id"`
	SourceID       *int64               `json:"source_id"`
	SessionID      *int64               `json:"session_id"`
	ObservationIDs []string             `json:"observation_ids"`
	State          string               `json:"state"`
	Scope          string               `json:"scope"`
	AccountState   string               `json:"account_state"`
	Next           string               `json:"next"`
	Partial        bool                 `json:"partial"`
	Reused         bool                 `json:"reused"`
	SessionLosses  []engagecontent.Loss `json:"session_losses"`
}

const engageScope = "native_home_access_session_thread_starters_only"
const engageHelp = "Engage: `content fetch engage-home` captures only bounded native access-session thread starters at the fixed https://engage.cloud.microsoft/ root. No URL/account/thread/pagination arguments. Partial observations are not a complete feed, replies, mentions, tasks or current thread revisions. metadata_only is not no access or a healthy empty feed. Reads are archive-only. Unknown failures belong to the registration, never a guessed active account. Sign-in requires agreement and positive native identity plus terminal browser Close; visible Home alone is not authentication. No live Windows service or interactive challenge/expiry qualification.\n"

func (contentFetchEngageHomeCmd) Help() string { return engageHelp + contentHelp }

// Kept at the native boundary for controlled lifecycle/fault tests. Product
// calls always use the delivered early observer and its unchanged deadlines.
var observeEngageHome = engagecontent.ObserveHome

func (c *contentFetchEngageHomeCmd) Run(rt *runtime) error {
	if err := rt.contentFlags(); err != nil {
		return err
	}
	if err := checkKeys(rt, typeKeys[EngageFetchReport]()); err != nil {
		return err
	}
	if c.Timeout <= 0 {
		return errs.Usage("content fetch engage-home needs a positive --timeout and no target arguments")
	}
	ctx, cancel := context.WithTimeout(rt.ctx, c.Timeout)
	defer cancel()
	previous := rt.ctx
	rt.ctx = ctx
	defer func() { rt.ctx = previous }()
	report, err := rt.fetchEngage(browser.LaunchOptions{Exe: c.Browser, Profile: browser.ProfileDir(rt.dbPath), Headless: true})
	if err != nil {
		return rt.contentError(err)
	}
	return rt.write("content fetch engage-home", report)
}

func earlyEngagePage(page transcripts.PageDriver) (engagecontent.Driver, error) {
	if page == nil {
		return nil, &engagecontent.ReadError{Code: "early_observer_unsupported"}
	}
	value := reflect.ValueOf(page)
	switch value.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan, reflect.Interface:
		if value.IsNil() {
			return nil, &engagecontent.ReadError{Code: "early_observer_unsupported"}
		}
	}
	driver, ok := page.(engagecontent.Driver)
	if !ok {
		return nil, &engagecontent.ReadError{Code: "early_observer_unsupported"}
	}
	return driver, nil
}

func captureEngage(ctx context.Context, options browser.LaunchOptions) (result engagecontent.Result, err error) {
	if err := ctx.Err(); err != nil {
		return result, err
	}
	options.StartURL, options.Headless = "", true
	exe, kind, err := findBrowser(options.Exe)
	if err != nil {
		return result, errs.BrowserFailed("could not find the Engage browser")
	}
	options.Exe, options.Kind = exe, kind
	page, closer, err := openBrowser(ctx, options)
	if err != nil {
		return result, errs.BrowserFailed("could not open the Engage browser")
	}
	defer func() {
		if recover() != nil {
			err = errs.BrowserFailed("Engage browser capture failed")
		}
		if closeEngageBrowser(closer) != nil {
			err = errors.Join(err, &engagecontent.ReadError{Code: "cleanup_failed"})
		}
		if ctx.Err() != nil {
			err = errors.Join(err, ctx.Err())
		}
		if err != nil {
			result = engagecontent.Result{}
		}
	}()
	driver, err := earlyEngagePage(page)
	if err != nil {
		return result, err
	}
	result, err = observeEngageHome(ctx, driver)
	if err == nil {
		err = engagecontent.ValidateResult(ctx, result)
	}
	return result, err
}

func closeEngageBrowser(closer io.Closer) (err error) {
	defer func() {
		if recover() != nil {
			err = &engagecontent.ReadError{Code: "cleanup_failed"}
		}
	}()
	if closer == nil {
		return &engagecontent.ReadError{Code: "cleanup_failed"}
	}
	return closer.Close()
}

func engageFailure(err error) (string, string) {
	status, reason := "failed", "engage_capture_failed"
	var native *engagecontent.ReadError
	if errors.As(err, &native) {
		switch native.Code {
		case "unavailable", "navigation_failed":
			status, reason = "unavailable", "engage_unavailable"
		case "identity_missing", "identity_drift":
			reason = "engage_identity_unverified"
		case "too_large":
			reason = "engage_too_large"
		case "malformed":
			reason = "engage_malformed"
		case "early_observer_unsupported":
			reason = "engage_early_observer_unsupported"
		case "cleanup_failed":
			reason = "engage_cleanup_failed"
		}
	}
	var coded *errs.Coded
	if errors.As(err, &coded) && coded.Code == errs.CodeBrowserFailed {
		reason = "engage_browser_failed"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		reason = "engage_timeout"
	}
	if errors.Is(err, context.Canceled) {
		status, reason = "cancelled", "engage_cancelled"
	}
	return status, reason
}

func (rt *runtime) fetchEngage(options browser.LaunchOptions) (EngageFetchReport, error) {
	report := EngageFetchReport{ObservationIDs: []string{}, SessionLosses: []engagecontent.Loss{}, Scope: engageScope, AccountState: "unknown", Partial: true}
	var attempt store.KnowledgeAttempt
	if err := rt.cloudWrite(rt.ctx, func(st *store.Store) error {
		var err error
		attempt, err = st.BeginEngageAttempt(rt.ctx, store.KnowledgeInput{Kind: store.EngageKind, Platform: "browser", Locator: store.EngageHomeURL, At: transcriptsNow()})
		return err
	}); err != nil {
		return report, err
	}
	report.RegistrationID = attempt.LocatorID
	result, err := captureEngage(rt.ctx, options)
	acquired := transcriptsNow()
	if rt.ctx.Err() != nil {
		result, err = engagecontent.Result{}, rt.ctx.Err()
	}
	if err != nil {
		status, reason := engageFailure(err)
		report.State = reason
		diagnostic, cancel := context.WithTimeout(context.WithoutCancel(rt.ctx), 10*time.Second)
		defer cancel()
		recordErr := rt.cloudWrite(diagnostic, func(st *store.Store) error {
			return st.RecordEngageFailure(diagnostic, attempt, store.EngageFailure{Status: status, Reason: reason, At: transcriptsNow()})
		})
		if recordErr != nil {
			return report, errors.Join(err, recordErr)
		}
		fix := "Retry explicitly with `" + rt.contentCommand("fetch", "engage-home") + "`. Authentication remains unknown; historical instances are unchanged."
		if reason == "engage_unavailable" || reason == "engage_identity_unverified" {
			fix += " If the user agrees, optional `" + rt.contentCommand("signin", store.EngageHomeURL, "--user-agreed") + "` can verify a native session; sign-in is not asserted necessary."
		}
		return report, &errs.Coded{Code: reason, Exit: errs.ExitUsage, Message: "Engage capture not admitted; account/authentication unknown", Fix: fix}
	}
	err = rt.cloudWrite(rt.ctx, func(st *store.Store) error {
		if err := st.CommitEngageCapture(rt.ctx, attempt, store.EngageCapture{Result: result, AcquiredAt: acquired, AdmittedAt: transcriptsNow()}); err != nil {
			return err
		}
		src, ids, err := st.EngageAttemptReceipt(rt.ctx, attempt)
		if err != nil {
			return err
		}
		id, session := src.ID, src.CurrentSessionID
		report.SourceID, report.SessionID = &id, &session
		report.Reused = src.CurrentSession.CaptureID != attempt.ID
		for _, id := range ids {
			report.ObservationIDs = append(report.ObservationIDs, fmt.Sprintf("engage-observation:%d", id))
		}
		return nil
	})
	if err != nil {
		return EngageFetchReport{RegistrationID: attempt.LocatorID, AccountState: "unknown"}, err
	}
	report.State, report.AccountState, report.Next = result.State, "verified_at_last_success", rt.contentCommand("list")
	report.SessionLosses = append(report.SessionLosses, result.Losses...)
	return report, nil
}
