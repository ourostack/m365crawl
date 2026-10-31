package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

func workIQMailEnumerationProof(ctx context.Context, tx *sql.Tx, attempt int64, scope WorkIQScope, folders int, admitted bool) (string, *time.Time, error) {
	h := sha256.New()
	// hash.Hash writes cannot fail. Only private receipt IDs, not payloads or
	// opaque cursor bytes, enter this logical enumeration identity.
	_, _ = fmt.Fprintf(h, "%s:%d:", scope.Qualification, folders)
	var oldest *time.Time
	hashed, roots, terminals := 0, 0, 0
	err := eachRow(ctx, tx, `select p.id,p.observed_at,f.resource,p.expected_revision=f.revision-1
from workiq_pages p join workiq_frontiers f on f.id=p.frontier_id
where f.family='mail' and f.account_id=? and f.qualification=? and f.scope='' and f.retired=0
and p.prepared_attempt_id=? and p.prepared_complete=1
and ((?=0 and p.state='staged' and p.expected_revision>=f.admitted_revision)
or (?=1 and p.state='admitted' and f.admitted_revision=f.revision))
and p.expected_revision<f.revision
order by p.id`, []any{scope.Account, scope.Qualification, attempt, admitted, admitted}, func(row *sql.Rows) error {
		var id int64
		var observed, resource string
		var terminal bool
		if err := row.Scan(&id, &observed, &resource, &terminal); err != nil {
			return err
		}
		root := resource == "/me/mailFolders?includeHiddenFolders=true"
		messages := strings.HasSuffix(resource, "/messages/delta")
		if messages || root && folders == 0 {
			_, _ = fmt.Fprintf(h, "%d,", id)
			hashed++
		}
		at, err := workIQStoredTime(sql.NullString{String: observed, Valid: true})
		if err != nil {
			return err
		}
		if at.IsZero() {
			return errors.New("mail census original page clock is unknown")
		}
		if oldest == nil || at.Before(*oldest) {
			oldest = at
		}
		if terminal {
			if root {
				roots++
			} else if messages {
				terminals++
			}
		}
		return nil
	})
	if err != nil {
		return "", nil, err
	}
	if hashed == 0 || roots != 1 || terminals != folders || oldest == nil {
		return "", nil, errors.New("mail census has no complete original enumeration receipt proof")
	}
	return hex.EncodeToString(h.Sum(nil)), oldest, nil
}
