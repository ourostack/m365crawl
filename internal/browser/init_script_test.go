package browser

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/ourostack/m365crawl/internal/browser/browsertest"
)

type initScriptPage interface {
	AddInitScript(context.Context, string) (string, error)
	RemoveInitScript(context.Context, string) error
}

func initScriptServer(t *testing.T) *browsertest.Server {
	t.Helper()
	s := browsertest.NewServer(t)
	for _, method := range []string{"Page.enable", "Page.removeScriptToEvaluateOnNewDocument"} {
		s.Handle(method, func(browsertest.Request) (any, *browsertest.Error) {
			return map[string]any{}, nil
		})
	}
	return s
}

func scriptMethods(t *testing.T, page *Page) initScriptPage {
	t.Helper()
	methods, ok := any(page).(initScriptPage)
	if !ok {
		t.Fatal("owned page has no early script registration/removal")
	}
	return methods
}

func TestInitScriptRegistrationAndRemoval(t *testing.T) {
	s := initScriptServer(t)
	s.Handle("Page.addScriptToEvaluateOnNewDocument", func(browsertest.Request) (any, *browsertest.Error) {
		return map[string]any{"identifier": "native-registration"}, nil
	})
	page := scriptMethods(t, testPage(t, s))
	id, err := page.AddInitScript(context.Background(), "window.fixture = 'early';")
	if err != nil || id != "native-registration" {
		t.Fatalf("registration = %q, %v", id, err)
	}
	if err := page.RemoveInitScript(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	var methods []string
	for _, r := range s.Requests() {
		if r.Method == "Target.getTargets" || r.Method == "Target.attachToTarget" {
			continue
		}
		if r.SessionID != "S1" {
			t.Fatalf("unowned session for %s: %q", r.Method, r.SessionID)
		}
		methods = append(methods, r.Method)
		var params map[string]string
		if r.Method != "Page.enable" {
			if err := json.Unmarshal(r.Params, &params); err != nil {
				t.Fatal(err)
			}
		}
		if r.Method == "Page.addScriptToEvaluateOnNewDocument" && params["source"] != "window.fixture = 'early';" {
			t.Fatalf("source changed: %v", params)
		}
		if r.Method == "Page.removeScriptToEvaluateOnNewDocument" && params["identifier"] != "native-registration" {
			t.Fatalf("wrong registration removed: %v", params)
		}
	}
	if !reflect.DeepEqual(methods, []string{"Page.enable", "Page.addScriptToEvaluateOnNewDocument", "Page.removeScriptToEvaluateOnNewDocument"}) {
		t.Fatalf("command order: %v", methods)
	}
}

func TestInitScriptRegistrationFailures(t *testing.T) {
	for _, method := range []string{"Page.enable", "Page.addScriptToEvaluateOnNewDocument"} {
		t.Run(method, func(t *testing.T) {
			s := initScriptServer(t)
			s.Handle(method, func(browsertest.Request) (any, *browsertest.Error) {
				return nil, &browsertest.Error{Code: -1, Message: "synthetic command refusal"}
			})
			page := scriptMethods(t, testPage(t, s))
			if id, err := page.AddInitScript(context.Background(), "fixture"); err == nil || id != "" {
				t.Fatalf("failed registration = %q, %v", id, err)
			}
			count := 0
			for _, r := range s.Requests() {
				if r.Method == method {
					count++
				}
			}
			if count != 1 {
				t.Fatalf("uncertain registration retried: %d", count)
			}
		})
	}
	for _, response := range []any{map[string]any{}, map[string]any{"identifier": ""}, map[string]any{"identifier": 7}} {
		t.Run("malformed", func(t *testing.T) {
			s := initScriptServer(t)
			s.Handle("Page.addScriptToEvaluateOnNewDocument", func(browsertest.Request) (any, *browsertest.Error) {
				return response, nil
			})
			page := scriptMethods(t, testPage(t, s))
			if id, err := page.AddInitScript(context.Background(), "fixture"); err == nil || id != "" {
				t.Fatalf("malformed registration = %q, %v", id, err)
			}
		})
	}
}

func TestInitScriptRemovalFailures(t *testing.T) {
	s := initScriptServer(t)
	s.Handle("Page.removeScriptToEvaluateOnNewDocument", func(browsertest.Request) (any, *browsertest.Error) {
		return nil, &browsertest.Error{Code: -1, Message: "synthetic removal refusal"}
	})
	page := scriptMethods(t, testPage(t, s))
	before := len(s.Requests())
	if err := page.RemoveInitScript(context.Background(), ""); err == nil || len(s.Requests()) != before {
		t.Fatal("empty removal was submitted or accepted")
	}
	if err := page.RemoveInitScript(context.Background(), "native-registration"); err == nil {
		t.Fatal("removal failure hidden")
	}
}

func TestInitScriptCancelledAndUncertainRegistration(t *testing.T) {
	s := initScriptServer(t)
	page := scriptMethods(t, testPage(t, s))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if id, err := page.AddInitScript(ctx, "fixture"); !errors.Is(err, context.Canceled) || id != "" {
		t.Fatalf("cancelled registration = %q, %v", id, err)
	}
	if err := page.RemoveInitScript(ctx, "native-registration"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled removal = %v", err)
	}
	s.Handle("Page.addScriptToEvaluateOnNewDocument", func(browsertest.Request) (any, *browsertest.Error) {
		s.DropConnections()
		return map[string]any{"identifier": "lost-response"}, nil
	})
	if id, err := page.AddInitScript(context.Background(), "fixture"); err == nil || id != "" {
		t.Fatalf("uncertain registration = %q, %v", id, err)
	}
	count := 0
	for _, r := range s.Requests() {
		if r.Method == "Page.addScriptToEvaluateOnNewDocument" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("uncertain registration retried: %d", count)
	}
}
