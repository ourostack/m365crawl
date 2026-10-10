package sharepointcontent

import (
	"context"
	"errors"
	"testing"
)

type expiringLanding struct{}

func (expiringLanding) Navigate(ctx context.Context, _ string) error {
	<-ctx.Done()
	return ctx.Err()
}
func (expiringLanding) Host(context.Context) (string, error) {
	return "", errors.New("expired landing must not query host")
}
func (expiringLanding) Eval(context.Context, string, any) error {
	return errors.New("expired landing must not capture content")
}

func TestLandingOwnTimeoutIsExplicit(t *testing.T) {
	request := Request{Kind: "page", URL: "https://fixture.sharepoint.com/sites/sample/SitePages/Page.aspx"}
	got, err := land(context.Background(), expiringLanding{}, request)
	var safe *ReadError
	if !errors.As(err, &safe) || safe.Code != "timeout" || got != (admittedRequest{}) {
		t.Fatalf("own landing timeout misclassified: %#v,%v", got, err)
	}
}
