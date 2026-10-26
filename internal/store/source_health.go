package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"sort"
	"strings"
	"time"
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
}

// SourceFamily recognizes only the three sync-backed source families, not independent observations.
func SourceFamily(key string) (family, account string) {
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
	goodFingerprints map[string]bool
	byAccount        map[string]*sourceAttempt
}

func (a *sourceAttempt) apply(status, code, fp string, at time.Time, accounts []string) {
	if a.byAccount == nil {
		a.byAccount = map[string]*sourceAttempt{}
	}
	if len(accounts) > 0 {
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
			state = &sourceAttempt{goodFingerprints: map[string]bool{}}
			a.byAccount[account] = state
		}
		if status == "skipped_interval" || status == "unchanged" && (fp == "" || !state.goodFingerprints[fp]) {
			continue
		}
		state.status, state.code = status, code
		if status == "ok" || status == "ok_with_omissions" || status == "unchanged" {
			state.lastOK = at
			if fp != "" {
				state.goodFingerprints[fp] = true
			}
		}
	}
}

// SourceHealth derives health from sync outcomes; retained legacy refusals supply missing error information.
func (s *Store) SourceHealth(ctx context.Context) ([]SourceHealth, error) {
	attempts := map[string]*sourceAttempt{}
	var columns int
	if err := s.db.QueryRowContext(ctx, `select count(*) from pragma_table_info('sync_runs') where name='error_code'`).Scan(&columns); err != nil {
		return nil, err
	}
	if columns > 0 {
		err := s.each(ctx, `select source,status,error_code,fingerprint,finished_at,source_accounts_json from sync_runs where source<>'' order by id`, func(rs *sql.Rows) error {
			var key, status, code, fp string
			var at, accounts sql.NullString
			if err := rs.Scan(&key, &status, &code, &fp, &at, &accounts); err != nil {
				return err
			}
			a := attempts[key]
			if a == nil {
				a = &sourceAttempt{}
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
			if family == "teams" && scopeAccount == "" {
				accounts = nil
				if err := s.each(ctx, `select distinct tenant_id||'/'||user_id from records where source=? and tenant_id<>'' and user_id<>''
					union select tenant_id||'/'||user_id from accounts where ?='teams' order by 1`, func(rs *sql.Rows) error {
					var account string
					if err := rs.Scan(&account); err != nil {
						return err
					}
					accounts = append(accounts, account)
					return nil
				}, key, key); err != nil {
					return nil, err
				}
				if len(accounts) == 0 {
					accounts = []string{""}
				}
			}
			for _, account := range accounts {
				h := SourceHealth{SourceKey: key, Source: family, State: state, LastErrorCode: a.code, LastOKAt: healthTime(a.lastOK)}
				if account != "" {
					h.AccountID = &account
				}
				if family == "teams" && scopeAccount != "" && a.lastOK.IsZero() {
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
	return out, nil
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
		prior := a.byAccount[""]
		if prior == nil || prior.code == "" {
			a.apply("failed", f.Code, "", time.Time{}, nil)
		}
		return nil
	})
}

func (s *Store) sourceNewestItem(ctx context.Context, family, account string) (*time.Time, error) {
	if account == "" {
		return nil, nil
	}

	var newest sql.NullString
	var err error
	switch family {
	case "outlook_mail":
		err = s.db.QueryRowContext(ctx, `select max(state_seen_at) from mail_messages where account=?`, account).Scan(&newest)
	case "outlook_calendar":
		err = s.db.QueryRowContext(ctx, `select max(max(seen_at,coalesce(removed_at,seen_at))) from calendar_source_events where account_id=?`, account).Scan(&newest)
	default:
		err = s.db.QueryRowContext(ctx, `select max(at) from (
			select updated_at at from messages where tenant_id||'/'||user_id=?
			union all select updated_at from conversations where tenant_id||'/'||user_id=?
			union all select updated_at from activity where tenant_id||'/'||user_id=?
			union all select max(updated_at,coalesce(removed_at,updated_at)) from records where tenant_id||'/'||user_id=?
			union all select max(seen_at,coalesce(removed_at,seen_at)) from calendar_source_events where account_id=?
		)`, account, account, account, account, account).Scan(&newest)
	}
	return healthTime(parseTime(newest)), err
}

// SourcePrincipal applies the same active account links as calendar reads.
func (s *Store) SourcePrincipal(ctx context.Context, account string) (string, error) {
	var principal string
	err := s.db.QueryRowContext(ctx, `select coalesce((select principal_id from calendar_account_links where account_id=? and unlinked_at is null limit 1),?)`, account, account).Scan(&principal)
	return principal, err
}
