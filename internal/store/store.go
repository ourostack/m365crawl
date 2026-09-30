// Package store is the SQLite archive: schema, idempotent upserts, full-text search and the
// read queries behind every teamscrawl read command. Rows are partitioned by (tenant_id, user_id)
// so two accounts never mix, and messages that vanish from Teams' cache are kept.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	crawlstore "github.com/openclaw/crawlkit/store"
)

// ErrNoArchive is returned by OpenReadOnly when the archive file does not exist. Read commands
// treat it as an empty archive.
var ErrNoArchive = errors.New("no archive yet")

// timeLayout is fixed width so stored timestamps compare correctly as text.
const timeLayout = "2006-01-02T15:04:05.000Z"

// DefaultLimit applies when a Filter has no Limit.
const DefaultLimit = 50

// Store is an open archive.
type Store struct {
	cs       *crawlstore.Store
	db       *sql.DB
	readOnly bool
}

// Counts reports what one Apply call did.
type Counts struct{ Seen, Inserted, Updated, Unchanged int }

// Open creates or opens the archive at path for writing: parent directory 0700, file 0600.
func Open(ctx context.Context, path string) (*Store, error) {
	if err := ensureParent(path); err != nil {
		return nil, err
	}
	cs, err := crawlstore.Open(ctx, crawlstore.Options{Path: path, Schema: schemaDDL, SchemaVersion: SchemaVersion})
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = cs.Close()
		return nil, fmt.Errorf("chmod archive: %w", err)
	}
	return &Store{cs: cs, db: cs.DB()}, nil
}

// OpenReadOnly opens an existing archive read-only (safe beside an active writer). It returns
// ErrNoArchive when the file is missing.
func OpenReadOnly(ctx context.Context, path string) (*Store, error) {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return nil, ErrNoArchive
	}
	cs, err := crawlstore.OpenReadOnly(ctx, path)
	if err != nil {
		return nil, err
	}
	return &Store{cs: cs, db: cs.DB(), readOnly: true}, nil
}

// Close releases the database.
func (s *Store) Close() error { return s.cs.Close() }

// ensureParent creates the archive directory 0700. crawlkit would create it 0755, so this runs
// first. The default ~/.teamscrawl is tightened to 0700; a custom --db parent is left alone.
func ensureParent(path string) error {
	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return fmt.Errorf("create archive dir: %w", err)
	}
	if home, err := os.UserHomeDir(); err == nil && filepath.Clean(parent) == filepath.Join(home, ".teamscrawl") {
		if err := os.Chmod(parent, 0o700); err != nil { //nolint:gosec // G302: a directory, 0700 is the point
			return fmt.Errorf("chmod archive dir: %w", err)
		}
	}
	return nil
}

func fmtTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC().Format(timeLayout)
}

func parseTime(s sql.NullString) time.Time {
	if !s.Valid || s.String == "" {
		return time.Time{}
	}
	t, err := time.Parse(timeLayout, s.String)
	if err != nil {
		return time.Time{}
	}
	return t
}

