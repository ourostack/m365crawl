package browser

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/browser/browsertest"
)

// testBrowser is a Browser wired straight to a fake CDP server, with no process behind it.
func testBrowser(s *browsertest.Server) *Browser {
	return &Browser{wsURL: s.URL(), exited: make(chan struct{})}
}

func testPage(t *testing.T, s *browsertest.Server) *Page {
	t.Helper()
	p, err := testBrowser(s).Page(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestClientNoOriginHeader(t *testing.T) {
	s := browsertest.NewServer(t)
	testPage(t, s)
	hs := s.Headers()
	if len(hs) != 1 {
		t.Fatalf("handshakes = %d", len(hs))
	}
	if _, ok := hs[0]["Origin"]; ok {
		t.Fatalf("the websocket handshake carried an Origin header: %v", hs[0]["Origin"])
	}
}

func TestClientRefusesNonLoopback(t *testing.T) {
	for _, u := range []string{"ws://example.com:9222/devtools/browser/x", "ws://10.0.0.1:9222/x", "http://127.0.0.1:9222/x", "://bad", "ws://[::2]:1/x"} {
		if _, err := dialCDP(context.Background(), u); !errors.Is(err, errNotLoopback) {
			t.Errorf("%s: err = %v, want the loopback refusal", u, err)
		}
	}
	if _, err := dialCDP(context.Background(), "ws://127.0.0.1:1/devtools/browser/x"); err == nil || errors.Is(err, errNotLoopback) {
		t.Errorf("a refused connection on loopback is a plain dial error: %v", err)
	}
	if _, err := dialCDP(context.Background(), "ws://localhost:1/x"); errors.Is(err, errNotLoopback) {
		t.Error("localhost is allowed")
	}
}

func TestClientFlattenSession(t *testing.T) {
	s := browsertest.NewServer(t)
	p := testPage(t, s)
	ctx := context.Background()
	if err := p.Navigate(ctx, "about:blank"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Host(ctx); err != nil {
		t.Fatal(err)
	}
	if err := p.Eval(ctx, "1", nil); err != nil {
		t.Fatal(err)
	}
	var attach map[string]any
	pageCalls := 0
	for _, r := range s.Requests() {
		switch r.Method {
		case "Target.getTargets", "Target.attachToTarget":
			if r.SessionID != "" {
				t.Errorf("%s must be browser-level", r.Method)
			}
			if r.Method == "Target.attachToTarget" {
				_ = json.Unmarshal(r.Params, &attach)
			}
		case "Target.createTarget":
			t.Error("the initial about:blank tab must be reused, not a second tab")
		default:
			pageCalls++
			if r.SessionID != "S1" {
				t.Errorf("%s carried session %q", r.Method, r.SessionID)
			}
		}
	}
	if attach["flatten"] != true || attach["targetId"] != "T1" || pageCalls < 3 {
		t.Fatalf("attach params %v, page calls %d", attach, pageCalls)
	}
}

func TestPageCreatesTargetWhenNone(t *testing.T) {
	s := browsertest.NewServer(t)
	s.Handle("Target.getTargets", func(browsertest.Request) (any, *browsertest.Error) {
		return map[string]any{"targetInfos": []map[string]any{{"targetId": "W", "type": "service_worker"}}}, nil
	})
	testPage(t, s)
	created := false
	for _, r := range s.Requests() {
		created = created || r.Method == "Target.createTarget"
	}
	if !created {
		t.Fatal("with no page target, one must be created")
	}
}

func TestPageIsReused(t *testing.T) {
	s := browsertest.NewServer(t)
	b := testBrowser(s)
	p1, err := b.Page(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	p2, err := b.Page(context.Background())
	if err != nil || p1 != p2 {
		t.Fatalf("second Page = %p %v, want %p", p2, err, p1)
	}
	n := 0
	for _, r := range s.Requests() {
		if r.Method == "Target.getTargets" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("getTargets called %d times", n)
	}
}

func TestPageErrors(t *testing.T) {
	fail := func(browsertest.Request) (any, *browsertest.Error) {
		return nil, &browsertest.Error{Code: -32000, Message: "nope"}
	}
	for _, method := range []string{"Target.getTargets", "Target.attachToTarget"} {
		s := browsertest.NewServer(t)
		s.Handle(method, fail)
		if _, err := testBrowser(s).Page(context.Background()); err == nil {
			t.Errorf("%s failing must fail Page", method)
		}
	}
	s := browsertest.NewServer(t)
	s.Handle("Target.getTargets", func(browsertest.Request) (any, *browsertest.Error) { return map[string]any{}, nil })
	s.Handle("Target.createTarget", fail)
	if _, err := testBrowser(s).Page(context.Background()); err == nil {
		t.Error("createTarget failing must fail Page")
	}
	if _, err := (&Browser{wsURL: "ws://example.com/x"}).Page(context.Background()); !errors.Is(err, errNotLoopback) {
		t.Errorf("non-loopback: %v", err)
	}
}

func TestClientLargeMessage(t *testing.T) {
	s := browsertest.NewServer(t)
	p := testPage(t, s)
	big := strings.Repeat("a", 40<<20)
	s.Handle("Runtime.evaluate", func(browsertest.Request) (any, *browsertest.Error) {
		return map[string]any{"result": map[string]any{"type": "string", "value": big}}, nil
	})
	var got string
	if err := p.Eval(context.Background(), "big()", &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != len(big) {
		t.Fatalf("read %d bytes, want %d", len(got), len(big))
	}
}

func evalResult(v any) browsertest.Handler {
	return func(browsertest.Request) (any, *browsertest.Error) {
		return map[string]any{"result": map[string]any{"value": v}}, nil
	}
}

func TestEvalDecodes(t *testing.T) {
	s := browsertest.NewServer(t)
	p := testPage(t, s)
	s.Handle("Runtime.evaluate", evalResult(map[string]any{"n": 3}))
	var out struct{ N int }
	if err := p.Eval(context.Background(), "x", &out); err != nil || out.N != 3 {
		t.Fatalf("out = %+v %v", out, err)
	}
	var wrong string
	if err := p.Eval(context.Background(), "x", &wrong); err == nil {
		t.Fatal("a value of the wrong shape is a decode error")
	}
	var req struct {
		Expression    string
		AwaitPromise  bool
		ReturnByValue bool
	}
	rs := s.Requests()
	_ = json.Unmarshal(rs[len(rs)-1].Params, &req)
	if !req.AwaitPromise || !req.ReturnByValue || req.Expression != "x" {
		t.Fatalf("evaluate params = %+v", req)
	}
}

func TestEvalException(t *testing.T) {
	s := browsertest.NewServer(t)
	p := testPage(t, s)
	throw := func(exc map[string]any) browsertest.Handler {
		return func(browsertest.Request) (any, *browsertest.Error) {
			return map[string]any{"exceptionDetails": map[string]any{"text": "Uncaught", "exception": exc}}, nil
		}
	}
	cases := []struct {
		name string
		exc  map[string]any
		want string
	}{
		{"class name", map[string]any{"className": "TypeError"}, "TypeError"},
		{"odd class name", map[string]any{"className": "x y\nz"}, "Error"},
		{"no class name", map[string]any{}, "Error"},
		{"no exception", nil, "Error"},
	}
	for _, c := range cases {
		s.Handle("Runtime.evaluate", throw(c.exc))
		err := p.Eval(context.Background(), "x", nil)
		var ee *ErrEval
		if !errors.As(err, &ee) || ee.Name != c.want {
			t.Errorf("%s: err = %v, want ErrEval %s", c.name, err, c.want)
		}
	}
}

func TestEvalExceptionHidesText(t *testing.T) {
	s := browsertest.NewServer(t)
	p := testPage(t, s)
	s.Handle("Runtime.evaluate", func(browsertest.Request) (any, *browsertest.Error) {
		return map[string]any{"exceptionDetails": map[string]any{
			"text":        "Uncaught SECRET-TEXT https://tenant.example/secret",
			"description": "TypeError: SECRET-DESCRIPTION",
			"exception":   map[string]any{"className": "TypeError", "description": "SECRET-DESCRIPTION", "value": "SECRET-VALUE"},
		}}, nil
	})
	err := p.Eval(context.Background(), "x", nil)
	if err == nil || strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), "tenant.example") {
		t.Fatalf("the error leaks page text: %v", err)
	}
	if !strings.Contains(err.Error(), "TypeError") {
		t.Fatalf("the class name is kept: %v", err)
	}
}

func TestHostRetriesDestroyedContext(t *testing.T) {
	s := browsertest.NewServer(t)
	p := testPage(t, s)
	calls := 0
	s.Handle("Runtime.evaluate", func(browsertest.Request) (any, *browsertest.Error) {
		calls++
		switch calls {
		case 1:
			return nil, &browsertest.Error{Code: -32000, Message: "Execution context was destroyed."}
		case 2:
			return nil, &browsertest.Error{Code: -32000, Message: "Cannot find context with specified id"}
		}
		return map[string]any{"result": map[string]any{"value": "example.test"}}, nil
	})
	host, err := p.Host(context.Background())
	if err != nil || host != "example.test" || calls != 3 {
		t.Fatalf("host = %q %v after %d calls", host, err, calls)
	}
}

func TestHostGivesUp(t *testing.T) {
	s := browsertest.NewServer(t)
	p := testPage(t, s)
	destroyed := func(browsertest.Request) (any, *browsertest.Error) {
		return nil, &browsertest.Error{Code: -32000, Message: "Execution context was destroyed."}
	}
	s.Handle("Runtime.evaluate", destroyed)
	old := retryLimit
	t.Cleanup(func() { retryLimit = old })
	retryLimit = 3
	var ce *CDPError
	if _, err := p.Host(context.Background()); !errors.As(err, &ce) {
		t.Fatalf("err = %v", err)
	}
	s.Handle("Runtime.evaluate", func(browsertest.Request) (any, *browsertest.Error) {
		return nil, &browsertest.Error{Code: -32000, Message: "something else"}
	})
	if _, err := p.Host(context.Background()); err == nil || !strings.Contains(err.Error(), "something else") {
		t.Fatalf("a different error is returned at once: %v", err)
	}
	s.Handle("Runtime.evaluate", destroyed)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.Host(ctx); err == nil {
		t.Fatal("a cancelled context stops the retries")
	}
}

func TestNavigate(t *testing.T) {
	s := browsertest.NewServer(t)
	p := testPage(t, s)
	ctx := context.Background()

	states := []any{"loading", "interactive", "complete"}
	i := 0
	s.Handle("Runtime.evaluate", func(browsertest.Request) (any, *browsertest.Error) {
		v := states[i]
		if i < len(states)-1 {
			i++
		}
		return map[string]any{"result": map[string]any{"value": v}}, nil
	})
	if err := p.Navigate(ctx, "about:blank"); err != nil || i != 2 {
		t.Fatalf("Navigate = %v after %d polls", err, i)
	}

	s.Handle("Page.navigate", func(browsertest.Request) (any, *browsertest.Error) {
		return map[string]any{"errorText": "net::ERR_NAME_NOT_RESOLVED"}, nil
	})
	if err := p.Navigate(ctx, "https://nowhere.invalid/"); err == nil || !strings.Contains(err.Error(), "ERR_NAME_NOT_RESOLVED") {
		t.Fatalf("errorText: %v", err)
	}
	s.Handle("Page.navigate", func(browsertest.Request) (any, *browsertest.Error) {
		return nil, &browsertest.Error{Code: -32000, Message: "bad"}
	})
	if err := p.Navigate(ctx, "x"); err == nil {
		t.Fatal("a protocol error fails Navigate")
	}
	s.Handle("Page.navigate", func(browsertest.Request) (any, *browsertest.Error) { return map[string]any{}, nil })

	n := 0
	s.Handle("Runtime.evaluate", func(browsertest.Request) (any, *browsertest.Error) {
		n++
		if n == 1 {
			return nil, &browsertest.Error{Code: -32000, Message: "Execution context was destroyed."}
		}
		return map[string]any{"result": map[string]any{"value": "complete"}}, nil
	})
	if err := p.Navigate(ctx, "x"); err != nil {
		t.Fatalf("a destroyed context while loading is retried: %v", err)
	}

	s.Handle("Runtime.evaluate", func(browsertest.Request) (any, *browsertest.Error) {
		return nil, &browsertest.Error{Code: -32000, Message: "broken"}
	})
	if err := p.Navigate(ctx, "x"); err == nil {
		t.Fatal("a hard error while polling fails Navigate")
	}

	s.Handle("Runtime.evaluate", evalResult("loading"))
	short, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if err := p.Navigate(short, "x"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a page that never loads ends with the context: %v", err)
	}
}

func TestCallContextCancel(t *testing.T) {
	s := browsertest.NewServer(t)
	p := testPage(t, s)
	release := make(chan struct{})
	s.Handle("Runtime.evaluate", func(browsertest.Request) (any, *browsertest.Error) {
		<-release
		return map[string]any{"result": map[string]any{"value": 1}}, nil
	})
	defer close(release)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := p.Eval(ctx, "x", nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
}

func TestCallFailsWhenConnectionDrops(t *testing.T) {
	s := browsertest.NewServer(t)
	p := testPage(t, s)
	s.Handle("Runtime.evaluate", func(browsertest.Request) (any, *browsertest.Error) {
		s.DropConnections()
		return map[string]any{}, nil
	})
	if err := p.Eval(context.Background(), "x", nil); err == nil {
		t.Fatal("a call in flight when the connection drops must fail")
	}
	if !waitUntil(2*time.Second, func() bool { return p.Eval(context.Background(), "x", nil) != nil }) {
		t.Fatal("later calls on a dead connection must fail")
	}
	// A write on a dead connection fails the same way.
	if err := p.c.call(context.Background(), "", "X", nil, nil); err == nil {
		t.Fatal("dead client")
	}
}

func TestCallWriteFails(t *testing.T) {
	s := browsertest.NewServer(t)
	p := testPage(t, s)
	p.c.close() // closes the socket under the client
	if err := p.c.call(context.Background(), "", "X", nil, nil); err == nil {
		t.Fatal("write on a closed socket must fail")
	}
}

func TestCallMarshalFails(t *testing.T) {
	s := browsertest.NewServer(t)
	p := testPage(t, s)
	if err := p.c.call(context.Background(), "", "X", make(chan int), nil); err == nil {
		t.Fatal("unmarshalable params must fail")
	}
}

func TestReadLoopIgnoresNoise(t *testing.T) {
	s := browsertest.NewServer(t)
	p := testPage(t, s)
	s.Push("not json")
	s.Push(`{"method":"Page.loadEventFired","params":{}}`)
	s.Push(`{"id":9999,"result":{}}`)
	if err := p.Eval(context.Background(), "x", nil); err != nil {
		t.Fatalf("noise must not break the client: %v", err)
	}
}

func TestCDPErrorText(t *testing.T) {
	e := &CDPError{Code: -32000, Message: "boom"}
	if !strings.Contains(e.Error(), "-32000") || !strings.Contains(e.Error(), "boom") {
		t.Fatal(e.Error())
	}
	if isDestroyed(errors.New("Execution context was destroyed")) {
		t.Fatal("only protocol errors count")
	}
}

func TestHostCancelledDuringRetryWait(t *testing.T) {
	s := browsertest.NewServer(t)
	p := testPage(t, s)
	s.Handle("Runtime.evaluate", func(browsertest.Request) (any, *browsertest.Error) {
		return nil, &browsertest.Error{Code: -32000, Message: "Execution context was destroyed."}
	})
	old := retryEvery
	t.Cleanup(func() { retryEvery = old })
	retryEvery = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := p.Host(ctx); done <- err }()
	waitUntil(5*time.Second, func() bool { return len(s.Requests()) >= 4 })
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestNavigateCancelledDuringLoadWait(t *testing.T) {
	s := browsertest.NewServer(t)
	p := testPage(t, s)
	s.Handle("Runtime.evaluate", evalResult("loading"))
	old := loadPollWait
	t.Cleanup(func() { loadPollWait = old })
	loadPollWait = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Navigate(ctx, "x") }()
	waitUntil(5*time.Second, func() bool { return len(s.Requests()) >= 5 })
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestCallDefaultTimeout(t *testing.T) {
	s := browsertest.NewServer(t)
	p := testPage(t, s)
	release := make(chan struct{})
	defer close(release)
	s.Handle("Runtime.evaluate", func(browsertest.Request) (any, *browsertest.Error) {
		<-release
		return map[string]any{}, nil
	})
	old := callTimeout
	t.Cleanup(func() { callTimeout = old })
	callTimeout = 50 * time.Millisecond
	if err := p.Eval(context.Background(), "x", nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a call with no deadline of its own must time out by itself: %v", err)
	}
	if callTimeout == 0 || old != 5*time.Minute {
		t.Fatalf("default call timeout = %v", old)
	}
}

func TestCtxErrPrefersContext(t *testing.T) {
	boom := errors.New("closed network connection")
	if got := ctxErr(context.Background(), boom); got != boom {
		t.Fatalf("live context: %v", got)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := ctxErr(ctx, boom); !errors.Is(got, context.Canceled) {
		t.Fatalf("done context: %v", got)
	}
}
