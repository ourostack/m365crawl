package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestWorkIQHealthUsesEnabledOwnerAndDataOutcomeBeforeProbes(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	attempt := seedWorkIQMailAttempt(t, s)
	must0(s.CompleteWorkIQAttempt(ctx, attempt, WorkIQSummary{Status: "failed", ErrorCode: "workiq_policy_denied", FinishedAt: mailT0}))
	health := must(s.SourceHealth(ctx))
	if len(health) != 1 || health[0].Source != "workiq" || health[0].AccountID == nil ||
		*health[0].AccountID != mailAcct || health[0].DoctorCheck == nil || *health[0].DoctorCheck != "workiq" ||
		health[0].LastErrorCode != "workiq_policy_denied" {
		t.Fatal("data failure lost its exact WorkIQ owner/doctor/family")
	}
	for _, kind := range []string{"coverage", "status"} {
		probe := must(s.BeginWorkIQAttempt(ctx, WorkIQScope{Family: "mail", Account: mailAcct, Kind: kind}, mailT0.Add(time.Minute)))
		verifyWorkIQTestAttempt(t, s, probe)
		must0(s.CompleteWorkIQAttempt(ctx, probe, WorkIQSummary{Status: "ok", FinishedAt: mailT0.Add(2 * time.Minute)}))
	}
	health = must(s.SourceHealth(ctx))
	if len(health) != 1 || health[0].LastErrorCode != "workiq_policy_denied" || health[0].LastOKAt != nil {
		t.Fatal("coverage/status success cleared or refreshed a known data failure")
	}
	must0(s.SetWorkIQEnabled(ctx, false))
	if health := must(s.SourceHealth(ctx)); len(health) != 0 {
		t.Fatal("disabled WorkIQ still emitted health")
	}
}

func TestWorkIQMalformedHealthKeysAndOwnerScopeCannotBecomeCheckedEmpty(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct{ key, scope string }{
		{`workiq:not-json`, `["outlook/Profile"]`},
		{`workiq:["mail","outlook/Profile"]`, `["outlook/Other"]`},
	} {
		t.Run(tc.key+tc.scope, func(t *testing.T) {
			s := newStore(t)
			must0(s.SetWorkIQEnabled(ctx, true))
			must(s.db.ExecContext(ctx, `insert into sync_runs(source,status,error_code,started_at,finished_at,source_accounts_json) values(?,'failed','workiq_failed',?,?,?)`,
				tc.key, fmtTime(mailT0), fmtTime(mailT0), tc.scope))
			if _, err := s.SourceHealth(ctx); err == nil {
				t.Fatal("malformed WorkIQ outcome/owner became a false checked-empty or mismatched health entry")
			}
		})
	}
}

