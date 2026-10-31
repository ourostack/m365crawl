package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/workiq"
)

var (
	windowSince = time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	windowUntil = time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC)
)

func seedWorkIQWindowAttempt(t *testing.T, s *Store) int64 {
	t.Helper()
	ctx := context.Background()
	mustCommitMail(t, s, mailBatch(mailT0))
	must0(s.SetWorkIQEnabled(ctx, true))
	return beginWorkIQWindow(t, s)
}

func beginWorkIQWindow(t *testing.T, s *Store) int64 {
	t.Helper()
	ctx := context.Background()
	attempt := must(s.BeginWorkIQAttempt(ctx, WorkIQScope{Family: "mail", Account: mailAcct, Kind: "mail_window", WindowStart: windowSince, WindowEnd: windowUntil}, mailT0))
	verifyWorkIQTestAttempt(t, s, attempt)
	for after, done := int64(0), false; !done; {
		var err error
		after, done, err = s.IndexWorkIQMailCandidates(ctx, attempt, after, 128)
		must0(err)
	}
	return attempt
}

func windowMail(id, folder, header string, received time.Time, modified string) json.RawMessage {
	item := map[string]any{
		"id": id, "parentFolderId": folder, "internetMessageId": header, "subject": "Subject",
		"sentDateTime": "2026-09-10T07:59:00Z", "lastModifiedDateTime": modified,
		"from": map[string]any{"emailAddress": map[string]string{"name": "Ann Sender", "address": "ann@example.test"}},
		"body": map[string]string{"contentType": "html", "content": "<p>Window body " + id + "</p>"},
	}
	if !received.IsZero() {
		item["receivedDateTime"] = received.UTC().Format(time.RFC3339)
	}
	raw, _ := json.Marshal(item)
	return raw
}

func windowRemoved(id string) json.RawMessage {
	return json.RawMessage(`{"id":"` + id + `","@removed":{"reason":"deleted"}}`)
}

func windowPage(t *testing.T, values []json.RawMessage, next, delta string) workiq.Page {
	t.Helper()
	data := map[string]any{"value": values}
	if next != "" {
		data["@odata.nextLink"] = next
	}
	if delta != "" {
		data["@odata.deltaLink"] = delta
	}
	raw := must(json.Marshal(map[string]any{"structuredContent": map[string]any{"results": []any{map[string]any{"statusCode": 200, "data": data}}}}))
	return must(workiq.DecodePage(context.Background(), raw))
}

func stageWindow(t *testing.T, s *Store, attempt int64, folder, resource string, values []json.RawMessage, next, delta string) WorkIQWindowPage {
	t.Helper()
	ctx := context.Background()
	p := must(s.WorkIQWindowProgressFor(ctx, attempt, resource))
	return must(s.StageWorkIQWindowPage(ctx, attempt, folder, resource, p.Revision, windowPage(t, values, next, delta), mailT0))
}

