package cli

import (
	"errors"
	"net/url"
	"strings"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/sharepointcontent"
)

type contentSigninCmd struct {
	URL        string `arg:"" help:"Supported page or Stream target URL; opens only its admitted site root."`
	Browser    string `env:"M365CRAWL_BROWSER" help:"edge, chrome or executable path."`
	UserAgreed bool   `help:"The user agreed to the visible tool-owned window; required for nonterminal callers."`
}

func (contentSigninCmd) Help() string {
	return "Explicit visible-window sign-in; never runs during content reads or fetch automatically. " + (transcriptsSigninCmd{}).Help()
}
func (c *contentSigninCmd) Run(rt *runtime) error {
	if err := rt.contentFlags(); err != nil {
		return err
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
		return contentSigninError(err)
	}
	result, err := rt.signinWindow(exe, kind, u.Host, parts)
	if err != nil {
		return contentSigninError(err)
	}
	result.Next = "m365crawl content fetch page URL or m365crawl content fetch stream URL"
	return rt.write("content signin", result)
}

func contentSigninError(err error) error {
	var coded *errs.Coded
	if errors.As(err, &coded) {
		copy := *coded
		copy.Fix = strings.ReplaceAll(copy.Fix, "m365crawl transcripts signin", "m365crawl content signin URL")
		return &copy
	}
	return err
}
