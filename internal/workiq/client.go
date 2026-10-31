package workiq

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/strictjson"
)

type frame struct {
	msg wireMessage
	err error
}

type wireMessage struct {
	RPC    string          `json:"jsonrpc"`
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
}

// Client exposes fetch, and fixed document retrieve and fetch_blob calls; callers cannot select an
// MCP tool, its arguments or a client capability.
type Client struct {
	p       *process
	mu      contextMutex
	writeMu contextMutex
	next    uint64
	frames  chan frame
	drained chan struct{}
	heard   atomic.Bool // the child wrote at least one byte to stdout
	ready   atomic.Bool // the handshake finished: a later child exit is a lost session
	junk    []byte      // the start of the first non-JSON stdout line, for the diagnostic; set before its frame
	junkCut bool        // that line was longer than junk
}

// listener records that stdout produced any byte, even an incomplete line.
type listener struct {
	r     io.Reader
	heard *atomic.Bool
}

func (l listener) Read(p []byte) (int, error) {
	n, err := l.r.Read(p)
	if n > 0 {
		l.heard.Store(true)
	}
	return n, err
}

type contextMutex struct {
	once sync.Once
	held chan struct{}
}

func (m *contextMutex) lock(ctx context.Context, stopped <-chan struct{}) error {
	m.once.Do(func() { m.held = make(chan struct{}, 1) })
	if ctx.Err() != nil {
		return failure("workiq_timeout")
	}
	select {
	case m.held <- struct{}{}:
		return nil
	case <-ctx.Done():
		return failure("workiq_timeout")
	case <-stopped:
		return failure("workiq_failed")
	}
}

func (m *contextMutex) unlock() { <-m.held }

