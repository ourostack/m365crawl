package workiq

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

type observedPipeWriter struct {
	io.WriteCloser
	started chan struct{}
	once    sync.Once
}

func (w *observedPipeWriter) Write(data []byte) (int, error) {
	w.once.Do(func() { close(w.started) })
	return w.WriteCloser.Write(data)
}

func TestWorkIQBlockedRequestPipeStillHonorsItsDeadline(t *testing.T) {
	useStub(t, "blocked-write")
	client, err := Open(context.Background(), Invocation{}, "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		// JSON's required ampersand escaping makes this valid sub-64KiB URI exceed pipe capacity.
		_, err := client.Fetch(ctx, "/me/messages?"+strings.Repeat("a=1&", 14000))
		done <- err
	}()
	select {
	case err := <-done:
		requireCode(t, err, "workiq_timeout")
	case <-time.After(7 * time.Second):
		_ = client.Close()
		<-done
		t.Fatal("request timeout did not bound a blocked subprocess stdin write")
	}
	if client.p.cmd.ProcessState == nil {
		t.Fatal("timed-out writer left its owned process unjoined")
	}
}

func TestWorkIQQueuedCallCancellationDoesNotWaitForAnotherWrite(t *testing.T) {
	useStub(t, "blocked-write")
	client, err := Open(context.Background(), Invocation{}, "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	started := make(chan struct{})
	client.p.input = &observedPipeWriter{WriteCloser: client.p.input, started: started}
	first := make(chan error, 1)
	go func() {
		_, err := client.Fetch(context.Background(), "/me/messages?"+strings.Repeat("a=1&", 14000))
		first <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("synthetic blocked request never reached its real pipe")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	second := make(chan error, 1)
	go func() {
		_, err := client.Fetch(ctx, "/me/messages")
		second <- err
	}()
	select {
	case err := <-second:
		requireCode(t, err, "workiq_timeout")
	case <-time.After(7 * time.Second):
		_ = client.Close()
		<-first
		<-second
		t.Fatal("queued request ignored cancellation while waiting for the active writer")
	}
	if err := <-first; err == nil {
		t.Fatal("interrupted first writer became a successful response")
	}
}
