package syncer

import (
	"bytes"
	"context"
	"errors"
	"os"
	osexec "os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/indexeddb"
	"github.com/ourostack/m365crawl/internal/leveldb"
	"github.com/ourostack/m365crawl/internal/outlookdesktop"
	"github.com/ourostack/m365crawl/internal/outlookowa"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/teamsdesktop"
	gl "github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"
	"github.com/syndtr/goleveldb/leveldb/util"
)

func owaRoot(t *testing.T, variant string) string {
	t.Helper()
	root := t.TempDir()
	var err error
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	copyTree(t, filepath.Join("..", "..", "testdata", "outlook-owa-fixture", variant), filepath.Join(root, "Default"))
	return root
}

func owaProfile(root string) outlookowa.Profile {
	idb := filepath.Join(root, "Default", "IndexedDB")
	return outlookowa.Profile{Name: "Default", Dir: filepath.Join(root, "Default"), Source: teamsdesktop.Source{
		Profile: "Default", Origin: "https_outlook.office.com_0",
		LevelDBDir: filepath.Join(idb, "https_outlook.office.com_0.indexeddb.leveldb"),
		BlobDir:    filepath.Join(idb, "https_outlook.office.com_0.indexeddb.blob"),
	}}
}

func TestSyncOWAMailOnlyAndUnchanged(t *testing.T) {
	tmp := isolateTmp(t)
	db, root := newDB(t), owaRoot(t, "mail")
	o := outlookOpts(db, root)
	o.Root = t.TempDir()
	r, _ := run(t, o)
	m := sourceKeyed(t, r, "outlook|Default|mail")
	if len(r.Sources) != 1 || m.Status != StatusOK || m.Counts == nil || m.Counts.Mail == nil || m.Counts.Mail.Added != 5 || len(m.Accounts) != 1 || m.Accounts[0] != "outlook/Default" {
		t.Fatalf("mail-only read: %+v", r.Sources)
	}
	if count(t, db, `select count(*) from calendar_source_events where source='outlook'`) != 0 || count(t, db, `select count(*) from meta where key like 'outlook_identity:%' or key like 'outlook_read:%'`) != 0 {
		t.Fatal("OWA invented calendar/identity markers")
	}
	if count(t, db, `select count(*) from mail_messages`) != 5 || count(t, db, `select count(*) from mail_recipients where role='bcc'`) != 1 {
		t.Fatal("native mail not committed")
	}
	if r, _ = run(t, o); sourceKeyed(t, r, "outlook|Default|mail").Status != StatusUnchanged {
		t.Fatalf("calendar IdentityRead incorrectly required: %+v", r.Sources)
	}
	o.FullRead = true
	if r, _ = run(t, o); sourceKeyed(t, r, "outlook|Default|mail").Status != StatusOK {
		t.Fatal("FullRead did not read unchanged mail")
	}
	o.FullRead = false
	old := outlookOWAFingerprint
	outlookOWAFingerprint = func(p outlookowa.Profile) (string, time.Time, error) {
		fp, fresh, err := old(p)
		return "bumped-" + fp, fresh, err
	}
	t.Cleanup(func() { outlookOWAFingerprint = old })
	if r, _ = run(t, o); sourceKeyed(t, r, "outlook|Default|mail").Status != StatusOK {
		t.Fatal("version fingerprint change did not read")
	}
	if len(snapshotDirs(t, tmp)) != 0 {
		t.Fatal("snapshot leaked")
	}
}

func TestSyncOWAWindowsDefaultIsHermetic(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows default routing")
	}
	isolateTmp(t)
	local := t.TempDir()
	t.Setenv("LOCALAPPDATA", local)
	root := filepath.Join(local, "Microsoft", "Olk", "EBWebView")
	copyTree(t, filepath.Join("..", "..", "testdata", "outlook-owa-fixture", "mail"), filepath.Join(root, "Default"))
	o := implicitOpts(newDB(t), "")
	o.Root = t.TempDir()
	r, _ := run(t, o)
	if len(r.Sources) != 1 || sourceKeyed(t, r, "outlook|Default|mail").Counts.Mail.Added != 5 {
		t.Fatalf("implicit no-Teams routing: %+v", r.Sources)
	}
	t.Setenv("LOCALAPPDATA", t.TempDir())
	o.Root, o.DBPath = fixtureRoot, newDB(t)
	if r, _ := run(t, o); r.Status != StatusOK {
		t.Fatal("absent implicit Outlook failed Teams")
	}
	o.OutlookImplicit = false
	if _, _, err := Run(context.Background(), o); err == nil {
		t.Fatal("explicit missing default Outlook succeeded")
	}
}

func TestSyncOWAOffAndAccountFilter(t *testing.T) {
	isolateTmp(t)
	root := owaRoot(t, "mail")
	for _, filtered := range []bool{false, true} {
		o := outlookOpts(newDB(t), root)
		if filtered {
			o.Account = &acctA
		} else {
			o.OutlookEnabled = false
		}
		r, _ := run(t, o)
		for _, s := range r.Sources {
			if strings.HasPrefix(s.Source, "outlook") {
				t.Fatal("suppressed Outlook ran")
			}
		}
		if count(t, o.DBPath, `select count(*) from mail_messages`) != 0 {
			t.Fatal("suppressed source wrote mail")
		}
	}
}

