package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	crawlstore "github.com/openclaw/crawlkit/store"

	"github.com/ourostack/m365crawl/internal/workiq"
)

func TestWorkIQWindowAttemptsNeedAMailAccountAndAFixedRange(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	mustCommitMail(t, s, mailBatch(mailT0))
	must0(s.SetWorkIQEnabled(ctx, true))
	for _, scope := range []WorkIQScope{
		{Family: "mail", Account: mailAcct, Kind: "mail_window"},
		{Family: "mail", Account: mailAcct, Kind: "mail_window", WindowStart: windowUntil, WindowEnd: windowSince},
		{Family: "chats", Account: mailAcct, Kind: "mail_window", WindowStart: windowSince, WindowEnd: windowUntil},
	} {
		if _, err := s.BeginWorkIQAttempt(ctx, scope, mailT0); err == nil {
			t.Fatalf("an invalid window attempt began: %+v", scope)
		}
	}
	if got := WorkIQWindowListingResource("a b", windowSince, windowUntil); !strings.HasPrefix(got, "/me/mailFolders/a%20b/messages?$filter=receivedDateTime%20ge%202026-09-01T00:00:00Z%20and%20receivedDateTime%20lt%202026-09-08T00:00:00Z&") {
		t.Fatalf("listing resource = %s", got)
	}
	if p := (WorkIQWindowProgress{PendingCursor: "secret"}); fmt.Sprint(p) != "WorkIQWindowProgress" || fmt.Sprintf("%#v", p) != "WorkIQWindowProgress" {
		t.Fatal("a window position printed its cursor")
	}
	// A whole-account attempt cannot stage window state, and a window attempt can index mail.
	attempt := beginWorkIQWindow(t, s)
	sync := must(s.BeginWorkIQAttempt(ctx, WorkIQScope{Family: "mail", Account: mailAcct}, mailT0))
	verifyWorkIQTestAttempt(t, s, sync)
	if _, err := s.validateWorkIQWindowOwner(ctx, sync); err == nil {
		t.Fatal("a whole-account attempt passed as a window")
	}
	if _, err := s.validateWorkIQAttemptOwner(ctx, attempt); err == nil {
		t.Fatal("a window passed as a whole-account attempt")
	}
	if _, err := s.validateWorkIQMailRetention(ctx, -1); err == nil {
		t.Fatal("a missing attempt passed")
	}
}

func TestWorkIQWindowRefusesCorruptRangesAndClocks(t *testing.T) {
	ctx := context.Background()
	resource := WorkIQWindowDeltaResource("inbox-id", windowSince)
	for _, mutation := range []string{
		`update workiq_attempts set window_start='bad' where kind='mail_window'`,
		`update workiq_attempts set window_end=null where kind='mail_window'`,
		`update mail_messages set sent_at='bad' where detail_key=100`,
		`update workiq_pages set data_json=replace(data_json,'"id":"cached-copy"','"id":""')`,
	} {
		t.Run(mutation, func(t *testing.T) {
			s := newStore(t)
			mustCommitMail(t, s, mailBatch(mailT0, mailMsg(100, fInbox, "<cached@example.test>", "Subject", 0)))
			must0(s.SetWorkIQEnabled(ctx, true))
			attempt := beginWorkIQWindow(t, s)
			must0(s.EnsureWorkIQWindowFolders(ctx, attempt, []workiq.Folder{{ID: "inbox-id", Name: "Inbox", WellKnown: "inbox"}}))
			stageWindow(t, s, attempt, "inbox-id", resource, []json.RawMessage{
				windowMail("cached-copy", "inbox-id", "cached@example.test", windowSince.Add(time.Hour), "2026-09-01T01:00:00Z"),
			}, "", "")
			must(s.db.ExecContext(ctx, mutation))
			before := dump(t, s)
			if _, err := s.AdmitWorkIQWindow(ctx, attempt, []string{resource}, 128); err == nil || dump(t, s) != before {
				t.Fatal("corrupt window state was admitted")
			}
		})
	}
}

func TestWorkIQWindowItemsWithoutAVersionAreAlwaysNew(t *testing.T) {
	s := newStore(t)
	attempt := seedWorkIQWindowAttempt(t, s)
	resource := WorkIQWindowDeltaResource("inbox-id", windowSince)
	unversioned := windowMail("a", "inbox-id", "a@example.test", windowSince, "")
	stageWindow(t, s, attempt, "inbox-id", resource, []json.RawMessage{unversioned}, "", "")
	if out := stageWindow(t, s, attempt, "inbox-id", resource, []json.RawMessage{unversioned}, "", ""); out.Repeats != 0 || out.Affecting != 1 {
		t.Fatalf("an item without a version counted as an exact repeat: %+v", out)
	}
	if rowCount(t, s, `select count(*) from workiq_window_members where version is null`) != 1 {
		t.Fatal("a missing version was stored as text")
	}
}

