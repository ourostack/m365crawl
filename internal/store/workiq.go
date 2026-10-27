package store

import (
	"context"
	"errors"
	"time"

	"github.com/ourostack/m365crawl/internal/workiq"
)

type WorkIQScope struct {
	Family, Account, Qualification string
	WindowStart                    time.Time
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
}

func (s *Store) hasWorkIQ(ctx context.Context) (bool, error) {
	var version int
	if err := s.db.QueryRowContext(ctx, `select coalesce(max(version),0) from schema_migrations`).Scan(&version); err != nil {
		return false, err
	}
	if version < 21 {
		return false, nil
	}
	var tables int
	if err := s.db.QueryRowContext(ctx, `select count(*) from sqlite_master where type='table' and name in
('workiq_accounts','workiq_attempts','workiq_frontiers','workiq_pages','workiq_rows','workiq_aliases','workiq_source_keys',
'workiq_folders','workiq_censuses','workiq_census_members','workiq_gaps','workiq_mail_receipts')`).Scan(&tables); err != nil {
		return false, err
	}
	if tables != 12 {
		return false, errors.New("WorkIQ archive state is incomplete")
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
