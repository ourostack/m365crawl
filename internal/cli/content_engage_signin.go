package cli

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/ourostack/m365crawl/internal/browser"
	"github.com/ourostack/m365crawl/internal/engagecontent"
	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/store"
)

type EngageSigninReport struct {
	State         string                      `json:"state"`
	Service       string                      `json:"service"`
	AuthState     string                      `json:"auth_state"`
	Next          string                      `json:"next"`
	SourceAccount engagecontent.SourceAccount `json:"source_account"`
	VerifiedAt    time.Time                   `json:"verified_at"`
}

func engageSigninTarget(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && (u.Host == "engage.cloud.microsoft" || u.Host == "engage.cloud.microsoft:443") && u.User == nil && (u.Path == "" || u.Path == "/") && u.RawPath == "" && u.RawQuery == "" && !u.ForceQuery && !strings.Contains(raw, "#") && u.Fragment == "" && u.RawFragment == ""
}
func (rt *runtime) engageSigninError() error {
	return &errs.Coded{Code: "engage_signin_unverified", Exit: errs.ExitUsage, Message: "Engage native service session was not verified; authentication unknown",
		Fix: "No content was registered or admitted. If the user agrees, retry `" + rt.contentCommand("signin", store.EngageHomeURL, "--user-agreed") + "`; visible Home is preliminary readiness only."}
}
func (rt *runtime) signinEngage(options browser.LaunchOptions) (report EngageSigninReport, err error) {
	work, cancel := context.WithTimeout(rt.ctx, signinWait)
	defer cancel()
	options.StartURL, options.Headless = "", false
	rt.progressLine("opening a visible Engage window with the tool-owned profile")
	page, closer, err := openBrowser(work, options)
	if err != nil {
		return EngageSigninReport{}, rt.engageSigninError()
	}
	defer func() {
		if recover() != nil {
			err = rt.engageSigninError()
		}
		rt.progressLine("closing owned Engage window; verification requires successful terminal Close")
		if closeEngageBrowser(closer) != nil {
			err = rt.engageSigninError()
		}
		if work.Err() != nil {
			err = errors.Join(rt.engageSigninError(), work.Err())
		}
		if err != nil {
			report = EngageSigninReport{}
		} else {
			report.VerifiedAt = transcriptsNow()
		}
	}()
	driver, err := earlyEngagePage(page)
	if err != nil {
		return EngageSigninReport{}, rt.engageSigninError()
	}
	if err := driver.Navigate(work, store.EngageHomeURL); err != nil {
		return EngageSigninReport{}, rt.engageSigninError()
	}
	rt.progressLine("waiting for preliminary native Home readiness within the bounded sign-in lifetime; Home alone is not authentication")
	for {
		call, stop := context.WithTimeout(work, signinCallTimeout)
		ready, err := engagecontent.HomeReady(call, driver)
		stop()
		if err != nil || work.Err() != nil {
			return EngageSigninReport{}, rt.engageSigninError()
		}
		if ready {
			break
		}
		pause(work, signinPoll)
	}
	deadline, _ := work.Deadline()
	if time.Until(deadline) < 45*time.Second {
		return EngageSigninReport{}, rt.engageSigninError()
	}
	rt.progressLine("performing one bounded native session verification; captured content will be discarded")
	result, err := observeEngageHome(work, driver)
	if err == nil {
		err = engagecontent.ValidateResult(work, result)
	}
	if err != nil || work.Err() != nil {
		return EngageSigninReport{}, rt.engageSigninError()
	}
	// Deliberately discard all content. There is no archive registration/write.
	return EngageSigninReport{State: "verified", Service: "engage", AuthState: "verified_native_service_session", SourceAccount: result.Account, Next: rt.contentCommand("fetch", "engage-home")}, nil
}