func TestWorkIQNativeMailCoverageRequiresTheExactRecentDataOutcome(t *testing.T) {
	ctx := context.Background()
	for _, mode := range []string{"covered", "equal-data-clock", "older-data", "exact-24h", "future",
		"recent-original-data", "exact-original-24h", "old-original-recent-completion",
		"later-local-failure", "later-data-failure", "noncovering-same-clock", "probe-success", "disabled", "other-account", "retained-legacy", "legacy-missing-code"} {
		t.Run(mode, func(t *testing.T) {
			s := newStore(t)
			attempt := seedCompletedWorkIQMail(t, s)
			proof := must(s.InspectWorkIQMailCompletion(ctx, attempt))
			finish := mailT0.Add(time.Hour)
			now := finish.Add(time.Hour)
			failed := mailT0.Add(-time.Millisecond)
			account := mailAcct
			switch mode {
			case "equal-data-clock":
				failed = mailT0
			case "older-data":
				failed = mailT0.Add(time.Millisecond)
			case "exact-24h":
				now = finish.Add(24 * time.Hour)
			case "future":
				now = finish.Add(-time.Nanosecond)
			case "recent-original-data":
				now = mailT0.Add(24*time.Hour - time.Nanosecond)
			case "exact-original-24h":
				now = mailT0.Add(24 * time.Hour)
			case "old-original-recent-completion":
				finish = mailT0.Add(48 * time.Hour)
				now = finish.Add(time.Hour)
			case "later-local-failure":
				failed = finish.Add(time.Millisecond)
			case "other-account":
				account = "outlook/Other"
			}
			key := "outlook|" + strings.TrimPrefix(account, "outlook/") + "|mail"
			scope := `["` + account + `"]`
			lastOK := failed.Add(-time.Hour)
			must(s.db.ExecContext(ctx, `insert into sync_runs(started_at,finished_at,source,status,error_code,source_accounts_json)
values(?,?,?,'ok','',?),(?,?,?,'failed','outlook_mail_layout_unsupported',?)`,
				fmtTime(lastOK), fmtTime(lastOK), key, scope,
				fmtTime(failed), fmtTime(failed), key, scope))
			if mode == "retained-legacy" || mode == "legacy-missing-code" {
				must0(s.SetMailFailure(ctx, account, OutlookFailure{Code: "outlook_mail_layout_unsupported"}))
				if mode == "legacy-missing-code" {
					must(s.db.ExecContext(ctx, `update sync_runs set error_code='' where source=? and status='failed'`, key))
				}
			}
			must0(s.CompleteWorkIQAttempt(ctx, attempt, WorkIQSummary{Status: "ok", FinishedAt: finish, MailProof: &proof}))
			if mode == "later-data-failure" || mode == "noncovering-same-clock" || mode == "probe-success" {
				kind, status, code := "sync", "ok", ""
				if mode == "later-data-failure" {
					status, code = "failed", "workiq_failed"
				}
				if mode == "probe-success" {
					kind = "status"
				}
				next := must(s.BeginWorkIQAttempt(ctx, WorkIQScope{Family: "mail", Account: mailAcct, Kind: kind}, finish))
				verifyWorkIQTestAttempt(t, s, next)
				must0(s.CompleteWorkIQAttempt(ctx, next, WorkIQSummary{Status: status, ErrorCode: code, FinishedAt: finish}))
			}
			if mode == "disabled" {
				must0(s.SetWorkIQEnabled(ctx, false))
			}
			old := workIQHealthNow
			workIQHealthNow = func() time.Time { return now }
			t.Cleanup(func() { workIQHealthNow = old })
			rows, err := s.SourceHealth(ctx)
			if mode == "future" {
				if err == nil {
					t.Fatal("future data completion became trusted health")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var native *SourceHealth
			for i := range rows {
				if rows[i].Source == "outlook_mail" {
					native = &rows[i]
				}
			}
			want := "failing"
			if mode == "covered" || mode == "probe-success" || mode == "retained-legacy" ||
				mode == "recent-original-data" || mode == "legacy-missing-code" {
				want = "covered_by_server"
			}
			if native == nil || native.State != want || native.LastErrorCode != "outlook_mail_layout_unsupported" ||
				native.LastOKAt == nil || !native.LastOKAt.Equal(lastOK) {
				t.Fatal("mail health lost original failure/success or trusted the wrong server outcome")
			}
			var public map[string]any
			must0(json.Unmarshal(must(json.Marshal(native)), &public))
			if (public["covered_through"] != nil) != (want == "covered_by_server") {
				t.Fatal("covered clock was fabricated or omitted")
			}
			if want == "covered_by_server" && !native.CoveredThrough.Equal(finish) {
				t.Fatal("coverage replaced actual completion with the original observation clock")
			}
		})
	}
}

func TestWorkIQHealthProbeClassesDoNotEraseEachOthersFailures(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	_ = seedWorkIQMailAttempt(t, s) // open is not a recorded data outcome
	complete := func(kind, status, code string) {
		t.Helper()
		id := must(s.BeginWorkIQAttempt(ctx, WorkIQScope{Family: "mail", Account: mailAcct, Kind: kind}, mailT0))
		verifyWorkIQTestAttempt(t, s, id)
		must0(s.CompleteWorkIQAttempt(ctx, id, WorkIQSummary{Status: status, ErrorCode: code, FinishedAt: mailT0}))
	}
	complete("coverage", "partial", "workiq_partial_counts")
	complete("status", "failed", "workiq_signin_required")
	rows := must(s.SourceHealth(ctx))
	if len(rows) != 1 || rows[0].LastErrorCode != "workiq_signin_required" {
		t.Fatal("latest failed probe lost its own failure")
	}
	complete("status", "ok", "")
	rows = must(s.SourceHealth(ctx))
	if len(rows) != 1 || rows[0].LastErrorCode != "workiq_partial_counts" {
		t.Fatal("successful status erased a different counts/probe failure")
	}
	complete("coverage", "ok", "")
	if rows := must(s.SourceHealth(ctx)); len(rows) != 0 {
		t.Fatal("successful counts/probe recovery retained an already cleared own failure")
	}
}

func seedWorkIQCoveredHealth(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	attempt := seedCompletedWorkIQMail(t, s)
	proof := must(s.InspectWorkIQMailCompletion(ctx, attempt))
	key := "outlook|" + strings.TrimPrefix(mailAcct, "outlook/") + "|mail"
	must(s.db.ExecContext(ctx, `insert into sync_runs(started_at,finished_at,source,status,error_code,source_accounts_json)
values(?,?,?,'failed','outlook_mail_layout_unsupported',?)`,
		fmtTime(mailT0.Add(-time.Hour)), fmtTime(mailT0.Add(-time.Hour)), key, `["`+mailAcct+`"]`))
	must0(s.CompleteWorkIQAttempt(ctx, attempt, WorkIQSummary{Status: "ok", FinishedAt: mailT0.Add(time.Hour), MailProof: &proof}))
}

func TestWorkIQHealthReceiptAndOutcomeCorruptionFailClosed(t *testing.T) {
	ctx := context.Background()
	for _, change := range []string{
		`update workiq_mail_receipts set completed_at='bad'`,
		`update workiq_mail_receipts set source_data_at='bad'`,
		`update sync_runs set finished_at=null where source like 'workiq:%'`,
		`update sync_runs set finished_at='bad' where source like 'workiq:%'`,
		`update sync_runs set status='skipped_interval' where source like 'workiq:%'`,
		`update sync_runs set source='workiq:["mail","outlook/Unknown"]',source_accounts_json='["outlook/Unknown"]' where source like 'workiq:%'`,
	} {
		t.Run(change, func(t *testing.T) {
			s := newStore(t)
			seedWorkIQCoveredHealth(t, s)
			old := workIQHealthNow
			workIQHealthNow = func() time.Time { return mailT0.Add(2 * time.Hour) }
			t.Cleanup(func() { workIQHealthNow = old })
			must(s.db.ExecContext(ctx, change))
			if _, err := s.SourceHealth(ctx); err == nil {
				t.Fatal("corrupt source/receipt evidence became checked-empty or covering health")
			}
		})
	}
}

func TestWorkIQHealthReadFaultsDoNotInventCheckedOrCoveredState(t *testing.T) {
	old := workIQHealthNow
	workIQHealthNow = func() time.Time { return mailT0.Add(2 * time.Hour) }
	t.Cleanup(func() { workIQHealthNow = old })
	t.Run("typed failure health", func(t *testing.T) {
		sweepReadFaults(t, func(t *testing.T, s *Store) {
			attempt := seedWorkIQMailAttempt(t, s)
			must0(s.CompleteWorkIQAttempt(context.Background(), attempt,
				WorkIQSummary{Status: "failed", ErrorCode: "workiq_failed", FinishedAt: mailT0}))
		}, func(ctx context.Context, s *Store) error {
			_, err := s.SourceHealth(ctx)
			return err
		})
	})
	t.Run("typed receipt fields", func(t *testing.T) {
		var outcome workIQHealthOutcome
		sweepReadFaults(t, func(t *testing.T, s *Store) {
			seedWorkIQCoveredHealth(t, s)
			var run int64
			must0(s.db.QueryRow(`select sync_run_id from workiq_mail_receipts`).Scan(&run))
			outcome = workIQHealthOutcome{id: run, account: mailAcct, family: "mail", kind: "sync",
				status: "ok", at: mailT0.Add(time.Hour)}
		}, func(ctx context.Context, s *Store) error {
			_, err := s.workIQCoveredMail(ctx, outcome, mailT0.Add(-time.Hour), mailT0.Add(2*time.Hour))
			return err
		})
	})
	// Native newest-item MAX is legitimately nullable; it is not a driver
	// failure and cannot invalidate independent exact data receipt evidence.
	// Exercise all actual read-error paths through the full covered answer,
	// and all typed source/receipt NULL conversions above without exemptions.
	for _, kind := range []string{"prepare", "query", "next", "rowsclose"} {
		t.Run("covered-"+kind, func(t *testing.T) {
			s := newStore(t)
			seedWorkIQCoveredHealth(t, s)
			inj := injectFaults(t, s)
			sweepKind(t, s, inj, kind, false, func(ctx context.Context, s *Store) error {
				_, err := s.SourceHealth(ctx)
				return err
			}, nil)
		})
	}
}

func TestWorkIQEnabledOpenAttemptDoesNotInventACompletedOutcome(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	_ = seedWorkIQMailAttempt(t, s)
	if rows := must(s.SourceHealth(ctx)); len(rows) != 0 {
		t.Fatal("enabled but unsettled source invented completed health")
	}
}

func TestWorkIQReceiptCannotCoverANativeFailureWithUnknownClock(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	attempt := seedCompletedWorkIQMail(t, s)
	proof := must(s.InspectWorkIQMailCompletion(ctx, attempt))
	must0(s.CompleteWorkIQAttempt(ctx, attempt, WorkIQSummary{Status: "ok", FinishedAt: mailT0.Add(time.Hour), MailProof: &proof}))
	must0(s.SetMailFailure(ctx, mailAcct, OutlookFailure{Code: "outlook_mail_layout_unsupported"}))
	old := workIQHealthNow
	workIQHealthNow = func() time.Time { return mailT0.Add(2 * time.Hour) }
	t.Cleanup(func() { workIQHealthNow = old })
	rows := must(s.SourceHealth(ctx))
	if len(rows) != 1 || rows[0].Source != "outlook_mail" || rows[0].State == "covered_by_server" ||
		rows[0].CoveredThrough != nil || rows[0].LastErrorCode != "outlook_mail_layout_unsupported" {
		t.Fatal("unknown native failure clock was replaced by a server freshness claim")
	}
}

// A stale Outlook mail cache is a read warning unless a WorkIQ sync covered every folder after the
// cache check found it stale; failures-only health never carries the stale entry.
func TestReadSourceHealthLetsWorkIQCoverAStaleMailCache(t *testing.T) {
	ctx := context.Background()
	for _, covers := range []bool{true, false} {
		s := newStore(t)
		attempt := seedCompletedWorkIQMail(t, s)
		proof := must(s.InspectWorkIQMailCompletion(ctx, attempt))
		finish := mailT0.Add(time.Hour)
		must0(s.CompleteWorkIQAttempt(ctx, attempt, WorkIQSummary{Status: "ok", FinishedAt: finish, MailProof: &proof}))
		must(s.db.ExecContext(ctx, `insert into meta(key,value) values(?,?) on conflict(key) do update set value=excluded.value`,
			mailFreshKey+mailAcct, fmtTime(mailT0.Add(-3*time.Hour))))
		checked := mailT0.Add(-time.Millisecond) // WorkIQ's data (observed at mailT0) is newer than the check
		if !covers {
			checked = mailT0.Add(time.Millisecond)
		}
		must0(s.RecordRun(ctx, Run{Source: "outlook|" + strings.TrimPrefix(mailAcct, "outlook/") + "|mail", Status: "ok", Fingerprint: "fp", StartedAt: checked, FinishedAt: checked}))
		old := workIQHealthNow
		workIQHealthNow = func() time.Time { return finish.Add(time.Hour) }
		stale := OutlookCacheStale(must(s.ReadSourceHealth(ctx)), "outlook_mail", mailAcct)
		workIQHealthNow = old
		if covers && stale != nil || !covers && (stale == nil || !stale.LastOKAt.Equal(checked)) {
			t.Fatalf("covers=%t: stale entry %+v", covers, stale)
		}
		if OutlookCacheStale(must(s.SourceHealth(ctx)), "outlook_mail", mailAcct) != nil {
			t.Fatal("failures-only health carried a stale cache entry")
		}
	}
}
