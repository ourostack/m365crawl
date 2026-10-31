package store

import (
	"context"
	"testing"
	"time"
)

func seedOutlookCalendarSource(t *testing.T, s *Store, account string, synced, fresh time.Time) {
	t.Helper()
	if _, err := s.db.Exec(`insert into calendar_sources(source, account_id, window_start, window_end, synced_at, cache_fresh_at) values('outlook',?,?,?,?,?)`,
		account, fmtTime(synced.Add(-24*time.Hour)), fmtTime(synced.Add(24*time.Hour)), fmtTime(synced), fmtTime(fresh)); err != nil {
		t.Fatal(err)
	}
}

func TestOutlookCacheHealthStaleOnlyWhenACheckFindsAnOldWrite(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	mustCommitMail(t, s, mailBatch(mailT0, mailMsg(1, fInbox, "<1@x>", "One", 1)))
	record := func(status, code string, at time.Time) {
		t.Helper()
		must0(s.RecordRun(ctx, Run{Source: "outlook|profile-1|mail", Status: status, ErrorCode: code, Fingerprint: "fp", StartedAt: at, FinishedAt: at}))
	}
	stale := func() []SourceHealth {
		t.Helper()
		rows, err := s.OutlookCacheHealth(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return rows
	}
	record("ok", "", mailT0)
	record("unchanged", "", mailT0.Add(OutlookCacheStaleAfter))
	if rows := stale(); len(rows) != 0 {
		t.Fatalf("a cache written within the threshold of the check is stale: %+v", rows)
	}
	checked := mailT0.Add(OutlookCacheStaleAfter + time.Minute)
	record("unchanged", "", checked)
	record("skipped_interval", "", checked.Add(time.Minute))
	rows := stale()
	if len(rows) != 1 {
		t.Fatalf("an unchanged check an hour after the last write is not stale: %+v", rows)
	}
	h := rows[0]
	if h.SourceKey != "outlook|profile-1|mail" || h.Source != "outlook_mail" || *h.AccountID != mailAcct || h.State != SourceStale ||
		h.LastErrorCode != OutlookCacheStaleCode || *h.DoctorCheck != "mail_readable" || !h.LastOKAt.Equal(checked) ||
		!h.CacheFreshAt.Equal(mailT0) || h.NewestItemAt == nil {
		t.Fatalf("stale entry: %+v", h)
	}
	if got := OutlookCacheStale(rows, "outlook_mail", mailAcct); got == nil || got.SourceKey != h.SourceKey {
		t.Fatalf("lookup: %+v", got)
	}
	if OutlookCacheStale(rows, "outlook_calendar", mailAcct) != nil || OutlookCacheStale(rows, "outlook_mail", "outlook/other") != nil {
		t.Fatal("lookup matched another source")
	}
	record("failed", "outlook_blocks_damaged", checked.Add(2*time.Minute))
	if rows := stale(); len(rows) != 0 {
		t.Fatalf("a failing source is also reported stale: %+v", rows)
	}
	// A later read of a newly written copy clears it.
	mustCommitMail(t, s, mailBatch(checked.Add(3*time.Minute), mailMsg(1, fInbox, "<1@x>", "One", 1)))
	record("ok", "", checked.Add(3*time.Minute))
	if rows := stale(); len(rows) != 0 {
		t.Fatalf("a fresh read stays stale: %+v", rows)
	}
}

func TestOutlookCacheHealthCalendarUsesItsCacheFreshAt(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	seedOutlookCalendarSource(t, s, "outlook/Work", base, base.Add(-3*time.Hour))
	must0(s.RecordRun(ctx, Run{Source: "outlook|Work", Status: "ok_with_omissions", Fingerprint: "fp", StartedAt: base, FinishedAt: base}))
	must0(s.RecordRun(ctx, Run{Source: "outlook|Work|people", Status: "ok", StartedAt: base, FinishedAt: base}))
	must0(s.RecordRun(ctx, Run{Source: "outlook|Other", Status: "ok", Fingerprint: "fp", StartedAt: base, FinishedAt: base}))
	rows, err := s.OutlookCacheHealth(ctx)
	if err != nil || len(rows) != 1 || rows[0].Source != "outlook_calendar" || *rows[0].AccountID != "outlook/Work" ||
		*rows[0].DoctorCheck != "outlook_store" || !rows[0].CacheFreshAt.Equal(base.Add(-3*time.Hour)) || !rows[0].LastOKAt.Equal(base) {
		t.Fatalf("calendar staleness: %+v %v", rows, err)
	}
}

// Mail and the calendar are read from one store copy, so their write times are one clock. An
// archive from before mail recorded its own uses the calendar's, only from the same read.
func TestMailCacheFreshSharesTheCalendarClock(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	read, fresh := base, base.Add(-2*time.Hour)
	b := mailBatch(read, mailMsg(1, fInbox, "<1@x>", "One", 1))
	b.FreshAt = fresh
	mustCommitMail(t, s, b)
	must0(s.SetMailRead(ctx, mailAcct, read))
	seedOutlookCalendarSource(t, s, mailAcct, read, fresh)
	got, err := s.MailCacheFresh(ctx, mailAcct)
	if err != nil || !got.Equal(fresh) {
		t.Fatalf("mail cache_fresh_at = %v %v, want the calendar's %v", got, err, fresh)
	}
	if _, err := s.db.Exec(`delete from meta where key=?`, mailFreshKey+mailAcct); err != nil {
		t.Fatal(err)
	}
	if got, err := s.MailCacheFresh(ctx, mailAcct); err != nil || !got.Equal(fresh) {
		t.Fatalf("legacy fallback = %v %v", got, err)
	}
	if _, err := s.db.Exec(`update calendar_sources set synced_at=? where account_id=?`, fmtTime(read.Add(time.Minute)), mailAcct); err != nil {
		t.Fatal(err)
	}
	if got, err := s.MailCacheFresh(ctx, mailAcct); err != nil || !got.IsZero() {
		t.Fatalf("a calendar row of another read lent its clock: %v %v", got, err)
	}
}

func TestMailCacheFreshIsTheEarliestAccountInScope(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	if got, err := s.MailCacheFresh(ctx, ""); err != nil || !got.IsZero() {
		t.Fatalf("empty archive: %v %v", got, err)
	}
	mustCommitMail(t, s, mailBatch(base, mailMsg(1, fInbox, "<1@x>", "One", 1)))
	other := mailBatch(base.Add(time.Hour), mailMsg(2, fInbox, "<2@x>", "Two", 1))
	other.Account = "outlook/other"
	mustCommitMail(t, s, other)
	none := mailBatch(base.Add(2*time.Hour), mailMsg(3, fInbox, "<3@x>", "Three", 1))
	none.Account, none.FreshAt = "outlook/unknown", time.Time{}
	mustCommitMail(t, s, none)
	must0(s.SetMailRead(ctx, "outlook/unknown", base.Add(2*time.Hour)))
	for account, want := range map[string]time.Time{"": base, mailAcct: base, "outlook/other": base.Add(time.Hour), "outlook/unknown": {}} {
		if got, err := s.MailCacheFresh(ctx, account); err != nil || !got.Equal(want) {
			t.Errorf("%q: %v %v, want %v", account, got, err, want)
		}
	}
}

func TestOutlookCacheOnArchivesWithoutItsTables(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	mustCommitMail(t, s, mailBatch(base, mailMsg(1, fInbox, "<1@x>", "One", 1)))
	must0(s.RecordRun(ctx, Run{Source: "outlook|profile-1|mail", Status: "unchanged", Fingerprint: "fp", StartedAt: base.Add(3 * time.Hour), FinishedAt: base.Add(3 * time.Hour)}))
	must0(s.RecordRun(ctx, Run{Source: "outlook|profile-1", Status: "ok", Fingerprint: "fp", StartedAt: base.Add(3 * time.Hour), FinishedAt: base.Add(3 * time.Hour)}))
	if _, err := s.db.Exec(`drop table calendar_sources`); err != nil {
		t.Fatal(err)
	}
	rows, err := s.OutlookCacheHealth(ctx)
	if err != nil || len(rows) != 1 || rows[0].Source != "outlook_mail" {
		t.Fatalf("without calendar_sources: %+v %v", rows, err)
	}
	if _, err := s.db.Exec(`drop table meta`); err != nil {
		t.Fatal(err)
	}
	if rows, err := s.OutlookCacheHealth(ctx); err != nil || len(rows) != 0 {
		t.Fatalf("without meta: %+v %v", rows, err)
	}
	if got, err := s.MailCacheFresh(ctx, ""); err != nil || !got.IsZero() {
		t.Fatalf("without meta: %v %v", got, err)
	}
}

func seedOutlookCacheEvidence(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	mustCommitMail(t, s, mailBatch(base, mailMsg(1, fInbox, "<1@x>", "One", 1)))
	seedOutlookCalendarSource(t, s, mailAcct, base, base)
	for _, key := range []string{"outlook|profile-1|mail", "outlook|profile-1"} {
		must0(s.RecordRun(ctx, Run{Source: key, Status: "unchanged", Fingerprint: "fp", StartedAt: base.Add(3 * time.Hour), FinishedAt: base.Add(3 * time.Hour)}))
	}
}

func TestOutlookCacheReadFailuresSurface(t *testing.T) {
	s := newStore(t)
	seedOutlookCacheEvidence(t, s)
	if rows, err := s.OutlookCacheHealth(context.Background()); err != nil || len(rows) != 2 {
		t.Fatalf("seeded staleness: %+v %v", rows, err)
	}
	sweepReadFaults(t, seedOutlookCacheEvidence, func(ctx context.Context, s *Store) error {
		_, err := s.OutlookCacheHealth(ctx)
		return err
	}, 4, 6, 7, 9)
	sweepReadFaults(t, seedOutlookCacheEvidence, func(ctx context.Context, s *Store) error {
		_, err := s.MailCacheFresh(ctx, "")
		return err
	}, 3)
	sweepReadFaults(t, seedOutlookCacheEvidence, func(ctx context.Context, s *Store) error {
		_, err := s.MailStatusOf(ctx, mailAcct)
		return err
	}, 5)
}

func TestCommitMailRecordsTheStoreWriteTime(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	b := mailBatch(base, mailMsg(1, fInbox, "<1@x>", "One", 1))
	b.FreshAt = base.Add(-time.Minute)
	mustCommitMail(t, s, b)
	var v string
	if err := s.db.QueryRowContext(ctx, `select value from meta where key=?`, mailFreshKey+mailAcct).Scan(&v); err != nil || v != fmtTime(b.FreshAt) {
		t.Fatalf("fresh = %q %v", v, err)
	}
}
