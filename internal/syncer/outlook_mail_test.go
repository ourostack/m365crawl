package syncer

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/hxstore/hxbuild"
	"github.com/ourostack/m365crawl/internal/outlookdesktop"
	"github.com/ourostack/m365crawl/internal/outlookmail"
)

var mailDay = time.Date(2031, 3, 3, 9, 0, 0, 0, time.UTC)

// mailObjects is a small synthetic mailbox: an inbox with two messages, one with an inline body and
// one whose body is a file under the profile's Files directory.
func mailObjects(datPath string) []*hxbuild.Object {
	hdr := func(key, detail uint32, subject string) *hxbuild.Object {
		return hxbuild.NewMailHeader(hxbuild.MailHeaderSpec{Key: key, Stamp: 1, DetailKey: detail, FolderKey: 101, Received: mailDay, Subject: subject,
			SenderName: "Fixture Sender", SenderAddr: "sender@example.invalid", Unread: 1, Importance: 1})
	}
	det := func(key uint32, id string) *hxbuild.Object {
		return hxbuild.NewMailDetail(hxbuild.MailDetailSpec{Key: key, Stamp: 1, MessageID: id, Class: "IPM.Note", Sent: mailDay})
	}
	return []*hxbuild.Object{
		hxbuild.NewMailFolder(hxbuild.MailFolderSpec{Key: 101, Parent: 1000, Name: "Fixture Inbox", Type: 0x61}),
		hdr(11, 21, "Fixture one"), hdr(12, 22, "Fixture two"),
		det(21, "<one@example.invalid>"), det(22, "<two@example.invalid>"),
		hxbuild.NewMailBody(hxbuild.MailBodySpec{Key: 21, Stamp: 1, HTML: []byte("<p>inline body</p>")}),
		hxbuild.NewMailBody(hxbuild.MailBodySpec{Key: 22, Stamp: 1, Path: datPath}),
	}
}

// appendBlock adds one data block of objects to the profile's store, after the fixture's blocks.
func appendBlock(t *testing.T, root string, objs ...*hxbuild.Object) {
	t.Helper()
	path := filepath.Join(root, "Main", "HxStore.hxd")
	b, err := os.ReadFile(path) //nolint:gosec // a test temp dir
	if err != nil {
		t.Fatal(err)
	}
	b = append(b, hxbuild.EncodeBlock(hxbuild.BlockTypeData, hxbuild.FramedPayload(hxbuild.Head(15), objs...))...)
	if err := os.WriteFile(path, b, 0o600); err != nil { //nolint:gosec // a test temp dir
		t.Fatal(err)
	}
}

