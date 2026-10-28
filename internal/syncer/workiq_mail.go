package syncer

import (
	"context"
	"errors"
	"net/url"

	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/workiq"
)

type workIQMailRun struct {
	options  WorkIQOptions
	session  *workIQSession
	attempt  int64
	account  string
	report   *WorkIQAccountReport
	prepared map[string]int64
	visited  map[string]bool
	jobs     []string
	pages    int
}

func (r *workIQMailRun) progress(ctx context.Context, resource string) (store.WorkIQTraversalProgress, error) {
	var out store.WorkIQTraversalProgress
	err := workIQRead(ctx, r.options.DBPath, func(s *store.Store) (err error) {
		out, err = s.WorkIQTraversalProgressFor(ctx, store.WorkIQScope{Family: "mail", Account: r.account}, resource)
		return err
	})
	return out, err
}

func (r *workIQMailRun) prepare(ctx context.Context, resource string) error {
	for {
		var ids []int64
		if err := workIQRead(ctx, r.options.DBPath, func(s *store.Store) (err error) {
			ids, err = s.WorkIQPendingMailPages(ctx, r.attempt, resource, r.prepared[resource], 128)
			return err
		}); err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		for _, id := range ids {
			r.pages++
			if r.pages > 10000 {
				r.report.Gaps["workiq_page_limit"]++
				return workIQFailure("workiq_page_limit")
			}
			done, limit := false, 128
			for !done {
				err := workIQWrite(ctx, r.options.DBPath, func(writeCtx context.Context, s *store.Store) (err error) {
					done, err = s.PrepareWorkIQMailPage(writeCtx, r.attempt, id, limit)
					return err
				})
				if err != nil {
					if ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded) && limit > 1 {
						limit /= 2
						continue
					}
					return err
				}
			}
			r.prepared[resource] = id
		}
	}
}

func (r *workIQMailRun) fetchResource(ctx context.Context, resource string) error {
	if r.visited[resource] {
		return nil
	}
	if err := r.prepare(ctx, resource); err != nil {
		return err
	}
	p, err := r.progress(ctx, resource)
	if err != nil {
		return err
	}
	for !p.Terminal {
		path := resource
		if p.Revision > p.AdmittedRevision {
			path = p.PendingCursor
		} else if p.Cursor != "" {
			path = p.Cursor
		}
		base, _ := url.ParseRequestURI(resource)
		if _, err := workiq.RelativePath(path, base.Path); err != nil {
			return err
		}
		page, err := r.session.fetch(ctx, path)
		if err != nil {
			return err
		}
		if err := workIQWrite(ctx, r.options.DBPath, func(writeCtx context.Context, s *store.Store) error {
			return s.StageWorkIQPage(writeCtx, store.WorkIQPageBatch{
				AttemptID: r.attempt, Resource: resource, ExpectedRevision: p.Revision, Page: page, ObservedAt: workIQNow(),
			})
		}); err != nil {
			return err
		}
		if err := r.prepare(ctx, resource); err != nil {
			return err
		}
		p, err = r.progress(ctx, resource)
		if err != nil {
			return err
		}
	}
	r.visited[resource] = true
	r.jobs = append(r.jobs, resource)
	return nil
}

func (r *workIQMailRun) folders(ctx context.Context, parent string) ([]workiq.Folder, error) {
	var out []workiq.Folder
	after := ""
	for {
		var batch []workiq.Folder
		if err := workIQRead(ctx, r.options.DBPath, func(s *store.Store) (err error) {
			batch, err = s.WorkIQMailTraversalFolders(ctx, r.attempt, after, 128, parent)
			return err
		}); err != nil {
			return nil, err
		}
		out = append(out, batch...)
		if len(out) > 100000 {
			return nil, workIQFailure("workiq_folder_limit")
		}
		if len(batch) < 128 {
			return out, nil
		}
		after = batch[len(batch)-1].ID
	}
}

