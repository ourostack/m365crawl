package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"modernc.org/sqlite"
)

type archiveOpenProbe struct {
	marker      string
	connections atomic.Int64
	commits     atomic.Int64
	hold        context.Context
}

var activeArchiveOpenProbe atomic.Pointer[archiveOpenProbe]

func init() {
	sqlite.RegisterConnectionHook(func(conn sqlite.ExecQuerierContext, dsn string) error {
		probe := activeArchiveOpenProbe.Load()
		if probe == nil || !strings.Contains(dsn, probe.marker) {
			return nil
		}
		probe.connections.Add(1)
		conn.(sqlite.HookRegisterer).RegisterCommitHook(func() int32 {
			probe.commits.Add(1)
			return 0
		})
		if probe.hold != nil {
			<-probe.hold.Done()
			return probe.hold.Err()
		}
		return nil
	})
}

func TestOpenColdSchemaDoesNotCommitEveryDDLStatement(t *testing.T) {
	path := filepath.Join(filepath.Dir(writableArchivePath(t)), "observed-cold-schema.db")
	probe := &archiveOpenProbe{marker: filepath.Base(path)}
	activeArchiveOpenProbe.Store(probe)
	t.Cleanup(func() { activeArchiveOpenProbe.Store(nil) })
	start := time.Now()
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	status, err := s.Status(context.Background())
	closeErr := s.Close()
	if err != nil || closeErr != nil || status.SchemaVersion != SchemaVersion {
		t.Fatal("cold archive did not initialize a complete usable schema", err, closeErr)
	}
	t.Logf("cold archive: connections=%d commits=%d elapsed=%s", probe.connections.Load(), probe.commits.Load(), time.Since(start))
	// Schema, version-table creation, version row, and the existing mail conversation-index migration.
	if got := probe.commits.Load(); got != 4 {
		t.Fatalf("cold schema initialization amplified commits: got %d, want 4", got)
	}
}

func TestOpenMidSchemaFailureDoesNotLeavePartialTables(t *testing.T) {
	path := writableArchivePath(t)
	db, err := sql.Open("sqlite", sqliteFileURI(path, ""))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	must(db.Exec(`create view conversations as select 'synthetic conflict' as last_message_at`))
	s, err := Open(context.Background(), path)
	if s != nil {
		_ = s.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "apply schema") {
		t.Fatal("fixture did not reach the real mid-schema DDL failure", err)
	}
	var accounts int
	must0(db.QueryRow(`select count(*) from sqlite_master where type='table' and name='accounts'`).Scan(&accounts))
	if accounts != 0 {
		t.Fatal("failed schema setup committed an earlier table before refusing the conflict")
	}
}

func TestShortWriterOpenBudgetExpiresBeforeCallback(t *testing.T) {
	path := filepath.Join(filepath.Dir(writableArchivePath(t)), "observed-open-budget.db")
	s := must(Open(context.Background(), path))
	must0(s.Close())
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	probe := &archiveOpenProbe{marker: filepath.Base(path), hold: ctx}
	activeArchiveOpenProbe.Store(probe)
	t.Cleanup(func() { activeArchiveOpenProbe.Store(nil) })
	entered := false
	start := time.Now()
	err := WithWorkIQWriter(ctx, path, func(context.Context, *Store) error {
		entered = true
		return nil
	})
	activeArchiveOpenProbe.Store(nil)
	t.Logf("delayed real connection: elapsed=%s connections=%d callback=%t canceled=%t deadline=%t err=%T:%v",
		time.Since(start), probe.connections.Load(), entered, errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded), err, err)
	if !errors.Is(err, context.DeadlineExceeded) || entered || probe.connections.Load() == 0 {
		t.Fatal("opening consumed its budget without the original deadline refusal", err)
	}
	release := must(AcquireWriter(context.Background(), path, WriterOrdinarySync))
	release()
}