// mailRoot is an Outlook root whose store holds the fixture's calendar and the synthetic mailbox.
func mailRoot(t *testing.T) string {
	t.Helper()
	root := outlookRoot(t, "HxStore.hxd")
	files := filepath.Join(root, "Main", "Files")
	if err := os.MkdirAll(files, 0o750); err != nil {
		t.Fatal(err)
	}
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write([]byte("<p>file body</p>"))
	_ = zw.Close()
	if err := os.WriteFile(filepath.Join(files, "two.dat"), gz.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	appendBlock(t, root, mailObjects("~/Files/two.dat")...)
	return root
}

// countSnapshots counts the copies of the store a sync takes.
func countSnapshots(t *testing.T) *int {
	t.Helper()
	n := 0
	old := outlookSnapshot
	outlookSnapshot = func(ctx context.Context, path string) (outlookdesktop.Info, func(), error) {
		n++
		return old(ctx, path)
	}
	t.Cleanup(func() { outlookSnapshot = old })
	return &n
}

// One copy of the store serves the calendar and the mail; the mail has its own row and counts; a
// second sync reads nothing; the file body is read once and kept.
func TestSyncReadsMailWithCalendar(t *testing.T) {
	isolateTmp(t)
	utcDays(t)
	db := newDB(t)
	root := mailRoot(t)
	copies := countSnapshots(t)

	r, _ := run(t, outlookOpts(db, root))
	if *copies != 1 {
		t.Fatalf("%d copies of the store for one sync", *copies)
	}
	cal, mail := sourceKeyed(t, r, "outlook|Main"), sourceKeyed(t, r, "outlook|Main|mail")
	if cal.Counts.Calendar.Events.Inserted != 10 || cal.Counts.Mail != nil {
		t.Fatalf("calendar row: %+v %+v", cal, *cal.Counts)
	}
	if mail.Counts == nil || mail.Counts.Mail == nil || mail.Counts.Mail.Added != 2 || mail.Accounts[0] != "outlook/Main" {
		t.Fatalf("mail row: %+v", mail)
	}
	if n := count(t, db, `select count(*) from mail_messages where account='outlook/Main'`); n != 2 {
		t.Fatalf("%d messages", n)
	}
	if n := count(t, db, `select count(*) from mail_messages where body_state='file' and body_text like '%file body%'`); n != 1 {
		t.Fatal("the file body was not read")
	}
	if count(t, db, `select count(*) from meta where key='outlook_mail_read:outlook/Main'`) != 1 || count(t, db, `select count(*) from sync_runs where source='outlook|Main|mail'`) != 1 {
		t.Fatal("the read marker or the run row is missing")
	}

	*copies = 0
	r, _ = run(t, outlookOpts(db, root))
	if *copies != 0 || sourceKeyed(t, r, "outlook|Main").Status != StatusUnchanged || sourceKeyed(t, r, "outlook|Main|mail").Status != StatusUnchanged {
		t.Fatalf("second sync: %d copies %+v", *copies, r.Sources)
	}
}

// An archive that holds no mail marker (written before mail, or after a failed mail read) reads the
// mail again although the store is unchanged; a changed mail mapper does too.
func TestSyncMailReadAfterUpgradeAndMapperBump(t *testing.T) {
	isolateTmp(t)
	utcDays(t)
	db := newDB(t)
	root := mailRoot(t)
	run(t, outlookOpts(db, root))

	if _, err := openRaw(t, db).Exec(`delete from meta where key like 'outlook_mail_read:%'`); err != nil {
		t.Fatal(err)
	}
	r, _ := run(t, outlookOpts(db, root))
	mail := sourceKeyed(t, r, "outlook|Main|mail")
	if mail.Status == StatusUnchanged || mail.Counts.Mail.Added != 0 || mail.Counts.Mail.Updated != 0 {
		t.Fatalf("a missing marker did not read the mail: %+v", mail)
	}
	if count(t, db, `select count(*) from meta where key='outlook_mail_read:outlook/Main'`) != 1 {
		t.Fatal("the marker was not written again")
	}

	old := outlookMailMapperVersion
	outlookMailMapperVersion = old + 1
	t.Cleanup(func() { outlookMailMapperVersion = old })
	if r, _ = run(t, outlookOpts(db, root)); sourceKeyed(t, r, "outlook|Main|mail").Status == StatusUnchanged {
		t.Fatalf("a bumped mail mapper did not read the unchanged store: %+v", r.Sources)
	}
}

// A mail layout refusal fails the mail source alone: the calendar commits, the marker is gone and
// the failure is remembered. A calendar refusal leaves the mail read alone.
func TestSyncMailAndCalendarFailApart(t *testing.T) {
	isolateTmp(t)
	utcDays(t)
	t.Run("mail refused", func(t *testing.T) {
		db := newDB(t)
		root := mailRoot(t)
		appendBlock(t, root, hxbuild.NewMailHeader(hxbuild.MailHeaderSpec{Tag: 0x42f, Key: 90, DetailKey: 21, FolderKey: 101}))
		r, _, err := Run(context.Background(), outlookOpts(db, root))
		var coded *errs.Coded
		if !errors.As(err, &coded) || coded.Code != errs.CodePartialSync {
			t.Fatalf("err = %v", err)
		}
		mail := sourceKeyed(t, r, "outlook|Main|mail")
		if mail.Status != StatusFailed || mail.Error.Code != outlookmail.CodeMailLayoutUnsupported || sourceKeyed(t, r, "outlook|Main").Status != StatusOK {
			t.Fatalf("%+v", r.Sources)
		}
		if count(t, db, `select count(*) from calendar_source_events where source='outlook'`) == 0 || count(t, db, `select count(*) from mail_messages`) != 0 {
			t.Fatal("the calendar did not commit alone")
		}
		if count(t, db, `select count(*) from meta where key='outlook_mail_failure:outlook/Main'`) != 1 || count(t, db, `select count(*) from meta where key='outlook_mail_read:outlook/Main'`) != 0 {
			t.Fatal("the failure was not remembered")
		}
	})
	t.Run("calendar refused", func(t *testing.T) {
		db := newDB(t)
		root := outlookRoot(t, "store-new-tag-mixed.hxd")
		appendBlock(t, root, mailObjects("")...)
		r, _, err := Run(context.Background(), outlookOpts(db, root))
		var coded *errs.Coded
		if !errors.As(err, &coded) || coded.Code != errs.CodePartialSync {
			t.Fatalf("err = %v", err)
		}
		if cal := sourceKeyed(t, r, "outlook|Main"); cal.Status != StatusFailed || cal.Error.Code != "outlook_layout_unsupported" {
			t.Fatalf("%+v", r.Sources)
		}
		if mail := sourceKeyed(t, r, "outlook|Main|mail"); mail.Counts.Mail.Added != 2 {
			t.Fatalf("%+v", mail)
		}
	})
}

// Where mail is not read the sync says so in a row of its own and does not fail.
func TestSyncMailUnsupportedPlatform(t *testing.T) {
	isolateTmp(t)
	utcDays(t)
	old := outlookMailSupported
	outlookMailSupported = func() bool { return false }
	t.Cleanup(func() { outlookMailSupported = old })
	db := newDB(t)
	root := mailRoot(t)
	r, _ := run(t, outlookOpts(db, root))
	mail := sourceKeyed(t, r, "outlook|Main|mail")
	c := errs.MailUnsupportedPlatform()
	if mail.Status != StatusUnavailable || mail.Error.Code != "mail_unsupported_platform" || mail.Error.Message != c.Message || r.Status != StatusOK {
		t.Fatalf("%s %+v", r.Status, mail)
	}
	if count(t, db, `select count(*) from mail_messages`) != 0 {
		t.Fatal("mail was read")
	}
	// The unchanged store reads nothing and still says it.
	if r, _ = run(t, outlookOpts(db, root)); sourceKeyed(t, r, "outlook|Main|mail").Status != StatusUnavailable || sourceKeyed(t, r, "outlook|Main").Status != StatusUnchanged {
		t.Fatalf("%+v", r.Sources)
	}
	// A run that fails in the calendar alone is still a failed run: the note is no commit.
	root = outlookRoot(t, "store-new-tag-mixed.hxd")
	if _, _, err := Run(context.Background(), outlookOpts(newDB(t), root)); err == nil {
		t.Fatal("a refused calendar did not fail the sync")
	}
}

// A Teams account filter leaves Outlook out, and with it the mail.
func TestSyncMailFollowsOutlook(t *testing.T) {
	isolateTmp(t)
	utcDays(t)
	db := newDB(t)
	root := mailRoot(t)
	o := outlookOpts(db, root)
	o.OutlookEnabled = false
	run(t, o)
	if count(t, db, `select count(*) from mail_messages`) != 0 {
		t.Fatal("mail was read with Outlook off")
	}
}

// The interval skip covers the mail too.
func TestSyncMailSkippedByInterval(t *testing.T) {
	isolateTmp(t)
	utcDays(t)
	db := newDB(t)
	root := mailRoot(t)
	now := time.Date(2031, 3, 5, 9, 0, 0, 0, time.UTC)
	old := outlookNow
	outlookNow = func() time.Time { return now }
	t.Cleanup(func() { outlookNow = old })
	o := outlookOpts(db, root)
	o.OutlookMinReadInterval = 0
	run(t, o)
	now = now.Add(time.Minute)
	r, _ := run(t, o)
	if s := sourceKeyed(t, r, "outlook|Main|mail"); s.Status != StatusSkippedInterval || s.NextReadAfter == nil {
		t.Fatalf("%+v", s)
	}
}

// A cancelled sync does not start the mail read.
func TestSyncMailNotStartedAfterCancel(t *testing.T) {
	isolateTmp(t)
	utcDays(t)
	db := newDB(t)
	root := mailRoot(t)
	ctx, cancel := context.WithCancel(context.Background())
	old := outlookSnapshot
	outlookSnapshot = func(c context.Context, path string) (outlookdesktop.Info, func(), error) {
		info, cleanup, err := old(c, path)
		cancel() // after the copy: the calendar commit fails or the mail is not started
		return info, cleanup, err
	}
	t.Cleanup(func() { outlookSnapshot = old })
	_, _, _ = Run(ctx, outlookOpts(db, root))
	if count(t, db, `select count(*) from mail_messages`) != 0 {
		t.Fatal("mail was read after the cancel")
	}
}

// Each way the mail source can fail on the archive's side is that source's db_error, and the
// calendar committed beside it is kept.
func TestSyncMailDatabaseFailures(t *testing.T) {
	isolateTmp(t)
	utcDays(t)
	root := mailRoot(t)
	abort := func(table, when string) string {
		return `create trigger boom before insert on ` + table + ` when ` + when + ` begin select raise(abort, 'injected'); end;`
	}
	for name, ddl := range map[string]string{
		"holds":     `alter table mail_messages rename column account to acct`,
		"need body": `alter table mail_messages rename column body_state to bs`,
		"commit":    abort("mail_messages", `1`),
		"marker":    abort("meta", `new.key like 'outlook_mail_read:%'`),
		"run row":   abort("sync_runs", `new.source like '%|mail'`),
	} {
		t.Run(name, func(t *testing.T) {
			db := archiveWith(t, ddl)
			rep, _, err := Run(context.Background(), outlookOpts(db, root))
			if err == nil {
				t.Fatal("no error")
			}
			if m := sourceKeyed(t, rep, "outlook|Main|mail"); m.Status != StatusFailed || m.Error.Code != errs.CodeDBError || sourceKeyed(t, rep, "outlook|Main").Status != StatusOK {
				t.Fatalf("%+v", rep.Sources)
			}
		})
	}
	t.Run("state", func(t *testing.T) {
		db := newDB(t)
		run(t, outlookOpts(db, root))
		if _, err := openRaw(t, db).Exec(`insert into meta(key, value) values('outlook_mail_failure:outlook/Main', 'not json')`); err != nil {
			t.Fatal(err)
		}
		if code := outlookFailure(t, outlookOpts(db, root)); code != errs.CodeDBError {
			t.Fatal(code)
		}
	})
}

// touch moves the profile store's modification time forward, so the store reads as changed.
func touch(t *testing.T, root string, d time.Duration) {
	t.Helper()
	path := filepath.Join(root, "Main", "HxStore.hxd")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	at := info.ModTime().Add(d)
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
}

// A sync stopped after the calendar commit has recorded the store's new fingerprint, but not the
// mail: the next sync reads the mail all the same, and finds the message that arrived meanwhile.
func TestSyncMailReadAfterASyncStoppedBetweenCalendarAndMail(t *testing.T) {
	isolateTmp(t)
	utcDays(t)
	db := newDB(t)
	root := mailRoot(t)
	run(t, outlookOpts(db, root))
	appendBlock(t, root,
		hxbuild.NewMailHeader(hxbuild.MailHeaderSpec{Key: 13, Stamp: 1, DetailKey: 23, FolderKey: 101, Received: mailDay, Subject: "Fixture three", SenderName: "Fixture Sender", SenderAddr: "sender@example.invalid", Unread: 1, Importance: 1}),
		hxbuild.NewMailDetail(hxbuild.MailDetailSpec{Key: 23, Stamp: 1, MessageID: "<three@example.invalid>", Class: "IPM.Note", Sent: mailDay}))
	touch(t, root, time.Minute)

	ctx, cancel := context.WithCancel(context.Background())
	afterCalendar = cancel
	t.Cleanup(func() { afterCalendar = func() {} })
	_, _, _ = Run(ctx, outlookOpts(db, root))
	afterCalendar = func() {}
	if n := count(t, db, `select count(*) from mail_messages`); n != 2 {
		t.Fatalf("%d messages after the stopped sync", n)
	}

	r, _ := run(t, outlookOpts(db, root))
	if mail := sourceKeyed(t, r, "outlook|Main|mail"); mail.Status == StatusUnchanged || mail.Counts.Mail.Added != 1 {
		t.Fatalf("the mail was not read after the stopped sync: %+v", r.Sources)
	}
}

// A header that is reached after unknown bytes is not used, but it still names its message: the
// message is never recorded absent, however many reads see it that way.
func TestSyncMailResyncedHeaderIsNotAbsence(t *testing.T) {
	isolateTmp(t)
	utcDays(t)
	db := newDB(t)
	root := mailRoot(t)
	run(t, outlookOpts(db, root))

	// The store is rewritten without message one's header; a stray header for it follows three
	// bytes of unknown framing. Its detail object is still there.
	path := filepath.Join(root, "Main", "HxStore.hxd")
	putOutlookStore(t, root, "HxStore.hxd")
	keep := mailObjects("~/Files/two.dat")
	stray := hxbuild.NewMailHeader(hxbuild.MailHeaderSpec{Key: 11, Stamp: 1, DetailKey: 21, FolderKey: 101, Received: mailDay, Subject: "Fixture one"}).Encode()
	payload := append(hxbuild.FramedPayload(hxbuild.Head(15), keep[0], keep[2], keep[3], keep[4], keep[5], keep[6]), append([]byte{1, 2, 3}, stray...)...) // no clean header 11
	b, err := os.ReadFile(path)                                                                                                                            //nolint:gosec // a test temp dir
	if err != nil {
		t.Fatal(err)
	}
	b = append(b, hxbuild.EncodeBlock(hxbuild.BlockTypeData, payload)...)
	if err := os.WriteFile(path, b, 0o600); err != nil { //nolint:gosec // a test temp dir
		t.Fatal(err)
	}
	for i := 1; i <= 3; i++ {
		touch(t, root, time.Duration(i)*time.Minute)
		run(t, outlookOpts(db, root))
	}
	if n := count(t, db, `select count(*) from mail_messages where detail_key=21 and gone_at is null and evicted_at is null`); n != 1 {
		t.Fatal("a message named by a resynced header was marked gone")
	}
	if n := count(t, db, `select count(*) from mail_absent where detail_key=21`); n != 0 {
		t.Fatal("an absence was recorded for it")
	}
}

// The failure record is a note for doctor: a write that fails does not change the outcome.
func TestSyncMailFailureRecordCanFail(t *testing.T) {
	isolateTmp(t)
	utcDays(t)
	root := mailRoot(t)
	appendBlock(t, root, hxbuild.NewMailHeader(hxbuild.MailHeaderSpec{Tag: 0x42f, Key: 90, DetailKey: 21, FolderKey: 101}))
	db := archiveWith(t, `create trigger boom before insert on meta when new.key like 'outlook_mail_failure:%' begin select raise(abort, 'injected'); end;`)
	var progress bytes.Buffer
	o := outlookOpts(db, root)
	o.Progress = &progress
	rep, _, err := Run(context.Background(), o)
	if err == nil || sourceKeyed(t, rep, "outlook|Main|mail").Status != StatusFailed || !bytes.Contains(progress.Bytes(), []byte("could not record the failure")) {
		t.Fatalf("%v %s", err, progress.String())
	}
	if count(t, db, `select count(*) from meta where key='outlook_mail_read:outlook/Main'`) != 0 {
		t.Fatal("a marker stands after a failed mail read")
	}
}

// The marker is cleared before the calendar commit; a database that refuses that fails the profile
// before anything is committed.
func TestSyncMarkerClearFailure(t *testing.T) {
	isolateTmp(t)
	utcDays(t)
	root := mailRoot(t)
	db := archiveWith(t, `create trigger boom before delete on meta when old.key like 'outlook_mail_read:%' begin select raise(abort, 'injected'); end;`)
	if _, err := openRaw(t, db).Exec(`insert into meta(key, value) values('outlook_mail_read:outlook/Main', 'x')`); err != nil {
		t.Fatal(err)
	}
	if code := outlookFailure(t, outlookOpts(db, root)); code != errs.CodeDBError {
		t.Fatal(code)
	}
	if count(t, db, `select count(*) from calendar_source_events where source='outlook'`) != 0 {
		t.Fatal("the calendar committed past a marker it could not clear")
	}
}
