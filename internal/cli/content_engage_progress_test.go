package cli

import (
	"context"
	"github.com/ourostack/m365crawl/internal/engagecontent"
	"strings"
	"testing"
)

func TestEngageSigninProgressExplainsNativeVerification(t *testing.T) {
	e := cloudCLIEnv(t)
	engageControlled(t, &engageFixturePage{})
	saved := observeEngageHome
	t.Cleanup(func() { observeEngageHome = saved })
	observeEngageHome = func(context.Context, engagecontent.Driver) (engagecontent.Result, error) {
		return engageCLIResult("A"), nil
	}
	code, _, stderr := e.run("content", "signin", "https://engage.cloud.microsoft/", "--user-agreed")
	if code != 0 || !strings.Contains(stderr, "native session verification") || !strings.Contains(stderr, "closing owned Engage window") {
		t.Fatalf("missing bounded sign-in progress: %s", stderr)
	}
}