func TestSyncOWAFailureAndInterval(t *testing.T) {
	isolateTmp(t)
	for _, failure := range []string{"copy", "map", "commit"} {
		t.Run(failure, func(t *testing.T) {
			root, db := owaRoot(t, "mail"), newDB(t)
			o := outlookOpts(db, root)
			run(t, o)
			o.FullRead, o.OutlookMinReadInterval = true, 0
			now := time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC)
			oldNow, oldCopy := outlookNow, outlookOWASnapshot
			outlookNow = func() time.Time { return now }
			t.Cleanup(func() { outlookNow, outlookOWASnapshot = oldNow, oldCopy })
			switch failure {
			case "copy":
				outlookOWASnapshot = func(context.Context, teamsdesktop.Source) (string, func(), error) {
					return "", func() {}, errs.SnapshotInconsistent("synthetic-private-detail")
				}
			case "map":
				// Replace only our temp fixture, never the committed fixture.
				p := owaProfile(root)
				if err := os.Remove(filepath.Join(p.Source.LevelDBDir, "000003.log")); err != nil {
					t.Fatal(err)
				}
				copyTree(t, filepath.Join("..", "..", "testdata", "outlook-owa-fixture", "multiple"), p.Dir)
			case "commit":
				if _, err := openRaw(t, db).Exec(`create trigger owa_fail before insert on mail_source_ids begin select raise(abort,'synthetic commit refusal'); end`); err != nil {
					t.Fatal(err)
				}
				if _, err := openRaw(t, db).Exec(`delete from mail_source_ids`); err != nil {
					t.Fatal(err)
				}
			}
			r, _, err := Run(context.Background(), o)
			m := sourceKeyed(t, r, "outlook|Default|mail")
			if err == nil || m.Status != StatusFailed || m.Error == nil || strings.Contains(m.Error.Message, "private") || count(t, db, `select count(*) from mail_messages`) != 5 {
				t.Fatalf("failed mail erased archive or leaked detail: %+v %v", m, err)
			}
			if count(t, db, `select count(*) from meta where key='outlook_mail_read:outlook/Default'`) != 0 || count(t, db, `select count(*) from meta where key='outlook_mail_failure:outlook/Default'`) != 1 {
				t.Fatal("failed read marker persisted")
			}
			now = now.Add(time.Minute)
			r, _, err = Run(context.Background(), o)
			if next := sourceKeyed(t, r, "outlook|Default|mail"); err == nil || next.Error.Code != m.Error.Code || next.Status != StatusFailed {
				t.Fatalf("interval hid failed read: %+v %v", next, err)
			}
			outlookOWASnapshot = oldCopy
			switch failure {
			case "map":
				copyTree(t, filepath.Join("..", "..", "testdata", "outlook-owa-fixture", "mail"), owaProfile(root).Dir)
			case "commit":
				if _, err := openRaw(t, db).Exec(`drop trigger owa_fail`); err != nil {
					t.Fatal(err)
				}
			}
			now = now.Add(5 * time.Minute)
			o.FullRead = false
			r, _ = run(t, o)
			if sourceKeyed(t, r, "outlook|Default|mail").Status != StatusOK {
				t.Fatal("failure made next eligible read skip")
			}
			now = now.Add(time.Minute)
			r, _ = run(t, o)
			if m := sourceKeyed(t, r, "outlook|Default|mail"); m.Status != StatusSkippedInterval || m.NextReadAfter == nil || !m.NextReadAfter.Equal(now.Add(4*time.Minute)) {
				t.Fatalf("success interval: %+v", m)
			}
		})
	}
}

func TestSyncOWACancellationClosesAndCleansCopy(t *testing.T) {
	tmp := isolateTmp(t)
	db, root := newDB(t), owaRoot(t, "mail")
	ctx, cancel := context.WithCancel(context.Background())
	old := outlookOWASnapshot
	outlookOWASnapshot = func(ctx context.Context, s teamsdesktop.Source) (string, func(), error) {
		dir, cleanup, err := old(ctx, s)
		cancel()
		return dir, cleanup, err
	}
	t.Cleanup(func() { outlookOWASnapshot = old })
	o := outlookOpts(db, root)
	o.Root = t.TempDir()
	_, _, err := Run(ctx, o)
	if !errors.Is(err, context.Canceled) || count(t, db, `select count(*) from mail_messages`) != 0 || len(snapshotDirs(t, tmp)) != 0 {
		t.Fatalf("cancellation: %v", err)
	}
}