func TestWorkIQWindowRowCountFaultsFailClosed(t *testing.T) {
	resource := WorkIQWindowDeltaResource("inbox-id", windowSince)
	setup := func(t *testing.T, s *Store) {
		attempt := seedWorkIQWindowAttempt(t, s)
		must0(s.EnsureWorkIQWindowFolders(context.Background(), attempt, []workiq.Folder{{ID: "inbox-id", Name: "Inbox", WellKnown: "inbox"}}))
		stageWindow(t, s, attempt, "inbox-id", resource, []json.RawMessage{
			windowMail("in", "inbox-id", "in@example.test", windowSince, "2026-09-01T00:00:00Z"),
		}, "", "")
	}
	s := newStore(t)
	setup(t, s)
	inj := injectFaults(t, s)
	sweepKind(t, s, inj, "rowsaffected", true, func(ctx context.Context, s *Store) error {
		_, err := s.AdmitWorkIQWindow(ctx, windowAttemptID(t, s), []string{resource}, 128)
		return err
	}, nil)
	s = newStore(t)
	setup(t, s)
	inj = injectFaults(t, s)
	sweepKind(t, s, inj, "rowsaffected", true, func(ctx context.Context, s *Store) error {
		return s.RetireWorkIQWindowFrontier(ctx, windowAttemptID(t, s), resource, 1)
	}, nil)
}

func TestWorkIQWindowMigrationNeedsItsPinnedConnection(t *testing.T) {
	s := newStore(t)
	failure := errors.New("no connection")
	pinWorkIQConn = func(context.Context, *sql.DB) (pragmaConn, error) { return nil, failure }
	t.Cleanup(func() {
		pinWorkIQConn = func(ctx context.Context, db *sql.DB) (pragmaConn, error) { return db.Conn(ctx) }
	})
	if err := s.migrateWorkIQWindow(context.Background()); !errors.Is(err, failure) {
		t.Fatal(err)
	}
}

func TestWorkIQWholeAccountCandidatesSkipCacheRowsAndRefuseCorruptClocks(t *testing.T) {
	ctx := context.Background()
	later := mailT0.Add(time.Hour)
	t.Run("cache row beside a window row", func(t *testing.T) {
		s := newStore(t)
		cached := mailMsg(100, fInbox, "<one@example.test>", "Subject", 0)
		cached.SenderAddress = "someone.else@example.test"
		mustCommitMail(t, s, mailBatch(mailT0, cached))
		must0(s.SetWorkIQEnabled(ctx, true))
		admitWindowCopy(t, s, "s1", "one@example.test", mailT0)
		attempt := beginWholeAccountAt(t, s, later)
		prepareRealMailInventory(t, s, attempt, []json.RawMessage{graphMailFixture("s9", "inbox-id", "one@example.test")})
		result := must(s.CommitWorkIQMailGroup(ctx, attempt, "one@example.test"))
		if result.Gaps["workiq_mail_identity_ambiguous"] != 1 || rowCount(t, s, `select count(*) from mail_messages`) != 3 {
			t.Fatalf("two candidates with an unproven one merged: %v", result.Gaps)
		}
	})
	for _, mutation := range []string{
		`update workiq_rows set server_seen_at='bad' where graph_id='s1'`,
		`update workiq_attempts set started_at='bad' where kind='sync'`,
	} {
		t.Run(mutation, func(t *testing.T) {
			s := newStore(t)
			mustCommitMail(t, s, mailBatch(mailT0))
			must0(s.SetWorkIQEnabled(ctx, true))
			admitWindowCopy(t, s, "s1", "one@example.test", mailT0)
			attempt := beginWholeAccountAt(t, s, later)
			prepareRealMailInventory(t, s, attempt, []json.RawMessage{graphMailFixture("s9", "inbox-id", "one@example.test")})
			must(s.db.ExecContext(ctx, mutation))
			if _, err := s.CommitWorkIQMailGroup(ctx, attempt, "one@example.test"); err == nil {
				t.Fatal("a corrupt clock decided a merge")
			}
		})
	}
}