func TestWorkIQWindowStagingClassifiesChangesAndClearsOutOfWindowPages(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	attempt := seedWorkIQWindowAttempt(t, s)
	resource := WorkIQWindowDeltaResource("inbox-id", windowSince)
	next := "/me/mailFolders/inbox-id/messages/delta?$skiptoken=two"
	october := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	out := stageWindow(t, s, attempt, "inbox-id", resource, []json.RawMessage{
		windowMail("late-1", "inbox-id", "late1@example.test", october, "2026-10-01T00:00:00Z"),
		windowMail("late-2", "inbox-id", "late2@example.test", windowUntil, "2026-10-01T00:00:00Z"),
	}, next, "")
	if !out.Cleared || out.OutOfWindow != 2 || out.Affecting != 0 || out.Items != 2 || out.Bytes == 0 {
		t.Fatalf("an out-of-window page was not stored already cleared: %+v", out)
	}
	if rowCount(t, s, `select count(*) from workiq_pages where data_json='' and body_bytes>0 and length(body_sha256)=64
and message_ids_json='["late-1","late-2"]' and cleared_at is not null`) != 1 {
		t.Fatal("a cleared page lost its digest, size or message IDs")
	}
	p := must(s.WorkIQWindowProgressFor(ctx, attempt, resource))
	if !p.Started || p.Terminal || p.Delta || p.PendingCursor != next {
		t.Fatalf("progress after a middle page: %+v", p)
	}
	delta := "/me/mailFolders/inbox-id/messages/delta?$deltatoken=end"
	out = stageWindow(t, s, attempt, "inbox-id", resource, []json.RawMessage{
		windowMail("in-1", "inbox-id", "in1@example.test", windowSince, "2026-09-01T00:00:00Z"),
		windowMail("late-1", "inbox-id", "late1@example.test", october, "2026-10-01T00:00:00Z"),
	}, "", delta)
	if out.Cleared || out.InWindow != 1 || out.Repeats != 1 || out.Affecting != 1 {
		t.Fatalf("an in-window page was cleared or misclassified: %+v", out)
	}
	// The walk's own resource holds the position, whatever link a page came from.
	p = must(s.WorkIQWindowProgressFor(ctx, attempt, next))
	if p.Started {
		t.Fatal("a link became its own position")
	}
	p = must(s.WorkIQWindowProgressFor(ctx, attempt, resource))
	if !p.Terminal || !p.Delta || p.PendingCursor != delta {
		t.Fatalf("progress after the terminal page: %+v", p)
	}
	for _, tc := range []struct {
		name      string
		values    []json.RawMessage
		affecting int
	}{
		{"exact repeat", []json.RawMessage{windowMail("in-1", "inbox-id", "in1@example.test", windowSince, "2026-09-01T00:00:00Z")}, 0},
		{"older version", []json.RawMessage{windowMail("in-1", "inbox-id", "in1@example.test", windowSince, "2026-08-31T00:00:00Z")}, 0},
		{"out-of-window change", []json.RawMessage{windowMail("late-1", "inbox-id", "late1@example.test", october, "2026-10-02T00:00:00Z")}, 0},
		{"out-of-window removal", []json.RawMessage{windowRemoved("late-2")}, 0},
		{"repeated removal", []json.RawMessage{windowRemoved("late-2")}, 0},
		{"in-window change", []json.RawMessage{windowMail("in-1", "inbox-id", "in1@example.test", windowSince, "2026-09-02T00:00:00.5Z")}, 1},
		{"new in-window item", []json.RawMessage{windowMail("in-2", "inbox-id", "in2@example.test", windowUntil.Add(-time.Second), "2026-09-07T00:00:00Z")}, 1},
		{"in-window removal", []json.RawMessage{windowRemoved("in-2")}, 1},
		{"unknown removal", []json.RawMessage{windowRemoved("never-seen")}, 1},
		{"undated item", []json.RawMessage{windowMail("undated", "inbox-id", "u@example.test", time.Time{}, "2026-09-03T00:00:00Z")}, 1},
	} {
		p := must(s.WorkIQWindowProgressFor(ctx, attempt, resource))
		out := must(s.StageWorkIQWindowPage(ctx, attempt, "inbox-id", resource, p.Revision, windowPage(t, tc.values, "", delta), mailT0))
		if out.Affecting != tc.affecting {
			t.Fatalf("%s: affecting %d, want %d (%+v)", tc.name, out.Affecting, tc.affecting, out)
		}
	}
	if rowCount(t, s, `select count(*) from workiq_window_members where graph_id='undated' and clock_unknown=1 and in_window=0`) != 1 ||
		rowCount(t, s, `select count(*) from workiq_pages p join workiq_window_members m on m.page_id=p.id where m.graph_id='undated' and p.data_json<>''`) != 1 {
		t.Fatal("an undated item's page body was not kept")
	}
	facts := must(s.WorkIQWindowFactsFor(ctx, attempt, resource))
	if facts.InWindow != 1 || facts.OutOfWindow != 1 || facts.StagedIn != 1 || facts.LargestPage == 0 || facts.StagedBytes == 0 || facts.ClockUnknown != 1 {
		t.Fatalf("window facts = %+v", facts)
	}
	if rowCount(t, s, `select count(*) from workiq_accounts where inventory_revision<>0`) != 0 {
		t.Fatal("window staging bumped the whole-account inventory revision")
	}
}

