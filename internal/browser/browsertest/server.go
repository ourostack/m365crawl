// Package browsertest holds the test doubles for internal/browser: a scripted Chrome DevTools
// Protocol server and a fake browser executable. Test code only.
package browsertest

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// BrowserPath is the DevTools websocket path the fake advertises in DevToolsActivePort.
const BrowserPath = "/devtools/browser/fake"

// Request is one CDP command the server received.
type Request struct {
	ID        int
	Method    string
	SessionID string
	Params    json.RawMessage
}

// Error is a scripted CDP error response.
type Error struct {
	Code    int
	Message string
}

// Handler answers one CDP method. A non-nil *Error becomes an error response; otherwise the
// result is marshalled into the response.
type Handler func(Request) (result any, fail *Error)

// Server is a loopback CDP server with scripted responses. Methods it was not told about get
// sensible defaults for the calls internal/browser makes.
type Server struct {
	Port int

	ln       net.Listener
	http     *http.Server
	mu       sync.Mutex
	handlers map[string]Handler
	requests []Request
	headers  []http.Header
	onClose  func()
	conns    map[*websocket.Conn]struct{}
	closed   bool
	active   int            // handlers still running; guarded by mu
	running  sync.WaitGroup // every running handler, so Close can wait for them
	serving  chan struct{}  // closed when the accept loop has returned
}

var listen = net.Listen

// accepted runs after each websocket handshake; tests use it to close the server mid-handshake.
var accepted = func() {}

// Listen starts a server on 127.0.0.1 with an ephemeral port. It panics if loopback cannot be
// listened on: nothing in a test can go on without it.
func Listen() *Server {
	ln, err := listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	s := &Server{ln: ln, Port: ln.Addr().(*net.TCPAddr).Port, handlers: map[string]Handler{}, conns: map[*websocket.Conn]struct{}{}, serving: make(chan struct{})}
	s.setDefaults()
	s.http = &http.Server{Handler: http.HandlerFunc(s.serve), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		defer close(s.serving)
		_ = s.http.Serve(ln)
	}()
	return s
}

// NewServer starts a server for one test and stops it when the test ends.
func NewServer(t testing.TB) *Server {
	t.Helper()
	s := Listen()
	t.Cleanup(s.Close)
	return s
}

// URL is the browser-level websocket URL.
func (s *Server) URL() string { return fmt.Sprintf("ws://127.0.0.1:%d%s", s.Port, BrowserPath) }

// Close stops the server, drops its websockets and waits until every goroutine that served them
// has returned, so nothing the server started outlives the test that started it. It is safe to
// call more than once.
func (s *Server) Close() {
	s.mu.Lock()
	s.closed = true
	for c := range s.conns {
		_ = c.CloseNow()
	}
	s.mu.Unlock()
	// http.Server.Close stops the accept loop and plain connections, but not hijacked websockets,
	// and it does not wait for handlers; the websockets were closed above and running waits.
	_ = s.http.Close()
	<-s.serving
	s.running.Wait()
}

// Handle scripts the response to one method.
func (s *Server) Handle(method string, h Handler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handlers[method] = h
}

// OnClose registers a function that runs after the server answered Browser.close.
func (s *Server) OnClose(f func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onClose = f
}

// Requests returns every command received so far, oldest first.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.requests...)
}

// Headers returns the HTTP headers of every websocket handshake.
func (s *Server) Headers() []http.Header {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]http.Header(nil), s.headers...)
}

// DropConnections closes every open websocket without a goodbye, like a browser that died.
func (s *Server) DropConnections() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for c := range s.conns {
		_ = c.CloseNow()
	}
}

// Push sends a raw text message (an event, or noise) to every open websocket.
func (s *Server) Push(data string) {
	s.mu.Lock()
	var conns []*websocket.Conn
	for c := range s.conns {
		conns = append(conns, c)
	}
	s.mu.Unlock()
	for _, c := range conns {
		_ = c.Write(context.Background(), websocket.MessageText, []byte(data))
	}
}

func (s *Server) setDefaults() {
	s.handlers["Target.getTargets"] = func(Request) (any, *Error) {
		return map[string]any{"targetInfos": []map[string]any{{"targetId": "T1", "type": "page", "url": "about:blank"}}}, nil
	}
	s.handlers["Target.createTarget"] = func(Request) (any, *Error) { return map[string]any{"targetId": "T2"}, nil }
	s.handlers["Target.attachToTarget"] = func(Request) (any, *Error) { return map[string]any{"sessionId": "S1"}, nil }
	var navigationMu sync.Mutex
	loader := 0
	s.handlers["Page.getFrameTree"] = func(Request) (any, *Error) {
		navigationMu.Lock()
		defer navigationMu.Unlock()
		return map[string]any{"frameTree": map[string]any{"frame": map[string]any{"id": "F1", "loaderId": fmt.Sprintf("L%d", loader)}}}, nil
	}
	s.handlers["Page.navigate"] = func(Request) (any, *Error) {
		navigationMu.Lock()
		defer navigationMu.Unlock()
		loader++
		return map[string]any{"frameId": "F1", "loaderId": fmt.Sprintf("L%d", loader)}, nil
	}
	s.handlers["Runtime.evaluate"] = func(Request) (any, *Error) {
		return map[string]any{"result": map[string]any{"type": "string", "value": "complete"}}, nil
	}
	s.handlers["Browser.close"] = func(Request) (any, *Error) { return map[string]any{}, nil }
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.URL.Path, "/devtools/browser/") {
		http.NotFound(w, r)
		return
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		http.Error(w, "server closed", http.StatusServiceUnavailable)
		return
	}
	s.running.Add(1)
	s.active++
	s.headers = append(s.headers, r.Header.Clone())
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.active--
		s.mu.Unlock()
		s.running.Done()
	}()
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	c.SetReadLimit(64 << 20)
	accepted()
	s.mu.Lock()
	if s.closed {
		// Close ran during the handshake and could not see this connection to drop it.
		s.mu.Unlock()
		_ = c.CloseNow()
		return
	}
	s.conns[c] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.conns, c)
		s.mu.Unlock()
		_ = c.CloseNow()
	}()
	ctx := context.Background()
	for {
		_, data, err := c.Read(ctx)
		if err != nil {
			return
		}
		var in struct {
			ID        int             `json:"id"`
			Method    string          `json:"method"`
			SessionID string          `json:"sessionId"`
			Params    json.RawMessage `json:"params"`
		}
		if json.Unmarshal(data, &in) != nil {
			continue
		}
		req := Request{ID: in.ID, Method: in.Method, SessionID: in.SessionID, Params: in.Params}
		s.mu.Lock()
		s.requests = append(s.requests, req)
		h := s.handlers[in.Method]
		s.mu.Unlock()
		out := map[string]any{"id": in.ID}
		if in.SessionID != "" {
			out["sessionId"] = in.SessionID
		}
		if h == nil {
			out["error"] = map[string]any{"code": -32601, "message": in.Method + " wasn't found"}
		} else if res, fail := h(req); fail != nil {
			out["error"] = map[string]any{"code": fail.Code, "message": fail.Message}
		} else {
			out["result"] = res
		}
		b, _ := json.Marshal(out)
		if err := c.Write(ctx, websocket.MessageText, b); err != nil {
			return
		}
		if in.Method == "Browser.close" {
			s.mu.Lock()
			f := s.onClose
			s.mu.Unlock()
			if f != nil {
				f()
			}
		}
	}
}
