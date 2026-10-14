package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/browser"
	"github.com/ourostack/m365crawl/internal/engagecontent"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/transcripts"
)

type engageFixturePage struct {
	events           []string
	user, host, fail string
}

func (p *engageFixturePage) Navigate(_ context.Context, target string) error {
	p.events = append(p.events, "navigate:"+target)
	if p.fail == "navigate" {
		return errors.New("synthetic navigation")
	}
	return nil
}
func (p *engageFixturePage) Host(context.Context) (string, error) {
	if p.host != "" {
		return p.host, nil
	}
	return "engage.cloud.microsoft", nil
}
func (p *engageFixturePage) AddInitScript(context.Context, string) (string, error) {
	p.events = append(p.events, "register")
	return "synthetic-registration", nil
}
func (p *engageFixturePage) RemoveInitScript(context.Context, string) error {
	p.events = append(p.events, "remove")
	if p.fail == "cleanup" {
		return errors.New("synthetic cleanup")
	}
	return nil
}
func (p *engageFixturePage) Eval(_ context.Context, expr string, out any) error {
	if p.fail == "panic" {
		panic("synthetic")
	}
	var value any
	switch {
	case strings.Contains(expr, ".stop()"):
		r := engageCLIResult(p.user)
		value = map[string]any{"State": r.State, "Generation": "synthetic-generation", "Account": r.Account, "AccountEvidence": []engagecontent.SourceAccount{r.Account},
			"Threads": []any{map[string]any{"Ordinal": 0, "ID": "thread", "NetworkID": "network", "GroupID": "group", "StarterID": "starter", "Blocks": []string{"syntheticword", "", "syntheticword"}}}}
	case strings.Contains(expr, "node.click()"):
		value = map[string]any{"Clicked": true, "Generation": "synthetic-generation"}
	default:
		value = map[string]any{"Allowed": true, "Home": p.fail != "home", "Generation": "synthetic-generation"}
	}
	if out == nil {
		return nil
	}
	b, _ := json.Marshal(value)
	return json.Unmarshal(b, out)
}
func engageCLIResult(user string) engagecontent.Result {
	if user == "" {
		user = "A"
	}
	return engagecontent.Result{State: "observations", Qualification: "native_home_observations", Account: engagecontent.SourceAccount{Host: "engage.cloud.microsoft", NetworkID: "network", UserID: user},
		Threads: []engagecontent.ThreadObservation{{Ordinal: 0, ID: "thread", NetworkID: "network", GroupID: "group", StarterID: "starter", Blocks: []string{"syntheticword", "", "syntheticword"}}},
		Losses:  []engagecontent.Loss{{Code: "clock_unmapped", Count: 4}}}
}
func engageControlled(t *testing.T, p *engageFixturePage) *cloudFixtureCloser {
	t.Helper()
	saved := openBrowser
	t.Cleanup(func() { openBrowser = saved })
	c := &cloudFixtureCloser{}
	openBrowser = func(_ context.Context, o browser.LaunchOptions) (transcripts.PageDriver, io.Closer, error) {
		if o.StartURL != "" || o.DisposableFixture {
			t.Fatal("product launch defaults widened")
		}
		return p, c, nil
	}
	return c
}

func TestEngageExplicitFetchLifecycle(t *testing.T) {
	e := cloudCLIEnv(t)
	p := &engageFixturePage{}
	closer := engageControlled(t, p)
	for _, args := range [][]string{
		{"content", "fetch", "engage-home", "https://engage.cloud.microsoft/"},
		{"content", "fetch", "engage-home", "--registration-id", "1"},
		{"content", "fetch", "engage-home", "--timeout", "0"},
		{"--fields", "unsupported", "content", "fetch", "engage-home"},
	} {
		if code, _, _ := e.run(args...); code == 0 {
			t.Fatal("invalid command admitted")
		}
	}
	if closer.n != 0 {
		t.Fatal("invalid command launched")
	}
	if code, out, stderr := e.run("content", "fetch", "engage-home"); code != 0 {
		t.Fatalf("fetch %s %s", out, stderr)
	} else if doc := decode(t, out); doc["state"] != "observations" || doc["account_state"] != "verified_at_last_success" || doc["session_id"] == nil {
		t.Fatal(doc)
	}
	if closer.n != 1 || len(p.events) < 3 || p.events[0] != "register" || p.events[1] != "navigate:https://engage.cloud.microsoft/" || p.events[len(p.events)-1] != "remove" {
		t.Fatal(p.events)
	}
}