func TestWorkIQWholeAccountCandidateFaultsFailClosed(t *testing.T) {
	later := mailT0.Add(time.Hour)
	setup := func(t *testing.T, s *Store) {
		mustCommitMail(t, s, mailBatch(mailT0))
		must0(s.SetWorkIQEnabled(context.Background(), true))
		admitWindowCopy(t, s, "s1", "one@example.test", mailT0)
		attempt := beginWholeAccountAt(t, s, later)
		prepareRealMailInventory(t, s, attempt, []json.RawMessage{graphMailFixture("s9", "inbox-id", "one@example.test")})
	}
	sweepFaults(t, setup, func(ctx context.Context, s *Store) error {
		var attempt int64
		must0(s.cs.DB().QueryRow(`select max(id) from workiq_attempts where kind='sync'`).Scan(&attempt))
		_, err := s.CommitWorkIQMailGroup(ctx, attempt, "one@example.test")
		return err
	})
}

// seedWindowRoute records proven and unproven windows of the mail account as rows, the way a
// window run leaves them.
func seedWindowRoute(t *testing.T, s *Store, since, until string, proven bool, counts string) {
	t.Helper()
	ctx := context.Background()
	must(s.db.ExecContext(ctx, `insert into workiq_attempts(family,account_id,started_at,finished_at,kind,status,window_start,window_end,counts_json)
values('mail',?,'2026-10-01T00:00:00Z','2026-10-01T01:00:00Z','mail_window','ok_with_omissions',?,?,?)`, mailAcct, since, until, counts))
	if proven {
		must(s.db.ExecContext(ctx, `insert into workiq_window_coverage(attempt_id,account_id,folder_id,since,until,snapshot_at)
values((select max(id) from workiq_attempts),?,'',?,?,'2026-10-01T00:30:00Z')`, mailAcct, since, until))
		return
	}
	must(s.db.ExecContext(ctx, `insert into workiq_gaps(attempt_id,code,n) values((select max(id) from workiq_attempts),'workiq_window_unproven',1)`))
}