// jsonOrNil marshals a non-empty slice; empty ones are stored as NULL.
func jsonOrNil[T any](v []T) any {
	if len(v) == 0 {
		return nil
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func unmarshalNull[T any](s sql.NullString) []T {
	if !s.Valid || s.String == "" {
		return nil
	}
	var out []T
	_ = json.Unmarshal([]byte(s.String), &out)
	return out
}

func rawOrNil(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return string(b)
}

// SQL runs a read-only query. It is only available on a store opened with OpenReadOnly, and only
// for statements that read (the connection is also read-only at the file level).
func (s *Store) SQL(ctx context.Context, q string) (cols []string, rows [][]any, err error) {
	if !s.readOnly {
		return nil, nil, errors.New("sql requires a read-only archive connection")
	}
	fields := strings.Fields(strings.ToLower(q))
	if len(fields) == 0 {
		return nil, nil, errors.New("empty query")
	}
	switch fields[0] {
	case "select", "with", "explain", "values":
	default:
		return nil, nil, fmt.Errorf("only read queries are allowed, got %q", fields[0])
	}
	if attachWord.MatchString(q) {
		return nil, nil, errors.New("attach is not allowed")
	}
	res, err := s.cs.Query(ctx, q)
	if err != nil {
		return nil, nil, err
	}
	return res.Columns, res.Rows, nil
}

// Run is one sync attempt, recorded in sync_runs.
type Run struct {
	StartedAt, FinishedAt time.Time
	Source, Fingerprint   string
	Status                string // ok, ok_with_omissions, unchanged, failed
	Counts                any    // marshaled as counts_json
	Omissions             map[string]int
}

var attachWord = regexp.MustCompile(`(?i)\battach\b`)

const successStatuses = `('ok','ok_with_omissions','unchanged')`

// RecordRun appends a sync attempt.
func (s *Store) RecordRun(ctx context.Context, r Run) error {
	var counts, omissions any
	if r.Counts != nil {
		b, err := json.Marshal(r.Counts)
		if err != nil {
			return err
		}
		counts = string(b)
	}
	if len(r.Omissions) > 0 {
		b, _ := json.Marshal(r.Omissions)
		omissions = string(b)
	}
	_, err := s.db.ExecContext(ctx, `insert into sync_runs(started_at, finished_at, source, fingerprint, status, counts_json, omissions_json) values(?,?,?,?,?,?,?)`,
		fmtTime(r.StartedAt), fmtTime(r.FinishedAt), r.Source, r.Fingerprint, r.Status, counts, omissions)
	return err
}

// LastFingerprint is the fingerprint of the newest successful run for source, or "".
func (s *Store) LastFingerprint(ctx context.Context, source string) (string, error) {
	var fp string
	err := s.db.QueryRowContext(ctx, `select fingerprint from sync_runs where source = ? and status in `+successStatuses+` order by id desc limit 1`, source).Scan(&fp)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return fp, err
}

// RunRow is a recorded sync attempt.
type RunRow struct {
	ID          int64     `json:"id"`
	StartedAt   time.Time `json:"started_at,omitzero"`
	FinishedAt  time.Time `json:"finished_at,omitzero"`
	Source      string    `json:"source"`
	Fingerprint string    `json:"fingerprint"`
	Status      string    `json:"status"`
	Counts      any       `json:"counts,omitempty"`
	Omissions   any       `json:"omissions,omitempty"`
}

// AccountStatus is one account's archive size.
type AccountStatus struct {
	TenantID      string    `json:"tenant_id"`
	UserID        string    `json:"user_id"`
	Conversations int       `json:"conversations"`
	Messages      int       `json:"messages"`
	People        int       `json:"people"`
	Activity      int       `json:"activity"`
	NewestSentAt  time.Time `json:"newest_sent_at,omitzero"`
	LastSyncedAt  time.Time `json:"last_synced_at,omitzero"`
}

// StatusRow is what `status` and `doctor` report about the archive.
type StatusRow struct {
	SchemaVersion int             `json:"schema_version"`
	FTSPresent    bool            `json:"fts_present"`
	Accounts      []AccountStatus `json:"accounts"`
	NewestSentAt  time.Time       `json:"newest_sent_at,omitzero"`
	LastRun       *RunRow         `json:"last_run,omitempty"`
	LastSuccessAt time.Time       `json:"last_success_at,omitzero"`
}

// Status summarizes the archive.
func (s *Store) Status(ctx context.Context) (StatusRow, error) {
	var st StatusRow
	var err error
	if st.SchemaVersion, err = s.cs.SchemaVersion(ctx); err != nil {
		return st, err
	}
	var fts int
	if err = s.db.QueryRowContext(ctx, `select count(*) from sqlite_master where name in ('message_fts','conversation_fts')`).Scan(&fts); err != nil {
		return st, err
	}
	st.FTSPresent = fts == 2
	st.Accounts = []AccountStatus{}
	rows, err := s.db.QueryContext(ctx, `
select a.tenant_id, a.user_id, a.last_synced_at,
  (select count(*) from conversations c where c.tenant_id=a.tenant_id and c.user_id=a.user_id),
  (select count(*) from messages m where m.tenant_id=a.tenant_id and m.user_id=a.user_id),
  (select count(*) from people p where p.tenant_id=a.tenant_id),
  (select count(*) from activity x where x.tenant_id=a.tenant_id and x.user_id=a.user_id),
  (select max(m.sent_at) from messages m where m.tenant_id=a.tenant_id and m.user_id=a.user_id)
from accounts a order by a.tenant_id, a.user_id`)
	if err != nil {
		return st, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var a AccountStatus
		var synced, newest sql.NullString
		if err := rows.Scan(&a.TenantID, &a.UserID, &synced, &a.Conversations, &a.Messages, &a.People, &a.Activity, &newest); err != nil {
			return st, err
		}
		a.LastSyncedAt, a.NewestSentAt = parseTime(synced), parseTime(newest)
		if a.NewestSentAt.After(st.NewestSentAt) {
			st.NewestSentAt = a.NewestSentAt
		}
		st.Accounts = append(st.Accounts, a)
	}
	if err := rows.Err(); err != nil {
		return st, err
	}
	var id int64
	var started, finished sql.NullString
	var r RunRow
	var counts, omissions sql.NullString
	err = s.db.QueryRowContext(ctx, `select id, started_at, finished_at, source, fingerprint, status, counts_json, omissions_json from sync_runs order by id desc limit 1`).
		Scan(&id, &started, &finished, &r.Source, &r.Fingerprint, &r.Status, &counts, &omissions)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return st, err
	default:
		r.ID, r.StartedAt, r.FinishedAt = id, parseTime(started), parseTime(finished)
		if counts.Valid {
			_ = json.Unmarshal([]byte(counts.String), &r.Counts)
		}
		if omissions.Valid {
			_ = json.Unmarshal([]byte(omissions.String), &r.Omissions)
		}
		st.LastRun = &r
	}
	var ok sql.NullString
	if err := s.db.QueryRowContext(ctx, `select max(coalesce(finished_at, started_at)) from sync_runs where status in `+successStatuses).Scan(&ok); err != nil {
		return st, err
	}
	st.LastSuccessAt = parseTime(ok)
	return st, nil
}
