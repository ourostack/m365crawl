package cli

import (
	"context"

	"github.com/ourostack/m365crawl/internal/browser"
	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/sharepointcontent"
)

func captureCloud(ctx context.Context, options browser.LaunchOptions, request sharepointcontent.CanonicalRequest) (result sharepointcontent.Result, err error) {
	canonical, err := sharepointcontent.NormalizeRequest(request.Request)
	if err != nil || canonical != request {
		return result, errs.Usage("content fetch requires an admitted page or Stream URL")
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	options.Headless = true
	page, closer, err := openBrowser(ctx, options)
	if err != nil {
		return result, errs.BrowserFailed("could not open the content browser")
	}
	defer func() {
		if recover() != nil {
			result, err = sharepointcontent.Result{}, errs.BrowserFailed("content browser capture failed")
		}
		if closer.Close() != nil {
			result, err = sharepointcontent.Result{}, errs.BrowserFailed("could not close the content browser")
		}
		if ctx.Err() != nil {
			result, err = sharepointcontent.Result{}, ctx.Err()
		}
	}()
	return sharepointcontent.Fetch(ctx, page, request.Request)
}