func TestWorkIQMailWindowRouteReadsCoverageTwinsAndProof(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	mustCommitMail(t, s, mailBatch(mailT0, mailMsg(100, fInbox, "<cached@example.test>", "Subject", 0)))
	since, until := windowSince, windowUntil
	got := must(s.WorkIQMailWindowRoute(ctx, mailAcct, since, since, nil, nil))
	if !got.Covered {
		t.Fatal("an empty range was not covered")
	}
	if got = must(s.WorkIQMailWindowRoute(ctx, mailAcct, since, until, nil, nil)); got.Covered || got.Unproven {
		t.Fatalf("an archive without windows covered a range: %+v", got)
	}
	seedWindowRoute(t, s, "2026-09-01T00:00:00Z", "2026-09-08T00:00:00Z", false, `{"window_items_open":42}`)
	if got = must(s.WorkIQMailWindowRoute(ctx, mailAcct, since, until, nil, nil)); !got.Unproven || got.OpenItems != 42 || got.Covered {
		t.Fatalf("an unproven window was not reported: %+v", got)
	}
	seedWindowRoute(t, s, "2026-09-01T00:00:00Z", "2026-09-04T00:00:00Z", true, `{}`)
	seedWindowRoute(t, s, "2026-09-03T00:00:00Z", "2026-09-08T00:00:00Z", true, `{}`)
	if got = must(s.WorkIQMailWindowRoute(ctx, mailAcct, since, until, nil, nil)); !got.Covered {
		t.Fatalf("overlapping proven windows did not cover their union: %+v", got)
	}
	if got = must(s.WorkIQMailWindowRoute(ctx, mailAcct, since.Add(-time.Second), until, nil, nil)); got.Covered {
		t.Fatal("a range starting before every window was covered")
	}
	// A deferred member inside the range whose match is in another folder may be a twin.
	must(s.db.ExecContext(ctx, `insert into workiq_frontiers(family,account_id,resource,scope,qualification) values('mail',?,'/r','window:x','workiq-v1')`, mailAcct))
	must(s.db.ExecContext(ctx, `insert into workiq_folders(account_id,graph_id,folder_key,well_known,inventory_attempt) values(?,'g-sent',900,'sent',0)`, mailAcct))
	must(s.db.ExecContext(ctx, `insert into workiq_window_members(frontier_id,graph_id,account_id,folder_id,received_at,in_window,state,observed_at,match_rowid)
values((select max(id) from workiq_frontiers),'twin',?,'g-sent','2026-09-05T00:00:00Z',1,'deferred','2026-10-01T00:00:00Z',(select rowid from mail_messages where detail_key=100))`, mailAcct))
	for _, tc := range []struct {
		keys  []uint32
		kinds []string
		twins int
	}{
		{[]uint32{fSent}, []string{"sent"}, 1},
		{[]uint32{fInbox}, []string{"inbox"}, 0},
		{[]uint32{fDeleted}, []string{"deleted"}, 0},
		{[]uint32{fToMe}, []string{"to_me"}, 1},
		{[]uint32{fProject}, []string{"other"}, 1},
	} {
		if got = must(s.WorkIQMailWindowRoute(ctx, mailAcct, since, until, tc.keys, tc.kinds)); got.Twins != tc.twins || !got.Covered {
			t.Fatalf("%v: twins %d, want %d", tc.kinds, got.Twins, tc.twins)
		}
	}
	if got = must(s.WorkIQMailWindowRoute(ctx, mailAcct, since, until, nil, nil)); got.Twins != 0 {
		t.Fatal("an unfiltered read counted twins")
	}
	sweepReadFaults(t, func(t *testing.T, r *Store) {
		mustCommitMail(t, r, mailBatch(mailT0))
		seedWindowRoute(t, r, "2026-09-01T00:00:00Z", "2026-09-08T00:00:00Z", false, `{"window_items_open":1}`)
	}, func(ctx context.Context, r *Store) error {
		_, err := r.WorkIQMailWindowRoute(ctx, mailAcct, since, until, nil, nil)
		return err
	})
	sweepReadFaults(t, func(t *testing.T, r *Store) {
		mustCommitMail(t, r, mailBatch(mailT0))
		seedWindowRoute(t, r, "2026-09-01T00:00:00Z", "2026-09-08T00:00:00Z", true, `{}`)
	}, func(ctx context.Context, r *Store) error {
		_, err := r.WorkIQMailWindowRoute(ctx, mailAcct, since, until, []uint32{fSent}, []string{"sent"})
		return err
	})
	for _, mutation := range []string{
		`update workiq_window_coverage set since='bad'`,
		`update workiq_window_coverage set until='bad'`,
	} {
		r := newStore(t)
		seedWindowRoute(t, r, "2026-09-01T00:00:00Z", "2026-09-08T00:00:00Z", true, `{}`)
		_, err := r.db.ExecContext(ctx, mutation)
		if err == nil {
			_, err = r.WorkIQMailWindowRoute(ctx, mailAcct, since, until, nil, nil)
		}
		if err == nil {
			t.Fatalf("%s: corrupt coverage was read", mutation)
		}
	}
	r := newStore(t)
	seedWindowRoute(t, r, "2026-09-01T00:00:00Z", "2026-09-08T00:00:00Z", false, `not json`)
	if _, err := r.WorkIQMailWindowRoute(ctx, mailAcct, since, until, nil, nil); err == nil {
		t.Fatal("corrupt window counts were read")
	}
}

func TestWorkIQMailWindowRouteOnAnArchiveWithoutWindows(t *testing.T) {
	ctx := context.Background()
	path := writableArchivePath(t)
	cs := must(crawlstore.Open(ctx, crawlstore.Options{Path: path, Schema: strings.Replace(strings.Replace(strings.Replace(schemaDDL, workiqSchemaDDL, "", 1), workiqDocumentSchemaDDL, "", 1), workiqWindowSchemaDDL, "", 1), SchemaVersion: 20}))
	legacy := &Store{cs: cs, db: cs.DB()}
	must0(legacy.migrate(ctx))
	must0(legacy.Close())
	old := must(OpenReadOnly(ctx, path))
	defer func() { _ = old.Close() }()
	if got := must(old.WorkIQMailWindowRoute(ctx, mailAcct, windowSince, windowUntil, nil, nil)); got != (WorkIQWindowRoute{}) {
		t.Fatalf("an archive without windows reported window state: %+v", got)
	}
}

