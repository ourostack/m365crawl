package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/ourostack/m365crawl/internal/workiq"
)

func (s *Store) BeginWorkIQAttempt(ctx context.Context, scope WorkIQScope, at time.Time) (int64, error) {
	enabled, err := s.WorkIQEnabled(ctx)
	if err != nil {
		return 0, err
	}
	if !enabled || at.IsZero() || scope.Family != "mail" && scope.Family != "chats" {
		return 0, errors.New("WorkIQ attempt requires opt-in and a known source scope")
	}
	facts, err := s.WorkIQAccounts(ctx)
	if err != nil {
		return 0, err
	}
	known := false
	for _, fact := range facts {
		known = known || fact.ID == scope.Account && fact.Family == scope.Family
	}
	if !known {
		return 0, errors.New("WorkIQ source account is not archived")
	}
	if scope.Qualification == "" {
		scope.Qualification = "workiq-v1"
	}
	var id int64
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `insert into workiq_attempts(family,account_id,started_at,window_start,qualification) values(?,?,?,?,?)`,
			scope.Family, scope.Account, fmtTime(at), fmtTime(scope.WindowStart), scope.Qualification)
		if err != nil {
			return err
		}
		id, err = result.LastInsertId()
		return err
	})
	return id, err
}

func (s *Store) StageWorkIQPage(ctx context.Context, batch WorkIQPageBatch) error {
	if _, err := s.validateWorkIQAttemptOwner(ctx, batch.AttemptID); err != nil {
		return err
	}
	page, err := workiq.DecodePage(ctx, batch.Page.JSON)
	if err != nil {
		return err
	}
	if batch.ObservedAt.IsZero() {
		return errors.New("WorkIQ page has no observation time")
	}
	resource, err := workiq.RelativePath(batch.Resource, "")
	if err != nil {
		return err
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		var family, account, qualification, scope, status string
		if err := tx.QueryRowContext(ctx, `select family,account_id,qualification,coalesce(window_start,''),status from workiq_attempts where id=?`, batch.AttemptID).
			Scan(&family, &account, &qualification, &scope, &status); err != nil {
			return err
		}
		if status != "open" || batch.Scope != "" && batch.Scope != scope || batch.Qualification != "" && batch.Qualification != qualification {
			return errors.New("WorkIQ page does not match an open attempt")
		}
		// RelativePath already proved this is a parseable relative URI.
		u, _ := url.ParseRequestURI(resource)
		mail := u.Path == "/me/messages" || strings.HasPrefix(u.Path, "/me/messages/") ||
			u.Path == "/me/mailFolders" || strings.HasPrefix(u.Path, "/me/mailFolders/")
		chat := u.Path == "/me/chats" || strings.HasPrefix(u.Path, "/chats/")
		if family == "mail" && !mail || family == "chats" && !chat {
			return errors.New("WorkIQ resource is outside the attempt family")
		}
		for _, cursor := range []string{page.NextCursor, page.DeltaCursor} {
			if cursor != "" {
				if _, err := workiq.RelativePath(cursor, u.Path); err != nil {
					return err
				}
			}
		}
		if _, err := tx.ExecContext(ctx, `insert into workiq_frontiers(family,account_id,resource,scope,qualification) values(?,?,?,?,?)
on conflict(family,account_id,resource,scope,qualification) do nothing`, family, account, resource, scope, qualification); err != nil {
			return err
		}
		var frontier, revision int64
		if err := tx.QueryRowContext(ctx, `select id,revision from workiq_frontiers where family=? and account_id=? and resource=? and scope=? and qualification=? and retired=0`,
			family, account, resource, scope, qualification).Scan(&frontier, &revision); err != nil {
			return err
		}
		if revision != batch.ExpectedRevision {
			return errors.New("WorkIQ cursor changed before publication")
		}
		if _, err := tx.ExecContext(ctx, `insert into workiq_pages(attempt_id,frontier_id,expected_revision,data_json,next_cursor,delta_cursor,observed_at) values(?,?,?,?,?,?,?)`,
			batch.AttemptID, frontier, revision, string(page.JSON), page.NextCursor, page.DeltaCursor, fmtTime(batch.ObservedAt)); err != nil {
			return err
		}
		next := page.NextCursor
		if next == "" {
			next = page.DeltaCursor
		}
		_, err := tx.ExecContext(ctx, `update workiq_frontiers set pending_cursor=?,revision=revision+1 where id=? and revision=?`, next, frontier, revision)
		return err
	})
}

func (s *Store) CompleteWorkIQAttempt(ctx context.Context, attempt int64, summary WorkIQSummary) error {
	switch summary.Status {
	case "ok", "ok_with_omissions", "partial", "failed":
	default:
		return errors.New("invalid WorkIQ attempt outcome")
	}
	if summary.FinishedAt.IsZero() {
		return errors.New("WorkIQ attempt has no completion time")
	}
	if summary.Status == "ok" || summary.Status == "ok_with_omissions" {
		if _, err := s.validateWorkIQAttemptOwner(ctx, attempt); err != nil {
			return err
		}
	}
	// These closed map[string]int types contain no values JSON can fail to encode.
	counts, _ := json.Marshal(summary.Counts)
	gaps, _ := json.Marshal(summary.Gaps)
	return s.inTx(ctx, func(tx *sql.Tx) error {
		var family, account, started, status string
		if err := tx.QueryRowContext(ctx, `select family,account_id,started_at,status from workiq_attempts where id=?`, attempt).
			Scan(&family, &account, &started, &status); err != nil {
			return err
		}
		if status != "open" {
			return errors.New("WorkIQ attempt is already settled")
		}
		gapCount := 0
		for code, n := range summary.Gaps {
			if !strings.HasPrefix(code, "workiq_") || len(code) > 128 || n < 1 {
				return errors.New("invalid WorkIQ gap count")
			}
			gapCount += n
			if _, err := tx.ExecContext(ctx, `insert into workiq_gaps(attempt_id,code,n) values(?,?,?)
on conflict(attempt_id,code,resource) do update set n=excluded.n`, attempt, code, n); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `update workiq_attempts set finished_at=?,status=?,error_code=?,counts_json=?,gap_count=?,all_folders=? where id=?`,
			fmtTime(summary.FinishedAt), summary.Status, summary.ErrorCode, string(counts), gapCount, b2i(summary.AllFolders), attempt); err != nil {
			return err
		}
		key, _ := json.Marshal([]string{family, account})
		accounts, _ := json.Marshal([]string{account})
		_, err := tx.ExecContext(ctx, `insert into sync_runs(started_at,finished_at,source,status,counts_json,omissions_json,error_code,source_accounts_json) values(?,?,?,?,?,?,?,?)`,
			started, fmtTime(summary.FinishedAt), "workiq:"+string(key), summary.Status, string(counts), string(gaps), summary.ErrorCode, string(accounts))
		return err
	})
}