func TestSyncOWAChangedCopyImportsWithoutAbsenceOrSkip(t *testing.T) {
	tmp := isolateTmp(t)
	db, root := newDB(t), owaRoot(t, "mail")
	p := owaProfile(root)
	ctx := context.Background()
	origin, err := outlookowa.Open(ctx, p.Source.LevelDBDir, p.Source.BlobDir)
	if err != nil {
		t.Fatal(err)
	}
	res, err := outlookowa.Collect(ctx, origin, "outlook/Default", false)
	if closeErr := origin.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatal(err)
	}
	extra := res.Messages[0]
	for _, m := range res.Messages {
		if m.ID == "Message/Normal==" {
			extra = m
		}
	}
	if extra.Value.Received.IsZero() {
		t.Fatal("absence scenario requires a received time inside known coverage")
	}
	extra.ID, extra.Value.MessageID = "Synthetic/Extra==", "<extra@example.test>"
	res.Messages = append(res.Messages, extra)
	res.SeenIDs = append(res.SeenIDs, extra.ID)
	st, err := store.Open(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	fp, fresh, err := outlookowa.Fingerprint(p)
	if err != nil || fp == "" {
		t.Fatal(err)
	}
	if _, err := st.CommitOWAMail(ctx, store.OWAMailBatch{Account: "outlook/Default", ReadAt: time.Now().UTC(), FreshAt: fresh.Add(-time.Hour), Result: res, Trusted: true}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	old := outlookOWASnapshot
	outlookOWASnapshot = func(ctx context.Context, s teamsdesktop.Source) (string, func(), error) {
		dir, cleanup, err := old(ctx, s)
		if err == nil {
			at := fresh.Add(time.Hour)
			err = os.Chtimes(filepath.Join(s.LevelDBDir, "CURRENT"), at, at)
		}
		return dir, cleanup, err
	}
	t.Cleanup(func() { outlookOWASnapshot = old })
	o := outlookOpts(db, root)
	r, _ := run(t, o)
	m := sourceKeyed(t, r, "outlook|Default|mail")
	if m.Status != StatusOmissions || m.Omissions["outlook_owa_source_changed"] != 1 || count(t, db, `select count(*) from mail_absent`) != 0 || count(t, db, `select count(*) from mail_messages`) != 6 {
		t.Fatalf("unstable snapshot inferred absence: %+v", m)
	}
	outlookOWASnapshot = old
	r, _ = run(t, o)
	if sourceKeyed(t, r, "outlook|Default|mail").Status != StatusOK || count(t, db, `select count(*) from mail_absent`) != 1 {
		t.Fatal("uncertain snapshot was skipped or stable later write did not observe absence")
	}
	o.FullRead = true
	run(t, o)
	if count(t, db, `select count(*) from mail_messages where gone_at is not null`) != 0 {
		t.Fatal("same freshness confirmed second absence")
	}
	if err := os.Chtimes(filepath.Join(p.Source.LevelDBDir, "CURRENT"), fresh, fresh); err != nil {
		t.Fatal(err)
	}
	run(t, o)
	if count(t, db, `select count(*) from mail_messages where gone_at is not null`) != 0 {
		t.Fatal("backward source freshness confirmed absence")
	}
	later := fresh.Add(2 * time.Hour)
	if err := os.Chtimes(filepath.Join(p.Source.LevelDBDir, "CURRENT"), later, later); err != nil {
		t.Fatal(err)
	}
	run(t, o)
	if count(t, db, `select count(*) from mail_messages where gone_at is not null`) != 1 {
		t.Fatal("later distinct trusted source write did not confirm absence")
	}
	if len(snapshotDirs(t, tmp)) != 0 {
		t.Fatal("copy leaked")
	}
}

func TestSyncOWAImplicitAndIndependentProfiles(t *testing.T) {
	isolateTmp(t)
	root := owaRoot(t, "mail")
	copyTree(t, filepath.Join("..", "..", "testdata", "outlook-owa-fixture", "multiple"), filepath.Join(root, "Refused"))
	o := implicitOpts(newDB(t), root)
	r, _ := run(t, o)
	m := sourceKeyed(t, r, "outlook|Refused|mail")
	if m.Status != StatusUnavailable || m.Error.Code != "outlook_mail_layout_unsupported" || sourceKeyed(t, r, "outlook|Default|mail").Counts.Mail.Added != 5 {
		t.Fatalf("implicit refusal did not isolate profiles: %+v", r.Sources)
	}
	if count(t, o.DBPath, `select count(*) from mail_messages where account='outlook/Refused'`) != 0 {
		t.Fatal("multiple mailbox profile wrote rows")
	}
	bad := filepath.Join(root, "Unreadable", "IndexedDB", "https_outlook.office.com_0.indexeddb.leveldb")
	if err := os.MkdirAll(filepath.Dir(bad), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bad, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	r, _ = run(t, o)
	if m := sourceKeyed(t, r, "outlook|Unreadable|mail"); m.Status != StatusUnavailable || m.Error.Code != CodeOutlookProfileUnreadable {
		t.Fatalf("invalid source: %+v", m)
	}
}

func TestSyncOWADatabaseFailuresCannotVouchForRead(t *testing.T) {
	isolateTmp(t)
	for _, tc := range []struct {
		name, ddl       string
		skip, unchanged bool
	}{
		{"prior", `update sync_runs set omissions_json='{' where source='outlook|Default|mail'`, false, false},
		{"mail-state", `insert into meta(key,value) values('outlook_mail_failure:outlook/Default','{')`, false, false},
		{"checked", `create trigger owa_fail before insert on meta when new.key='outlook_checked:Default' begin select raise(abort,'synthetic'); end`, false, false},
		{"attempt", `create trigger owa_fail before insert on meta when new.key='outlook_last_attempt:Default' begin select raise(abort,'synthetic'); end`, false, false},
		{"clear-read", `create trigger owa_fail before delete on meta when old.key='outlook_mail_read:outlook/Default' begin select raise(abort,'synthetic'); end`, false, false},
		{"read-marker", `create trigger owa_fail before insert on meta when new.key='outlook_mail_read:outlook/Default' begin select raise(abort,'synthetic'); end`, false, false},
		{"run-row", `create trigger owa_fail before insert on sync_runs when new.source='outlook|Default|mail' begin select raise(abort,'synthetic'); end`, false, false},
		{"unchanged-run", `create trigger owa_fail before insert on sync_runs when new.source='outlook|Default|mail' and new.status='unchanged' begin select raise(abort,'synthetic'); end`, false, true},
		{"skip-marker", `create trigger owa_fail before insert on meta when new.key='outlook_skipped:Default' begin select raise(abort,'synthetic'); end`, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, db := owaRoot(t, "mail"), newDB(t)
			o := outlookOpts(db, root)
			now := time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC)
			old := outlookNow
			outlookNow = func() time.Time { return now }
			t.Cleanup(func() { outlookNow = old })
			run(t, o)
			if _, err := openRaw(t, db).Exec(tc.ddl); err != nil {
				t.Fatal(err)
			}
			o.FullRead = !tc.unchanged
			if tc.skip {
				o.OutlookMinReadInterval = 0
				now = now.Add(time.Minute)
			}
			var progress bytes.Buffer
			o.Progress = &progress
			r, _, err := Run(context.Background(), o)
			m := sourceKeyed(t, r, "outlook|Default|mail")
			if err == nil || m.Status != StatusFailed || m.Error.Code != errs.CodeDBError || count(t, db, `select count(*) from mail_messages`) != 5 {
				t.Fatalf("archive failure: %+v %v", m, err)
			}
			if tc.name != "clear-read" && count(t, db, `select count(*) from meta where key='outlook_mail_read:outlook/Default'`) != 0 {
				t.Fatal("archive failure left read marker")
			}
			if tc.name == "clear-read" && !strings.Contains(progress.String(), "could not record the mail failure") {
				t.Fatal("refusal of both marker writes silently hid the persistence limitation")
			}
		})
	}
}

func TestSyncOWAPanicIsFixedInternalFailure(t *testing.T) {
	isolateTmp(t)
	old := outlookOWASnapshot
	outlookOWASnapshot = func(context.Context, teamsdesktop.Source) (string, func(), error) {
		panic("synthetic-private-do-not-export")
	}
	t.Cleanup(func() { outlookOWASnapshot = old })
	o := outlookOpts(newDB(t), owaRoot(t, "mail"))
	r, _, err := Run(context.Background(), o)
	m := sourceKeyed(t, r, "outlook|Default|mail")
	if err == nil || m.Error.Code != errs.CodeInternal || strings.Contains(m.Error.Message, "private") {
		t.Fatalf("panic leaked or lost bug classification: %+v %v", m, err)
	}
}

