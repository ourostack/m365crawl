package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/workiq"
)

// admitWindowCopy admits one server copy through a window, as a new unproven row.
func admitWindowCopy(t *testing.T, s *Store, id, header string, observed time.Time) int64 {
	t.Helper()
	ctx := context.Background()
	attempt := beginWorkIQWindow(t, s)
	must0(s.EnsureWorkIQWindowFolders(ctx, attempt, []workiq.Folder{{ID: "inbox-id", Name: "Inbox", WellKnown: "inbox"}}))
	resource := WorkIQWindowDeltaResource("inbox-id", windowSince)
	p := must(s.WorkIQWindowProgressFor(ctx, attempt, resource))
	page := windowPage(t, []json.RawMessage{windowMail(id, "inbox-id", header, windowSince.Add(time.Hour), "2026-09-01T01:00:00Z")}, "", "")
	_ = must(s.StageWorkIQWindowPage(ctx, attempt, "inbox-id", resource, p.Revision, page, observed))
	_ = must(s.AdmitWorkIQWindow(ctx, attempt, []string{resource}, 128))
	must0(s.CompleteWorkIQAttempt(ctx, attempt, WorkIQSummary{Status: "ok", FinishedAt: observed}))
	var row int64
	must0(s.db.QueryRowContext(ctx, `select item_rowid from workiq_rows where graph_id=? and identity_unproven=1`, id).Scan(&row))
	return row
}

func beginWholeAccountAt(t *testing.T, s *Store, at time.Time) int64 {
	t.Helper()
	attempt := must(s.BeginWorkIQAttempt(context.Background(), WorkIQScope{Family: "mail", Account: mailAcct}, at))
	verifyWorkIQTestAttempt(t, s, attempt)
	return attempt
}

func TestWorkIQWholeAccountCensusResolvesWindowRows(t *testing.T) {
	ctx := context.Background()
	later := mailT0.Add(time.Hour)
	for _, tc := range []struct {
		name     string
		observed time.Time
		values   []json.RawMessage
		rows     int
		merged   bool
		gap      int
	}{
		{"same copy clears the mark", mailT0, []json.RawMessage{graphMailFixture("s1", "inbox-id", "one@example.test")}, 1, false, 0},
		{"a twin stays its own row", mailT0, []json.RawMessage{graphMailFixture("s1", "inbox-id", "one@example.test"), graphMailFixture("s2", "inbox-id", "one@example.test")}, 2, false, 1},
		{"a moved copy joins the window row", mailT0, []json.RawMessage{graphMailFixture("s9", "inbox-id", "one@example.test")}, 1, true, 0},
		{"a copy seen during the census never joins", later.Add(time.Minute), []json.RawMessage{graphMailFixture("s9", "inbox-id", "one@example.test")}, 2, false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newStore(t)
			mustCommitMail(t, s, mailBatch(mailT0))
			must0(s.SetWorkIQEnabled(ctx, true))
			window := admitWindowCopy(t, s, "s1", "one@example.test", tc.observed)
			attempt := beginWholeAccountAt(t, s, later)
			prepareRealMailInventory(t, s, attempt, tc.values)
			result := must(s.CommitWorkIQMailGroup(ctx, attempt, "one@example.test"))
			if rowCount(t, s, `select count(*) from mail_messages`) != tc.rows || result.Gaps["workiq_mail_identity_ambiguous"] != tc.gap {
				t.Fatalf("rows %d, gaps %v", rowCount(t, s, `select count(*) from mail_messages`), result.Gaps)
			}
			if tc.merged != (rowCount(t, s, `select count(*) from workiq_aliases where kind='mail' and namespace='graph' and alias in ('s1','s9') and item_rowid=?`, window) == 2) {
				t.Fatal("a moved copy did not join, or a copy joined that must not")
			}
			unproven := rowCount(t, s, `select count(*) from workiq_rows where item_rowid=? and identity_unproven=1`, window)
			if want := map[bool]int{true: 0, false: 1}[tc.merged || len(tc.values) == 2 || tc.name == "same copy clears the mark"]; unproven != want {
				t.Fatalf("window row mark = %d, want %d", unproven, want)
			}
		})
	}
}