func TestEngageUnknownAttemptAttributionCLI(t *testing.T) {
	e := cloudCLIEnv(t)
	p := &engageFixturePage{}
	closer := engageControlled(t, p)
	saved := observeEngageHome
	t.Cleanup(func() { observeEngageHome = saved })
	observeEngageHome = func(context.Context, engagecontent.Driver) (engagecontent.Result, error) {
		return engageCLIResult(p.user), nil
	}
	for _, user := range []string{"A", "B"} {
		p.user = user
		if code, _, stderr := e.run("content", "fetch", "engage-home"); code != 0 {
			t.Fatal(stderr)
		}
	}
	st, err := store.OpenReadOnly(context.Background(), e.db)
	if err != nil {
		t.Fatal(err)
	}
	before, err := st.EngageSources(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_ = st.Close()
	for _, mode := range []string{"unavailable", "close", "cancel"} {
		closer.err = nil
		observeEngageHome = func(context.Context, engagecontent.Driver) (engagecontent.Result, error) {
			return engagecontent.Result{}, &engagecontent.ReadError{Code: "unavailable"}
		}
		if mode == "close" {
			closer.err = errors.New("synthetic")
			observeEngageHome = func(context.Context, engagecontent.Driver) (engagecontent.Result, error) {
				return engageCLIResult("C"), nil
			}
		}
		if mode == "cancel" {
			observeEngageHome = func(context.Context, engagecontent.Driver) (engagecontent.Result, error) {
				return engagecontent.Result{}, context.Canceled
			}
		}
		if code, _, stderr := e.run("content", "fetch", "engage-home"); code == 0 || strings.Contains(stderr, "signin_required") {
			t.Fatalf("unknown failure: %s", stderr)
		}
		st, err = store.OpenReadOnly(context.Background(), e.db)
		if err != nil {
			t.Fatal(err)
		}
		after, err := st.EngageSources(context.Background())
		_ = st.Close()
		if err != nil || len(after.Sources) != 2 || !after.Sources[0].AdmittedAt.Equal(*before.Sources[0].AdmittedAt) || !after.Sources[1].AdmittedAt.Equal(*before.Sources[1].AdmittedAt) || after.Registrations[0].AccountState != "unknown" {
			t.Fatalf("%+v %v", after, err)
		}
	}
}

func TestEngageCaptureTerminalBarriers(t *testing.T) {
	for _, mode := range []string{"unsupported", "nil", "panic", "close", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			p := &engageFixturePage{fail: mode}
			closer := engageControlled(t, p)
			saved := observeEngageHome
			t.Cleanup(func() { observeEngageHome = saved })
			observeEngageHome = func(context.Context, engagecontent.Driver) (engagecontent.Result, error) {
				if mode == "panic" {
					panic("private")
				}
				if mode == "cancel" {
					return engagecontent.Result{}, context.Canceled
				}
				return engageCLIResult("A"), nil
			}
			if mode == "close" {
				closer.err = errors.New("close")
			}
			if mode == "nil" || mode == "unsupported" {
				openBrowser = func(context.Context, browser.LaunchOptions) (transcripts.PageDriver, io.Closer, error) {
					if mode == "nil" {
						var p *engageFixturePage
						return p, closer, nil
					}
					return &cloudFixturePage{}, closer, nil
				}
			}
			result, err := captureEngage(context.Background(), browser.LaunchOptions{})
			if err == nil || result.Account.Host != "" || closer.n != 1 {
				t.Fatalf("%+v %v close%d", result, err, closer.n)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := captureEngage(ctx, browser.LaunchOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	_ = time.Second
}
