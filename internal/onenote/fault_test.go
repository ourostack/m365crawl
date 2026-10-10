package onenote

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"reflect"
	"strings"
	"testing"
)

var errInjectedRead = errors.New("private-source-value injected failure")

type faultConnection struct {
	driver.Conn
	at, calls int
	kind      string
	fired     bool
	cancel    context.CancelFunc
}

func (c *faultConnection) hit() bool {
	c.calls++
	if c.calls != c.at {
		return false
	}
	c.fired = true
	return true
}

func (c *faultConnection) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	if c.kind == "query" && c.hit() {
		return nil, errInjectedRead
	}
	if c.kind == "cancel-query" && c.hit() {
		c.cancel()
		return nil, errInjectedRead
	}
	rows, err := c.Conn.(driver.QueryerContext).QueryContext(ctx, q, args)
	if err != nil {
		return nil, err
	}
	if strings.HasSuffix(q, "ORDER BY rowid") {
		if c.kind == "cancel" && c.hit() {
			c.cancel()
		}
		return &faultIndexRows{Rows: rows, conn: c}, nil
	}
	if c.kind == "close" || c.kind == "cancel-close" {
		return &faultIndexRows{Rows: rows, conn: c}, nil
	}
	return rows, nil
}

func (c *faultConnection) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	if c.kind == "exec" && c.hit() {
		return nil, errInjectedRead
	}
	return c.Conn.(driver.ExecerContext).ExecContext(ctx, q, args)
}

type faultIndexRows struct {
	driver.Rows
	conn *faultConnection
}

func (r *faultIndexRows) Next(values []driver.Value) error {
	if r.conn.kind == "next" && r.conn.hit() {
		return errInjectedRead
	}
	if r.conn.kind == "scan" && len(values) > len(r.Rows.Columns()) {
		values = values[:len(r.Rows.Columns())]
	}
	err := r.Rows.Next(values)
	if err == nil && r.conn.kind == "cancel-row" && r.conn.hit() {
		r.conn.cancel()
	}
	return err
}

func (r *faultIndexRows) Columns() []string {
	columns := r.Rows.Columns()
	if r.conn.kind == "scan" && (r.conn.fired || r.conn.hit()) {
		return append(columns, "unexpected-column")
	}
	return columns
}

func (r *faultIndexRows) Close() error {
	err := r.Rows.Close()
	if r.conn.kind == "close" && r.conn.hit() {
		return errInjectedRead
	}
	if r.conn.kind == "cancel-close" && r.conn.hit() {
		r.conn.cancel()
	}
	return err
}

type indexConnector struct{ conn *faultConnection }

func (c indexConnector) Connect(context.Context) (driver.Conn, error) { return c.conn, nil }
func (c indexConnector) Driver() driver.Driver                        { return nil }

func TestReadIndexFatalFailureDiscardsEarlierRows(t *testing.T) {
	path := indexFixture(t, "fault.db", hierarchyFixture)
	original := indexOpen
	t.Cleanup(func() { indexOpen = original })
	for _, kind := range []string{"exec", "query", "next", "scan", "close", "cancel", "cancel-query", "cancel-close", "cancel-row"} {
		for at := 1; at <= 80; at++ {
			ctx, cancel := context.WithCancel(context.Background())
			var fault *faultConnection
			indexOpen = func(name, dsn string) (*sql.DB, error) {
				probe, err := sql.Open(name, dsn)
				if err != nil {
					return nil, err
				}
				defer func() { _ = probe.Close() }()
				conn, err := probe.Driver().Open(dsn)
				if err != nil {
					return nil, err
				}
				fault = &faultConnection{Conn: conn, at: at, kind: kind, cancel: cancel}
				return sql.OpenDB(indexConnector{conn: fault}), nil
			}
			got, err := ReadIndex(ctx, path)
			cancel()
			if fault == nil {
				t.Fatal("fault connection not created")
			}
			if !fault.fired {
				if err != nil {
					t.Fatalf("%s unarmed read failed: %v", kind, err)
				}
				break
			}
			if err == nil || !reflect.DeepEqual(got, Result{}) || strings.Contains(err.Error(), "private-source-value") {
				t.Fatalf("%s #%d retained rows or leaked error: %#v, %v", kind, at, got, err)
			}
			if strings.HasPrefix(kind, "cancel") && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancel #%d lost cancellation identity: %v", at, err)
			}
		}
	}
}

func TestReadIndexOpenPathAndCloseFailures(t *testing.T) {
	path := indexFixture(t, "boundary.db", hierarchyFixture)
	open, abs, closeDB := indexOpen, indexAbs, indexClose
	t.Cleanup(func() { indexOpen, indexAbs, indexClose = open, abs, closeDB })
	for _, kind := range []string{"path", "open", "close"} {
		indexOpen, indexAbs, indexClose = open, abs, closeDB
		switch kind {
		case "path":
			indexAbs = func(string) (string, error) { return "", errInjectedRead }
		case "open":
			indexOpen = func(string, string) (*sql.DB, error) { return nil, errInjectedRead }
		case "close":
			indexClose = func(db *sql.DB) error {
				_ = db.Close()
				return errInjectedRead
			}

		}
		got, err := ReadIndex(context.Background(), path)
		if err == nil || !reflect.DeepEqual(got, Result{}) || strings.Contains(err.Error(), "private-source-value") {
			t.Fatalf("%s boundary = %#v, error %v", kind, got, err)
		}
	}
}

func TestReadIndexCloseFailurePreservesCancellation(t *testing.T) {
	path := indexFixture(t, "cancel-close.db", hierarchyFixture)
	original := indexClose
	t.Cleanup(func() { indexClose = original })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	indexClose = func(db *sql.DB) error {
		err := db.Close()
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		return errInjectedRead
	}
	got, err := ReadIndex(ctx, path)
	if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(got, Result{}) {
		t.Fatalf("late close lost cancellation: %#v, %v", got, err)
	}
}

func TestReadIndexSuccessfulCloseStillChecksCancellation(t *testing.T) {
	path := indexFixture(t, "cancel-after-read.db", hierarchyFixture)
	original := indexClose
	t.Cleanup(func() { indexClose = original })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	indexClose = func(db *sql.DB) error {
		err := db.Close()
		cancel()
		return err
	}
	got, err := ReadIndex(ctx, path)
	if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(got, Result{}) {
		t.Fatalf("close-time cancellation published source: %#v, %v", got, err)
	}
}
