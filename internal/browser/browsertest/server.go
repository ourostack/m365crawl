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
}

var listen = net.Listen

// Listen starts a server on 127.0.0.1 with an ephemeral port. It panics if loopback cannot be
// listened on: nothing in a test can go on without it.
func Listen() *Server {
	ln, err := listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	s := &Server{ln: ln, Port: ln.Addr().(*net.TCPAddr).Port, handlers: map[string]Handler{}, conns: map[*websocket.Conn]struct{}{}}
	s.setDefaults()
	s.http = &http.Server{Handler: http.HandlerFunc(s.serve), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = s.http.Serve(ln) }()
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

// Close stops the server and drops its connections.
func (s *Server) Close() { _ = s.http.Close() }

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
	s.handlers["Page.navigate"] = func(Request) (any, *Error) { return map[string]any{"frameId": "F1"}, nil }
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
	s.headers = append(s.headers, r.Header.Clone())
	s.mu.Unlock()
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	c.SetReadLimit(64 << 20)
	s.mu.Lock()
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
