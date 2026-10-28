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

func (s *Store) PrepareWorkIQMailPage(ctx context.Context, attempt, pageID int64, limit int) (bool, error) {
	binding, err := s.validateWorkIQAttemptOwner(ctx, attempt)
	if err != nil {
		return false, err
	}
	if binding.Fact.Family != "mail" || limit < 1 || limit > 128 {
		return false, errors.New("invalid WorkIQ mail preparation scope or bound")
	}
	var raw, observed, resource, family, account, state, qualification, scope string
	var prepared, offset int64
	var complete bool
	if err := s.db.QueryRowContext(ctx, `select p.data_json,p.observed_at,p.prepared_attempt_id,p.prepared_offset,p.prepared_complete,
f.resource,f.family,f.account_id,p.state,f.qualification,f.scope from workiq_pages p join workiq_frontiers f on f.id=p.frontier_id where p.id=?`, pageID).
		Scan(&raw, &observed, &prepared, &offset, &complete, &resource, &family, &account, &state, &qualification, &scope); err != nil {
		return false, err
	}
	var actualQualification string
	if err := s.db.QueryRowContext(ctx, `select qualification from workiq_attempts where id=?`, attempt).Scan(&actualQualification); err != nil {
		return false, err
	}
	if family != "mail" || account != binding.Fact.ID || state != "staged" || qualification != actualQualification || scope != "" {
		return false, errors.New("staged page does not match the verified mail attempt")
	}
	if prepared == attempt && complete {
		return true, nil
	}
	at, err := workIQStoredTime(sql.NullString{String: observed, Valid: true})
	if err != nil {
		return false, err
	}
	page, err := workiq.DecodePage(ctx, []byte(raw))
	if err != nil {
		return false, err
	}
	path, err := workiq.RelativePath(resource, "")
	if err != nil {
		return false, err
	}
	u, _ := url.ParseRequestURI(path)
	root := u.Path == "/me/mailFolders"
	escaped := strings.TrimPrefix(u.EscapedPath(), "/me/mailFolders/")
	child := strings.HasSuffix(escaped, "/childFolders")
	messages := strings.HasSuffix(escaped, "/messages/delta()")
	var fixedKind string
	if !root && !child && !messages {
		switch escaped {
		case "inbox":
			fixedKind = "inbox"
		case "sentitems":
			fixedKind = "sent"
		case "deleteditems":
			fixedKind = "deleted"
		case "drafts":
			fixedKind = "drafts"
		case "archive":
			fixedKind = "archive"
		case "junkemail":
			fixedKind = "junk"
		}
		if fixedKind == "" {
			return false, errors.New("unsupported staged mail resource")
		}
	}
	var values []json.RawMessage
	if fixedKind != "" {
		values = []json.RawMessage{page.Data}
	} else {
		var data struct {
			Value *[]json.RawMessage `json:"value"`
		}
		if json.Unmarshal(page.Data, &data) != nil || data.Value == nil {
			return false, errors.New("mail inventory page has no bounded value array")
		}
		values = *data.Value
	}
	start := int(offset)
	if prepared != attempt {
		start = 0
	}
	if start < 0 || start > len(values) {
		return false, errors.New("invalid staged page preparation offset")
	}
	end := min(start+limit, len(values))
	var folder string
	if child || messages {
		suffix := "/childFolders"
		if messages {
			suffix = "/messages/delta()"
		}
		// RelativePath already validated every percent escape in this URI.
		folder, _ = url.PathUnescape(strings.TrimSuffix(escaped, suffix))
		if folder == "" {
			return false, errors.New("invalid staged folder identity")
		}
	}
	if messages {
		var ids []string
		for _, value := range values[start:end] {
			m, err := workiq.DecodeMail(ctx, value)
			if err != nil {
				return false, err
			}
			ids = append(ids, m.ID)
		}
		if err := s.StageWorkIQMailItems(ctx, attempt, folder, values[start:end], *at); err != nil {
			return false, err
		}
		for _, id := range ids {
			if _, err := s.db.ExecContext(ctx, `update workiq_census_members set page_id=? where census_id=(select id from workiq_censuses where attempt_id=? and account_id=?)
and graph_id=?`, pageID, attempt, account, id); err != nil {
				return false, err
			}
		}
	} else {
		for _, value := range values[start:end] {
			f, err := workiq.DecodeFolder(ctx, value)
			if err != nil {
				return false, err
			}
			if child && f.ParentID != "" && f.ParentID != folder {
				return false, errors.New("child folder escaped its parent inventory")
			}
			if child {
				f.ParentID = folder
			}
			f.WellKnown = fixedKind
			if err := s.StageWorkIQMailFolder(ctx, attempt, f); err != nil {
				return false, err
			}
			if root {
				if _, err := s.db.ExecContext(ctx, `update workiq_folders set root_listed=1 where account_id=? and graph_id=? and inventory_attempt=?`,
					account, f.ID, attempt); err != nil {
					return false, err
				}
			}
		}
	}
	done := end == len(values)
	result, err := s.db.ExecContext(ctx, `update workiq_pages set prepared_attempt_id=?,prepared_offset=?,prepared_complete=?
where id=? and state='staged' and prepared_attempt_id=? and prepared_offset=?`,
		attempt, end, b2i(done), pageID, prepared, offset)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if count != 1 {
		return false, errors.New("staged page changed before preparation receipt")
	}
	return done, nil
}