func Open(ctx context.Context, inv Invocation, account string) (*Client, error) {
	p, err := startWorkIQ(ctx, inv, readMCP, account)
	if err != nil {
		return nil, err
	}
	c := &Client{p: p, frames: make(chan frame), drained: make(chan struct{})}
	go c.read(ctx)
	result, err := c.call(ctx, handshakeTimeout, "initialize", map[string]any{
		"protocolVersion": "2025-06-18", "capabilities": map[string]any{},
		"clientInfo": map[string]any{"name": "m365crawl", "version": "1"},
	})
	var coded *errs.Coded
	// A WorkIQ with no cached account never answers and writes nothing while it waits for its own
	// interactive sign-in; any output means it is running, so that stays a timeout.
	if errors.As(err, &coded) && coded.Code == "workiq_timeout" && ctx.Err() == nil && !c.heard.Load() && c.p.errors.empty() {
		err = failure("workiq_signin_required")
	}
	if err == nil {
		var info struct {
			Protocol string `json:"protocolVersion"`
			Caps     struct {
				Tools json.RawMessage `json:"tools"`
			} `json:"capabilities"`
		}
		if json.Unmarshal(result, &info) != nil || info.Protocol != "2025-06-18" || len(info.Caps.Tools) == 0 {
			err = failure("workiq_protocol")
		}
	}
	if err == nil {
		err = c.write(ctx, map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	}
	if err != nil {
		_ = c.Close()
		return nil, err
	}
	c.ready.Store(true)
	return c, nil
}

func (c *Client) read(ctx context.Context) {
	defer close(c.drained)
	scan := bufio.NewScanner(listener{r: c.p.output, heard: &c.heard})
	scan.Buffer(make([]byte, 64<<10), maxFrame+1)
	controls := 0
	for scan.Scan() {
		raw := scan.Bytes()
		budget := strictjson.Budget{Context: ctx, MaxDepth: 64, MaxMembers: 131072}
		var msg wireMessage
		_, parseErr := budget.Parse(raw)
		if parseErr != nil || json.Unmarshal(raw, &msg) != nil || msg.RPC != "2.0" {
			c.junk, c.junkCut = append([]byte(nil), raw[:min(len(raw), 4*diagnosticStdout)]...), len(raw) > 4*diagnosticStdout
			c.deliver(frame{err: failure("workiq_protocol")})
			return
		}
		if msg.Method != "" {
			controls++
			if controls > 4096 {
				c.deliver(frame{err: failure("workiq_protocol")})
				return
			}
			if len(msg.ID) == 0 {
				continue
			}
			reply := map[string]any{"jsonrpc": "2.0", "id": msg.ID}
			if msg.Method == "ping" {
				reply["result"] = map[string]any{}
			} else {
				reply["error"] = map[string]any{"code": -32601, "message": "Client capability not supported"}
			}
			if err := c.write(ctx, reply); err != nil {
				c.deliver(frame{err: err})
				return
			}
			continue
		}
		controls = 0
		if !c.deliver(frame{msg: msg}) {
			return
		}
	}
	if scan.Err() != nil {
		c.deliver(frame{err: failure("workiq_page_too_large")})
		return
	}
	c.deliver(frame{err: io.EOF})
}

func (c *Client) deliver(f frame) bool {
	select {
	case c.frames <- f:
		return true
	case <-c.p.stop:
		return false
	}
}

func (c *Client) write(ctx context.Context, v any) error {
	deadline, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	if err := c.writeMu.lock(deadline, c.p.stop); err != nil {
		return err
	}
	defer c.writeMu.unlock()
	done := make(chan error, 1)
	go func() { done <- json.NewEncoder(c.p.input).Encode(v) }()
	select {
	case err := <-done:
		if err != nil && c.ready.Load() {
			// The child stopped reading: reap it, then report what it left behind.
			_ = c.p.close()
			return c.diagnose(c.lost(c.p.errors.diagnostic()))
		}
		if err != nil {
			return failure("workiq_failed")
		}
		return nil
	case <-deadline.Done():
		_ = c.p.close()
		<-done
		return failure("workiq_timeout")
	case <-c.p.stop:
		<-done
		return failure("workiq_failed")
	}
}

func (c *Client) call(ctx context.Context, limit time.Duration, method string, params any) (json.RawMessage, error) {
	deadline, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	if err := c.mu.lock(deadline, c.p.stop); err != nil {
		return nil, err
	}
	defer c.mu.unlock()
	c.next++
	id := c.next
	if err := c.write(deadline, map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		return nil, err
	}
	select {
	case f := <-c.frames:
		if f.err != nil {
			var coded *errs.Coded
			if errors.As(f.err, &coded) {
				return nil, c.diagnose(coded)
			}
			_ = c.p.close() // wait for stderr draining before classifying a child that exited
			return nil, c.diagnose(c.lost(c.p.errors.diagnostic()))
		}
		msg := f.msg
		if string(msg.ID) != strconv.FormatUint(id, 10) {
			return nil, failure("workiq_protocol")
		}
		if len(msg.Error) > 0 && string(msg.Error) != "null" {
			return nil, &diagnosed{err: classify(msg.Error), d: Diagnostic{Server: rpcDescriptor(msg.Error)}}
		}
		if len(msg.Result) == 0 {
			return nil, failure("workiq_protocol")
		}
		return msg.Result, nil
	case <-deadline.Done():
		_ = c.p.close()
		return nil, failure("workiq_timeout")
	case <-c.p.stop:
		return nil, failure("workiq_failed")
	}
}

func (c *Client) Fetch(ctx context.Context, path string) (Page, error) {
	relative, err := RelativePath(path, "")
	if err != nil {
		return Page{}, err
	}
	return c.fetchPath(ctx, relative)
}

// fetchPath fetches a path its caller already validated or built.
func (c *Client) fetchPath(ctx context.Context, relative string) (Page, error) {
	raw, err := c.call(ctx, requestTimeout, "tools/call", map[string]any{"name": "fetch", "arguments": map[string]any{"entityUrls": []string{relative}}})
	if err != nil {
		_ = c.Close()
		return Page{}, err
	}
	page, err := decodePage(ctx, raw)
	if err != nil {
		_ = c.Close()
	}
	return page, err
}

func (c *Client) Close() error {
	err := c.p.close()
	<-c.drained
	return err
}