func TestWorkIQWindowStagingRefusesPagesOutsideTheirPosition(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	attempt := seedWorkIQWindowAttempt(t, s)
	resource := WorkIQWindowDeltaResource("inbox-id", windowSince)
	good := windowPage(t, []json.RawMessage{windowMail("a", "inbox-id", "a@example.test", windowSince, "2026-09-01T00:00:00Z")}, "", "/me/mailFolders/inbox-id/messages/delta?$deltatoken=x")
	before := dump(t, s)
	for name, call := range map[string]func() error{
		"stale revision": func() error {
			_, err := s.StageWorkIQWindowPage(ctx, attempt, "inbox-id", resource, 3, good, mailT0)
			return err
		},
		"other folder resource": func() error {
			_, err := s.StageWorkIQWindowPage(ctx, attempt, "sent-id", resource, 0, good, mailT0)
			return err
		},
		"item outside its folder": func() error {
			page := windowPage(t, []json.RawMessage{windowMail("a", "sent-id", "a@example.test", windowSince, "")}, "", "")
			_, err := s.StageWorkIQWindowPage(ctx, attempt, "inbox-id", resource, 0, page, mailT0)
			return err
		},
		"cursor outside its folder": func() error {
			page := windowPage(t, nil, "/me/mailFolders/sent-id/messages/delta?$skiptoken=x", "")
			_, err := s.StageWorkIQWindowPage(ctx, attempt, "inbox-id", resource, 0, page, mailT0)
			return err
		},
		"no value array": func() error {
			raw := must(json.Marshal(map[string]any{"structuredContent": map[string]any{"results": []any{map[string]any{"statusCode": 200, "data": map[string]any{}}}}}))
			_, err := s.StageWorkIQWindowPage(ctx, attempt, "inbox-id", resource, 0, workiq.Page{JSON: raw}, mailT0)
			return err
		},
		"undecodable item": func() error {
			page := windowPage(t, []json.RawMessage{json.RawMessage(`{"id":""}`)}, "", "")
			_, err := s.StageWorkIQWindowPage(ctx, attempt, "inbox-id", resource, 0, page, mailT0)
			return err
		},
		"undecodable page": func() error {
			_, err := s.StageWorkIQWindowPage(ctx, attempt, "inbox-id", resource, 0, workiq.Page{JSON: []byte(`{`)}, mailT0)
			return err
		},
		"bad resource": func() error {
			_, err := s.StageWorkIQWindowPage(ctx, attempt, "inbox-id", "https://example.test/x", 0, good, mailT0)
			return err
		},
		"no observation clock": func() error {
			_, err := s.StageWorkIQWindowPage(ctx, attempt, "inbox-id", resource, 0, good, time.Time{})
			return err
		},
		"whole-account attempt": func() error {
			sync := must(s.BeginWorkIQAttempt(ctx, WorkIQScope{Family: "mail", Account: mailAcct}, mailT0))
			verifyWorkIQTestAttempt(t, s, sync)
			_, err := s.StageWorkIQWindowPage(ctx, sync, "inbox-id", resource, 0, good, mailT0)
			before = dump(t, s)
			return err
		},
	} {
		if err := call(); err == nil || dump(t, s) != before {
			t.Fatalf("%s: a page outside its position was accepted or left a change", name)
		}
	}
	if _, err := s.WorkIQWindowProgressFor(ctx, attempt, "https://example.test/x"); err == nil {
		t.Fatal("an unsafe resource had progress")
	}
	if err := s.RetireWorkIQWindowFrontier(ctx, attempt, resource, 0); err == nil {
		t.Fatal("a position that does not exist was retired")
	}
	stageWindow(t, s, attempt, "inbox-id", resource, []json.RawMessage{windowMail("a", "inbox-id", "a@example.test", windowSince, "2026-09-01T00:00:00Z")}, "", "/me/mailFolders/inbox-id/messages/delta?$deltatoken=x")
	must0(s.RetireWorkIQWindowFrontier(ctx, attempt, resource, 1))
	if p := must(s.WorkIQWindowProgressFor(ctx, attempt, resource)); p.Started || rowCount(t, s, `select count(*) from workiq_window_members`) != 1 {
		t.Fatal("a retired position was still current or lost its members")
	}
	out := stageWindow(t, s, attempt, "inbox-id", resource, []json.RawMessage{windowMail("a", "inbox-id", "a@example.test", windowSince, "2026-09-01T00:00:00Z")}, "", "")
	if out.Affecting != 1 || rowCount(t, s, `select count(*) from workiq_frontiers where scope like 'window:%' and generation=1 and retired=0`) != 1 {
		t.Fatal("a new generation did not start its own members")
	}
}

