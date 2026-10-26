package syncer

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/ourostack/m365crawl/internal/store"
)

// recordSourceHealth fills gaps in the existing outcome history: successful source writes already recorded their transactional runs.
func (r *runner) recordSourceHealth(ctx context.Context, rep *Report, runErr error) error {
	at := time.Now().UTC()
	var pending []store.Run
	record := func(key, status, code, fp string, accounts []string) {
		pending = append(pending, store.Run{Source: key, Status: status, ErrorCode: code, Fingerprint: fp, SourceAccounts: accounts, StartedAt: at, FinishedAt: at})
	}
	if rep.Status == "" {
		if runErr == nil {
			return nil
		}
		status := StatusFailed
		if teamsAbsent(runErr) {
			status = StatusUnavailable
			prior, err := r.st.SourceHealth(ctx)
			if err != nil {
				return err
			}
			retireMissingTeams(rep, prior, record)
		}
		record("teams", status, codedOf(runErr).Code, "", nil)
	} else {
		for _, sr := range rep.Sources {
			family, _ := store.SourceFamily(sr.Source)
			if family == "" {
				continue
			}
			code := ""
			if sr.Error != nil {
				code = sr.Error.Code
			}
			switch sr.Status {
			case StatusFailed, StatusUnavailable, StatusSkippedInterval:
				accounts := sr.Accounts
				if family == "teams" && len(accounts) == 0 {
					accounts = r.o.AccountScope()
				}
				record(sr.Source, sr.Status, code, "", accounts)
			case StatusUnchanged:
				// Mail reuses the profile's successful fingerprint without reading it again.
				if strings.HasSuffix(sr.Source, "|mail") {
					fp, err := r.st.LastFingerprint(ctx, sr.Source)
					if err != nil {
						return err
					}
					record(sr.Source, sr.Status, "", fp, sr.Accounts)
				}
			}
		}
		// Cancellation leaves later sources untried; absence from the report then proves nothing.
		prior, err := r.st.SourceHealth(ctx)
		if err != nil {
			return err
		}
		if !errors.Is(runErr, context.Canceled) && !errors.Is(runErr, context.DeadlineExceeded) {
			retireMissingTeams(rep, prior, record)
			if r.outlookOn() {
				retireMissingProfiles(rep, prior, record)
			}
		}
	}
	for _, outcome := range pending {
		if err := r.st.RecordRun(ctx, outcome); err != nil {
			return err
		}
	}
	var err error
	rep.FailedSourceHealth, err = r.st.SourceHealth(ctx)
	if err == nil && rep.Status != "" {
		current := map[string]bool{}
		for _, sr := range rep.Sources {
			current[sr.Source] = true
		}
		active := []store.SourceHealth{}
		for _, h := range rep.FailedSourceHealth {
			if current[h.SourceKey] {
				active = append(active, h)
			}
		}
		rep.FailedSourceHealth = active
	}
	return err
}

// AccountScope is nil for an unfiltered source; "*" belongs only on run-level rows.
func (o Options) AccountScope() []string {
	if o.Account == nil {
		return nil
	}
	return []string{o.Account.TenantID + "/" + o.Account.UserID}
}

func retireMissingProfiles(rep *Report, health []store.SourceHealth, record func(string, string, string, string, []string)) {
	present := map[string]bool{}
	rootAttempt := false
	for _, sr := range rep.Sources {
		if sr.Source == "outlook" {
			rootAttempt = true
			if sr.Error == nil || sr.Error.Code != CodeNoOutlookProfiles {
				return // discovery failed: absence is not established
			}
		}
		if strings.HasPrefix(sr.Source, "outlook|") {
			profile := strings.TrimSuffix(strings.TrimPrefix(sr.Source, "outlook|"), "|mail")
			present[profile] = true
		}
	}
	for _, h := range health {
		if h.Source == "teams" {
			continue
		}
		if h.SourceKey == "outlook" && rootAttempt {
			continue
		}
		if h.AccountID != nil {
			profile := strings.TrimPrefix(*h.AccountID, "outlook/")
			if present[profile] {
				continue
			}
		}
		record(h.SourceKey, StatusUnavailable, "no_profile", "", nil)
	}
}

func retireMissingTeams(rep *Report, health []store.SourceHealth, record func(string, string, string, string, []string)) {
	present := map[string]bool{}
	for _, sr := range rep.Sources {
		present[sr.Source] = true
	}
	for _, h := range health {
		if h.Source != "teams" || present[h.SourceKey] {
			continue
		}
		record(h.SourceKey, StatusUnavailable, "no_teams_origin", "", nil)
		present[h.SourceKey] = true
	}
}
