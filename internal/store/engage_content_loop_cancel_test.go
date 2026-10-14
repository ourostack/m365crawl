package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"
)

type engageSessionCancelReader struct {
	knowledgeReader
	inj    *injector
	cancel context.CancelFunc
	rows   *sql.Rows
}

func (r *engageSessionCancelReader) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	sessionQuery := strings.Contains(query, "from engage_sessions order by rowid")
	if sessionQuery {
		// Isolate readEngage's own cancellation guard from SQL's asynchronous
		// watcher: actual rows must still yield before the read context is checked.
		ctx = context.WithoutCancel(ctx)
	}
	rows, err := r.knowledgeReader.QueryContext(ctx, query, args...)
	if err == nil && sessionQuery {
		r.rows = rows
		// A yielded NULL row would fail Scan. Cancellation must win before Scan,
		// not merely be noticed later by the Result validator.
		r.inj.arm("scan", 1)
		r.cancel()
	}
	return rows, err
}

func TestEngageSessionCancellationBeforeScanClosesRows(t *testing.T) {
	s := newStore(t)
	commitEngage(t, s, engageResult("A"), base)
	commitEngage(t, s, engageResult("B"), base.Add(time.Hour))
	inj := injectFaults(t, s)
	tx, err := s.db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reader := &engageSessionCancelReader{knowledgeReader: tx, inj: inj, cancel: cancel}

	out, err := readEngage(ctx, reader, EngageFilter{}, 0, true, 0)
	if !errors.Is(err, context.Canceled) || len(out.Rows) != 0 {
		t.Fatalf("cancellation must precede session Scan: rows=%d error=%v", len(out.Rows), err)
	}
	if len(out.Sources) != 2 {
		t.Fatalf("cancellation occurred before the session boundary: sources=%d", len(out.Sources))
	}
	for _, source := range out.Sources {
		if source.CurrentSession.ID != 0 {
			t.Fatal("a canceled session was decoded or published")
		}
	}
	if !inj.disarm() || reader.rows == nil {
		t.Fatal("the actual driver did not yield the armed session row")
	}
	// A second stored session remains unread. Next would yield it if the guard
	// returned without closing this live rowset; rollback has not happened yet.
	if reader.rows.Next() || reader.rows.Err() != nil {
		t.Fatal("cancellation did not close the live sessions rowset cleanly")
	}
}
