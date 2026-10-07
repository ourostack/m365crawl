package cli

import (
	"context"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/hxstore/hxbuild"
	"github.com/ourostack/m365crawl/internal/outlookdesktop"
	"github.com/ourostack/m365crawl/internal/store"
)

// mailStoreRoot is an Outlook root whose store holds the fixture's calendar and a small synthetic
// mailbox: an inbox with an unread and a read message.
func mailStoreRoot(t *testing.T) string {
	t.Helper()
	root := outlookStoreRoot(t, "HxStore.hxd")
	path := filepath.Join(root, "Main", "HxStore.hxd")
	b, err := os.ReadFile(path) //nolint:gosec // a test temp dir
	if err != nil {
		t.Fatal(err)
	}
	day := time.Date(2031, 3, 3, 9, 0, 0, 0, time.UTC)
	hdr := func(key, detail, unread uint32, subject string) *hxbuild.Object {
		return hxbuild.NewMailHeader(hxbuild.MailHeaderSpec{Key: key, Stamp: 1, DetailKey: detail, FolderKey: 101, Received: day.Add(time.Duration(key) * time.Hour),
			Subject: subject, SenderName: "Fixture Sender", SenderAddr: "sender@example.invalid", Unread: unread, Importance: 1})
	}
	det := func(key uint32, id string) *hxbuild.Object {
		return hxbuild.NewMailDetail(hxbuild.MailDetailSpec{Key: key, Stamp: 1, MessageID: id, Class: "IPM.Note", Sent: day})
	}
	objs := []*hxbuild.Object{
		hxbuild.NewMailFolder(hxbuild.MailFolderSpec{Key: 101, Parent: 1000, Name: "Fixture Inbox", Type: 0x61}),
		hdr(11, 21, 1, "Fixture one"), hdr(12, 22, 0, "Fixture two"),
		det(21, "<one@example.invalid>"), det(22, "<two@example.invalid>"),
		hxbuild.NewMailBody(hxbuild.MailBodySpec{Key: 21, Stamp: 1, HTML: []byte("<p>inline body</p>")}),
	}
	b = append(b, hxbuild.EncodeBlock(hxbuild.BlockTypeData, hxbuild.FramedPayload(hxbuild.Head(15), objs...))...)
	if err := os.WriteFile(path, b, 0o600); err != nil { //nolint:gosec // a test temp dir
		t.Fatal(err)
	}
	return root
}

// forceMailSupported makes the test read as on a Mac, whatever it runs on.
func forceMailSupported(t *testing.T) {
	t.Helper()
	old := mailSupported
	mailSupported = func() bool { return true }
	t.Cleanup(func() { mailSupported = old })
}

func mailBlockOf(t *testing.T, doc map[string]any) map[string]any {
	t.Helper()
	m, ok := doc["mail"].(map[string]any)
	if !ok {
		t.Fatalf("no mail block in %v", doc)
	}
	return m
}

// status carries a mail block: the counts of what a sync read and the state of reading mail.
func TestStatusMailBlock(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("a Windows sync does not read mail")
	}
	e := textEnv(t)
	root := mailStoreRoot(t)
	if code, _, stderr := e.run("--outlook-root", root, "sync"); code != 0 {
		t.Fatalf("sync exit %d: %s", code, stderr)
	}
	code, out, errOut := e.run("--outlook-root", root, "status")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	m := mailBlockOf(t, decode(t, out))
	if m["state"] != "ok" || m["messages"] != float64(2) || m["unread"] != float64(1) || m["folders"] != float64(1) || m["oldest_at"] == nil || m["synced_at"] == nil {
		t.Fatalf("mail block: %v", m)
	}

	// With the Outlook source off for the run the block says so, and keeps the counts.
	_, out, _ = e.run("status")
	if m = mailBlockOf(t, decode(t, out)); m["state"] != "skipped" || m["messages"] != float64(2) {
		t.Fatalf("off: %v", m)
	}

	for _, color := range []bool{false, true} {
		t.Setenv("CLICOLOR_FORCE", "")
		suffix := "plain"
		if color {
			suffix = "color"
			t.Setenv("CLICOLOR_FORCE", "1")
		}
		code, out, errOut = e.run("--format", "text", "--outlook-root", root, "status")
		if code != 0 {
			t.Fatalf("exit %d: %s", code, errOut)
		}
		checkGolden(t, "status_mail."+suffix, e.scrub(out))
	}
}

