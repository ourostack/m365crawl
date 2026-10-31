package store

import (
	"context"
	"errors"
	"time"

	"github.com/ourostack/m365crawl/internal/workiq"
)

type WorkIQScope struct {
	Family, Account, Qualification string
	Kind                           string
	WindowStart                    time.Time
	WindowEnd                      time.Time
}

type WorkIQPageBatch struct {
	AttemptID                      int64
	Resource, Scope, Qualification string
	ExpectedRevision               int64
	Page                           workiq.Page
	ObservedAt                     time.Time
}

type WorkIQSummary struct {
	Status, ErrorCode string
	FinishedAt        time.Time
	AllFolders        bool
	Folders           int
	Counts            map[string]int
	Gaps              map[string]int
	MailProof         *WorkIQMailCompletionProof
	Diagnostic        *workiq.Diagnostic // the failed WorkIQ child's redacted diagnostic, kept for health
	// StopAt says where (a hash of the request) and why an unexplained failure stopped a sync, so the
	// next run of the scope can tell whether it stopped at the same place again.
	StopAt string
}

const workIQCacheMessageFilter = `not exists(select 1 from workiq_rows w
where w.kind='message' and w.account_id=messages.tenant_id||'/'||messages.user_id
and w.item_rowid=messages.rowid and w.winner='workiq_server')`

const workIQCacheConversationFilter = `not exists(select 1 from workiq_rows w
where w.kind='conversation' and w.account_id=conversations.tenant_id||'/'||conversations.user_id
and w.item_rowid=conversations.rowid and w.winner='workiq_server')`

const workIQCacheMailFilter = `not exists(select 1 from workiq_rows w
where w.kind='mail' and w.account_id=mail_messages.account
and w.item_rowid=mail_messages.rowid and w.winner='workiq_server')`

// hasWorkIQ is the one presence check that does not read "no tables" as "no feature": WorkIQ's
// identity and progress state is evidence a writable open must not recreate empty, so an archive
// recorded at WorkIQSchemaVersion or above without it is refused as incomplete (checkSchemaNotNewer
// runs this before every open). Its number was settled long before #306.
func (s *Store) hasWorkIQ(ctx context.Context) (bool, error) {
	version, tables, err := featureState(ctx, s.db, workIQFeature)
	if err != nil || version < workIQFeature.version {
		return false, err
	}
	if tables != len(workIQFeature.tables) {
		return false, errors.New(workIQFeature.name + " is incomplete")
	}
	return true, nil
}

// WorkIQEnabled reads opt-in without migrating or probing the external source.
func (s *Store) WorkIQEnabled(ctx context.Context) (bool, error) {
	ok, err := s.hasWorkIQ(ctx)
	if err != nil || !ok {
		return false, err
	}
	var value string
	if err := s.db.QueryRowContext(ctx, `select coalesce((select value from meta where key='workiq_enabled'),'0')`).Scan(&value); err != nil {
		return false, err
	}
	if value != "0" && value != "1" {
		return false, errors.New("invalid WorkIQ opt-in state")
	}
	return value == "1", nil
}

func (s *Store) SetWorkIQEnabled(ctx context.Context, enabled bool) error {
	value := "0"
	if enabled {
		value = "1"
	}
	_, err := s.db.ExecContext(ctx, `insert into meta(key,value) values('workiq_enabled',?)
on conflict(key) do update set value=excluded.value where meta.value<>excluded.value`, value)
	return err
}
