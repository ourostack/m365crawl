package cli

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/browser"
	"github.com/ourostack/m365crawl/internal/engagecontent"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/transcripts"
)

func TestEngageCaptureFaults(t *testing.T) {
	e := cloudCLIEnv(t)
	_ = e
	saved := observeEngageHome
	t.Cleanup(func() { observeEngageHome = saved })
	for _, mode := range []string{"open", "find", "invalid", "late-cancel"} {
		t.Run(mode, func(t *testing.T) {
			p := &engageFixturePage{}
			c := engageControlled(t, p)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			observeEngageHome = func(context.Context, engagecontent.Driver) (engagecontent.Result, error) {
				if mode == "late-cancel" {
					cancel()
				}
				r := engageCLIResult("A")
				r.Complete = mode == "invalid"
				return r, nil
			}
			o := browser.LaunchOptions{}
			if mode == "find" {
				o.Exe = "/synthetic/missing/browser"
				savedFind := findBrowser
				t.Cleanup(func() { findBrowser = savedFind })
				findBrowser = func(string) (string, browser.Kind, error) { return "", "", errors.New("synthetic absent browser") }
			}
			if mode == "open" {
				openBrowser = func(context.Context, browser.LaunchOptions) (transcripts.PageDriver, io.Closer, error) {
					return nil, nil, errors.New("open")
				}
			}
			r, err := captureEngage(ctx, o)
			if err == nil || r.Account.Host != "" {
				t.Fatal("failed capture published")
			}
			if mode != "find" && mode != "open" && c.n != 1 {
				t.Fatal("missing terminal close")
			}
		})
	}
}

func TestEngageFetchWriteFaults(t *testing.T) {
	for _, mode := range []string{"begin", "record", "commit", "readback", "missing-browser", "failure", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			e := cloudCLIEnv(t)
			p := &engageFixturePage{}
			engageControlled(t, p)
			observer := observeEngageHome
			archive := openArchive
			t.Cleanup(func() { observeEngageHome = observer; openArchive = archive })
			observeEngageHome = func(context.Context, engagecontent.Driver) (engagecontent.Result, error) {
				return engageCLIResult("A"), nil
			}
			if mode == "begin" {
				openArchive = func(context.Context, string) (*store.Store, error) { return nil, errors.New("begin") }
			}
			if mode == "record" || mode == "failure" || mode == "timeout" {
				observeEngageHome = func(context.Context, engagecontent.Driver) (engagecontent.Result, error) {
					if mode == "record" {
						openArchive = func(context.Context, string) (*store.Store, error) { return nil, errors.New("diagnostic") }
					}
					if mode == "timeout" {
						return engagecontent.Result{}, context.DeadlineExceeded
					}
					return engagecontent.Result{}, &engagecontent.ReadError{Code: "unavailable"}
				}
			}
			if mode == "commit" || mode == "readback" {
				observeEngageHome = func(context.Context, engagecontent.Driver) (engagecontent.Result, error) {
					db, err := sql.Open("sqlite", e.db)
					if err != nil {
						t.Fatal(err)
					}
					defer func() { _ = db.Close() }()
					stmt := `create trigger engage_fault before insert on engage_sessions begin select raise(abort,'synthetic'); end`
					if mode == "readback" {
						stmt = `create trigger engage_fault after insert on engage_capture_members begin update engage_fts set text='corrupt'; end`
					}
					if _, err := db.Exec(stmt); err != nil {
						t.Fatal(err)
					}
					return engageCLIResult("A"), nil
				}
			}
			rt := &runtime{ctx: context.Background(), g: &Globals{}, dbPath: e.db, stdout: io.Discard, stderr: io.Discard}
			o := browser.LaunchOptions{}
			if mode == "missing-browser" {
				o.Exe = "/synthetic/missing/browser"
				savedFind := findBrowser
				t.Cleanup(func() { findBrowser = savedFind })
				findBrowser = func(string) (string, browser.Kind, error) { return "", "", errors.New("synthetic absent browser") }
			}
			report, err := rt.fetchEngage(o)
			if err == nil || report.SourceID != nil || report.SessionID != nil {
				t.Fatalf("failed write published: %+v %v", report, err)
			}
			if mode == "missing-browser" {
				st, err := store.OpenReadOnly(context.Background(), e.db)
				if err != nil {
					t.Fatal(err)
				}
				snap, err := st.EngageSources(context.Background())
				_ = st.Close()
				if err != nil || len(snap.Registrations) != 1 || len(snap.Sources) != 0 {
					t.Fatal("first missing browser invisible")
				}
			}
		})
	}
}

func TestEngageFailureClassifications(t *testing.T) {
	for _, tc := range []struct{ code, want string }{
		{"navigation_failed", "engage_unavailable"}, {"identity_missing", "engage_identity_unverified"}, {"identity_drift", "engage_identity_unverified"},
		{"too_large", "engage_too_large"}, {"malformed", "engage_malformed"}, {"early_observer_unsupported", "engage_early_observer_unsupported"}, {"cleanup_failed", "engage_cleanup_failed"}, {"unknown", "engage_capture_failed"},
	} {
		_, reason := engageFailure(&engagecontent.ReadError{Code: tc.code})
		if reason != tc.want {
			t.Fatal(reason)
		}
	}
}

func TestEngageSigninFaultsAndDiscard(t *testing.T) {
	for _, mode := range []string{"open", "unsupported", "navigate", "home", "invalid", "cancel", "panic", "metadata"} {
		t.Run(mode, func(t *testing.T) {
			e := cloudCLIEnv(t)
			p := &engageFixturePage{fail: mode}
			c := engageControlled(t, p)
			saved := observeEngageHome
			wait := signinWait
			t.Cleanup(func() { observeEngageHome = saved; signinWait = wait })
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "home" {
				signinWait = time.Millisecond
			}
			if mode == "open" {
				openBrowser = func(context.Context, browser.LaunchOptions) (transcripts.PageDriver, io.Closer, error) {
					return nil, nil, errors.New("open")
				}
			}
			if mode == "unsupported" {
				openBrowser = func(context.Context, browser.LaunchOptions) (transcripts.PageDriver, io.Closer, error) {
					return &cloudFixturePage{}, c, nil
				}
			}
			observeEngageHome = func(context.Context, engagecontent.Driver) (engagecontent.Result, error) {
				if mode == "panic" {
					panic("synthetic")
				}
				if mode == "cancel" {
					cancel()
				}
				r := engageCLIResult("A")
				if mode == "invalid" {
					r.Complete = true
				}
				if mode == "metadata" {
					r.State = "metadata_only"
					r.Threads = nil
					r.Losses = []engagecontent.Loss{{Code: "native_content_not_observed", Count: 1}}
				}
				return r, nil
			}
			rt := &runtime{ctx: ctx, g: &Globals{}, dbPath: e.db, stdout: io.Discard, stderr: io.Discard}
			result, err := rt.signinEngage(browser.LaunchOptions{})
			if mode == "metadata" {
				if err != nil || result.AuthState != "verified_native_service_session" {
					t.Fatal(err)
				}
			} else if err == nil || result.AuthState != "" {
				t.Fatal("unverified signin published")
			}
		})
	}
}
