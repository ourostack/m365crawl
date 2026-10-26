package store

import (
	"context"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/calendar"
	"github.com/ourostack/m365crawl/internal/teamsdesktop"
)

func TestSourceHealthLegacyFailureRetainsSuccessAndUsesArchiveClock(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	// The appointment is in 2031, but the archive was written in 2026.
	commitOutlook(t, s, time.UTC, "synthetic", outlookEvent(calendar.TriFalse, base))
	must0(s.SetOutlookFailure(ctx, "outlook/Main", &OutlookFailure{Code: "outlook_layout_unsupported"}))
	rows, err := s.SourceHealth(ctx)
	if err != nil || len(rows) != 1 || rows[0].LastOKAt == nil || !rows[0].LastOKAt.Equal(base) ||
		rows[0].NewestItemAt == nil || !rows[0].NewestItemAt.Equal(base) {
		t.Fatalf("legacy refusal lost success or used calendar start time: %+v %v", rows, err)
	}
}

func TestSourceHealthLegacyMailAndMissingMetadata(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	must0(s.SetMailFailure(ctx, "outlook/Main", OutlookFailure{Code: "outlook_mail_layout_unsupported"}))
	rows, err := s.SourceHealth(ctx)
	if err != nil || len(rows) != 1 || rows[0].State != "never_succeeded" || rows[0].NewestItemAt != nil {
		t.Fatalf("legacy mail refusal: %+v %v", rows, err)
	}
	must0(s.runInTx(ctx, []string{`alter table sync_runs drop column error_code`, `drop table meta`}))
	rows, err = s.SourceHealth(ctx)
	if err != nil || len(rows) != 0 {
		t.Fatalf("old empty archive: %+v %v", rows, err)
	}
}

func TestSourceHealthRejectsCorruptOutcomeAndFailure(t *testing.T) {
	ctx := context.Background()
	for _, setup := range []string{
		`insert into sync_runs(started_at,finished_at,source,status,source_accounts_json) values('2026-09-01','2026-09-01','teams','failed','invalid')`,
		`insert into meta values('outlook_mail_failure:outlook/Main','invalid')`,
	} {
		s := newStore(t)
		if _, err := s.db.Exec(setup); err != nil {
			t.Fatal(err)
		}
		if _, err := s.SourceHealth(ctx); err == nil {
			t.Fatal("corrupt health evidence became a successful answer")
		}
	}
}

func TestSourceHealthFamilyExcludesIndependentObservations(t *testing.T) {
	for _, tc := range []struct{ key, family, account string }{
		{"", "", ""}, {"outlook", "outlook_calendar", ""},
		{"outlook_mail", "outlook_mail", ""},
		{"outlook|Main", "outlook_calendar", "outlook/Main"},
		{"outlook|Main|mail", "outlook_mail", "outlook/Main"},
		{"outlook|Main|people", "", ""}, {"outlook|Main|attendees", "", ""},
		{"Profile|https_teams.example.test", "teams", ""},
	} {
		family, account := SourceFamily(tc.key)
		if family != tc.family || account != tc.account {
			t.Fatalf("%q: %q %q", tc.key, family, account)
		}
	}
}

func seedSourceHealthEvidence(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	upsert(t, s, []teamsdesktop.GenericRecord{grec(&acctA, "Synthetic", "Rows", `"key"`, `{}`)}, base)
	must0(s.RecordRun(ctx, Run{Source: srcA, Status: "failed", ErrorCode: "internal", StartedAt: base, FinishedAt: base}))
	must0(s.RecordRun(ctx, Run{Source: "outlook|Main|people", Status: "failed", ErrorCode: "internal", StartedAt: base, FinishedAt: base}))
	must0(s.SetMailFailure(ctx, "outlook/Main", OutlookFailure{Code: "outlook_mail_layout_unsupported"}))
}

func TestSourceHealthReadFailuresSurface(t *testing.T) {
	sweepReadFaults(t, seedSourceHealthEvidence, func(ctx context.Context, s *Store) error {
		_, err := s.SourceHealth(ctx)
		return err
	}, 6, 8)
}

func TestSourceHealthPrincipalUsesActiveLink(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	_, err := s.db.Exec(`insert into calendar_account_links(source,account_id,principal_id,method,linked_at) values('outlook','outlook/Main','tenant/user','config',?)`, fmtTime(base))
	must0(err)
	if got, err := s.SourcePrincipal(ctx, "outlook/Main"); err != nil || got != "tenant/user" {
		t.Fatalf("active link: %q %v", got, err)
	}
	if got, err := s.SourcePrincipal(ctx, "outlook/Other"); err != nil || got != "outlook/Other" {
		t.Fatalf("unlinked account: %q %v", got, err)
	}
}

func TestSourceHealthKnownDiscoveryReadFailuresSurface(t *testing.T) {
	sweepReadFaults(t, func(t *testing.T, s *Store) {
		seedReadable(t, s)
		must0(s.RecordRun(context.Background(), Run{Source: "teams", Status: "failed", ErrorCode: "no_full_disk_access", StartedAt: base, FinishedAt: base}))
	}, func(ctx context.Context, s *Store) error {
		_, err := s.SourceHealth(ctx)
		return err
	}, 6, 7)
}

func TestSourceHealthScopedUnknownReadFailuresSurface(t *testing.T) {
	sweepReadFaults(t, func(t *testing.T, s *Store) {
		must0(s.RecordRun(context.Background(), Run{Source: "synthetic|unknown", SourceAccounts: []string{"tenant/missing"}, Status: "failed", ErrorCode: "internal", StartedAt: base, FinishedAt: base}))
		rows, err := s.SourceHealth(context.Background())
		if err != nil || len(rows) != 1 || rows[0].AccountID != nil {
			t.Fatalf("unobserved filter became identity: %+v %v", rows, err)
		}
	}, func(ctx context.Context, s *Store) error {
		_, err := s.SourceHealth(ctx)
		return err
	}, 5)
}
