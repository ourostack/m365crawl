package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/ourostack/m365crawl/internal/workiq"
)

type WorkIQTraversalProgress struct {
	WorkIQProgress
	Terminal bool
}

func (s *Store) WorkIQTraversalProgressFor(ctx context.Context, scope WorkIQScope, resource string) (WorkIQTraversalProgress, error) {
	if scope.Account == "" || scope.Family != "mail" && scope.Family != "chats" {
		return WorkIQTraversalProgress{}, errors.New("invalid WorkIQ traversal scope")
	}
	if _, err := workiq.RelativePath(resource, ""); err != nil {
		return WorkIQTraversalProgress{}, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return WorkIQTraversalProgress{}, err
	}
	defer func() { _ = tx.Rollback() }()
	p, err := loadWorkIQProgress(ctx, tx, scope, resource)
	if err != nil {
		return WorkIQTraversalProgress{}, err
	}
	if p.Revision < p.AdmittedRevision || p.AdmittedRevision < 0 {
		return WorkIQTraversalProgress{}, errors.New("invalid retained WorkIQ traversal revision")
	}
	out := WorkIQTraversalProgress{WorkIQProgress: p}
	if p.Revision > p.AdmittedRevision {
		if err := tx.QueryRowContext(ctx, `select next_cursor='' from workiq_pages
where frontier_id=? and expected_revision=? and state='staged'`, p.ID, p.Revision-1).Scan(&out.Terminal); err != nil {
			return WorkIQTraversalProgress{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return WorkIQTraversalProgress{}, err
	}
	return out, nil
}

func (s *Store) WorkIQPendingMailPages(ctx context.Context, attempt int64, resource string, after int64, limit int) ([]int64, error) {
	return s.workIQPendingPages(ctx, attempt, resource, after, limit, "mail")
}

func (s *Store) workIQPendingPages(ctx context.Context, attempt int64, resource string, after int64, limit int, family string) ([]int64, error) {
	binding, err := s.validateWorkIQAttemptOwner(ctx, attempt)
	if err != nil {
		return nil, err
	}
	if binding.Fact.Family != family || after < 0 || limit < 1 || limit > 128 {
		return nil, errors.New("invalid bounded source page traversal")
	}
	if _, err := workiq.RelativePath(resource, ""); err != nil {
		return nil, err
	}
	var out []int64
	err = mailEach(ctx, s.db, `select p.id from workiq_pages p join workiq_frontiers f on f.id=p.frontier_id
join workiq_attempts a on a.id=? and a.family=f.family and a.account_id=f.account_id and a.qualification=f.qualification and coalesce(a.window_start,'')=f.scope
where f.resource=? and f.retired=0 and p.state='staged' and p.id>? order by p.id limit ?`,
		[]any{attempt, resource, after, limit}, func(row *sql.Rows) error {
			var id int64
			if err := row.Scan(&id); err != nil {
				return err
			}
			out = append(out, id)
			return nil
		})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) WorkIQPendingChatPages(ctx context.Context, attempt int64, resource string, after int64, limit int) ([]int64, error) {
	return s.workIQPendingPages(ctx, attempt, resource, after, limit, "chats")
}

// An optional parent selects one completed sibling list: "" selects the root
// listing. Without a parent the caller can inspect all current inventory.
func (s *Store) WorkIQMailTraversalFolders(ctx context.Context, attempt int64, after string, limit int, parents ...string) ([]workiq.Folder, error) {
	binding, err := s.validateWorkIQAttemptOwner(ctx, attempt)
	if err != nil {
		return nil, err
	}
	if binding.Fact.Family != "mail" || limit < 1 || limit > 128 || len(parents) > 1 {
		return nil, errors.New("invalid bounded mail folder traversal")
	}
	filtered, parent := 0, ""
	if len(parents) == 1 {
		filtered, parent = 1, parents[0]
	}
	var out []workiq.Folder
	err = mailEach(ctx, s.db, `select w.graph_id,w.parent_graph_id,f.name,w.well_known,w.child_count,w.total_items
from workiq_folders w join mail_folders f on f.account=w.account_id and f.folder_key=w.folder_key
where w.account_id=? and w.inventory_attempt=? and w.graph_id>?
and (?=0 or (?='' and w.root_listed=1) or (?<>'' and w.parent_graph_id=?)) order by w.graph_id limit ?`,
		[]any{binding.Fact.ID, attempt, after, filtered, parent, parent, parent, limit}, func(row *sql.Rows) error {
			var f workiq.Folder
			var child, total sql.NullInt64
			if err := row.Scan(&f.ID, &f.ParentID, &f.Name, &f.WellKnown, &child, &total); err != nil {
				return err
			}
			if f.ID == "" || f.ID == f.ParentID || child.Valid && child.Int64 < 0 || total.Valid && total.Int64 < 0 {
				return errors.New("invalid retained mail folder inventory")
			}
			if child.Valid {
				f.ChildCount = &child.Int64
			}
			if total.Valid {
				f.TotalCount = &total.Int64
			}
			out = append(out, f)
			return nil
		})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) WorkIQMailTraversalGroups(ctx context.Context, attempt int64, after *string, limit int) ([]string, error) {
	binding, err := s.validateWorkIQAttemptOwner(ctx, attempt)
	if err != nil {
		return nil, err
	}
	if binding.Fact.Family != "mail" || limit < 1 || limit > 128 || after != nil && workiq.NormalizeMessageID(*after) != *after {
		return nil, errors.New("invalid bounded mail identity traversal")
	}
	var census int64
	if err := s.db.QueryRowContext(ctx, `select id from workiq_censuses where attempt_id=? and account_id=? and complete=1 and inventory_ready=1 and carry_complete=1`,
		attempt, binding.Fact.ID).Scan(&census); err != nil {
		return nil, err
	}
	var cursor any
	if after != nil {
		cursor = *after
	}
	var out []string
	err = mailEach(ctx, s.db, `select distinct normalized_id from workiq_census_members
where census_id=? and admitted=0 and (? is null or normalized_id>?) order by normalized_id limit ?`,
		[]any{census, cursor, cursor, limit}, func(row *sql.Rows) error {
			var normalized string
			if err := row.Scan(&normalized); err != nil {
				return err
			}
			if workiq.NormalizeMessageID(normalized) != normalized {
				return errors.New("invalid retained mail identity normalization")
			}
			out = append(out, normalized)
			return nil
		})
	if err != nil {
		return nil, err
	}
	return out, nil
}