func TestSyncOWACancelledReceiptCannotLeaveReadMarker(t *testing.T) {
	isolateTmp(t)
	o := outlookOpts(newDB(t), owaRoot(t, "mail"))
	o.Root = t.TempDir()
	run(t, o)
	o.FullRead = true
	ctx, cancel := context.WithCancel(context.Background())
	old := outlookOWARecordRun
	outlookOWARecordRun = func(ctx context.Context, st *store.Store, receipt store.Run) error {
		cancel()
		return old(ctx, st, receipt)
	}
	t.Cleanup(func() { outlookOWARecordRun = old })
	_, _, err := Run(ctx, o)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("receipt cancellation: %v", err)
	}
	if count(t, o.DBPath, `select count(*) from meta where key='outlook_mail_read:outlook/Default'`) != 0 {
		t.Fatal("cancelled source receipt left successful read marker")
	}
	outlookOWARecordRun = old
	o.FullRead = false
	if r, _ := run(t, o); sourceKeyed(t, r, "outlook|Default|mail").Status != StatusOK {
		t.Fatal("cancelled receipt made next eligible read skip")
	}
}

func TestSyncExplicitHxStoreKeepsItsOwnSourceSafety(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("native Windows junction")
	}
	isolateTmp(t)
	target := outlookRoot(t, "HxStore.hxd")
	link := filepath.Join(t.TempDir(), "HxRoot")
	output, err := osexec.Command("cmd.exe", "/c", "mklink", "/J", link, target).CombinedOutput() //nolint:gosec // G204: fixed junction command; operands are generated test-owned HxStore fixture directories.
	if err != nil {
		t.Fatalf("controlled HxStore alias: %v %s", err, output)
	}
	t.Cleanup(func() {
		if err := os.Remove(link); err != nil {
			t.Error(err)
		}
	})
	r, _ := run(t, outlookOpts(newDB(t), link))
	if sourceKeyed(t, r, "outlook|Main").Status != StatusOK {
		t.Fatal("OWA safety changed the explicit HxStore route")
	}
}

func TestSyncOWALossesSurviveUnchangedRead(t *testing.T) {
	isolateTmp(t)
	root, db := owaRoot(t, "mail"), newDB(t)
	p := owaProfile(root)
	if err := os.Remove(filepath.Join(p.Source.BlobDir, "1", "00", "2")); err != nil {
		t.Fatal(err)
	}
	o := outlookOpts(db, root)
	for i := 0; i < 2; i++ {
		r, _ := run(t, o)
		m := sourceKeyed(t, r, "outlook|Default|mail")
		if m.Status != StatusOmissions || m.Omissions["outlook_owa_reader_blob_missing"] != 1 || count(t, db, `select count(*) from mail_messages`) != 5 {
			t.Fatalf("sparse blob loss/read: %+v", m)
		}
		if i == 1 && m.Counts != nil {
			t.Fatal("unchanged source was copied again")
		}
	}
	if count(t, db, `select count(*) from mail_absent`) != 0 {
		t.Fatal("lossy read inferred absence")
	}
}

func TestSyncOWASourceDisappearingAfterCopyIsRetryable(t *testing.T) {
	tmp := isolateTmp(t)
	root, db := owaRoot(t, "mail"), newDB(t)
	o := outlookOpts(db, root)
	old := outlookOWASnapshot
	outlookOWASnapshot = func(ctx context.Context, s teamsdesktop.Source) (string, func(), error) {
		dir, cleanup, err := old(ctx, s)
		if err != nil {
			return dir, cleanup, err
		}
		moved := s.LevelDBDir + ".synthetic-moved"
		if err := os.Rename(s.LevelDBDir, moved); err != nil {
			cleanup()
			return "", func() {}, err
		}
		return dir, func() {
			cleanup()
			if err := os.Rename(moved, s.LevelDBDir); err != nil {
				t.Error(err)
			}
		}, nil
	}
	t.Cleanup(func() { outlookOWASnapshot = old })
	r, _, err := Run(context.Background(), o)
	m := sourceKeyed(t, r, "outlook|Default|mail")
	if err == nil || m.Error.Code != CodeOutlookProfileUnreadable || count(t, db, `select count(*) from mail_messages`) != 0 || len(snapshotDirs(t, tmp)) != 0 {
		t.Fatalf("source disappeared after copy: %+v %v", m, err)
	}
	outlookOWASnapshot = old
	r, _ = run(t, o)
	if sourceKeyed(t, r, "outlook|Default|mail").Status != StatusOK {
		t.Fatal("disappeared source was marked read")
	}
}

func TestDefaultOutlookRootDispatchesUnsupportedProviderAndErrors(t *testing.T) {
	local := t.TempDir()
	t.Setenv("LOCALAPPDATA", filepath.Join(local, "AppData", "Local"))
	t.Setenv("HOME", local)
	t.Setenv("USERPROFILE", local)
	old := outlookOWADefaultRoot
	t.Cleanup(func() { outlookOWADefaultRoot = old })
	outlookOWADefaultRoot = func() (string, error) { return "", outlookdesktop.ErrNotSupported }
	root, err := defaultOutlookRoot()
	if runtime.GOOS == "darwin" {
		if err != nil || root == "" {
			t.Fatalf("legacy provider did not supply its default: %q %v", root, err)
		}
	} else if root != "" || !errors.Is(err, outlookdesktop.ErrNotSupported) {
		t.Fatalf("unsupported OWA provider did not delegate to real legacy provider: %q %v", root, err)
	}
	denied := errors.New("synthetic provider root unavailable")
	outlookOWADefaultRoot = func() (string, error) { return "", denied }
	if root, err := defaultOutlookRoot(); root != "" || !errors.Is(err, denied) {
		t.Fatalf("provider failure was replaced by another default: %q %v", root, err)
	}
}

