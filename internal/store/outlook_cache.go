package store

import (
	"context"
	"database/sql"
	"time"
)

// OutlookCacheStaleAfter is how long Outlook may have gone without writing its local cache, as seen
// by a sync that checked the cache, before the archive calls the cache stale. A running new Outlook
// for Mac rewrote its store every 16 seconds (median; 51 seconds at most) over 15 minutes sampled
// on 2026-10-30, far more often than mail arrived, so an hour with no write at a check means Outlook
// was closed, asleep or not syncing, not that the mailbox was quiet. A sync reads Outlook at most
// every five minutes (OutlookMinReadInterval), well inside the hour.
const OutlookCacheStaleAfter = time.Hour

// OutlookCacheStaleCode is the last_error_code of a stale entry. It names a condition of the
// source, not a failed read: the read succeeded and found the cache unchanged.
const OutlookCacheStaleCode = "outlook_cache_stale"

// SourceStale is the source_health state of an Outlook source whose cache is stale.
const SourceStale = "stale"

// outlookCacheTables says whether the tables that hold Outlook's cache write times exist: an
// archive from before mail or before the calendar has neither.
func (s *Store) outlookCacheTables(ctx context.Context) (meta, sources bool, err error) {
	err = s.db.QueryRowContext(ctx, `select exists(select 1 from sqlite_master where name='meta'),
		exists(select 1 from sqlite_master where name='calendar_sources')`).Scan(&meta, &sources)
	return meta, sources, err
}

// mailFreshAt is the store write time behind the account's last committed mail read. An archive
// written before this time was recorded for mail falls back to the Outlook calendar's
// cache_fresh_at, but only when the calendar row comes from the same read as the mail read marker
// (the two are stamped with the same read start), so both are the same store copy's clock.
func (s *Store) mailFreshAt(ctx context.Context, account string, meta, sources bool) (time.Time, error) {
	if !meta {
		return time.Time{}, nil
	}
	query := `select (select value from meta where key=?)`
	args := []any{mailFreshKey + account}
	if sources {
		query = `select coalesce((select value from meta where key=?), (select cache_fresh_at from calendar_sources
			where source='outlook' and account_id=? and synced_at=(select value from meta where key=?)))`
		args = append(args, account, mailReadKey+account)
	}
	var fresh sql.NullString
	err := s.db.QueryRowContext(ctx, query, args...).Scan(&fresh)
	return parseTime(fresh), err
}

// MailCacheFresh is the earliest store write time behind the last committed mail read of the
// accounts that account names (every mail account when it is empty), so mail written to Outlook
// after it may be missing from at least one of them. It is zero when no write time is known.
func (s *Store) MailCacheFresh(ctx context.Context, account string) (time.Time, error) {
	meta, sources, err := s.outlookCacheTables(ctx)
	if err != nil || !meta {
		return time.Time{}, err
	}
	var accounts []string
	err = s.each(ctx, `select distinct substr(key, instr(key, ':')+1) from meta where key like ? escape '\' or key like ? escape '\'`, func(rs *sql.Rows) error {
		var a string
		if err := rs.Scan(&a); err != nil {
			return err
		}
		if account == "" || a == account {
			accounts = append(accounts, a)
		}
		return nil
	}, escapeLike(mailFreshKey)+"%", escapeLike(mailReadKey)+"%")
	if err != nil {
		return time.Time{}, err
	}
	var out time.Time
	for _, a := range accounts {
		fresh, err := s.mailFreshAt(ctx, a, meta, sources)
		if err != nil {
			return time.Time{}, err
		}
		if !fresh.IsZero() && (out.IsZero() || fresh.Before(out)) {
			out = fresh
		}
	}
	return out, nil
}

// OutlookCacheHealth reports each Outlook mail or calendar source whose latest attempt succeeded
// (`ok`, `ok_with_omissions` or `unchanged`) and found that Outlook had not written its local cache
// for more than OutlookCacheStaleAfter. `unchanged` proves the cache is the copy last read, not that
// Outlook is current: nothing written to Outlook after the cache's write time can be in the archive.
// The age is measured at that check, never at the time of this call, so an archive that has not
// synced for a while is not called stale on that account (its archive age says so). Failing
// sources are left to SourceHealth.
func (s *Store) OutlookCacheHealth(ctx context.Context) ([]SourceHealth, error) {
	meta, sources, err := s.outlookCacheTables(ctx)
	if err != nil {
		return nil, err
	}
	type check struct {
		key string
		at  time.Time
	}
	var checks []check
	err = s.each(ctx, `select r.source, r.finished_at from sync_runs r join (select max(id) id from sync_runs
		where source like 'outlook|%' and status<>'skipped_interval' group by source) l on r.id=l.id
		where r.status in ('ok','ok_with_omissions','unchanged') order by r.source`, func(rs *sql.Rows) error {
		var c check
		var at sql.NullString
		if err := rs.Scan(&c.key, &at); err != nil {
			return err
		}
		c.at = parseTime(at)
		checks = append(checks, c)
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := []SourceHealth{}
	for _, c := range checks {
		family, account := SourceFamily(c.key)
		var fresh time.Time
		switch {
		case family == "outlook_mail":
			fresh, err = s.mailFreshAt(ctx, account, meta, sources)
		case family == "outlook_calendar" && sources:
			var ns sql.NullString
			err = s.db.QueryRowContext(ctx, `select (select cache_fresh_at from calendar_sources where source='outlook' and account_id=?)`, account).Scan(&ns)
			fresh = parseTime(ns)
		}
		if err != nil {
			return nil, err
		}
		if fresh.IsZero() || c.at.Sub(fresh) <= OutlookCacheStaleAfter {
			continue
		}
		doctor := "outlook_store"
		if family == "outlook_mail" {
			doctor = "mail_readable"
		}
		h := SourceHealth{SourceKey: c.key, Source: family, AccountID: &account, DoctorCheck: &doctor, State: SourceStale,
			LastErrorCode: OutlookCacheStaleCode, LastOKAt: &c.at, CacheFreshAt: &fresh}
		if h.NewestItemAt, err = s.sourceNewestItem(ctx, family, account); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, nil
}

// OutlookCacheStale finds the stale entry of one Outlook source family and account in health.
func OutlookCacheStale(health []SourceHealth, family, account string) *SourceHealth {
	for i, h := range health {
		if h.Source == family && h.State == SourceStale && h.AccountID != nil && *h.AccountID == account {
			return &health[i]
		}
	}
	return nil
}
