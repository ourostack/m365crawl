package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/ourostack/m365crawl/internal/workiq"
)

// SourceHealth discloses a failed source without claiming that the archive is current.
type SourceHealth struct {
	SourceKey     string     `json:"-"`
	Source        string     `json:"source"`
	AccountID     *string    `json:"account_id"`
	DoctorCheck   *string    `json:"doctor_check"`
	State         string     `json:"state"`
	LastErrorCode string     `json:"last_error_code"`
	LastOKAt      *time.Time `json:"last_ok_at"`
	NewestItemAt  *time.Time `json:"newest_item_at"`
	// CacheFreshAt is set only on a stale Outlook entry: when Outlook last wrote its local cache.
	CacheFreshAt   *time.Time `json:"cache_fresh_at,omitempty"`
	WorkIQFamily   string     `json:"-"`
	WorkIQEnabled  bool       `json:"-"`
	CoveredThrough *time.Time `json:"covered_through,omitempty"`
	// LastDiagnostic is the redacted trace the WorkIQ child left on the failure it reports.
	LastDiagnostic *workiq.Diagnostic `json:"last_diagnostic,omitempty"`
	attemptedAt    time.Time
}

// SourceFamily recognizes sync-backed source families, not independent observations.
func SourceFamily(key string) (family, account string) {
	if strings.HasPrefix(key, "workiq:") {
		_, owner, _, ok := WorkIQSourceScope(key)
		if !ok {
			return "", ""
		}
		return "workiq", owner
	}
	if key == "outlook_mail" {
		return "outlook_mail", ""
	}
	if rest, ok := strings.CutPrefix(key, "outlook|"); ok {
		if profile, mail := strings.CutSuffix(rest, "|mail"); mail {
			return "outlook_mail", "outlook/" + profile
		}
		if strings.Contains(rest, "|") {
			return "", ""
		}
		return "outlook_calendar", "outlook/" + rest
	}
	if key == "outlook" {
		return "outlook_calendar", ""
	}
	if key == "" {
		return "", ""
	}
	return "teams", ""
}

// SourceFailureState keeps new unavailable codes visible until they are explicitly classified.
func SourceFailureState(status, code string, succeeded bool) string {
	switch status {
	case "failed":
	case "unavailable":
		switch code {
		case "mail_unsupported_platform", "no_profile", "no_outlook_profiles", "teams_not_installed", "no_teams_origin":
			return ""
		case "outlook_layout_unsupported", "outlook_mail_layout_unsupported", "outlook_version_unsupported",
			"outlook_store_version", "no_full_disk_access", "outlook_profile_unreadable":
		default:
			if !strings.Contains(code, "layout") && !strings.Contains(code, "version") {
				return "failing"
			}
		}
	default:
		return ""
	}
	if succeeded {
		return "failing"
	}
	return "never_succeeded"
}

type sourceAttempt struct {
	status, code     string
	lastOK           time.Time
	at               time.Time
	accountObserved  bool
	goodFingerprints map[string]bool
	byAccount        map[string]*sourceAttempt
}

func (a *sourceAttempt) apply(status, code, fp string, at time.Time, accounts []string) {
	if a.byAccount == nil {
		a.byAccount = map[string]*sourceAttempt{}
	}
	if len(accounts) > 0 {
		if prior := a.byAccount[""]; prior != nil {
			// Identity resolution must retain successful aggregate history, not its unknown failure.
			a.lastOK, a.goodFingerprints = prior.lastOK, prior.goodFingerprints
		}
		delete(a.byAccount, "") // a known identity supersedes the pre-identity attempt
	} else {
		for account := range a.byAccount {
			accounts = append(accounts, account)
		}
		if len(accounts) == 0 {
			accounts = []string{""}
		}
	}
	for _, account := range accounts {
		state := a.byAccount[account]
		if state == nil {
			state = &sourceAttempt{lastOK: a.lastOK, goodFingerprints: map[string]bool{}}
			for fp := range a.goodFingerprints {
				state.goodFingerprints[fp] = true
			}
			a.byAccount[account] = state
		}
		if status == "skipped_interval" || status == "unchanged" && (fp == "" || !state.goodFingerprints[fp]) {
			continue
		}
		state.status, state.code = status, code
		state.at = at
		if status == "ok" || status == "ok_with_omissions" || status == "unchanged" {
			state.lastOK = at
			if status != "unchanged" && account != "" {
				state.accountObserved = true
			}
			if fp != "" {
				state.goodFingerprints[fp] = true
			}
		}
	}
}