func (s *Store) SealWorkIQMailInventory(ctx context.Context, attempt int64, at time.Time) error {
	binding, err := s.validateWorkIQAttemptOwner(ctx, attempt)
	if err != nil {
		return err
	}
	if binding.Fact.Family != "mail" || at.IsZero() {
		return errors.New("invalid WorkIQ mail inventory scope or completion clock")
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		scope := WorkIQScope{Family: "mail", Account: binding.Fact.ID}
		if err := tx.QueryRowContext(ctx, `select qualification from workiq_attempts where id=?`, attempt).Scan(&scope.Qualification); err != nil {
			return err
		}
		root, err := loadWorkIQProgress(ctx, tx, scope, "/me/mailFolders?includeHiddenFolders=true")
		if err != nil {
			return err
		}
		if root.ID == 0 {
			return errors.New("mail inventory has no complete hidden-folder root traversal")
		}
		if err := preparedWorkIQFrontier(ctx, tx, attempt, root); err != nil {
			return err
		}
		type entry struct {
			id, parent string
			root       bool
			children   sql.NullInt64
		}
		var folders []entry
		byID := map[string]entry{}
		if err := eachRow(ctx, tx, `select graph_id,parent_graph_id,root_listed,child_count from workiq_folders
where account_id=? and inventory_attempt=?`, []any{scope.Account, attempt}, func(row *sql.Rows) error {
			var f entry
			if err := row.Scan(&f.id, &f.parent, &f.root, &f.children); err != nil {
				return err
			}
			folders = append(folders, f)
			byID[f.id] = f
			return nil
		}); err != nil {
			return err
		}
		childCounts := map[string]int64{}
		for _, f := range folders {
			if !f.root {
				childCounts[f.parent]++
			}
		}
		settled := map[string]bool{}
		for _, f := range folders {
			if f.children.Valid && f.children.Int64 != childCounts[f.id] {
				return errors.New("mail inventory changed during child-folder enumeration")
			}
			path := map[string]bool{}
			for id := f.id; !settled[id]; {
				current, listed := byID[id]
				if !listed {
					return errors.New("mail inventory contains an unlisted folder parent")
				}
				if path[id] {
					return errors.New("mail inventory contains a folder cycle")
				}
				path[id] = true
				if current.root {
					if _, parentListed := byID[current.parent]; parentListed {
						return errors.New("mail inventory root is also a listed child folder")
					}
					break
				}
				id = current.parent
			}
			for id := range path {
				settled[id] = true
			}
		}
		full := true
		for _, f := range folders {
			base := "/me/mailFolders/" + url.PathEscape(f.id)
			if !f.children.Valid || f.children.Int64 > 0 {
				child, err := loadWorkIQProgress(ctx, tx, scope, base+"/childFolders?includeHiddenFolders=true")
				if err != nil {
					return err
				}
				if child.ID == 0 {
					return errors.New("mail inventory is missing a child-folder traversal")
				}
				if err := preparedWorkIQFrontier(ctx, tx, attempt, child); err != nil {
					return err
				}
			}
			progress, err := loadWorkIQProgress(ctx, tx, scope, base+"/messages/delta()")
			if err != nil {
				return err
			}
			if progress.ID == 0 {
				return errors.New("mail inventory is missing a folder message baseline")
			}
			if err := preparedWorkIQFrontier(ctx, tx, attempt, progress); err != nil {
				return err
			}
			var terminal string
			if err := tx.QueryRowContext(ctx, `select delta_cursor from workiq_pages where frontier_id=? and expected_revision=? and state='staged'`,
				progress.ID, progress.Revision-1).Scan(&terminal); err != nil {
				return err
			}
			if terminal == "" {
				return errors.New("mail folder traversal has no terminal delta link")
			}
			full = full && progress.Baseline
		}
		var ready bool
		if err := tx.QueryRowContext(ctx, `select exists(select 1 from meta where key=?)`, workIQMailIndexPrefix+scope.Account).Scan(&ready); err != nil {
			return err
		}
		if !ready {
			return errors.New("native mail candidate index is not prepared")
		}
		var unproven bool
		if err := tx.QueryRowContext(ctx, `select exists(select 1 from workiq_census_members cm join workiq_censuses c on c.id=cm.census_id
where c.attempt_id=? and cm.retained=0 and (cm.page_id is null or not exists(select 1 from workiq_pages p where p.id=cm.page_id and p.prepared_attempt_id=? and p.prepared_complete=1 and p.state='staged')))`,
			attempt, attempt).Scan(&unproven); err != nil {
			return err
		}
		if unproven {
			return errors.New("mail census contains data without prepared page proof")
		}
		var id int64
		err = tx.QueryRowContext(ctx, `select id from workiq_censuses where attempt_id=? and account_id=?`, attempt, scope.Account).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			r, err := tx.ExecContext(ctx, `insert into workiq_censuses(attempt_id,account_id) values(?,?)`, attempt, scope.Account)
			if err != nil {
				return err
			}
			id, err = r.LastInsertId()
			if err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		proof, dataAt, err := workIQMailEnumerationProof(ctx, tx, attempt, scope, len(folders))
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `update workiq_censuses set inventory_ready=1,full_enumeration=?,carry_complete=?,complete=?,completed_at=?,
enumeration_key=?,source_data_at=? where id=?`,
			b2i(full), b2i(full), b2i(full), workIQTimeValue(&at), proof, workIQTimeValue(dataAt), id)
		return err
	})
}
