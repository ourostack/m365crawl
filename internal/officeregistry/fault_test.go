package officeregistry

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

var errInjected = errors.New("private-registry-value")

type faultConn struct {
	driver.Conn
	kind      string
	at, calls int
	fired     bool
	cancel    context.CancelFunc
}

func (c *faultConn) hit() bool {
	c.calls++
	if c.calls != c.at {
		return false
	}
	c.fired = true
	return true
}

func (c *faultConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if c.kind == "query" && c.hit() {
		return nil, errInjected
	}
	if c.kind == "cancel" && c.hit() {
		c.cancel()
		return nil, errInjected
	}
	rows, err := c.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
	if err != nil {
		return nil, err
	}
	return &faultRows{Rows: rows, conn: c, data: strings.HasSuffix(query, "ORDER BY node_id"),
		values: strings.Contains(query, "FROM HKEY_CURRENT_USER_values")}, nil
}

func (c *faultConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if c.kind == "exec" && c.hit() {
		return nil, errInjected
	}
	return c.Conn.(driver.ExecerContext).ExecContext(ctx, query, args)
}

type faultRows struct {
	driver.Rows
	conn   *faultConn
	data   bool
	values bool
}

func (r *faultRows) Columns() []string {
	columns := r.Rows.Columns()
	if r.data && r.conn.kind == "scan" && (r.conn.fired || r.conn.hit()) {
		return append(columns, "unexpected")
	}
	return columns
}

func (r *faultRows) Next(values []driver.Value) error {
	if r.data && r.conn.kind == "next" && r.conn.hit() {
		return errInjected
	}
	if r.conn.kind == "scan" && len(values) > len(r.Rows.Columns()) {
		values = values[:len(r.Rows.Columns())]
	}
	err := r.Rows.Next(values)
	if err == nil && r.data && r.values && (r.conn.kind == "value-id" || r.conn.kind == "value-name") && r.conn.hit() {
		column := 0
		if r.conn.kind == "value-name" {
			column = 1
		}
		values[column] = nil
	}
	return err
}

func (r *faultRows) Close() error {
	err := r.Rows.Close()
	if r.conn.kind == "close" && r.conn.hit() {
		return errInjected
	}
	if r.data && r.values && r.conn.kind == "cancel-value-close" && r.conn.hit() {
		r.conn.cancel()
	}
	return err
}

type faultConnector struct{ conn *faultConn }

func (c faultConnector) Connect(context.Context) (driver.Conn, error) { return c.conn, nil }
func (c faultConnector) Driver() driver.Driver                        { return nil }

func TestReadIndexFatalFailureDiscardsAll(t *testing.T) {
	path := registryFixture(t, "fault.db", fixtureSchema, nativeFixture)
	open := indexOpen
	t.Cleanup(func() { indexOpen = open })
	for _, kind := range []string{"query", "exec", "next", "scan", "close", "cancel", "value-id", "value-name", "cancel-value-close"} {
		for at := 1; at < 80; at++ {
			ctx, cancel := context.WithCancel(context.Background())
			var fault *faultConn
			indexOpen = func(name, uri string) (*sql.DB, error) {
				probe, err := sql.Open(name, uri)
				if err != nil {
					return nil, err
				}
				defer func() { _ = probe.Close() }()
				conn, err := probe.Driver().Open(uri)
				if err != nil {
					return nil, err
				}
				fault = &faultConn{Conn: conn, kind: kind, at: at, cancel: cancel}
				return sql.OpenDB(faultConnector{conn: fault}), nil
			}
			got, err := ReadIndex(ctx, path)
			cancel()
			if fault == nil {
				t.Fatal("fault connection not created")
			}
			if !fault.fired {
				if err != nil {
					t.Fatalf("unarmed %s read failed: %v", kind, err)
				}
				break
			}
			if kind == "value-id" || kind == "value-name" {
				if err != nil {
					t.Fatalf("malformed selected value was admitted: %#v, %v", got, err)
				}
				continue
			}
			if err == nil || !reflect.DeepEqual(got, Result{}) || strings.Contains(err.Error(), "private-registry-value") {
				t.Fatalf("%s #%d leaked partial data/error: %#v, %v", kind, at, got, err)
			}
			if strings.HasPrefix(kind, "cancel") && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation identity lost: %v", err)
			}
		}
	}
}

func TestReadIndexOpenAndTerminalCloseBoundaries(t *testing.T) {
	path := registryFixture(t, "boundary.db", fixtureSchema, nativeFixture)
	open, abs, closeDB := indexOpen, indexAbs, indexClose
	t.Cleanup(func() { indexOpen, indexAbs, indexClose = open, abs, closeDB })
	for _, kind := range []string{"path", "open", "close", "close-cancel", "successful-close-cancel"} {
		indexOpen, indexAbs, indexClose = open, abs, closeDB
		ctx, cancel := context.WithCancel(context.Background())
		switch kind {
		case "path":
			indexAbs = func(string) (string, error) { return "", errInjected }
		case "open":
			indexOpen = func(string, string) (*sql.DB, error) { return nil, errInjected }
		default:
			indexClose = func(db *sql.DB) error {
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
				if strings.Contains(kind, "cancel") {
					cancel()
				}
				if kind == "successful-close-cancel" {
					return nil
				}
				return errInjected
			}
		}
		got, err := ReadIndex(ctx, path)
		cancel()
		if err == nil || !reflect.DeepEqual(got, Result{}) || strings.Contains(err.Error(), "private-registry-value") {
			t.Fatalf("%s boundary = %#v, %v", kind, got, err)
		}
		if strings.Contains(kind, "cancel") && !errors.Is(err, context.Canceled) {
			t.Fatalf("close cancellation identity lost: %v", err)
		}
	}
}

type boundaryContext struct {
	context.Context
	cancel context.CancelFunc
	checks atomic.Int64
	at     int
}

func (c *boundaryContext) Err() error {
	if c.checks.Add(1) == int64(c.at) {
		c.cancel()
	}
	return c.Context.Err()
}

func TestReadIndexCancellationAtEveryBoundary(t *testing.T) {
	path := registryFixture(t, "checked-cancel.db", fixtureSchema, nativeFixture)
	for at := 1; at < 200; at++ {
		parent, cancel := context.WithCancel(context.Background())
		ctx := &boundaryContext{Context: parent, cancel: cancel, at: at}
		got, err := ReadIndex(ctx, path)
		cancelled := parent.Err() != nil
		cancel()
		if !cancelled {
			if err != nil || len(got.Documents) != 1 {
				t.Fatalf("uncancelled source = %#v, %v", got, err)
			}
			break
		}
		if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(got, Result{}) {
			t.Fatalf("cancelled native source published partial result: %#v, %v", got, err)
		}
	}
}