// SourceHealth derives health from sync outcomes; retained legacy refusals supply missing error information.
func (s *Store) SourceHealth(ctx context.Context) ([]SourceHealth, error) {
	attempts := map[string]*sourceAttempt{}
	var workiqOutcomes []workIQHealthOutcome
	var codes, accounts, schema int
	var hasRecords bool
	if err := s.db.QueryRowContext(ctx, `select
		(select count(*) from pragma_table_info('sync_runs') where name='error_code'),
		(select count(*) from pragma_table_info('sync_runs') where name='source_accounts_json'),
		exists(select 1 from sqlite_master where name='records'),
		coalesce((select max(version) from schema_migrations),1)`).Scan(&codes, &accounts, &hasRecords, &schema); err != nil {
		return nil, err
	}
	if schema >= 3 && !hasRecords {
		return nil, errors.New("archive is missing records")
	}
	// Legacy outcomes have no account scope; resolve every proven association before replaying scoped recoveries.
	associations := map[string][]string{}
	query := `select 'teams',tenant_id||'/'||user_id from accounts`
	if hasRecords {
		query += ` union select distinct source,tenant_id||'/'||user_id from records where tenant_id<>'' and user_id<>''`
	}
	if err := s.each(ctx, query+` order by 1,2`, func(rs *sql.Rows) error {
		var source, account string
		if err := rs.Scan(&source, &account); err != nil {
			return err
		}
		associations[source] = append(associations[source], account)
		return nil
	}); err != nil {
		return nil, err
	}
	codeColumn, accountColumn := "''", "null"
	if codes > 0 {
		codeColumn = "error_code"
	}
	if accounts > 0 {
		accountColumn = "source_accounts_json"
	}
	err := s.each(ctx, `select id,source,status,`+codeColumn+`,fingerprint,finished_at,`+accountColumn+` from sync_runs where source<>'' order by id`, func(rs *sql.Rows) error {
		var id int64
		var key, status, code, fp string
		var at, accounts sql.NullString
		if err := rs.Scan(&id, &key, &status, &code, &fp, &at, &accounts); err != nil {
			return err
		}
		if strings.HasPrefix(key, "workiq:") {
			family, account, kind, valid := WorkIQSourceScope(key)
			var scope []string
			if !valid || !accounts.Valid || json.Unmarshal([]byte(accounts.String), &scope) != nil ||
				len(scope) != 1 || scope[0] != account {
				return errors.New("invalid archived WorkIQ outcome scope")
			}
			clock, err := workIQStoredTime(at)
			if err != nil {
				return err
			}
			if clock == nil || clock.IsZero() {
				return errors.New("WorkIQ outcome has no known completion clock")
			}
			workiqOutcomes = append(workiqOutcomes, workIQHealthOutcome{id: id, key: key,
				family: family, account: account, kind: kind, status: status, code: code, at: *clock})
			return nil
		}
		a := attempts[key]
		if a == nil {
			a = &sourceAttempt{}
			if family, _ := SourceFamily(key); family == "teams" && len(associations[key]) > 0 {
				a.byAccount = map[string]*sourceAttempt{}
				for _, account := range associations[key] {
					a.byAccount[account] = &sourceAttempt{accountObserved: true, goodFingerprints: map[string]bool{}}
				}
			}
			attempts[key] = a
		}
		var scope []string
		if accounts.Valid {
			if err := json.Unmarshal([]byte(accounts.String), &scope); err != nil {
				return err
			}
		}
		a.apply(status, code, fp, parseTime(at), scope)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if err := s.legacySourceAttempts(ctx, attempts); err != nil {
		return nil, err
	}
	out := []SourceHealth{}
	keys := make([]string, 0, len(attempts))
	for key := range attempts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		family, sourceAccount := SourceFamily(key)
		if family == "" {
			continue
		}
		scope := make([]string, 0, len(attempts[key].byAccount))
		for account := range attempts[key].byAccount {
			scope = append(scope, account)
		}
		sort.Strings(scope)
		for _, scopeAccount := range scope {
			a := attempts[key].byAccount[scopeAccount]
			state := SourceFailureState(a.status, a.code, !a.lastOK.IsZero())
			if state == "" {
				continue
			}
			accounts := []string{sourceAccount}
			if family == "teams" {
				accounts = []string{scopeAccount}
			}
			for _, account := range accounts {
				h := SourceHealth{SourceKey: key, Source: family, State: state, LastErrorCode: a.code, LastOKAt: healthTime(a.lastOK), attemptedAt: a.at}
				if account != "" {
					h.AccountID = &account
				}
				if family == "teams" && scopeAccount != "" && !a.accountObserved {
					var known bool
					if err := s.db.QueryRowContext(ctx, `select exists(select 1 from accounts where tenant_id||'/'||user_id=?)`, account).Scan(&known); err != nil {
						return nil, err
					}
					if !known {
						h.AccountID = nil
					}
				}
				if key == "teams" && account != "" && h.LastOKAt == nil {
					var last sql.NullString
					if err := s.db.QueryRowContext(ctx, `select (select last_synced_at from accounts where tenant_id||'/'||user_id=?)`, account).Scan(&last); err != nil {
						return nil, err
					}
					h.LastOKAt = healthTime(parseTime(last))
					h.State = SourceFailureState(a.status, a.code, h.LastOKAt != nil)
				}
				if family != "teams" {
					check := "outlook_store"
					if family == "outlook_mail" {
						check = "mail_readable"
					}
					h.DoctorCheck = &check
				}
				var err error
				if h.NewestItemAt, err = s.sourceNewestItem(ctx, family, account); err != nil {
					return nil, err
				}
				out = append(out, h)
			}
		}
	}
	return s.withWorkIQHealth(ctx, out, workiqOutcomes)
}

func healthTime(at time.Time) *time.Time {
	if at.IsZero() {
		return nil
	}
	return &at
}

func (s *Store) legacySourceAttempts(ctx context.Context, attempts map[string]*sourceAttempt) error {
	var hasMeta bool
	if err := s.db.QueryRowContext(ctx, `select exists(select 1 from sqlite_master where name='meta')`).Scan(&hasMeta); err != nil || !hasMeta {
		return err
	}
	return s.each(ctx, `select key,value from meta where key like 'outlook_mail_failure:%' or key like 'outlook_failure:%'`, func(rs *sql.Rows) error {
		var key, raw string
		if err := rs.Scan(&key, &raw); err != nil {
			return err
		}
		var f OutlookFailure
		if err := json.Unmarshal([]byte(raw), &f); err != nil {
			return err
		}
		account, mail := strings.CutPrefix(key, mailFailureKey)
		if !mail {
			account = strings.TrimPrefix(key, outlookFailureKey)
		}
		source := "outlook|" + strings.TrimPrefix(account, "outlook/")
		if mail {
			source += "|mail"
		}
		a := attempts[source]
		if a == nil {
			a = &sourceAttempt{}
			attempts[source] = a
		}
		// Canonical outcomes carry the attempt clock. Retained legacy refusals
		// supplement missing error information, not overwrite a scoped outcome.
		for _, state := range a.byAccount {
			if state.code != "" {
				return nil
			}
		}
		prior := a.byAccount[""]
		if prior == nil || prior.code == "" {
			at := time.Time{}
			if len(a.byAccount) == 1 {
				for _, state := range a.byAccount {
					if state.status == "failed" || state.status == "unavailable" {
						at = state.at
					}
				}
			}
			a.apply("failed", f.Code, "", at, nil)
		}
		return nil
	})
}

func (s *Store) sourceNewestItem(ctx context.Context, family, account string) (*time.Time, error) {
	if account == "" {
		return nil, nil
	}

	var calendarTable, mailTable, recordsTable bool
	var mailModified, folderModified, schema int
	if err := s.db.QueryRowContext(ctx, `select
		exists(select 1 from sqlite_master where name='calendar_source_events'),
		exists(select 1 from sqlite_master where name='mail_messages'),
		exists(select 1 from sqlite_master where name='records'),
		(select count(*) from pragma_table_info('mail_messages') where name='modified_at'),
		(select count(*) from pragma_table_info('mail_folders') where name='modified_at'),
		coalesce((select max(version) from schema_migrations),1)`).Scan(&calendarTable, &mailTable, &recordsTable, &mailModified, &folderModified, &schema); err != nil {
		return nil, err
	}
	if family == "outlook_mail" && schema >= 6 && !mailTable ||
		family == "outlook_calendar" && schema >= 5 && !calendarTable ||
		family == "teams" && (schema >= 3 && !recordsTable || schema >= 5 && !calendarTable) {
		return nil, errors.New("archive is missing a source timestamp table")
	}
	var newest sql.NullString
	var query string
	args := []any{account}
	switch family {
	case "outlook_mail":
		if !mailTable {
			return nil, nil
		}
		clock := `max(first_seen_at,state_seen_at,coalesce(gone_at,first_seen_at),coalesce(evicted_at,first_seen_at))`
		if mailModified > 0 {
			clock = `max(coalesce(modified_at,first_seen_at),` + clock + `)`
		}
		query = `select max(at) from (select ` + clock + ` at from mail_messages where account=?`
		if folderModified > 0 {
			query += ` union all select modified_at from mail_folders where account=?`
			args = append(args, account)
		}
		query += `)`
	case "outlook_calendar":
		if !calendarTable {
			return nil, nil
		}
		query = `select max(max(seen_at,coalesce(removed_at,seen_at))) from calendar_source_events where account_id=?`
	default:
		query = `select max(at) from (
			select updated_at at from messages where tenant_id||'/'||user_id=?
			union all select updated_at from conversations where tenant_id||'/'||user_id=?
			union all select updated_at from activity where tenant_id||'/'||user_id=?`
		args = append(args, account, account)
		if recordsTable {
			query += ` union all select max(updated_at,coalesce(removed_at,updated_at)) from records where tenant_id||'/'||user_id=?`
			args = append(args, account)
		}
		if calendarTable {
			query += ` union all select max(seen_at,coalesce(removed_at,seen_at)) from calendar_source_events where account_id=?`
			args = append(args, account)
		}
		query += `)`
	}
	err := s.db.QueryRowContext(ctx, query, args...).Scan(&newest)
	return healthTime(parseTime(newest)), err
}

// SourcePrincipal applies the same active account links as calendar reads.
func (s *Store) SourcePrincipal(ctx context.Context, account string) (string, error) {
	var principal string
	err := s.db.QueryRowContext(ctx, `select coalesce((select principal_id from calendar_account_links where account_id=? and unlinked_at is null limit 1),?)`, account, account).Scan(&principal)
	return principal, err
}