// The other states of the block: no archive, nothing to read, nothing read yet, this platform.
func TestStatusMailStates(t *testing.T) {
	t.Run("no archive and no Outlook", func(t *testing.T) {
		forceMailSupported(t)
		e := newEnv(t)
		_, out, _ := e.runDefault("status")
		if m := mailBlockOf(t, decode(t, out)); m["state"] != "no_profile" || m["messages"] != float64(0) {
			t.Fatalf("%v", m)
		}
	})
	t.Run("profile not read yet", func(t *testing.T) {
		forceMailSupported(t)
		e := newEnv(t)
		e.sync()
		_, out, _ := e.run("--outlook-root", mailStoreRoot(t), "status")
		if m := mailBlockOf(t, decode(t, out)); m["state"] != "skipped" {
			t.Fatalf("%v", m)
		}
	})
	t.Run("unsupported platform", func(t *testing.T) {
		old := mailSupported
		mailSupported = func() bool { return false }
		t.Cleanup(func() { mailSupported = old })
		e := newEnv(t)
		_, out, _ := e.run("status")
		if m := mailBlockOf(t, decode(t, out)); m["state"] != "unsupported_platform" {
			t.Fatalf("%v", m)
		}
	})
	t.Run("malformed mail table", func(t *testing.T) {
		forceMailSupported(t)
		e := newEnv(t)
		e.sync()
		e.exec(`drop table mail_fts; drop table mail_messages; create table mail_messages(x)`)
		if code, _, _ := e.run("status"); code == 0 {
			t.Fatal("status succeeded on a malformed mail table")
		}
	})
	t.Run("archive from before mail", func(t *testing.T) {
		forceMailSupported(t)
		e := newEnv(t)
		e.sync()
		e.exec(`drop table mail_fts; drop table mail_messages; drop table mail_folders`)
		code, out, errOut := e.run("status")
		if code != 0 {
			t.Fatalf("status exit %d: %s", code, errOut)
		}
		if m := mailBlockOf(t, decode(t, out)); m["messages"] != float64(0) || m["folders"] != float64(0) {
			t.Fatalf("%v", m)
		}
	})
}

