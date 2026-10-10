package sharepointcontent

import (
	"context"
	"net/url"
	"reflect"
	"strings"
	"time"

	"github.com/ourostack/m365crawl/internal/transcripts"
)

func land(ctx context.Context, page transcripts.PageDriver, request Request) (result admittedRequest, err error) {
	defer func() {
		if contextErr := ctx.Err(); contextErr != nil {
			result, err = admittedRequest{}, contextErr
		}
	}()
	if err := ctx.Err(); err != nil {
		return admittedRequest{}, err
	}
	if page == nil || (reflect.ValueOf(page).Kind() == reflect.Pointer && reflect.ValueOf(page).IsNil()) {
		return admittedRequest{}, &ReadError{Code: "invalid_input"}
	}
	admitted, err := admit(request)
	if err != nil {
		return admittedRequest{}, err
	}
	landing, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	failure := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if landing.Err() != nil {
			return &ReadError{Code: "timeout"}
		}
		return &ReadError{Code: "unreadable"}
	}
	siteURL := url.URL{Scheme: "https", Host: admitted.Host, Path: admitted.Site}
	if err := page.Navigate(landing, siteURL.String()); err != nil {
		return admittedRequest{}, failure()
	}
	if landing.Err() != nil {
		return admittedRequest{}, failure()
	}
	host, err := page.Host(landing)
	if err != nil || landing.Err() != nil {
		return admittedRequest{}, failure()
	}
	host = strings.TrimSuffix(strings.ToLower(host), ":443")
	if transcripts.IsLoginHost(host) {
		return admittedRequest{}, &ReadError{Code: "signin_required"}
	}
	if host != admitted.Host {
		return admittedRequest{}, &ReadError{Code: "elsewhere"}
	}
	return admitted, nil
}
