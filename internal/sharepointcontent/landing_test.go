package sharepointcontent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type landingPage struct {
	calls   []string
	host    string
	navErr  error
	hostErr error
	navHook func()
}

func (p *landingPage) Navigate(ctx context.Context, _ string) error {
	p.calls = append(p.calls, "navigate")
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) > 30*time.Second {
		return errors.New("landing deadline absent")
	}
	if p.navHook != nil {
		p.navHook()
	}
	return p.navErr
}

func (p *landingPage) Host(context.Context) (string, error) {
	p.calls = append(p.calls, "host")
	return p.host, p.hostErr
}

func (p *landingPage) Eval(context.Context, string, any) error {
	p.calls = append(p.calls, "eval")
	return errors.New("landing must not evaluate content")
}

func TestLandingLifecycle(t *testing.T) {
	request := Request{Kind: "page", URL: "https://fixture.sharepoint.com/sites/sample/SitePages/Page.aspx"}
	for _, test := range []struct {
		name, host, code string
		navErr, hostErr  error
	}{
		{"same-host", "fixture.sharepoint.com", "", nil, nil},
		{"same-host443", "fixture.sharepoint.com:443", "", nil, nil},
		{"login", "login.microsoftonline.com", "signin_required", nil, nil},
		{"elsewhere", "other.sharepoint.com", "elsewhere", nil, nil},
		{"wrong-port", "fixture.sharepoint.com:8443", "elsewhere", nil, nil},
		{"navigation", "fixture.sharepoint.com", "unreadable", errors.New("private-source-value"), nil},
		{"host-failure", "", "unreadable", nil, errors.New("private-source-value")},
	} {
		t.Run(test.name, func(t *testing.T) {
			page := &landingPage{host: test.host, navErr: test.navErr, hostErr: test.hostErr}
			got, err := land(context.Background(), page, request)
			if test.code == "" {
				if err != nil || got.Path != "/sites/sample/SitePages/Page.aspx" {
					t.Fatalf("admitted landing changed: %#v,%v", got, err)
				}
			} else {
				var safe *ReadError
				if !errors.As(err, &safe) || safe.Code != test.code || got != (admittedRequest{}) || strings.Contains(err.Error(), "private-source-value") {
					t.Fatalf("unsafe landing failure: %#v,%v", got, err)
				}
			}
			if strings.Contains(strings.Join(page.calls, ","), "eval") {
				t.Fatal("landing evaluated content")
			}
		})
	}
}

func TestLandingInvalidDriverAndContextNeverPublishes(t *testing.T) {
	request := Request{Kind: "page", URL: "https://fixture.sharepoint.com/sites/sample/SitePages/Page.aspx"}
	var nilPage *landingPage
	if got, err := land(context.Background(), nilPage, request); err == nil || got != (admittedRequest{}) {
		t.Fatalf("typed nil driver admitted: %#v,%v", got, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	page := &landingPage{host: "fixture.sharepoint.com"}
	if got, err := land(ctx, page, request); !errors.Is(err, context.Canceled) || got != (admittedRequest{}) || len(page.calls) != 0 {
		t.Fatalf("cancelled source reached driver: %#v,%v", got, err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	page = &landingPage{host: "fixture.sharepoint.com", navHook: cancel}
	if got, err := land(ctx, page, request); !errors.Is(err, context.Canceled) || got != (admittedRequest{}) || len(page.calls) != 1 {
		t.Fatalf("cancelled navigation published/continued: %#v,%v", got, err)
	}
}