func tableOWARoot(t *testing.T) string {
	t.Helper()
	root := owaRoot(t, "mail")
	p := owaProfile(root)
	src, err := leveldb.Load(p.Source.LevelDBDir)
	if err != nil {
		t.Fatal(err)
	}
	values := map[string][]byte{}
	if err := src.Scan(nil, func(k, v []byte) error {
		values[string(k)] = append([]byte(nil), v...)
		return nil
	}); err != nil {
		_ = src.Close()
		t.Fatal(err)
	}
	if err := src.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(p.Source.LevelDBDir, p.Source.LevelDBDir+".original-log-copy"); err != nil {
		t.Fatal(err)
	}
	w, err := gl.OpenFile(p.Source.LevelDBDir, &opt.Options{Compression: opt.SnappyCompression, BlockSize: 2048})
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range values {
		if err := w.Put([]byte(k), v, nil); err != nil {
			_ = w.Close()
			t.Fatal(err)
		}
	}
	if err := w.CompactRange(util.Range{}); err != nil {
		_ = w.Close()
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return root
}

func owaHeaderStore(t *testing.T, o *indexeddb.Origin) (int64, int64) {
	t.Helper()
	dbs, err := o.Databases()
	if err != nil {
		t.Fatal(err)
	}
	for _, db := range dbs {
		if strings.HasPrefix(db.Name, "owa-offline-data-") {
			for _, s := range db.Stores {
				if s.Name == "messages" {
					return db.ID, s.ID
				}
			}
		}
	}
	t.Fatal("synthetic header metadata missing")
	return 0, 0
}

func TestSyncOWALazyOriginClosesBeforeCleanupIncludingCloseFailure(t *testing.T) {
	for _, failClose := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "close-error"}[failClose], func(t *testing.T) {
			tmp := isolateTmp(t)
			root, db := tableOWARoot(t), newDB(t)
			o := outlookOpts(db, root)
			run(t, o)
			o.FullRead = true
			oldOpen, oldClose, oldCopy := outlookOWAOpen, outlookOWAClose, outlookOWASnapshot
			t.Cleanup(func() { outlookOWAOpen, outlookOWAClose, outlookOWASnapshot = oldOpen, oldClose, oldCopy })
			var origin *indexeddb.Origin
			var dbID, storeID int64
			outlookOWAOpen = func(ctx context.Context, dir, blob string) (*indexeddb.Origin, error) {
				result, err := oldOpen(ctx, dir, blob)
				if err == nil {
					origin = result
					dbID, storeID = owaHeaderStore(t, result)
				}
				return result, err
			}
			outlookOWAClose = func(origin *indexeddb.Origin) error {
				large := false
				err := origin.Records(dbID, storeID, func(r indexeddb.Record) error {
					large = large || len(r.Raw) >= leveldb.LazyMin()
					return nil
				})
				if err != nil || !large || origin.Stats().Tables == 0 {
					t.Errorf("no real lazy table read before close: %v", err)
				}
				if err := oldClose(origin); err != nil {
					return err
				}
				if failClose {
					return errors.New("synthetic private close failure")
				}
				return nil
			}
			outlookOWASnapshot = func(ctx context.Context, source teamsdesktop.Source) (string, func(), error) {
				dir, cleanup, err := oldCopy(ctx, source)
				return dir, func() {
					if origin == nil {
						t.Error("snapshot cleanup preceded origin opening")
					} else if err := origin.Records(dbID, storeID, func(indexeddb.Record) error { return nil }); !errors.Is(err, leveldb.ErrClosed) {
						t.Errorf("snapshot cleanup preceded actual lazy-origin closure: %v", err)
					}
					cleanup()
					if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
						t.Errorf("closed snapshot cleanup failed: %v", err)
					}
				}, err
			}
			r, _, err := Run(context.Background(), o)
			m := sourceKeyed(t, r, "outlook|Default|mail")
			if failClose {
				if err == nil || m.Status != StatusFailed || m.Error.Code != CodeOutlookProfileUnreadable || strings.Contains(m.Error.Message, "private") {
					t.Fatalf("failed close looked successful or leaked: %+v %v", m, err)
				}
				if count(t, db, `select count(*) from meta where key='outlook_mail_read:outlook/Default'`) != 0 || count(t, db, `select count(*) from mail_absent`) != 0 {
					t.Fatal("failed close vouched for read/absence")
				}
			} else if err != nil || m.Status != StatusOK {
				t.Fatalf("real table read failed: %+v %v", m, err)
			}
			if count(t, db, `select count(*) from mail_messages`) != 5 || len(snapshotDirs(t, tmp)) != 0 {
				t.Fatal("close path damaged archive or leaked private snapshot")
			}
			outlookOWAOpen, outlookOWAClose, outlookOWASnapshot = oldOpen, oldClose, oldCopy
			o.FullRead = false
			next, _ := run(t, o)
			want := StatusUnchanged
			if failClose {
				want = StatusOK
			}
			if sourceKeyed(t, next, "outlook|Default|mail").Status != want {
				t.Fatal("close outcome produced the wrong next eligible read")
			}
		})
	}
}

func TestSyncOWAFirstFingerprintAndMailHoldsFaultsRetry(t *testing.T) {
	for _, where := range []string{"first-fingerprint", "mail-holds"} {
		t.Run(where, func(t *testing.T) {
			tmp := isolateTmp(t)
			root, db := owaRoot(t, "mail"), newDB(t)
			o := outlookOpts(db, root)
			run(t, o)
			o.FullRead = true
			fp, copy := outlookOWAFingerprint, outlookOWASnapshot
			t.Cleanup(func() { outlookOWAFingerprint, outlookOWASnapshot = fp, copy })
			var restore func()
			if where == "first-fingerprint" {
				outlookOWAFingerprint = func(p outlookowa.Profile) (string, time.Time, error) {
					moved := p.Source.LevelDBDir + ".synthetic-moved"
					if err := os.Rename(p.Source.LevelDBDir, moved); err != nil {
						return "", time.Time{}, err
					}
					restore = func() {
						if err := os.Rename(moved, p.Source.LevelDBDir); err != nil {
							t.Error(err)
						}
					}
					return fp(p)
				}
			} else {
				raw := openRaw(t, db)
				outlookOWASnapshot = func(ctx context.Context, source teamsdesktop.Source) (string, func(), error) {
					dir, cleanup, err := copy(ctx, source)
					if err != nil {
						return dir, cleanup, err
					}
					if _, err := raw.Exec(`alter table mail_messages rename column account to synthetic_broken_account`); err != nil {
						cleanup()
						return "", func() {}, err
					}
					return dir, func() {
						if _, err := raw.Exec(`alter table mail_messages rename column synthetic_broken_account to account`); err != nil {
							t.Error(err)
						}
						cleanup()
					}, nil
				}
			}
			r, _, err := Run(context.Background(), o)
			if restore != nil {
				restore()
			}
			m := sourceKeyed(t, r, "outlook|Default|mail")
			want := CodeOutlookProfileUnreadable
			if where == "mail-holds" {
				want = errs.CodeDBError
			}
			if err == nil || m.Status != StatusFailed || m.Error.Code != want || count(t, db, `select count(*) from mail_messages`) != 5 {
				t.Fatalf("source/database boundary failed unsafely: %+v %v", m, err)
			}
			if count(t, db, `select count(*) from meta where key='outlook_mail_read:outlook/Default'`) != 0 || count(t, db, `select count(*) from mail_absent`) != 0 || len(snapshotDirs(t, tmp)) != 0 {
				t.Fatal("failed boundary vouched for read/absence or leaked a snapshot")
			}
			outlookOWAFingerprint, outlookOWASnapshot = fp, copy
			o.FullRead = false
			if next, _ := run(t, o); sourceKeyed(t, next, "outlook|Default|mail").Status != StatusOK {
				t.Fatal("failed boundary made next eligible read skip")
			}
		})
	}
}

