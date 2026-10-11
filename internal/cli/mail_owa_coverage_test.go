package cli

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/outlookdesktop"
	"github.com/ourostack/m365crawl/internal/outlookmail"
	"github.com/ourostack/m365crawl/internal/outlookowa"
	"github.com/ourostack/m365crawl/internal/store"
)

func TestDoctorNativeResolvedOptionalAbsence(t *testing.T) {
	parent := createdOWARoot(t)
	missing := filepath.Join(parent, "missing")
	for _, tc := range []struct {
		name     string
		optional bool
		root     string
		cause    error
		warn     bool
	}{
		{"optional-resolved-missing", true, missing, fs.ErrNotExist, false},
		{"explicit-resolved-missing", false, missing, fs.ErrNotExist, true},
		{"optional-unresolved-missing", true, "", fs.ErrNotExist, true},
		{"optional-resolved-unreadable", true, missing, fs.ErrPermission, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt := &runtime{outlookDefault: tc.optional}
			s := outlookMailSource{owa: true, root: tc.root,
				err: &fs.PathError{Op: "synthetic-private-operation", Path: "synthetic-private-path", Err: tc.cause}}
			c := rt.outlookNativeCheck(s, nil)
			wantDetail, wantFix := "the new Outlook OWA cache is not installed; nothing to read", ""
			if tc.warn {
				wantDetail = "the Outlook OWA profile paths cannot be examined (outlook_profile_unreadable)"
				wantFix = "Check that the Outlook cache directories are readable by your account."
			}
			if c.Name != "outlook_store" || !c.OK || c.Warn != tc.warn || c.Detail != wantDetail || c.Fix != wantFix {
				t.Fatal("native_optional_absence_not_fixed_or_wrong_warning")
			}
		})
	}
}

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
	t.Run("pre-cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		rows, grouping, truncated, err := st.MailThread(ctx, "outlook/Main", 601)
		if !errors.Is(err, context.Canceled) || rows != nil || grouping != "" || truncated {
			t.Fatal("cancelled_thread_returned_partial_or_success")
		}
	})
	t.Run("success-after-cancelled-read", func(t *testing.T) {
		rows, grouping, truncated, err := st.MailThread(context.Background(), "outlook/Main", 601)
		if err != nil || len(rows) != 2 || grouping != "conversation" || truncated ||
			rows[0].DetailKey != 601 || rows[1].DetailKey != 602 {
			t.Fatal("uncancelled_thread_not_complete_after_cancelled_read")
		}
	})
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
