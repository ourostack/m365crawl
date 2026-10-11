package cli

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ourostack/m365crawl/internal/outlookdesktop"
	"github.com/ourostack/m365crawl/internal/outlookmail"
	"github.com/ourostack/m365crawl/internal/outlookowa"
	"github.com/ourostack/m365crawl/internal/store"
)

func TestDoctorMailMetadataRoutesLegacyAndNativeFailures(t *testing.T) {
	oldLegacy, oldNative := outlookDiscover, outlookNativeDiscover
	t.Cleanup(func() { outlookDiscover, outlookNativeDiscover = oldLegacy, oldNative })
	outlookNativeDiscover = func(string) ([]outlookowa.Profile, []outlookowa.SkippedProfile, error) { return nil, nil, nil }
	rt := &runtime{outlookOn: true, outlookDefault: true, outlookRoot: createdOWARoot(t)}
	for _, tc := range []struct {
		err  error
		want string
	}{
		{outlookdesktop.ErrNotSupported, "not read"},
		{outlookdesktop.ErrRootNotFound, "not installed"},
		{nil, "no profile"},
	} {
		outlookDiscover = func(string) ([]outlookdesktop.Profile, []string, []outlookdesktop.SkippedProfile, error) {
			return nil, nil, nil, tc.err
		}
		if c := rt.outlookStoreCheck(nil); !c.OK || c.Warn || !strings.Contains(c.Detail, tc.want) {
			t.Fatalf("legacy metadata state = %+v", c)
		}
	}
	outlookNativeDiscover = func(string) ([]outlookowa.Profile, []outlookowa.SkippedProfile, error) {
		return []outlookowa.Profile{{Name: "Synthetic"}}, nil, nil
	}
	// A native profile without an archive is a readable path, not a decoded mailbox.
	if c := rt.outlookStoreCheck(nil); c.Warn || !strings.Contains(c.Detail, "OWA") {
		t.Fatalf("native metadata = %+v", c)
	}
	outlookNativeDiscover = func(string) ([]outlookowa.Profile, []outlookowa.SkippedProfile, error) {
		return nil, []outlookowa.SkippedProfile{{Name: "Unavailable"}}, nil
	}
	if profiles, problem := rt.outlookProfiles(); len(profiles) != 1 || profiles[0].Name != "Unavailable" || problem == nil {
		t.Fatalf("unexamined profile lost = %v", profiles)
	}
	if c := rt.outlookNativeCheck(outlookMailSource{owa: true}, nil); c.Warn || !strings.Contains(c.Detail, "no profile") {
		t.Fatalf("default native empty cache = %+v", c)
	}
	rt.outlookDefault = false
	if c := rt.outlookNativeCheck(outlookMailSource{owa: true}, nil); !c.Warn || !strings.Contains(c.Fix, "EBWebView") {
		t.Fatalf("named native root without profiles must warn = %+v", c)
	}
}

type mailCancelBudget struct {
	context.Context
	remaining atomic.Int64
	cancel    context.CancelFunc
}

func (c *mailCancelBudget) Err() error {
	if c.remaining.Add(-1) < 0 {
		c.cancel()
	}
	return c.Context.Err()
}

// Sweep real archive reads with progressively later cancellation; failed conversation lookups must never return success or partial thread items.
func TestMailOWAThreadCancellationNeverReturnsPartialSuccess(t *testing.T) {
	e := textEnv(t)
	a := mailMsg(601, fInbox, "<one@example.test>", "", "First", 2, true)
	b := mailMsg(602, fInbox, "<two@example.test>", "", "Second", 1, false)
	a.ConversationID, b.ConversationID = "synthetic-thread", "synthetic-thread"
	seedMail(t, e, []outlookmail.Message{a, b})
	st, err := store.OpenReadOnly(context.Background(), e.db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	failures, successes := 0, 0
	for budget := int64(0); budget < 50; budget++ {
		ctx, cancel := context.WithCancel(context.Background())
		c := &mailCancelBudget{Context: ctx, cancel: cancel}
		c.remaining.Store(budget)
		rows, grouping, _, err := st.MailThread(c, "outlook/Main", 601)
		cancel()
		if err != nil {
			failures++
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("unexpected cancellation error: %v", err)
			}
		} else {
			successes++
			if len(rows) != 2 || grouping != "conversation" {
				t.Fatalf("partial success: %d %q", len(rows), grouping)
			}
		}
	}
	if failures == 0 || successes == 0 {
		t.Fatalf("cancellation sweep did not exercise both outcomes: %d/%d", failures, successes)
	}
}

func TestMailOWANativeStatusText(t *testing.T) {
	e := textEnv(t)
	root := createdOWARoot(t)
	copyTree(t, filepath.Join("..", "..", "testdata", "outlook-owa-fixture", "mail"), filepath.Join(root, "Default"))
	if code, _, stderr := e.run("--outlook-root", root, "sync"); code != 0 {
		t.Fatalf("sync = %d: %s", code, stderr)
	}
	code, out, stderr := e.run("--format", "text", "--max-age", "0", "--outlook-root", root, "status")
	if code != 0 || !strings.Contains(out, "5 messages, 1 unread, 7 folders") {
		t.Fatalf("native status text = %d: %s %s", code, out, stderr)
	}
}