func TestSyncOWACancellationBeforeSubsequentProfileDoesNotReadIt(t *testing.T) {
	tmp := isolateTmp(t)
	root, db := owaRoot(t, "mail"), newDB(t)
	copyTree(t, filepath.Join(root, "Default"), filepath.Join(root, "Second"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st, err := store.Open(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	old := outlookOWASnapshot
	outlookOWASnapshot = func(ctx context.Context, source teamsdesktop.Source) (string, func(), error) {
		dir, cleanup, err := old(ctx, source)
		cancel()
		return dir, cleanup, err
	}
	t.Cleanup(func() { outlookOWASnapshot = old })
	r := runner{o: outlookOpts(db, root), st: st}
	out := r.outlookOWASources(ctx, &Report{})
	if len(out) != 2 || !errors.Is(out[0].err, context.Canceled) || !errors.Is(out[1].err, context.Canceled) || out[1].key != "outlook|Second|mail" {
		t.Fatalf("cancellation did not stop the subsequent source: %+v", out)
	}
	if count(t, db, `select count(*) from meta where key='outlook_last_attempt:Second' or key like 'outlook_mail_read:%'`) != 0 || count(t, db, `select count(*) from mail_messages`) != 0 || len(snapshotDirs(t, tmp)) != 0 {
		t.Fatal("subsequent profile was attempted/read or cancellation leaked resources")
	}
}

func TestOWADefaultResolutionErrorAndEmptyRootAreExplicit(t *testing.T) {
	isolateTmp(t)
	old := outlookDefaultRoot
	t.Cleanup(func() { outlookDefaultRoot = old })
	st, err := store.Open(context.Background(), newDB(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	r := runner{st: st}
	outlookDefaultRoot = func() (string, error) { return "", errors.New("synthetic private provider error") }
	out := r.outlookOWASources(context.Background(), &Report{})
	if len(out) != 1 || out[0].err == nil || codedOf(out[0].err).Code != CodeOutlookProfileUnreadable || strings.Contains(out[0].err.Error(), "private") {
		t.Fatalf("default resolver failure hidden or leaked: %+v", out)
	}
	root := t.TempDir()
	outlookDefaultRoot = func() (string, error) { return root, nil }
	out = r.outlookOWASources(context.Background(), &Report{})
	if len(out) != 1 || out[0].key != "outlook" || codedOf(out[0].err).Code != CodeNoOutlookProfiles {
		t.Fatalf("explicit empty default source succeeded: %+v", out)
	}
}

func TestSyncOWAFormatProbeRefusalIsNotAnEmptySuccess(t *testing.T) {
	isolateTmp(t)
	root, db := owaRoot(t, "mail"), newDB(t)
	o := outlookOpts(db, root)
	run(t, o)
	old := outlookOWADiscover
	outlookOWADiscover = func(path string) ([]outlookowa.Profile, []outlookowa.SkippedProfile, error) {
		profiles, skipped, err := old(path)
		if err == nil {
			return nil, nil, os.ErrPermission
		}
		return profiles, skipped, err
	}
	t.Cleanup(func() { outlookOWADiscover = old })
	r, _, err := Run(context.Background(), o)
	if err == nil || sourceKeyed(t, r, "outlook").Error.Code != CodeOutlookProfileUnreadable {
		t.Fatalf("format probe error became a silent HxStore/empty fallback: %+v %v", r.Sources, err)
	}
	probe := runner{o: o}
	if probe.outlookPresent() {
		t.Fatal("failed format probe claimed readable source availability")
	}
	if count(t, db, `select count(*) from mail_messages`) != 5 || count(t, db, `select count(*) from mail_absent`) != 0 {
		t.Fatal("format-probe failure changed cached mail or inferred absence")
	}
}

func TestSyncOWAActiveLazyCancellationKeepsSnapshotUntilClose(t *testing.T) {
	tmp := isolateTmp(t)
	root, db := tableOWARoot(t), newDB(t)
	o := outlookOpts(db, root)
	run(t, o)
	o.FullRead = true
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	oldOpen, oldCopy := outlookOWAOpen, outlookOWASnapshot
	t.Cleanup(func() { outlookOWAOpen, outlookOWASnapshot = oldOpen, oldCopy })
	var origin *indexeddb.Origin
	var snapshotContext context.Context
	var snapshot string
	var dbID, storeID int64
	outlookOWASnapshot = func(copyCtx context.Context, source teamsdesktop.Source) (string, func(), error) {
		dir, cleanup, err := oldCopy(copyCtx, source)
		snapshotContext, snapshot = copyCtx, dir
		return dir, func() {
			if origin != nil {
				if err := origin.Records(dbID, storeID, func(indexeddb.Record) error { return nil }); !errors.Is(err, leveldb.ErrClosed) {
					t.Errorf("returned cleanup preceded lazy Origin.Close: %v", err)
				}
			}
			cleanup()
			if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("private snapshot removal failed after close: %v", err)
			}
		}, err
	}
	outlookOWAOpen = func(readCtx context.Context, dir, blob string) (*indexeddb.Origin, error) {
		result, err := oldOpen(readCtx, dir, blob)
		if err != nil {
			return result, err
		}
		origin = result
		dbID, storeID = owaHeaderStore(t, result)
		large := false
		if err := result.Records(dbID, storeID, func(r indexeddb.Record) error {
			large = large || len(r.Raw) >= leveldb.LazyMin()
			return nil
		}); err != nil || !large || result.Stats().Tables == 0 {
			_ = result.Close()
			t.Fatalf("cancellation scenario did not open an actual lazy table: %v", err)
		}
		cancel()
		// If production still gives Snapshot the consumer context, observe the real borrowed
		// automatic-removal callback while this actual lazy handle remains open.
		if snapshotContext.Err() != nil {
			until := time.Now().Add(time.Second)
			for time.Now().Before(until) {
				if _, err := os.Stat(filepath.Join(snapshot, "leveldb", "CURRENT")); errors.Is(err, os.ErrNotExist) {
					break
				}
				time.Sleep(time.Millisecond)
			}
			t.Error("consumer cancellation reached snapshot automatic-removal context before Origin.Close")
		}
		if _, err := os.Stat(filepath.Join(snapshot, "leveldb", "CURRENT")); err != nil {
			t.Errorf("automatic cancellation removed snapshot data before Origin.Close: %v", err)
		}
		return result, nil
	}
	_, _, err := Run(ctx, o)
	if !errors.Is(err, context.Canceled) || count(t, db, `select count(*) from mail_messages`) != 5 || count(t, db, `select count(*) from mail_absent`) != 0 || count(t, db, `select count(*) from meta where key='outlook_mail_read:outlook/Default'`) != 0 {
		t.Fatalf("active-reader cancellation changed archive, absence or eligibility: %v", err)
	}
	if len(snapshotDirs(t, tmp)) != 0 {
		t.Fatal("active-reader cancellation leaked private snapshot")
	}
	outlookOWAOpen, outlookOWASnapshot = oldOpen, oldCopy
	o.FullRead = false
	if r, _ := run(t, o); sourceKeyed(t, r, "outlook|Default|mail").Status != StatusOK {
		t.Fatal("canceled active reader made next eligible read skip")
	}
}

func TestSyncOWAReadableProfileSurvivesLegacyClassificationFailure(t *testing.T) {
	isolateTmp(t)
	root, db := owaRoot(t, "mail"), newDB(t)
	denied := filepath.Join(root, "Denied")
	if err := os.MkdirAll(filepath.Join(denied, "IndexedDB"), 0o700); err != nil {
		t.Fatal(err)
	}
	oldHX, oldOWA := outlookDiscover, outlookOWADiscover
	t.Cleanup(func() { outlookDiscover, outlookOWADiscover = oldHX, oldOWA })
	outlookDiscover = func(path string) ([]outlookdesktop.Profile, []string, []outlookdesktop.SkippedProfile, error) {
		profiles, classic, _, err := oldHX(path)
		if err != nil {
			return profiles, classic, nil, err
		}
		return profiles, classic, []outlookdesktop.SkippedProfile{{Name: "Denied", Dir: denied, Reason: "no_full_disk_access", Err: os.ErrPermission}}, errs.NoFullDiskAccess(denied, os.ErrPermission)
	}
	outlookOWADiscover = func(path string) ([]outlookowa.Profile, []outlookowa.SkippedProfile, error) {
		profiles, _, err := oldOWA(path)
		return profiles, []outlookowa.SkippedProfile{{Name: "Denied", Dir: denied, Reason: "unreadable", Err: os.ErrPermission}}, err
	}
	o := outlookOpts(db, root)
	r, _, err := Run(context.Background(), o)
	if err == nil || r.Status != StatusPartial {
		t.Fatalf("inaccessible profile was hidden: %+v %v", r.Sources, err)
	}
	healthy := sourceKeyed(t, r, "outlook|Default|mail")
	failed := sourceKeyed(t, r, "outlook|Denied|mail")
	if healthy.Status != StatusOK || healthy.Counts.Mail == nil || healthy.Counts.Mail.Added != 5 || failed.Status != StatusFailed || failed.Error.Code != CodeOutlookProfileUnreadable || strings.Contains(failed.Error.Fix, "Full Disk Access") {
		t.Fatalf("legacy uncertainty suppressed healthy OWA or gave legacy guidance: %+v", r.Sources)
	}
	if count(t, db, `select count(*) from mail_messages where account='outlook/Default'`) != 5 || count(t, db, `select count(*) from meta where key='outlook_mail_read:outlook/Denied'`) != 0 {
		t.Fatal("profile failures were not isolated from healthy OWA progress")
	}
	probe := runner{o: o}
	if !probe.outlookPresent() {
		t.Fatal("legacy uncertainty hid readable OWA availability")
	}
}

func TestOwnedOWASnapshotForwardsCopyDeadlineAndReleasesContext(t *testing.T) {
	isolateTmp(t)
	p := owaProfile(owaRoot(t, "mail"))
	type valueKey struct{}
	parent := context.WithValue(context.Background(), valueKey{}, "synthetic scope")
	ctx, cancel := context.WithTimeout(parent, 20*time.Millisecond)
	defer cancel()
	old := outlookOWASnapshot
	var copyContext context.Context
	outlookOWASnapshot = func(copyCtx context.Context, source teamsdesktop.Source) (string, func(), error) {
		copyContext = copyCtx
		if copyCtx.Value(valueKey{}) != "synthetic scope" {
			t.Error("copy lost caller values")
		}
		select {
		case <-copyCtx.Done():
		case <-time.After(time.Second):
			t.Fatal("caller deadline was not forwarded to cancellable copying")
		}
		return old(copyCtx, source)
	}
	t.Cleanup(func() { outlookOWASnapshot = old })
	dir, cleanup, err := ownedOWASnapshot(ctx, p.Source)
	cleanup()
	if dir != "" || !errors.Is(err, context.DeadlineExceeded) || copyContext.Err() == nil {
		t.Fatalf("copy deadline was lost/masked or its context remained live: %q %v", dir, err)
	}
	outlookOWASnapshot = old
	var activeCopyCtx context.Context
	outlookOWASnapshot = func(copyCtx context.Context, source teamsdesktop.Source) (string, func(), error) {
		activeCopyCtx = copyCtx
		return old(copyCtx, source)
	}
	dir, cleanup, err = ownedOWASnapshot(parent, p.Source)
	if err != nil || activeCopyCtx.Err() != nil {
		t.Fatalf("copy handoff failed: %q %v", dir, err)
	}
	cleanup()
	if !errors.Is(activeCopyCtx.Err(), context.Canceled) {
		t.Fatal("cleanup retained the detached copy context")
	}
}

func TestSyncOWAActiveLazyDeadlinePreservesReaderDeadlineAndCleanup(t *testing.T) {
	tmp := isolateTmp(t)
	root, db := tableOWARoot(t), newDB(t)
	st, err := store.Open(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	deadline, _ := ctx.Deadline()
	oldOpen := outlookOWAOpen
	t.Cleanup(func() { outlookOWAOpen = oldOpen })
	outlookOWAOpen = func(readCtx context.Context, dir, blob string) (*indexeddb.Origin, error) {
		o, err := oldOpen(readCtx, dir, blob)
		if err != nil {
			return o, err
		}
		dbID, storeID := owaHeaderStore(t, o)
		large := false
		if err := o.Records(dbID, storeID, func(r indexeddb.Record) error {
			large = large || len(r.Raw) >= leveldb.LazyMin()
			return nil
		}); err != nil || !large {
			_ = o.Close()
			t.Fatalf("no opened lazy table for active-reader deadline: %v", err)
		}
		gotDeadline, ok := readCtx.Deadline()
		if !ok || !gotDeadline.Equal(deadline) {
			t.Error("active reader lost its original deadline")
		}
		<-readCtx.Done()
		if _, err := os.Stat(filepath.Join(dir, "CURRENT")); err != nil {
			t.Errorf("active deadline deleted snapshot before closing reader: %v", err)
		}
		return o, nil
	}
	r := runner{o: outlookOpts(db, root), st: st}
	_, _, err = r.outlookOWAMail(ctx, owaProfile(root))
	if !errors.Is(err, context.DeadlineExceeded) || len(snapshotDirs(t, tmp)) != 0 || count(t, db, `select count(*) from mail_messages`) != 0 || count(t, db, `select count(*) from meta where key='outlook_mail_read:outlook/Default'`) != 0 {
		t.Fatalf("active reader deadline was masked or leaked/applied mail: %v", err)
	}
}

func TestOWAFormatRoutingDistinguishesPositiveLegacyAndRootErrors(t *testing.T) {
	for _, kind := range []string{"hx", "classic", "legacy-error-owa-empty", "both-root-errors"} {
		t.Run(kind, func(t *testing.T) {
			isolateTmp(t)
			root, db := owaRoot(t, "mail"), newDB(t)
			hx, owa := outlookDiscover, outlookOWADiscover
			t.Cleanup(func() { outlookDiscover, outlookOWADiscover = hx, owa })
			o := outlookOpts(db, root)
			switch kind {
			case "hx":
				root = outlookRoot(t, "HxStore.hxd")
				o.OutlookRoot = root
				r, _ := run(t, o)
				if sourceKeyed(t, r, "outlook|Main").Status != StatusOK {
					t.Fatal("positive HxStore root lost legacy route")
				}
			case "classic":
				outlookDiscover = func(string) ([]outlookdesktop.Profile, []string, []outlookdesktop.SkippedProfile, error) {
					return nil, []string{"Classic"}, nil, nil
				}
				r, _, err := Run(context.Background(), o)
				if err == nil || len(r.OutlookClassicOnly) != 1 || sourceKeyed(t, r, "outlook").Error.Code != CodeNoOutlookProfiles {
					t.Fatalf("positive classic source changed route or became an empty success: %+v %v", r, err)
				}
			default:
				outlookDiscover = func(string) ([]outlookdesktop.Profile, []string, []outlookdesktop.SkippedProfile, error) {
					return nil, nil, nil, errs.Internal(errors.New("synthetic legacy root failure"))
				}
				outlookOWADiscover = func(string) ([]outlookowa.Profile, []outlookowa.SkippedProfile, error) {
					if kind == "both-root-errors" {
						return nil, nil, os.ErrPermission
					}
					return nil, nil, nil
				}
				r, _, err := Run(context.Background(), o)
				want := errs.CodeInternal
				if kind == "both-root-errors" {
					want = CodeOutlookProfileUnreadable
				}
				if err == nil || sourceKeyed(t, r, "outlook").Error.Code != want {
					t.Fatalf("unrecognized/error root became an empty successful source: %+v %v", r.Sources, err)
				}
			}
		})
	}
}

func TestSyncOWACancelAtReaderHandoffDoesNotOpenOrigin(t *testing.T) {
	tmp := isolateTmp(t)
	root, db := tableOWARoot(t), newDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	old := outlookOWAPrivateSnapshot
	outlookOWAPrivateSnapshot = func(ctx context.Context, source teamsdesktop.Source) (string, func(), error) {
		dir, cleanup, err := old(ctx, source)
		if err == nil {
			cancel()
		}
		return dir, cleanup, err
	}
	t.Cleanup(func() { outlookOWAPrivateSnapshot = old })
	o := outlookOpts(db, root)
	o.Root = t.TempDir()
	_, _, err := Run(ctx, o)
	if !errors.Is(err, context.Canceled) || count(t, db, `select count(*) from mail_messages`) != 0 || count(t, db, `select count(*) from meta where key='outlook_mail_read:outlook/Default'`) != 0 || len(snapshotDirs(t, tmp)) != 0 {
		t.Fatalf("handoff cancellation applied mail, vouched for a read or leaked snapshot: %v", err)
	}
}

func TestSyncOWAReadableFormatOverridesEachNegativeLegacyProbe(t *testing.T) {
	for _, kind := range []string{"skipped-only", "error-only"} {
		t.Run(kind, func(t *testing.T) {
			isolateTmp(t)
			root, db := owaRoot(t, "mail"), newDB(t)
			old := outlookDiscover
			t.Cleanup(func() { outlookDiscover = old })
			outlookDiscover = func(path string) ([]outlookdesktop.Profile, []string, []outlookdesktop.SkippedProfile, error) {
				if kind == "error-only" {
					return nil, nil, nil, os.ErrPermission
				}
				return nil, nil, []outlookdesktop.SkippedProfile{{Name: "Default", Dir: filepath.Join(path, "Default"), Reason: "no_full_disk_access", Err: os.ErrPermission}}, nil
			}
			o := outlookOpts(db, root)
			o.Root = t.TempDir()
			r, _ := run(t, o)
			if len(r.Sources) != 1 || sourceKeyed(t, r, "outlook|Default|mail").Counts.Mail.Added != 5 {
				t.Fatalf("negative legacy probe overrode positive OWA evidence: %+v", r.Sources)
			}
		})
	}
}