func TestWorkIQWindowAdmissionNeverMergesByMessageID(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	mustCommitMail(t, s, mailBatch(mailT0, mailMsg(100, fInbox, "<cached@example.test>", "Subject", 0)))
	odd := mailMsg(101, fInbox, "<odd@example.test>", "Subject", 0)
	odd.SenderAddress = "someone.else@example.test"
	mustCommitMail(t, s, mailBatch(mailT0, mailMsg(100, fInbox, "<cached@example.test>", "Subject", 0), odd))
	must0(s.SetWorkIQEnabled(ctx, true))
	attempt := beginWorkIQWindow(t, s)
	must0(s.EnsureWorkIQWindowFolders(ctx, attempt, []workiq.Folder{{ID: "inbox-id", Name: "Inbox", WellKnown: "inbox"}, {ID: "child-id", ParentID: "inbox-id", Name: "Child"}}))
	resource := WorkIQWindowDeltaResource("inbox-id", windowSince)
	in := windowSince.Add(48 * time.Hour)
	stageWindow(t, s, attempt, "inbox-id", resource, []json.RawMessage{
		windowMail("cached-copy", "inbox-id", "<CACHED@example.test>", in, "2026-09-03T00:00:00Z"),
		windowMail("fresh", "inbox-id", "fresh@example.test", in, "2026-09-03T00:00:00Z"),
		windowMail("odd-copy", "inbox-id", "odd@example.test", in, "2026-09-03T00:00:00Z"),
		windowMail("no-header", "inbox-id", "", in, "2026-09-03T00:00:00Z"),
	}, "", "/me/mailFolders/inbox-id/messages/delta?$deltatoken=x")
	first := must(s.AdmitWorkIQWindow(ctx, attempt, []string{resource}, 2))
	if first.Done || first.Admitted+first.Deferred != 2 {
		t.Fatalf("a bounded admission did not stop at its limit: %+v", first)
	}
	rest := must(s.AdmitWorkIQWindow(ctx, attempt, []string{resource}, 128))
	if !rest.Done || first.Admitted+rest.Admitted != 3 || first.Deferred+rest.Deferred != 1 ||
		first.Gaps["workiq_mail_identity_uncorroborated"]+rest.Gaps["workiq_mail_identity_uncorroborated"] != 1 ||
		first.Gaps["workiq_mail_message_id_missing"]+rest.Gaps["workiq_mail_message_id_missing"] != 1 {
		t.Fatalf("admission outcome: %+v %+v", first, rest)
	}
	if rowCount(t, s, `select count(*) from mail_messages`) != 5 ||
		rowCount(t, s, `select count(*) from workiq_rows where identity_unproven=1`) != 3 ||
		rowCount(t, s, `select count(*) from workiq_window_members where graph_id='cached-copy' and state='deferred' and match_rowid=(select rowid from mail_messages where detail_key=100)`) != 1 ||
		rowCount(t, s, `select count(*) from mail_messages where detail_key=100 and sender_name='Ann Sender'`) != 1 {
		t.Fatal("a window merged by Message-ID, or lost a deferred match")
	}
	if rowCount(t, s, `select count(*) from workiq_pages where data_json<>''`) != 1 {
		t.Fatal("the page holding a deferred item was cleared, or an admitted page kept its body")
	}
	// A whole-account sync binds the deferred copy: the next admission resolves it and clears the page.
	var row int64
	must0(s.db.QueryRowContext(ctx, `select rowid from mail_messages where detail_key=100`).Scan(&row))
	must(s.db.ExecContext(ctx, `insert into workiq_aliases(kind,account_id,namespace,alias,item_rowid) values('mail',?,'graph','cached-copy',?)`, mailAcct, row))
	done := must(s.AdmitWorkIQWindow(ctx, attempt, []string{resource}, 128))
	if done.Resolved != 1 || done.Cleared != 1 || rowCount(t, s, `select count(*) from workiq_pages where data_json<>''`) != 0 {
		t.Fatalf("a bound deferred member was not resolved and cleared: %+v", done)
	}
	// Admitting the same members again changes nothing.
	before := dump(t, s)
	if again := must(s.AdmitWorkIQWindow(ctx, attempt, []string{resource}, 128)); !again.Done || dump(t, s) != before {
		t.Fatal("a repeated admission changed the archive")
	}
	if err := s.RecordWorkIQWindowCoverage(ctx, attempt, []string{"inbox-id"}, true, windowUntil); err != nil {
		t.Fatal(err)
	}
	if rowCount(t, s, `select count(*) from workiq_window_coverage where folder_id in ('inbox-id','') and deferred=0`) != 2 {
		t.Fatal("coverage rows were not recorded")
	}
}