func acquireWorkIQMail(ctx context.Context, options WorkIQOptions, session *workIQSession, attempt int64, account string, report *WorkIQAccountReport) error {
	r := workIQMailRun{options: options, session: session, attempt: attempt, account: account, report: report,
		prepared: map[string]int64{}, visited: map[string]bool{}}
	var after int64
	for {
		done := false
		if err := workIQWrite(ctx, options.DBPath, func(writeCtx context.Context, s *store.Store) (err error) {
			after, done, err = s.IndexWorkIQMailCandidates(writeCtx, attempt, after, 128)
			return err
		}); err != nil {
			return err
		}
		if done {
			break
		}
	}
	for _, kind := range []string{"inbox", "sentitems", "drafts", "deleteditems", "junkemail", "archive"} {
		if err := r.fetchResource(ctx, "/me/mailFolders/"+kind); err != nil {
			return err
		}
	}
	if err := r.fetchResource(ctx, "/me/mailFolders?includeHiddenFolders=true"); err != nil {
		return err
	}
	queue, err := r.folders(ctx, "")
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, folder := range queue {
		seen[folder.ID] = true
	}
	full := true
	for i := 0; i < len(queue); i++ {
		folder := queue[i]
		if folder.ChildCount == nil || *folder.ChildCount > 0 {
			if err := r.fetchResource(ctx, "/me/mailFolders/"+url.PathEscape(folder.ID)+"/childFolders?includeHiddenFolders=true"); err != nil {
				return err
			}
			children, err := r.folders(ctx, folder.ID)
			if err != nil {
				return err
			}
			queue, err = appendWorkIQMailQueue(queue, children, seen)
			if err != nil {
				return err
			}
		}
		resource := "/me/mailFolders/" + url.PathEscape(folder.ID) + "/messages/delta()"
		p, err := r.progress(ctx, resource)
		if err != nil {
			return err
		}
		full = full && p.AdmittedRevision == 0
		if err := r.fetchResource(ctx, resource); err != nil {
			return err
		}
	}
	report.Counts["folders"] = len(queue)
	var proof store.WorkIQMailInventoryProof
	if err := workIQRead(ctx, options.DBPath, func(s *store.Store) (err error) {
		proof, err = s.InspectWorkIQMailInventory(ctx, attempt)
		return err
	}); err != nil {
		return err
	}
	if err := workIQWrite(ctx, options.DBPath, func(writeCtx context.Context, s *store.Store) error {
		return s.SealWorkIQMailInventoryProof(writeCtx, attempt, proof, workIQNow())
	}); err != nil {
		return err
	}
	after = 0
	for {
		done := false
		if err := workIQWrite(ctx, options.DBPath, func(writeCtx context.Context, s *store.Store) (err error) {
			after, done, err = s.CarryWorkIQMailInventory(writeCtx, attempt, after, 128)
			return err
		}); err != nil {
			return err
		}
		if done {
			break
		}
	}
	var groupAfter *string
	for {
		var groups []string
		if err := workIQRead(ctx, options.DBPath, func(s *store.Store) (err error) {
			groups, err = s.WorkIQMailTraversalGroups(ctx, attempt, groupAfter, 128)
			return err
		}); err != nil {
			return err
		}
		if len(groups) == 0 {
			break
		}
		for _, group := range groups {
			for {
				var result store.WorkIQMailResult
				if err := workIQWrite(ctx, options.DBPath, func(writeCtx context.Context, s *store.Store) (err error) {
					result, err = s.CommitWorkIQMailGroup(writeCtx, attempt, group)
					return err
				}); err != nil {
					return err
				}
				report.Counts["mail_added"] += result.Added
				report.Counts["mail_updated"] += result.Updated
				for code, n := range result.Gaps {
					report.Gaps[code] += n
				}
				if !result.Pending {
					break
				}
			}
		}
		last := groups[len(groups)-1]
		groupAfter = &last
	}
	for _, resource := range r.jobs {
		p, err := r.progress(ctx, resource)
		if err != nil {
			return err
		}
		if err := workIQWrite(ctx, options.DBPath, func(writeCtx context.Context, s *store.Store) error {
			return s.AdmitWorkIQFrontier(writeCtx, attempt, resource, p.Revision)
		}); err != nil {
			return err
		}
	}
	if full && len(report.Gaps) == 0 {
		for {
			done := false
			if err := workIQWrite(ctx, options.DBPath, func(writeCtx context.Context, s *store.Store) (err error) {
				done, err = s.ConfirmWorkIQMailCensusBatch(writeCtx, attempt, 128)
				return err
			}); err != nil {
				return err
			}
			if done {
				break
			}
		}
	}
	report.Counts["pages"] = r.pages
	return nil
}

func appendWorkIQMailQueue(queue, children []workiq.Folder, seen map[string]bool) ([]workiq.Folder, error) {
	if len(queue) > 100000 || len(children) > 100000-len(queue) {
		return nil, workIQFailure("workiq_folder_limit")
	}
	for _, child := range children {
		if seen[child.ID] {
			return nil, workIQFailure("workiq_folder_inventory_ambiguous")
		}
		seen[child.ID] = true
		queue = append(queue, child)
	}
	return queue, nil
}
