package workiq

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"sync"

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

// Client exposes only fetch; callers cannot select an MCP tool or client capability.
type Client struct {
	p       *process
	mu      sync.Mutex
	writeMu sync.Mutex
	next    uint64
	frames  chan frame
	drained chan struct{}
}

func Open(ctx context.Context, inv Invocation, account string) (*Client, error) {
	p, err := startWorkIQ(ctx, inv, readMCP, account)
	if err != nil {
		return nil, err
	}
	c := &Client{p: p, frames: make(chan frame), drained: make(chan struct{})}
	go c.read()
	result, err := c.call(ctx, "initialize", map[string]any{
		"protocolVersion": "2025-06-18", "capabilities": map[string]any{},
		"clientInfo": map[string]any{"name": "m365crawl", "version": "1"},
	})
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
		err = c.write(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	}
	if err != nil {
		_ = c.Close()
		return nil, err
	}
	return c, nil
}

func (c *Client) read() {
	defer close(c.drained)
	scan := bufio.NewScanner(c.p.output)
	scan.Buffer(make([]byte, 64<<10), maxFrame+1)
	controls := 0
	for scan.Scan() {
		raw := scan.Bytes()
		budget := strictjson.Budget{Context: context.Background(), MaxDepth: 64, MaxMembers: 131072}
		var msg wireMessage
		_, parseErr := budget.Parse(raw)
		if parseErr != nil || json.Unmarshal(raw, &msg) != nil || msg.RPC != "2.0" {
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
			if err := c.write(reply); err != nil {
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

func (c *Client) write(v any) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if err := json.NewEncoder(c.p.input).Encode(v); err != nil {
		return failure("workiq_failed")
	}
	return nil
}

func (c *Client) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	deadline, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	c.next++
	id := c.next
	if err := c.write(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		return nil, err
	}
	select {
	case f := <-c.frames:
		if f.err != nil {
			var coded *errs.Coded
			if errors.As(f.err, &coded) {
				return nil, coded
			}
			_ = c.p.close() // wait for stderr draining before classifying a child that exited
			return nil, c.p.errors.diagnostic()
		}
		msg := f.msg
		if string(msg.ID) != strconv.FormatUint(id, 10) {
			return nil, failure("workiq_protocol")
		}
		if len(msg.Error) > 0 && string(msg.Error) != "null" {
			return nil, classify(msg.Error)
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
	raw, err := c.call(ctx, "tools/call", map[string]any{"name": "fetch", "arguments": map[string]any{"entityUrls": []string{relative}}})
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
