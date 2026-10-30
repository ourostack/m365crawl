package store

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"time"
)

var workIQHealthNow = time.Now

type workIQHealthOutcome struct {
	id                         int64
	key, family, account, kind string
	status, code               string
	at                         time.Time
}

type workIQHealthState struct {
	latest workIQHealthOutcome
	lastOK time.Time
}

func (s *Store) withWorkIQHealth(ctx context.Context, native []SourceHealth, outcomes []workIQHealthOutcome) ([]SourceHealth, error) {
	if len(outcomes) == 0 {
		var meta bool
		if err := s.db.QueryRowContext(ctx, `select exists(select 1 from sqlite_master where name='meta')`).Scan(&meta); err != nil {
			return nil, err
		}
		if !meta {
			return native, nil
		}
	}
	enabled, err := s.WorkIQEnabled(ctx)
	if err != nil {
		return nil, err
	}
	if !enabled {
		return native, nil
	}
	for i := range native {
		if native[i].Source == "outlook_mail" {
			native[i].WorkIQEnabled = true
		}
	}
	if len(outcomes) == 0 {
		return native, nil
	}
	facts, err := s.WorkIQAccounts(ctx)
	if err != nil {
		return nil, err
	}
	known := map[string]bool{}
	for _, fact := range facts {
		known[fact.Family+"\x00"+fact.ID] = true
	}
	groups := map[string]map[string]*workIQHealthState{}
	now := workIQHealthNow()
	for _, outcome := range outcomes {
		group := outcome.family + "\x00" + outcome.account
		if !known[group] || outcome.at.IsZero() || outcome.at.After(now) {
			return nil, errors.New("invalid archived WorkIQ outcome owner or clock")
		}
		switch outcome.status {
		case "ok", "ok_with_omissions", "failed", "partial":
		default:
			return nil, errors.New("invalid archived WorkIQ outcome state")
		}
		if groups[group] == nil {
			groups[group] = map[string]*workIQHealthState{}
		}
		state := groups[group][outcome.kind]
		if state == nil {
			state = &workIQHealthState{}
			groups[group][outcome.kind] = state
		}
		state.latest = outcome
		if outcome.status == "ok" || outcome.status == "ok_with_omissions" {
			state.lastOK = outcome.at
		}
	}
	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for i := range native {
		h := &native[i]
		if h.Source != "outlook_mail" || h.AccountID == nil || h.attemptedAt.IsZero() || h.attemptedAt.After(now) {
			continue
		}
		group := groups["mail\x00"+*h.AccountID]
		if group == nil || group["sync"] == nil {
			continue
		}
		through, err := s.workIQCoveredMail(ctx, group["sync"].latest, h.attemptedAt, now)
		if err != nil {
			return nil, err
		}
		if through != nil {
			h.State, h.CoveredThrough = "covered_by_server", through
		}
	}
	for _, key := range keys {
		group := groups[key]
		selected := group["sync"]
		if selected == nil {
			// Different probe classes cannot clear one another's failures.
			for _, kind := range []string{"coverage", "status"} {
				candidate := group[kind]
				if candidate != nil && (candidate.latest.status == "failed" || candidate.latest.status == "partial") &&
					(selected == nil || candidate.latest.id > selected.latest.id) {
					selected = candidate
				}
			}
		}
		if selected == nil {
			continue
		}
		outcome := selected.latest
		status := outcome.status
		if status == "partial" {
			status = "failed"
		}
		state := SourceFailureState(status, outcome.code, !selected.lastOK.IsZero())
		if state == "" {
			continue
		}
		account, doctor := outcome.account, "workiq"
		native = append(native, SourceHealth{SourceKey: outcome.key, Source: "workiq", WorkIQFamily: outcome.family,
			AccountID: &account, DoctorCheck: &doctor, State: state, LastErrorCode: outcome.code,
			LastOKAt: healthTime(selected.lastOK)})
	}
	return native, nil
}

func (s *Store) workIQCoveredMail(ctx context.Context, outcome workIQHealthOutcome, failed, now time.Time) (*time.Time, error) {
	if outcome.status != "ok" || outcome.kind != "sync" || outcome.family != "mail" {
		return nil, nil
	}
	var completed, observed string
	err := s.db.QueryRowContext(ctx, `select r.completed_at,r.source_data_at from workiq_mail_receipts r
join workiq_attempts a on a.id=r.attempt_id
where r.sync_run_id=? and r.account_id=? and a.account_id=r.account_id
and a.family='mail' and a.kind='sync' and a.status='ok' and a.all_folders=1
and a.gap_count=0 and r.enumeration_key<>''`, outcome.id, outcome.account).Scan(&completed, &observed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	done, err := workIQStoredTime(sql.NullString{String: completed, Valid: true})
	if err != nil {
		return nil, err
	}
	data, err := workIQStoredTime(sql.NullString{String: observed, Valid: true})
	if err != nil {
		return nil, err
	}
	if done.IsZero() || data.IsZero() || !done.Equal(outcome.at) || done.After(now) ||
		done.Before(*data) || !data.After(failed) || !done.After(failed) ||
		now.Sub(*done) >= 24*time.Hour || now.Sub(*data) >= 24*time.Hour {
		return nil, nil
	}
	return done, nil
}
