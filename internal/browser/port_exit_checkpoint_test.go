package browser

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type portExitContext struct {
	context.Context
	publish func()
}

func (c portExitContext) Done() <-chan struct{} {
	c.publish()
	return nil
}

func TestWaitForPortPublishedAtExitCheckpoint(t *testing.T) {
	profile := newProfile(t)
	if err := os.MkdirAll(profile, 0o700); err != nil {
		t.Fatal(err)
	}
	b := &Browser{profile: profile, exited: make(chan struct{}), status: "0"}
	published := false
	ctx := portExitContext{Context: context.Background(), publish: func() {
		if published {
			t.Fatal("exit checkpoint did not return the newly published port")
		}
		published = true
		if err := os.WriteFile(filepath.Join(profile, devToolsFile), []byte("9222\n/devtools/browser/synthetic-exit\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		close(b.exited)
	}}
	url, err := b.waitForPort(ctx, time.Second)
	if err != nil || url != "ws://127.0.0.1:9222/devtools/browser/synthetic-exit" || !published {
		t.Fatalf("exit-published port = %q, %v, checkpoint=%t", url, err, published)
	}
}
