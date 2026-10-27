package store

import (
	"context"
	"testing"
	"time"
)

func TestSourceHealthFilteredSuccessDoesNotClearOtherAccounts(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	must0(s.RecordRun(ctx, Run{Source: "synthetic|teams", Status: "ok", Fingerprint: "good", StartedAt: base, FinishedAt: base, SourceAccounts: []string{"tenant/A", "tenant/B"}}))
	must0(s.RecordRun(ctx, Run{Source: "synthetic|teams", Status: "failed", ErrorCode: "internal", StartedAt: base, FinishedAt: base.Add(time.Second)}))
	must0(s.RecordRun(ctx, Run{Source: "synthetic|teams", Status: "ok", StartedAt: base, FinishedAt: base.Add(2 * time.Second), SourceAccounts: []string{"tenant/A"}}))
	rows, err := s.SourceHealth(ctx)
	if err != nil || len(rows) != 1 || rows[0].AccountID == nil || *rows[0].AccountID != "tenant/B" ||
		rows[0].LastOKAt == nil || !rows[0].LastOKAt.Equal(base) {
		t.Fatalf("one account's recovery hid another account's failure: %+v %v", rows, err)
	}
}

func TestSourceHealthRefusalClassification(t *testing.T) {
	// These are source-level refusals/errors, not record omissions or independent attendee/people outcomes.
	for _, tc := range []struct {
		source string
		codes  []string
		state  string
	}{
		{"teams", []string{"teams_not_installed", "no_teams_origin"}, ""},
		{"outlook_mail", []string{"mail_unsupported_platform", "no_profile"}, ""},
		{"outlook_calendar", []string{"no_outlook_profiles", "no_profile"}, ""},
		{"teams", []string{"no_full_disk_access"}, "never_succeeded"},
		{"outlook_mail", []string{"outlook_mail_layout_unsupported", "outlook_store_version", "outlook_layout_unsupported", "outlook_profile_unreadable", "no_full_disk_access"}, "never_succeeded"},
		{"outlook_calendar", []string{"outlook_layout_unsupported", "outlook_store_version", "outlook_version_unsupported", "outlook_profile_unreadable", "no_full_disk_access"}, "never_succeeded"},
		{"teams", []string{"snapshot_inconsistent", "unsupported_block_compression", "store_missing", "db_error", "internal", "interrupted", "future_unknown"}, "failing"},
		{"outlook_mail", []string{"outlook_store_unrecognized", "outlook_blocks_damaged", "outlook_mail_key_exhausted", "snapshot_inconsistent", "unsupported_block_compression", "db_error", "internal", "interrupted", "future_unknown"}, "failing"},
		{"outlook_calendar", []string{"outlook_store_unrecognized", "outlook_blocks_damaged", "snapshot_inconsistent", "unsupported_block_compression", "db_error", "internal", "interrupted", "future_unknown"}, "failing"},
	} {
		for _, code := range tc.codes {
			t.Run(tc.source+"/"+code, func(t *testing.T) {
				if got := SourceFailureState("unavailable", code, false); got != tc.state {
					t.Fatalf("unavailable %s: %q, want %q", code, got, tc.state)
				}
			})
		}
	}
}
func TestSourceHealthOutcomes(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	record := func(source, status, code, fp string, at time.Time) {
		t.Helper()
		must0(s.RecordRun(ctx, Run{Source: source, Status: status, ErrorCode: code, Fingerprint: fp, StartedAt: at, FinishedAt: at}))
	}
	record("outlook|Work|mail", "failed", "outlook_mail_layout_unsupported", "", base)
	rows, err := s.SourceHealth(ctx)
	if err != nil || len(rows) != 1 || rows[0].State != "never_succeeded" ||
		rows[0].AccountID == nil || *rows[0].AccountID != "outlook/Work" ||
		rows[0].LastOKAt != nil || rows[0].NewestItemAt != nil {
		t.Fatalf("first failure: %+v %v", rows, err)
	}
	record("outlook|Work|mail", "skipped_interval", "", "", base.Add(time.Second))
	rows, err = s.SourceHealth(ctx)
	if err != nil || len(rows) != 1 || rows[0].LastErrorCode != "outlook_mail_layout_unsupported" {
		t.Fatalf("skip erased failure: %+v %v", rows, err)
	}
	record("outlook|Work|mail", "unchanged", "", "unqualified", base.Add(2*time.Second))
	rows, err = s.SourceHealth(ctx)
	if err != nil || len(rows) != 1 {
		t.Fatalf("unqualified fingerprint erased failure: %+v %v", rows, err)
	}
	record("outlook|Work|mail", "ok", "", "good", base.Add(3*time.Second))
	record("outlook|Work|mail", "failed", "outlook_mail_layout_unsupported", "", base.Add(4*time.Second))
	rows, err = s.SourceHealth(ctx)
	if err != nil || len(rows) != 1 || rows[0].State != "failing" || rows[0].LastOKAt == nil ||
		!rows[0].LastOKAt.Equal(base.Add(3*time.Second)) {
		t.Fatalf("last success lost: %+v %v", rows, err)
	}
	record("outlook|Work|mail", "unchanged", "", "good", base.Add(5*time.Second))
	rows, err = s.SourceHealth(ctx)
	if err != nil || len(rows) != 0 {
		t.Fatalf("qualified unchanged did not recover: %+v %v", rows, err)
	}
	record("teams", "unavailable", "future_unknown", "", base)
	rows, err = s.SourceHealth(ctx)
	if err != nil || len(rows) != 1 || rows[0].State != "failing" || rows[0].AccountID != nil || rows[0].DoctorCheck != nil {
		t.Fatalf("unknown refusal was silent or invented identity: %+v %v", rows, err)
	}
	record("teams", "unavailable", "teams_not_installed", "", base)
	rows, err = s.SourceHealth(ctx)
	if err != nil || len(rows) != 0 {
		t.Fatalf("not applicable failure: %+v %v", rows, err)
	}
}
