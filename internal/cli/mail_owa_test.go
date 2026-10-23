package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/outlookdesktop"
	"github.com/ourostack/m365crawl/internal/outlookmail"
	"github.com/ourostack/m365crawl/internal/outlookowa"
	"github.com/ourostack/m365crawl/internal/store"
)

func createdOWARoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal("synthetic_owa_root_canonicalization_failed")
	}
	return root
}

// Catches platform refusal, missing nullable fields, and loss of native recipient order.
func TestMailOWAArchiveSerialization(t *testing.T) {
	e := textEnv(t)
	a := mailMsg(301, fInbox, "<native@example.test>", "", "Native first", 2, true)
	a.ConversationID = "synthetic-conversation"
	a.Recipients = []outlookmail.Recipient{
		{Name: "To Reader", Address: "to@example.test", Role: "to"},
		{Name: "Cc Reader", Address: "cc@example.test", Role: "cc"},
		{Name: "Bcc Reader", Address: "bcc@example.test", Role: "bcc"},
	}
	b := mailMsg(302, fSent, "<other@example.test>", "", "Different subject", 1, false)
	b.ConversationID = a.ConversationID
	seedMail(t, e, []outlookmail.Message{a, b})
	list := mailJSON(t, e, "mail", "list")
	for _, it := range items(t, list) {
		if it["conversation_id"] != "synthetic-conversation" {
			t.Fatalf("conversation_id = %v", it["conversation_id"])
		}
	}
	show := mailJSON(t, e, "mail", "show", "outlook/Main:301")
	recipients := show["recipients"].([]any)
	if len(recipients) != 3 {
		t.Fatalf("recipients = %v", recipients)
	}
	for i, want := range []string{"to", "cc", "bcc"} {
		r := recipients[i].(map[string]any)
		if r["role"] != want || r["kind_raw"] != float64(0) {
			t.Fatalf("recipient %d = %v", i, r)
		}
	}
	thread := mailJSON(t, e, "mail", "thread", "outlook/Main:301")
	if thread["grouping"] != "conversation" || thread["count"] != float64(2) {
		t.Fatalf("thread = %v", thread)
	}
	code, out, stderr := e.mail("--format", "text", "mail", "thread", "outlook/Main:301")
	if code != 0 || !strings.Contains(out, "thread: conversation identity") {
		t.Fatalf("native thread text = %d %s %s", code, out, stderr)
	}
	for _, args := range [][]string{
		{"--fields", "id,conversation_id", "mail", "list"},
		{"--fields", "id,conversation_id,recipients", "mail", "show", "outlook/Main:301"},
		{"--fields", "id,conversation_id", "mail", "thread", "outlook/Main:301"},
	} {
		mailJSON(t, e, args...)
	}
}

