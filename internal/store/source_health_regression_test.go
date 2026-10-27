package store

import (
	"context"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/calendar"
	"github.com/ourostack/m365crawl/internal/outlookmail"
)

func TestReview248LegacySchemaRetainsLastSuccess(t *testing.T) {
	ctx := context.Background()
	path := writableArchivePath(t)
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	commitOutlook(t, s, time.UTC, "review", outlookEvent(calendar.TriFalse, base))
	must0(s.SetOutlookFailure(ctx, "outlook/Main", &OutlookFailure{Code: "outlook_layout_unsupported"}))
	must0(s.runInTx(ctx, []string{
		`alter table sync_runs drop column error_code`,
		`alter table sync_runs drop column source_accounts_json`,
		`update schema_migrations set version=19`,
	}))
	must0(s.Close())
	s, err = OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	rows, err := s.SourceHealth(ctx)
	if err != nil || len(rows) != 1 {
		t.Fatalf("health=%+v err=%v", rows, err)
	}
	if rows[0].LastOKAt == nil || !rows[0].LastOKAt.Equal(base) || rows[0].State != "failing" {
		t.Fatalf("schema-19 successful read was discarded: state=%s last_ok_at=%v; want failing at %s", rows[0].State, rows[0].LastOKAt, base)
	}
}

func TestReview248LegacyTeamsFailureStaysVisible(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	must0(s.ApplyAccount(ctx, acctA))
	must0(s.RecordRun(ctx, Run{Source: srcA, Status: "failed", StartedAt: base, FinishedAt: base}))
	must0(s.runInTx(ctx, []string{
		`alter table sync_runs drop column error_code`,
		`alter table sync_runs drop column source_accounts_json`,
		`update schema_migrations set version=19`,
	}))
	rows, err := s.SourceHealth(ctx)
	if err != nil || len(rows) != 1 {
		t.Fatalf("recorded legacy failed outcome was hidden: health=%+v err=%v", rows, err)
	}
}

func TestReview248HistoricalTeamsSuccessSurvivesScopedFailure(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	must0(s.ApplyAccount(ctx, acctA))
	account := acctA.TenantID + "/" + acctA.UserID
	must0(s.RecordRun(ctx, Run{Source: srcA, Status: "ok", Fingerprint: "historical-success", StartedAt: base, FinishedAt: base}))
	must0(s.RecordRun(ctx, Run{Source: srcA, Status: "failed", ErrorCode: "store_missing", SourceAccounts: []string{account}, StartedAt: base.Add(time.Hour), FinishedAt: base.Add(time.Hour)}))
	rows, err := s.SourceHealth(ctx)
	if err != nil || len(rows) != 1 {
		t.Fatalf("health=%+v err=%v", rows, err)
	}
	if rows[0].LastOKAt == nil || !rows[0].LastOKAt.Equal(base) || rows[0].State != "failing" {
		t.Fatalf("historical success was discarded on first scoped outcome: state=%s last_ok_at=%v", rows[0].State, rows[0].LastOKAt)
	}
}

func TestReview248MailBodyModificationAdvancesNewestItem(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	m := mailMsg(1, fInbox, "<review@example.test>", "Synthetic review", 1)
	m.Body = outlookmail.Body{State: outlookmail.BodyNone}
	mustCommitMail(t, s, mailBatch(mailT0, m))
	written := mailT0.Add(time.Hour)
	m.Body = outlookmail.Body{State: outlookmail.BodyInline, HTML: []byte("<p>Now cached</p>")}
	result := mustCommitMail(t, s, mailBatch(written, m))
	if result.Updated != 1 {
		t.Fatalf("body update did not happen: %+v", result)
	}

	must0(s.RecordRun(ctx, Run{Source: "outlook|profile-1|mail", Status: "failed", ErrorCode: "outlook_mail_layout_unsupported", StartedAt: written.Add(time.Hour), FinishedAt: written.Add(time.Hour)}))
	rows, err := s.SourceHealth(ctx)
	if err != nil || len(rows) != 1 {
		t.Fatalf("health=%+v err=%v", rows, err)
	}
	if rows[0].NewestItemAt == nil || !rows[0].NewestItemAt.Equal(written) {
		t.Fatalf("actual archive body modification at %s is missing from newest_item_at=%v", written, rows[0].NewestItemAt)
	}
}

func TestSourceHealthHistoricalFingerprintSurvivesIdentityResolution(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	must0(s.ApplyAccount(ctx, acctA))
	must0(s.ApplyAccount(ctx, acctB))
	a, b := acctA.TenantID+"/"+acctA.UserID, acctB.TenantID+"/"+acctB.UserID
	for _, row := range []Run{
		{Status: "ok", Fingerprint: "historic"},
		{Status: "failed", ErrorCode: "internal", SourceAccounts: []string{a}},
		{Status: "failed", ErrorCode: "internal", SourceAccounts: []string{b}},
		{Status: "unchanged", Fingerprint: "historic", SourceAccounts: []string{a}},
	} {
		row.Source, row.StartedAt, row.FinishedAt = srcA, base, base
		must0(s.RecordRun(ctx, row))
	}
	rows, err := s.SourceHealth(ctx)
	if err != nil || len(rows) != 1 || rows[0].AccountID == nil || *rows[0].AccountID != b ||
		rows[0].LastOKAt == nil || !rows[0].LastOKAt.Equal(base) {
		t.Fatalf("historic qualification lost or A recovery cleared B: %+v %v", rows, err)
	}
}

func TestSourceHealthLegacyReadOnlyFailureKeepsUnknownCodeAndSchema(t *testing.T) {
	ctx := context.Background()
	path := writableArchivePath(t)
	s, err := Open(ctx, path)
	must0(err)
	must0(s.RecordRun(ctx, Run{Source: srcA, Status: "failed", StartedAt: base, FinishedAt: base}))
	must0(s.runInTx(ctx, []string{
		`alter table sync_runs drop column error_code`,
		`alter table sync_runs drop column source_accounts_json`,
		`update schema_migrations set version=19`,
	}))
	must0(s.Close())
	s, err = OpenReadOnly(ctx, path)
	must0(err)
	defer func() { _ = s.Close() }()
	rows, err := s.SourceHealth(ctx)
	if err != nil || len(rows) != 1 || rows[0].LastErrorCode != "" || rows[0].AccountID != nil {
		t.Fatalf("legacy missing evidence was hidden or invented: %+v %v", rows, err)
	}
	var version, columns int
	must0(s.db.QueryRow(`select max(version) from schema_migrations`).Scan(&version))
	must0(s.db.QueryRow(`select count(*) from pragma_table_info('sync_runs') where name in ('error_code','source_accounts_json')`).Scan(&columns))
	if version != 19 || columns != 0 {
		t.Fatalf("read migrated schema: version=%d new columns=%d", version, columns)
	}
}

func TestSourceHealthHistoricalSuccessDoesNotInventFilteredIdentity(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	must0(s.RecordRun(ctx, Run{Source: srcA, Status: "ok", Fingerprint: "historic", StartedAt: base, FinishedAt: base}))
	must0(s.RecordRun(ctx, Run{Source: srcA, Status: "failed", ErrorCode: "internal", SourceAccounts: []string{"tenant/never-observed"}, StartedAt: base, FinishedAt: base.Add(time.Hour)}))
	rows, err := s.SourceHealth(ctx)
	if err != nil || len(rows) != 1 || rows[0].AccountID != nil {
		t.Fatalf("inherited success invented an identity from a filter: %+v %v", rows, err)
	}
}