func TestWorkIQIncrementalCensusNeitherCarriesNorJoinsWindowRowsWithoutARemoval(t *testing.T) {
	ctx := context.Background()
	for _, removed := range []bool{false, true} {
		t.Run(map[bool]string{false: "no removal", true: "explicit removal"}[removed], func(t *testing.T) {
			s := newStore(t)
			first := seedWorkIQMailAttempt(t, s)
			resource := prepareRealMailInventory(t, s, first, []json.RawMessage{graphMailFixture("base", "inbox-id", "base@example.test")})
			_ = must(s.CommitWorkIQMailGroup(ctx, first, "base@example.test"))
			must0(s.AdmitWorkIQFrontier(ctx, first, resource, 1))
			must0(s.AdmitWorkIQFrontier(ctx, first, "/me/mailFolders?includeHiddenFolders=true", 1))
			must0(s.CompleteWorkIQAttempt(ctx, first, WorkIQSummary{Status: "ok", FinishedAt: mailT0}))
			window := admitWindowCopy(t, s, "s1", "one@example.test", mailT0)
			run := beginWholeAccountAt(t, s, mailT0.Add(time.Hour))
			values := []json.RawMessage{graphMailFixture("s9", "inbox-id", "one@example.test")}
			if removed {
				values = append(values, []byte(`{"id":"s1","@removed":{}}`))
			}
			prepareRealMailInventory(t, s, run, values)
			for after, done := int64(0), false; !done; {
				var err error
				after, done, err = s.CarryWorkIQMailInventory(ctx, run, after, 128)
				must0(err)
			}
			if rowCount(t, s, `select count(*) from workiq_census_members where graph_id='s1' and retained=1`) != 0 {
				t.Fatal("an incremental census carried a window row whole-account never saw")
			}
			result := must(s.CommitWorkIQMailGroup(ctx, run, "one@example.test"))
			joined := rowCount(t, s, `select count(*) from workiq_aliases where kind='mail' and namespace='graph' and alias='s9' and item_rowid=?`, window) == 1
			if joined != removed || (result.Gaps["workiq_mail_identity_ambiguous"] == 1) == removed {
				t.Fatalf("joined %t with gaps %v", joined, result.Gaps)
			}
		})
	}
}

func TestWorkIQAbsenceCensusesSkipUndatedWindowRowsWithoutAMessageID(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	first := beginRealFullWorkIQCensus(t, s, 0, []json.RawMessage{graphMailFixture("one", "inbox-id", "one@example.test")})
	_ = must(s.CommitWorkIQMailGroup(ctx, first, "one@example.test"))
	admitWindowCopy(t, s, "headerless", "", mailT0)
	run := beginRealFullWorkIQCensus(t, s, first, []json.RawMessage{graphMailFixture("one", "inbox-id", "one@example.test")})
	_ = must(s.CommitWorkIQMailGroup(ctx, run, "one@example.test"))
	finishWorkIQCensusBatches(t, s, run)
	next := beginRealFullWorkIQCensus(t, s, run, []json.RawMessage{graphMailFixture("one", "inbox-id", "one@example.test")})
	_ = must(s.CommitWorkIQMailGroup(ctx, next, "one@example.test"))
	must0(s.ConfirmWorkIQMailCensus(ctx, next))
	if rowCount(t, s, `select count(*) from workiq_rows where graph_id='headerless' and server_gone_at is null and identity_unproven=1`) != 1 {
		t.Fatal("an absence census judged a window row it cannot identify")
	}
}

func TestWorkIQCacheMailNeverJoinsAnUnprovenWindowRow(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	mustCommitMail(t, s, mailBatch(mailT0))
	must0(s.SetWorkIQEnabled(ctx, true))
	window := admitWindowCopy(t, s, "s1", "one@example.test", mailT0)
	mustCommitMail(t, s, mailBatch(mailT0, mailMsg(100, fInbox, "<one@example.test>", "Subject", 0)))
	if rowCount(t, s, `select count(*) from mail_messages`) != 2 ||
		rowCount(t, s, `select count(*) from workiq_rows where item_rowid=? and identity_unproven=1 and cache_seen=0`, window) != 1 {
		t.Fatal("a cache message joined a window row no census has vouched for")
	}
}
