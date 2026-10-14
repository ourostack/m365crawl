package store

import (
	"context"
	"database/sql"
)

// EngageAttemptReceipt audits the namespace in one read transaction and returns
// only the current admitted attempt's body-free source and ordered archive IDs.
// Fetch publication must not assemble every historical full observation merely
// to discover at most128 IDs from its own bounded session.
func (s *Store) EngageAttemptReceipt(ctx context.Context, a KnowledgeAttempt) (EngageSource, []int64, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return EngageSource{}, nil, err
	}
	defer func() { _ = tx.Rollback() }()
	snap, err := readEngage(ctx, tx, EngageFilter{}, 0, false, 0)
	if err != nil {
		return EngageSource{}, nil, err
	}
	var source *EngageSource
	for i := range snap.Sources {
		candidate := &snap.Sources[i]
		if candidate.AdmittedAttemptID == a.ID && candidate.RegistrationID == a.LocatorID {
			for _, reg := range snap.Registrations {
				if reg.ID == a.LocatorID && reg.LatestAttemptID == a.ID && reg.AccountState == "verified_at_last_success" {
					source = candidate
				}
			}
		}
	}
	if source == nil {
		return EngageSource{}, nil, ErrStaleKnowledgeAttempt
	}
	rows, err := tx.QueryContext(ctx, `select rowid from engage_occurrences where session_id=? order by ordinal`, source.CurrentSessionID)
	if err != nil {
		return EngageSource{}, nil, err
	}
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return EngageSource{}, nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return EngageSource{}, nil, err
	}
	if err := tx.Commit(); err != nil {
		return EngageSource{}, nil, err
	}
	return *source, ids, nil
}
