package syncer

import (
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
	"github.com/ourostack/m365crawl/internal/outlookowa"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/teamsdesktop"
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
			if failure == "map" {
				copyTree(t, filepath.Join("..", "..", "testdata", "outlook-owa-fixture", "mail"), owaProfile(root).Dir)
			} else if failure == "commit" {
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
			r, _, err := Run(context.Background(), o)
			m := sourceKeyed(t, r, "outlook|Default|mail")
			if err == nil || m.Status != StatusFailed || m.Error.Code != errs.CodeDBError || count(t, db, `select count(*) from mail_messages`) != 5 {
				t.Fatalf("archive failure: %+v %v", m, err)
			}
			if tc.name != "clear-read" && count(t, db, `select count(*) from meta where key='outlook_mail_read:outlook/Default'`) != 0 {
				t.Fatal("archive failure left read marker")
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
	output, err := osexec.Command("cmd.exe", "/c", "mklink", "/J", link, target).CombinedOutput()
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
