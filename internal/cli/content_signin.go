package cli

import (
	"errors"
	"net/url"
	"strings"

	"github.com/ourostack/m365crawl/internal/browser"
	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/sharepointcontent"
)

type contentSigninCmd struct {
	URL        string `arg:"" help:"Supported page or Stream target URL; opens only its admitted site root."`
	Browser    string `env:"M365CRAWL_BROWSER" help:"edge, chrome or executable path."`
	UserAgreed bool   `help:"The user agreed to the visible tool-owned window; required for nonterminal callers."`
}

func (contentSigninCmd) Help() string {
	return "Explicit visible-window sign-in; never runs during content reads or fetch automatically. Stream sign-in has no exact transcript selection: Next lists this archive's sources; choose the stored registration retry or an explicit request, never infer a transcript from the URL. Page sign-in can continue to its exact page fetch. " +
		strings.ReplaceAll((transcriptsSigninCmd{}).Help(), "m365crawl transcripts signin", "m365crawl content signin URL --user-agreed")
}
func (c *contentSigninCmd) Run(rt *runtime) error {
	if err := rt.contentFlags(); err != nil {
		return err
	}
	if engageSigninTarget(c.URL) {
		if err := checkKeys(rt, typeKeys[EngageSigninReport]()); err != nil {
			return err
		}
		exe, kind, err := findBrowser(c.Browser)
		if err != nil {
			return err
		}
		agreement := transcriptsSigninCmd{UserAgreed: c.UserAgreed}
		if err := agreement.agree(rt, kind); err != nil {
			return contentSigninError(err, rt.contentCommand("signin", c.URL))
		}
		result, err := rt.signinEngage(browser.LaunchOptions{Exe: exe, Kind: kind, Profile: browser.ProfileDir(rt.dbPath)})
		if err != nil {
			return err
		}
		return rt.write("content signin", result)
	}
	request, err := sharepointcontent.NormalizeRequest(sharepointcontent.Request{Kind: "page", URL: c.URL})
	if err != nil {
		request, err = sharepointcontent.NormalizeRequest(sharepointcontent.Request{Kind: "stream", URL: c.URL})
	}
	if err != nil {
		return errs.Usage("content signin needs a supported page or Stream target URL")
	}
	u, _ := url.Parse(request.Request.URL)
	parts := splitContentSite(u.Path)
	exe, kind, err := findBrowser(c.Browser)
	if err != nil {
		return err
	}
	agreement := transcriptsSigninCmd{UserAgreed: c.UserAgreed}
	if err := agreement.agree(rt, kind); err != nil {
		return contentSigninError(err, rt.contentCommand("signin", request.Request.URL))
	}
	result, err := rt.signinWindow(exe, kind, u.Host, parts)
	if err != nil {
		return contentSigninError(err, rt.contentCommand("signin", request.Request.URL))
	}
	result.Next = rt.contentCommand("fetch", request.Request.Kind, request.Request.URL)
	if request.Request.Kind == "stream" {
		result.Next = rt.contentCommand("sources")
	}
	return rt.write("content signin", result)
}

func contentSigninError(err error, command string) error {
	var coded *errs.Coded
	if errors.As(err, &coded) {
		copy := *coded
		copy.Fix = strings.ReplaceAll(copy.Fix, "m365crawl transcripts signin", command)
		return &copy
	}
	return err
}
