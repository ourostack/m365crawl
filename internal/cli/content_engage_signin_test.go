package cli

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/browser"
	"github.com/ourostack/m365crawl/internal/engagecontent"
)

func TestEngageSigninNativeCompletionBarrier(t *testing.T) {
	e := cloudCLIEnv(t)
	p := &engageFixturePage{}
	closer := engageControlled(t, p)
	savedTTY := stdinIsTTY
	stdinIsTTY = func() bool { return false }
	t.Cleanup(func() { stdinIsTTY = savedTTY })
	if code, _, _ := e.run("content", "signin", "https://engage.cloud.microsoft/"); code == 0 || closer.n != 0 {
		t.Fatal("agreement bypass")
	}
	for _, target := range []string{"https://engage.cloud.microsoft/path", "https://engage.cloud.microsoft/?q=x", "https://engage.cloud.microsoft/#x", "https://user@engage.cloud.microsoft/", "http://engage.cloud.microsoft/", "https://engage.cloud.microsoft:444/"} {
		if code, _, _ := e.run("content", "signin", target, "--user-agreed"); code == 0 {
			t.Fatal("target broadened")
		}
	}
	saved := observeEngageHome
	t.Cleanup(func() { observeEngageHome = saved })
	n := 0
	observeEngageHome = func(context.Context, engagecontent.Driver) (engagecontent.Result, error) {
		n++
		return engagecontent.Result{}, &engagecontent.ReadError{Code: "identity_missing"}
	}
	if code, _, stderr := e.run("content", "signin", "https://engage.cloud.microsoft/", "--user-agreed"); code == 0 || !strings.Contains(stderr, "engage_signin_unverified") {
		t.Fatal(stderr)
	}
	if n != 1 {
		t.Fatal("Home treated as auth or observer retried")
	}
	observeEngageHome = func(context.Context, engagecontent.Driver) (engagecontent.Result, error) {
		return engageCLIResult("A"), nil
	}
	if code, out, stderr := e.run("content", "signin", "https://engage.cloud.microsoft:443/", "--user-agreed"); code != 0 {
		t.Fatal(stderr)
	} else if doc := decode(t, out); doc["auth_state"] != "verified_native_service_session" || doc["state"] != "verified" || doc["verified_at"] == nil {
		t.Fatal(doc)
	}
	closer.err = errors.New("late close")
	if code, _, _ := e.run("content", "signin", "https://engage.cloud.microsoft/", "--user-agreed"); code == 0 {
		t.Fatal("late close verified")
	}
}
func TestEngageSigninInsufficientBudget(t *testing.T) {
	e := cloudCLIEnv(t)
	p := &engageFixturePage{}
	engageControlled(t, p)
	saved := signinWait
	signinWait = time.Second
	t.Cleanup(func() { signinWait = saved })
	rt := &runtime{ctx: context.Background(), g: &Globals{}, dbPath: e.db, stdout: io.Discard, stderr: io.Discard}
	if r, err := rt.signinEngage(browser.LaunchOptions{}); err == nil || r.AuthState != "" {
		t.Fatal("extended native budget")
	}
}
