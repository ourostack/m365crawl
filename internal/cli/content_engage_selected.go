package cli

import (
	"context"
	"time"

	"github.com/ourostack/m365crawl/internal/browser"
	"github.com/ourostack/m365crawl/internal/engagecontent"
	"github.com/ourostack/m365crawl/internal/errs"
)

const engageThreadScope = "native_selected_thread_access_session"
const engageSelectedHelp = "`content fetch engage-thread URL` captures one strictly selected Engage thread or Teams Engage deep link: starter/replies, native authors/clocks, links and generic attachment metadata. Always partial; unknown fields/losses and native reply page facts do not certify server completeness. Attachment title is not a filename; native_type is opaque; URLs grant no download/extraction rights and are never followed. Unsafe/signed/credential-bearing URLs are omitted or redacted in bodies, links and attachments. Uses the same tool-owned browser and separately agreed root sign-in; reads remain archive-only.\n"

type contentFetchEngageThreadCmd struct {
	URL     string        `arg:"" help:"Exact Engage thread URL or its supported app deep link; validated before browser acquisition."`
	Browser string        `env:"M365CRAWL_BROWSER" help:"edge, chrome or executable path; only the tool-owned archive profile."`
	Timeout time.Duration `default:"10m" help:"Positive overall timeout; never raises the native observer's 45s maximum."`
}

func (contentFetchEngageThreadCmd) Help() string {
	return engageSelectedHelp + engageHelp + contentHelp
}

var observeEngageThread = engagecontent.ObserveThread

func (c *contentFetchEngageThreadCmd) Run(rt *runtime) error {
	if err := rt.contentFlags(); err != nil {
		return err
	}
	if err := checkKeys(rt, typeKeys[EngageFetchReport]()); err != nil {
		return err
	}
	target, err := engagecontent.ParseThreadURL(c.URL)
	if err != nil || c.Timeout <= 0 {
		return errs.Usage("content fetch engage-thread needs one supported thread URL and a positive timeout")
	}
	ctx, cancel := context.WithTimeout(rt.ctx, c.Timeout)
	defer cancel()
	previous := rt.ctx
	rt.ctx = ctx
	defer func() { rt.ctx = previous }()
	report, err := rt.fetchEngageKind(browser.LaunchOptions{Exe: c.Browser, Profile: browser.ProfileDir(rt.dbPath), Headless: true}, &target)
	if err != nil {
		return rt.contentError(err)
	}
	return rt.write("content fetch engage-thread", report)
}