func TestWorkIQWindowAdmissionUpdatesABoundRowWithoutMarkingIt(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	sync := seedCompletedWorkIQMail(t, s)
	must0(s.CompleteWorkIQAttempt(ctx, sync, WorkIQSummary{Status: "ok", FinishedAt: mailT0}))
	attempt := beginWorkIQWindow(t, s)
	resource := WorkIQWindowDeltaResource("inbox-id", windowSince)
	stageWindow(t, s, attempt, "inbox-id", resource, []json.RawMessage{
		windowMail("one", "inbox-id", "one@example.test", windowSince.Add(time.Hour), "2026-09-20T00:00:00Z"),
	}, "", "/me/mailFolders/inbox-id/messages/delta?$deltatoken=x")
	got := must(s.AdmitWorkIQWindow(ctx, attempt, []string{resource}, 128))
	if got.Updated != 1 || got.Admitted != 0 || rowCount(t, s, `select count(*) from workiq_rows where identity_unproven=1`) != 0 ||
		rowCount(t, s, `select count(*) from mail_messages`) != 1 {
		t.Fatalf("a bound row was not updated in place: %+v", got)
	}
}

func TestWorkIQWindowCoverageRefusesUnadmittedMailAndEarlySnapshots(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	attempt := seedWorkIQWindowAttempt(t, s)
	resource := WorkIQWindowDeltaResource("inbox-id", windowSince)
	stageWindow(t, s, attempt, "inbox-id", resource, []json.RawMessage{
		windowMail("in", "inbox-id", "in@example.test", windowSince, "2026-09-01T00:00:00Z"),
	}, "", "/me/mailFolders/inbox-id/messages/delta?$deltatoken=x")
	if err := s.RecordWorkIQWindowCoverage(ctx, attempt, []string{"inbox-id"}, true, windowUntil); err == nil {
		t.Fatal("coverage was recorded over unadmitted mail")
	}
	if err := s.RecordWorkIQWindowCoverage(ctx, attempt, nil, false, windowUntil.Add(-time.Second)); err == nil {
		t.Fatal("a snapshot before the window's end proved it")
	}
	must0(s.RecordWorkIQWindowCoverage(ctx, attempt, nil, false, windowUntil))
	if rowCount(t, s, `select count(*) from workiq_window_coverage`) != 0 {
		t.Fatal("coverage without proven folders was recorded")
	}
}

func TestWorkIQWindowAdmissionRefusesCorruptMembers(t *testing.T) {
	ctx := context.Background()
	for _, mutation := range []string{
		`update workiq_pages set data_json=''`,
		`update workiq_window_members set item_index=7`,
		`update workiq_window_members set graph_id='other'`,
		`update workiq_window_members set observed_at='bad'`,
		`update workiq_pages set data_json='{'`,
	} {
		t.Run(mutation, func(t *testing.T) {
			s := newStore(t)
			attempt := seedWorkIQWindowAttempt(t, s)
			resource := WorkIQWindowDeltaResource("inbox-id", windowSince)
			must0(s.EnsureWorkIQWindowFolders(ctx, attempt, []workiq.Folder{{ID: "inbox-id", Name: "Inbox", WellKnown: "inbox"}}))
			stageWindow(t, s, attempt, "inbox-id", resource, []json.RawMessage{
				windowMail("in", "inbox-id", "in@example.test", windowSince, "2026-09-01T00:00:00Z"),
			}, "", "")
			must(s.db.ExecContext(ctx, mutation))
			before := dump(t, s)
			if _, err := s.AdmitWorkIQWindow(ctx, attempt, []string{resource}, 128); err == nil || dump(t, s) != before {
				t.Fatal("a corrupt member was admitted or left a partial change")
			}
		})
	}
	s := newStore(t)
	attempt := seedWorkIQWindowAttempt(t, s)
	if _, err := s.AdmitWorkIQWindow(ctx, attempt, nil, 0); err == nil {
		t.Fatal("an unbounded admission was accepted")
	}
	if err := s.EnsureWorkIQWindowFolders(ctx, attempt, []workiq.Folder{{ID: "x", WellKnown: "weird"}}); err == nil {
		t.Fatal("an unknown folder kind was accepted")
	}
	if err := s.EnsureWorkIQWindowFolders(ctx, attempt, []workiq.Folder{{ID: "x", ParentID: "x"}}); err == nil {
		t.Fatal("a folder that is its own parent was accepted")
	}
	original := scrubWorkIQProjection
	scrubWorkIQProjection = func([]byte) ([]byte, int) { return []byte(`{`), 0 }
	t.Cleanup(func() { scrubWorkIQProjection = original })
	if err := s.EnsureWorkIQWindowFolders(ctx, attempt, []workiq.Folder{{ID: "x"}}); err == nil || !strings.Contains(err.Error(), "folder identity") {
		t.Fatal("an unretainable folder identity was accepted")
	}
}
