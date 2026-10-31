package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"time"

	"github.com/ourostack/m365crawl/internal/workiq"
)

var workIQHealthNow = time.Now

type workIQHealthOutcome struct {
	id                         int64
	key, family, account, kind string
	status, code               string
	at                         time.Time
	invalid                    bool // the row is not a readable outcome
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
	invalid := map[string]workIQHealthOutcome{} // by key: rows that are not outcomes, unless a valid one follows
	now := workIQHealthNow()
	for _, outcome := range outcomes {
		group := outcome.family + "\x00" + outcome.account
		switch outcome.status {
		case "ok", "ok_with_omissions", "failed", "partial":
		default:
			outcome.invalid = true
		}
		if outcome.invalid || !known[group] || outcome.at.IsZero() || outcome.at.After(now) {
			invalid[outcome.key] = outcome
			continue
		}
		delete(invalid, outcome.key)
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
	kept := native[:0]
	for _, h := range native {
		group := map[string]*workIQHealthState(nil)
		if h.Source == "outlook_mail" && h.AccountID != nil && !h.attemptedAt.IsZero() && !h.attemptedAt.After(now) {
			group = groups["mail\x00"+*h.AccountID]
		}
		if group != nil && group["sync"] != nil {
			through, err := s.workIQCoveredMail(ctx, group["sync"].latest, h.attemptedAt, now)
			if err != nil {
				return nil, err
			}
			if through != nil && h.State == SourceStale {
				continue // WorkIQ holds the mail the stale cache is missing
			}
			if through != nil {
				h.State, h.CoveredThrough = "covered_by_server", through
			}
		}
		kept = append(kept, h)
	}
	native = kept
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
		if selected == nil || invalid[selected.latest.key].key != "" {
			continue // a later unreadable row of the same scope is reported instead
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
		diagnostic, err := s.workIQAttemptDiagnostic(ctx, outcome.id)
		if err != nil {
			return nil, err
		}
		native = append(native, SourceHealth{SourceKey: outcome.key, Source: "workiq", WorkIQFamily: outcome.family,
			AccountID: &account, DoctorCheck: &doctor, State: state, LastErrorCode: outcome.code,
			LastOKAt: healthTime(selected.lastOK), LastDiagnostic: diagnostic})
	}
	bad := make([]string, 0, len(invalid))
	for key := range invalid {
		bad = append(bad, key)
	}
	sort.Strings(bad)
	for _, key := range bad {
		outcome, doctor := invalid[key], "workiq"
		h := SourceHealth{SourceKey: key, Source: "workiq", WorkIQFamily: outcome.family, DoctorCheck: &doctor,
			State: "failing", LastErrorCode: WorkIQOutcomeInvalid}
		if outcome.account != "" {
			h.AccountID = &outcome.account
		}
		native = append(native, h)
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

func workIQDiagnosticKey(attempt int64) string {
	return "workiq_diagnostic:" + strconv.FormatInt(attempt, 10)
}

func workIQStopKey(attempt int64) string {
	return "workiq_stop:" + strconv.FormatInt(attempt, 10)
}

// LastWorkIQStop is where and why the latest settled attempt of one scope (family, account, kind)
// was stopped by an unexplained failure, or "" when it was not, or when the scope has none.
func (s *Store) LastWorkIQStop(ctx context.Context, family, account, kind string) (string, error) {
	var stop sql.NullString
	err := s.db.QueryRowContext(ctx, `select m.value from workiq_attempts a left join meta m on m.key='workiq_stop:'||a.id
where a.family=? and a.account_id=? and a.kind=? and a.status<>'open' order by a.id desc limit 1`, family, account, kind).Scan(&stop)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return stop.String, err
}

// workIQAttemptDiagnostic is the redacted WorkIQ child diagnostic an attempt kept, if any.
func (s *Store) workIQAttemptDiagnostic(ctx context.Context, attempt int64) (*workiq.Diagnostic, error) {
	var raw string
	var d workiq.Diagnostic
	err := s.db.QueryRowContext(ctx, `select value from meta where key=?`, workIQDiagnosticKey(attempt)).Scan(&raw)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil, nil
	case err == nil:
		err = json.Unmarshal([]byte(raw), &d)
	}
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// WorkIQOutcomeInvalid is the reported code of a WorkIQ source whose latest archived outcome row
// cannot be read as one, so that row degrades to its source's failure instead of failing every read.
const WorkIQOutcomeInvalid = "workiq_outcome_invalid"