func TestMailOWADoctorArchivedStates(t *testing.T) {
	root := createdOWARoot(t)
	copyTree(t, filepath.Join("..", "..", "testdata", "outlook-owa-fixture", "mail"), filepath.Join(root, "Default"))
	e := textEnv(t)
	e.emptyArchive()
	st, err := store.Open(context.Background(), e.db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	rt := &runtime{ctx: context.Background(), outlookOn: true, outlookRoot: root, now: func() time.Time { return now }}
	if profiles, problem := rt.outlookProfiles(); len(profiles) != 1 || profiles[0].Name != "Default" || problem != nil {
		t.Fatalf("OWA profile discovery = %v", profiles)
	}
	if c := rt.mailReadableCheck(st); !c.Warn || !strings.Contains(c.Detail, "not read yet") {
		t.Fatalf("unread profile = %+v", c)
	}
	if err := st.SetMailRead(rt.ctx, "outlook/Default", now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if c := rt.outlookStoreCheck(st); c.Warn || !strings.Contains(c.Detail, "mail read 1m ago") {
		t.Fatalf("read state = %+v", c)
	}
	if err := st.SetMailFailure(rt.ctx, "outlook/Default", store.OutlookFailure{Code: "outlook_mail_layout_unsupported", Message: "fixed layout failure"}); err != nil {
		t.Fatal(err)
	}
	if c := rt.outlookStoreCheck(st); !c.Warn || !strings.Contains(c.Detail, "outlook_mail_layout_unsupported") {
		t.Fatalf("failure state = %+v", c)
	}
	canceled, cancel := context.WithCancel(rt.ctx)
	cancel()
	rt.ctx = canceled
	if c := rt.outlookStoreCheck(st); !c.Warn || !strings.Contains(c.Detail, "cannot read the archived mail state") {
		t.Fatalf("state error = %+v", c)
	}
}

func TestMailOWADoctorPathFailures(t *testing.T) {
	rt := &runtime{outlookOn: true, outlookRoot: createdOWARoot(t)}
	if c := rt.outlookStoreCheck(nil); !c.Warn {
		t.Fatalf("explicit empty source accepted = %+v", c)
	}
	old := outlookNativeDiscover
	t.Cleanup(func() { outlookNativeDiscover = old })
	outlookNativeDiscover = func(string) ([]outlookowa.Profile, []outlookowa.SkippedProfile, error) {
		return nil, nil, os.ErrPermission
	}
	if c := rt.outlookStoreCheck(nil); !c.Warn || strings.Contains(c.Detail+c.Fix, "Full Disk Access") {
		t.Fatalf("native permission failure = %+v", c)
	}
	outlookNativeDiscover = func(string) ([]outlookowa.Profile, []outlookowa.SkippedProfile, error) {
		return nil, []outlookowa.SkippedProfile{{Name: "Unreadable", Err: os.ErrPermission}}, nil
	}
	if c := rt.outlookStoreCheck(nil); !c.Warn || !strings.Contains(c.Detail, "outlook_profile_unreadable") {
		t.Fatalf("skipped sibling = %+v", c)
	}
}

func TestMailDefaultRootPreservesNativeErrorsAndLegacyFallback(t *testing.T) {
	old := outlookNativeDefaultRoot
	t.Cleanup(func() { outlookNativeDefaultRoot = old })
	outlookNativeDefaultRoot = func() (string, error) { return "", outlookdesktop.ErrNotSupported }
	_, err := defaultMailRoot()
	_, legacyErr := outlookdesktop.DefaultRoot()
	if (err == nil) != (legacyErr == nil) || err != nil && !errors.Is(err, outlookdesktop.ErrNotSupported) {
		t.Fatalf("legacy fallback error = %v, want %v", err, legacyErr)
	}

	outlookNativeDefaultRoot = func() (string, error) { return "", os.ErrPermission }
	if _, err := defaultMailRoot(); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("native resolution error swallowed: %v", err)
	}
}
func TestMailOWAReadableRefusesUnexaminedProfiles(t *testing.T) {
	e := textEnv(t)
	e.emptyArchive()
	st, err := store.OpenReadOnly(context.Background(), e.db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	old := outlookNativeDiscover
	t.Cleanup(func() { outlookNativeDiscover = old })
	outlookNativeDiscover = func(string) ([]outlookowa.Profile, []outlookowa.SkippedProfile, error) {
		return nil, []outlookowa.SkippedProfile{{Name: "Unreadable", Err: os.ErrPermission}}, nil
	}
	rt := &runtime{ctx: context.Background(), outlookOn: true, outlookRoot: createdOWARoot(t)}
	if c := rt.mailReadableCheck(st); !c.Warn || !strings.Contains(c.Detail, "outlook_profile_unreadable") {
		t.Fatalf("unexamined profile must not look like no mail: %+v", c)
	}
}

func TestMailLegacyNullableFields(t *testing.T) {
	e := textEnv(t)
	seedMail(t, e, seedMessages())
	show := mailJSON(t, e, "mail", "show", "outlook/Main:101")
	if v, ok := show["conversation_id"]; !ok || v != nil {
		t.Fatalf("legacy conversation must be present and null: %v", show)
	}
	for _, rc := range show["recipients"].([]any) {
		r := rc.(map[string]any)
		if v, ok := r["role"]; !ok || v != nil {
			t.Fatalf("legacy role must be present and null: %v", r)
		}
	}
}

// Catches HxStore-only discovery and inconsistent native search/status/doctor routing.
func TestMailOWASourceCLI(t *testing.T) {
	e := textEnv(t)
	root := createdOWARoot(t)
	copyTree(t, filepath.Join("..", "..", "testdata", "outlook-owa-calendar-local-fixture", "calendar"), filepath.Join(root, "Default"))
	code, out, stderr := e.run("--outlook-root", root, "sync")
	if code != 0 {
		t.Fatalf("sync exit %d: %s", code, stderr)
	}
	if strings.Contains(out+stderr, "credential") {
		t.Fatal("credential decoy leaked")
	}
	list := mailJSON(t, e, "--outlook-root", root, "mail", "list")
	if list["count"] != float64(5) {
		t.Fatalf("mail count = %v", list["count"])
	}
	status := mailJSON(t, e, "--outlook-root", root, "status")
	if m := mailBlockOf(t, status); m["state"] != "ok" || m["messages"] != float64(5) {
		t.Fatalf("mail status = %v", m)
	}
	search := mailJSON(t, e, "--outlook-root", root, "search", "Synthetic", "--source", "mail")
	if search["count"] != float64(5) {
		t.Fatalf("search = %v", search)
	}
	rt := &runtime{outlookOn: true, outlookRoot: root}
	c := rt.outlookStoreCheck(nil)
	if c.Warn || !strings.Contains(c.Detail, "OWA") || !strings.Contains(c.Detail, "calendar") {
		t.Fatalf("OWA metadata check = %+v", c)
	}
}
