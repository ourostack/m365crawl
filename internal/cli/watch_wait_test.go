package cli

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/store"
)

// A wait answers satisfied with the messages a chat got after the cursor, as a wait/1 document.
func TestWatchWaitAnswersSatisfiedWithNewMessages(t *testing.T) {
	e := newEnv(t)
	e.sync()
	code, out, stderr := e.run("watch", fixtureChat, "--timeout", "1s", "--cursor", "2000-01-01T00:00:00Z", "--json")
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr)
	}
	d := decode(t, out)
	if d["ax"] != "wait/1" || d["state"] != "satisfied" {
		t.Fatalf("wait = %v, want a satisfied wait/1", d)
	}
	if events, _ := d["events"].([]any); len(events) == 0 {
		t.Fatalf("events = %v, want the chat's messages", d["events"])
	}
	if cursor, _ := d["cursor"].(string); cursor == "" {
		t.Fatalf("cursor is empty: a resumed wait would miss events")
	}
	code, out, _ = e.run("--format", "text", "watch", fixtureChat, "--timeout", "1s", "--cursor", "2000-01-01T00:00:00Z")
	if code != 0 || !strings.Contains(out, "wrote in the chat") {
		t.Fatalf("text wait exit %d, out %q; want one line per new message", code, out)
	}
}

// With nothing new, the wait times out, and the document names the command that waits again from its cursor.
func TestWatchWaitTimesOutWithAResumeCommand(t *testing.T) {
	e := newEnv(t)
	code, out, _ := e.run("watch", fixtureChat, "--timeout", "1s", "--json")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (nothing came yet is an answer)", code)
	}
	d := decode(t, out)
	resume, _ := d["resume"].(map[string]any)
	if d["state"] != "timed-out" || !strings.Contains(resume["command"].(string), "--cursor") {
		t.Fatalf("wait = %v, want timed-out with a resume command that names the cursor", d)
	}
	code, out, _ = e.run("--format", "text", "watch", fixtureChat, "--timeout", "1s")
	if code != 0 || !strings.Contains(out, "resume with") {
		t.Fatalf("text wait exit %d, out %q; want it to say how to resume", code, out)
	}
}

// A cursor that is not a time is a usage error, and nothing waits.
func TestWatchWaitRefusesABadCursor(t *testing.T) {
	e := newEnv(t)
	if code, _, stderr := e.run("watch", fixtureChat, "--cursor", "yesterday"); code != 2 || !strings.Contains(stderr, "--cursor") {
		t.Fatalf("exit %d, stderr %q; want usage with --cursor named", code, stderr)
	}
}

// A store failure while waiting is the command's error, not an answer.
func TestWatchWaitReturnsAStoreFailure(t *testing.T) {
	e := newEnv(t)
	e.sync()
	old := messagesAfter
	messagesAfter = func(context.Context, *store.Store, store.Filter) ([]store.MessageRow, bool, error) {
		return nil, false, errors.New("store went away")
	}
	t.Cleanup(func() { messagesAfter = old })
	if code, _, stderr := e.run("watch", fixtureChat, "--timeout", "1s", "--json"); code == 0 || !strings.Contains(stderr, "store went away") {
		t.Fatalf("exit %d, stderr %q; want the store failure as the error", code, stderr)
	}
}

// With no archive there are no messages to wait for.
func TestWatchWaitNewMessagesWithoutAnArchive(t *testing.T) {
	c := &watchCmd{Conversation: fixtureChat}
	events, err := c.newMessages(&runtime{ctx: context.Background()}, nil, time.Now())
	if err != nil || events != nil {
		t.Fatalf("events %v, err %v; want none", events, err)
	}
}
