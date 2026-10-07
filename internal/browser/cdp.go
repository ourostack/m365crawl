package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sync"

	"github.com/coder/websocket"
)

// readLimit bounds one websocket message. A transcript of tens of thousands of entries is many
// megabytes of JSON.
const readLimit = 64 << 20

// CDPError is an error response from the browser.
type CDPError struct {
	Code    int
	Message string
}

func (e *CDPError) Error() string {
	return fmt.Sprintf("browser protocol error %d: %s", e.Code, e.Message)
}

var errNotLoopback = errors.New("refusing to connect to a debugging address that is not on this machine")

type reply struct {
	result json.RawMessage
	err    error
}

// client is a minimal Chrome DevTools Protocol client over one websocket. It sends no Origin
// header: Chrome refuses a debugging socket whose Origin it was not told to allow.
type client struct {
	conn    *websocket.Conn
	cancel  context.CancelFunc
	mu      sync.Mutex
	nextID  int
	pending map[int]chan reply
	dead    error
}

// dialCDP connects to a browser debugging URL, which must point at this machine.
func dialCDP(ctx context.Context, rawurl string) (*client, error) {
	u, err := url.Parse(rawurl)
	if err != nil || (u.Scheme != "ws" && u.Scheme != "wss") {
		return nil, errNotLoopback
	}
	if h := u.Hostname(); h != "127.0.0.1" && h != "localhost" {
		return nil, errNotLoopback
	}
	conn, _, err := websocket.Dial(ctx, rawurl, nil)
	if err != nil {
		return nil, err
	}
	conn.SetReadLimit(readLimit)
	loopCtx, cancel := context.WithCancel(context.Background())
	c := &client{conn: conn, cancel: cancel, pending: map[int]chan reply{}}
	go c.readLoop(loopCtx)
	return c, nil
}

func (c *client) readLoop(ctx context.Context) {
	for {
		_, data, err := c.conn.Read(ctx)
		if err != nil {
			c.fail(err)
			return
		}
		var in struct {
			ID     int             `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(data, &in) != nil || in.ID == 0 {
			continue // an event, or noise
		}
		r := reply{result: in.Result}
		if in.Error != nil {
			r.err = &CDPError{Code: in.Error.Code, Message: in.Error.Message}
		}
		c.mu.Lock()
		ch := c.pending[in.ID]
		delete(c.pending, in.ID)
		c.mu.Unlock()
		if ch != nil {
			ch <- r
		}
	}
}

// fail ends every call in flight and every later one.
func (c *client) fail(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.dead = err
	for id, ch := range c.pending {
		ch <- reply{err: err}
		delete(c.pending, id)
	}
}

// call sends one command, on the given session when sessionID is not empty, and decodes the
// result into out when out is not nil.
func (c *client) call(ctx context.Context, sessionID, method string, params, out any) error {
	ch := make(chan reply, 1)
	c.mu.Lock()
	if c.dead != nil {
		err := c.dead
		c.mu.Unlock()
		return err
	}
	c.nextID++
	id := c.nextID
	c.pending[id] = ch
	c.mu.Unlock()

	msg := map[string]any{"id": id, "method": method}
	if params != nil {
		msg["params"] = params
	}
	if sessionID != "" {
		msg["sessionId"] = sessionID
	}
	data, err := json.Marshal(msg)
	if err == nil {
		err = c.conn.Write(ctx, websocket.MessageText, data)
	}
	if err != nil {
		c.forget(id)
		return err
	}
	select {
	case r := <-ch:
		if r.err != nil {
			return r.err
		}
		if out == nil {
			return nil
		}
		return json.Unmarshal(r.result, out)
	case <-ctx.Done():
		c.forget(id)
		return ctx.Err()
	}
}

func (c *client) forget(id int) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

func (c *client) close() {
	c.cancel()
	_ = c.conn.CloseNow()
}