func TestWorkIQMailWindowSpansMergeProvenWindows(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	if spans := must(s.WorkIQMailWindowSpans(ctx, mailAcct)); spans != nil {
		t.Fatalf("an archive without windows has spans: %v", spans)
	}
	seedWindowRoute(t, s, "2026-09-01T00:00:00Z", "2026-09-04T00:00:00Z", true, `{}`)
	seedWindowRoute(t, s, "2026-09-03T00:00:00Z", "2026-09-08T00:00:00Z", true, `{}`)
	seedWindowRoute(t, s, "2026-10-01T00:00:00Z", "2026-10-02T00:00:00Z", false, `{}`)
	spans := must(s.WorkIQMailWindowSpans(ctx, mailAcct))
	if len(spans) != 1 || !spans[0].From.Equal(windowSince) || !spans[0].To.Equal(windowUntil) {
		t.Fatalf("spans = %v", spans)
	}
	sweepReadFaults(t, func(t *testing.T, r *Store) {
		seedWindowRoute(t, r, "2026-09-01T00:00:00Z", "2026-09-08T00:00:00Z", true, `{}`)
	}, func(ctx context.Context, r *Store) error {
		_, err := r.WorkIQMailWindowSpans(ctx, mailAcct)
		return err
	})
	must(s.db.ExecContext(ctx, `update workiq_window_coverage set until='bad'`))
	if _, err := s.WorkIQMailWindowSpans(ctx, mailAcct); err == nil {
		t.Fatal("corrupt window coverage was read")
	}
	path := writableArchivePath(t)
	cs := must(crawlstore.Open(ctx, crawlstore.Options{Path: path, Schema: strings.Replace(strings.Replace(strings.Replace(schemaDDL, workiqSchemaDDL, "", 1), workiqDocumentSchemaDDL, "", 1), workiqWindowSchemaDDL, "", 1), SchemaVersion: 20}))
	legacy := &Store{cs: cs, db: cs.DB()}
	if spans, err := legacy.WorkIQMailWindowSpans(ctx, mailAcct); err != nil || spans != nil {
		t.Fatal("an old archive reported window spans", err)
	}
	must0(legacy.Close())
}

func TestWorkIQWindowAdmissionDecodesEachStagedPageOncePerBatch(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	attempt := seedWorkIQWindowAttempt(t, s)
	must0(s.EnsureWorkIQWindowFolders(ctx, attempt, []workiq.Folder{{ID: "inbox-id", Name: "Inbox", WellKnown: "inbox"}}))
	resource := WorkIQWindowDeltaResource("inbox-id", windowSince)
	in := windowSince.Add(time.Hour)
	// Graph IDs alternate between the two pages, so an order by Graph ID would revisit each page.
	stageWindow(t, s, attempt, "inbox-id", resource, []json.RawMessage{
		windowMail("a", "inbox-id", "a@example.test", in, "2026-09-02T00:00:00Z"),
		windowMail("c", "inbox-id", "c@example.test", in, "2026-09-02T00:00:00Z"),
		windowMail("e", "inbox-id", "e@example.test", in, "2026-09-02T00:00:00Z"),
	}, "/me/mailFolders/inbox-id/messages/delta?$skiptoken=two", "")
	stageWindow(t, s, attempt, "inbox-id", resource, []json.RawMessage{
		windowMail("b", "inbox-id", "b@example.test", in, "2026-09-02T00:00:00Z"),
		windowMail("d", "inbox-id", "d@example.test", in, "2026-09-02T00:00:00Z"),
		windowMail("f", "inbox-id", "f@example.test", in, "2026-09-02T00:00:00Z"),
	}, "", "/me/mailFolders/inbox-id/messages/delta?$deltatoken=x")
	decodes := 0
	defer func(old func(context.Context, []byte) (workiq.Page, error)) { decodeWorkIQWindowPage = old }(decodeWorkIQWindowPage)
	decodeWorkIQWindowPage = func(ctx context.Context, raw []byte) (workiq.Page, error) {
		decodes++
		return workiq.DecodePage(ctx, raw)
	}
	got := must(s.AdmitWorkIQWindow(ctx, attempt, []string{resource}, 128))
	if got.Admitted != 6 || !got.Done || decodes != 2 {
		t.Fatalf("admission decoded %d pages for two staged pages: %+v", decodes, got)
	}
	// A page whose items are not a list matches no member.
	stageWindow(t, s, attempt, "inbox-id", resource, []json.RawMessage{
		windowMail("g", "inbox-id", "g@example.test", in, "2026-09-02T00:00:00Z"),
	}, "", "/me/mailFolders/inbox-id/messages/delta?$deltatoken=y")
	decodeWorkIQWindowPage = func(context.Context, []byte) (workiq.Page, error) {
		return workiq.Page{Data: json.RawMessage(`{"value":1}`)}, nil
	}
	if _, err := s.AdmitWorkIQWindow(ctx, attempt, []string{resource}, 128); err == nil || !strings.Contains(err.Error(), "does not match its staged page") {
		t.Fatalf("a page without an item list was admitted: %v", err)
	}
}