func mailRuntime(t *testing.T, root string, on bool) (*runtime, *store.Store, *env) {
	t.Helper()
	e := newEnv(t)
	e.sync()
	st, err := store.OpenReadOnly(context.Background(), e.db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	now := time.Now().UTC()
	return &runtime{ctx: context.Background(), now: func() time.Time { return now }, dbPath: e.db, outlookRoot: root, outlookOn: on}, st, e
}

// doctor's mail_readable check says why mail is not read, when it was read and why a read failed;
// it warns and never fails.
func TestDoctorMailReadableCheck(t *testing.T) {
	forceMailSupported(t)
	root := mailStoreRoot(t)
	rt, st, e := mailRuntime(t, root, true)
	acct := "outlook/Main"

	if c := rt.mailReadableCheck(st); !c.OK || !c.Warn || !strings.Contains(c.Detail, "mail not read yet") || !strings.Contains(c.Fix, "m365crawl sync") {
		t.Fatalf("not read: %+v", c)
	}
	// A writable handle to record the states the sync would.
	w, err := store.Open(context.Background(), rt.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })
	if err := w.SetMailRead(context.Background(), acct, rt.now().Add(-90*time.Second)); err != nil {
		t.Fatal(err)
	}
	if c := rt.mailReadableCheck(st); !c.OK || c.Warn || c.Detail != "profile Main: mail read 1m ago" || c.Fix != "" {
		t.Fatalf("read: %+v", c)
	}
	if err := w.SetMailFailure(context.Background(), acct, store.OutlookFailure{Code: "outlook_mail_layout_unsupported", Message: "layout moved", Fix: "Update m365crawl."}); err != nil {
		t.Fatal(err)
	}
	if c := rt.mailReadableCheck(st); !c.OK || !c.Warn || !strings.Contains(c.Detail, "outlook_mail_layout_unsupported: layout moved") || c.Fix != "Update m365crawl." {
		t.Fatalf("failed: %+v", c)
	}
	if err := w.SetMailFailure(context.Background(), acct, store.OutlookFailure{Code: "c", Message: "m"}); err != nil {
		t.Fatal(err)
	}
	if c := rt.mailReadableCheck(st); !strings.Contains(c.Fix, "m365crawl sync") {
		t.Fatalf("a failure with no fix: %+v", c)
	}
	e.exec(`update meta set value='not json' where key like 'outlook\_mail\_failure:%' escape '\'`)
	if c := rt.mailReadableCheck(st); !c.Warn || !strings.Contains(c.Detail, "cannot read the mail state") {
		t.Fatalf("a bad state: %+v", c)
	}

	t.Run("off", func(t *testing.T) {
		off := *rt
		off.outlookOn = false
		if c := off.mailReadableCheck(st); !c.OK || c.Warn || !strings.Contains(c.Detail, "Outlook source is off") {
			t.Fatalf("%+v", c)
		}
	})
	t.Run("no archive", func(t *testing.T) {
		if c := rt.mailReadableCheck(nil); !c.OK || c.Warn || !strings.Contains(c.Detail, "no archive yet") {
			t.Fatalf("%+v", c)
		}
	})
	t.Run("no profile", func(t *testing.T) {
		none := *rt
		none.outlookRoot = t.TempDir()
		if c := none.mailReadableCheck(st); !c.OK || c.Warn || !strings.Contains(c.Detail, "no mail to read") {
			t.Fatalf("%+v", c)
		}
	})
	t.Run("default root unavailable", func(t *testing.T) {
		old := outlookDefaultRoot
		outlookDefaultRoot = func() (string, error) { return "", outlookdesktop.ErrNotSupported }
		t.Cleanup(func() { outlookDefaultRoot = old })
		none := *rt
		none.outlookRoot = ""
		if c := none.mailReadableCheck(st); !c.OK || c.Warn || !strings.Contains(c.Detail, "no mail to read") {
			t.Fatalf("%+v", c)
		}
	})
	t.Run("unsupported platform", func(t *testing.T) {
		old := mailSupported
		mailSupported = func() bool { return false }
		t.Cleanup(func() { mailSupported = old })
		if c := rt.mailReadableCheck(st); !c.OK || c.Warn || c.Detail != errs.MailUnsupportedPlatform().Message {
			t.Fatalf("%+v", c)
		}
	})
}

// mail_archive_mode warns when other users can read the archive and says nothing of its content.
func TestDoctorMailArchiveModeCheck(t *testing.T) {
	rt := &runtime{ctx: context.Background(), dbPath: filepath.Join(t.TempDir(), "m365crawl.db")}
	if goruntime.GOOS == "windows" {
		// File modes mean nothing here: the check says so and the mode cases below do not apply.
		if c := rt.mailArchiveModeCheck(); !c.OK || c.Warn || !strings.Contains(c.Detail, "not applicable on Windows") {
			t.Fatalf("windows: %+v", c)
		}
		return
	}
	if c := rt.mailArchiveModeCheck(); !c.OK || c.Warn || !strings.Contains(c.Detail, "no archive yet") {
		t.Fatalf("no archive: %+v", c)
	}
	if err := os.WriteFile(rt.dbPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if c := rt.mailArchiveModeCheck(); !c.OK || c.Warn || !strings.Contains(c.Detail, "0600") {
		t.Fatalf("0600: %+v", c)
	}
	if err := os.Chmod(rt.dbPath, 0o644); err != nil { //nolint:gosec // the check under test reads this mode
		t.Fatal(err)
	}
	if c := rt.mailArchiveModeCheck(); !c.OK || !c.Warn || !strings.Contains(c.Detail, "0644") || !strings.Contains(c.Fix, "chmod 600") {
		t.Fatalf("0644: %+v", c)
	}
	old := archiveModeApplicable
	archiveModeApplicable = func() bool { return false }
	t.Cleanup(func() { archiveModeApplicable = old })
	if c := rt.mailArchiveModeCheck(); !c.OK || c.Warn || !strings.Contains(c.Detail, "not applicable") {
		t.Fatalf("windows: %+v", c)
	}
}
