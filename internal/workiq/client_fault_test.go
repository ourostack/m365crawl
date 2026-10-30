package workiq

import (
	"context"
	"io"
	"strings"
	"testing"
)

type brokenInput struct{}

func (brokenInput) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
func (brokenInput) Close() error              { return nil }

type discardInput struct{}

func (discardInput) Write(p []byte) (int, error) { return len(p), nil }
func (discardInput) Close() error                { return nil }

func TestWorkIQTransportFailureBranches(t *testing.T) {
	for mode, code := range map[string]string{
		"flood": "workiq_protocol", "oversize": "workiq_page_too_large",
		"rpc-error": "workiq_signin_required", "missing-result": "workiq_protocol",
		"eof": "workiq_eula_required",
	} {
		t.Run(mode, func(t *testing.T) {
			useStub(t, mode)
			client, err := Open(context.Background(), Invocation{}, "")
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Fetch(context.Background(), "/me")
			requireCode(t, err, code)
			if d, _ := DiagnosticOf(err); mode == "rpc-error" && d.Server != "rpc error; code -32000; [redacted]" {
				t.Fatalf("JSON-RPC error descriptor %+v", d)
			}
			_ = client.Close()
		})
	}
	for _, mode := range []string{"notification", "extra-response"} {
		useStub(t, mode)
		client, err := Open(context.Background(), Invocation{}, "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := client.Fetch(context.Background(), "/me"); err != nil {
			t.Fatal(err)
		}
		if err := client.Close(); err != nil {
			t.Fatal(err)
		}
	}
	useStub(t, "")
	client, err := Open(context.Background(), Invocation{}, "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Fetch(context.Background(), "/organization")
	requireCode(t, err, "workiq_cursor_invalid")
	_ = client.p.input.Close()
	_, err = client.Fetch(context.Background(), "/me")
	requireCode(t, err, "workiq_session_lost")
}

func TestWorkIQMalformedFramesAndReplyIOFailure(t *testing.T) {
	for _, raw := range []string{
		`{"jsonrpc":"1.0","id":1,"result":{}}`,
		`{"jsonrpc":"2.0","method":"ping","id":"server"}`,
	} {
		p := &process{input: brokenInput{}, output: io.NopCloser(strings.NewReader(raw + "\n")), stop: make(chan struct{})}
		c := &Client{p: p, frames: make(chan frame, 1), drained: make(chan struct{})}
		go c.read(context.Background())
		f := <-c.frames
		if f.err == nil {
			t.Fatal("malformed frame or failed response IO became success")
		}
		<-c.drained
		close(p.stop)
	}
	p := &process{input: discardInput{}, stop: make(chan struct{})}
	close(p.stop)
	c := &Client{p: p, frames: make(chan frame)}
	_, err := c.call(context.Background(), requestTimeout, "initialize", nil)
	requireCode(t, err, "workiq_failed")
}
