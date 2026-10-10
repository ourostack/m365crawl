package onedrivelists

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

var errInjected = errors.New("private-lists-value")

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
	return &faultRows{Rows: rows, conn: c, data: strings.Contains(query, " ORDER BY ")}, nil
}

func (c *faultConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if c.kind == "exec" && c.hit() {
		return nil, errInjected
	}
	return c.Conn.(driver.ExecerContext).ExecContext(ctx, query, args)
}

type faultRows struct {
	driver.Rows
	conn *faultConn
	data bool
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
	return r.Rows.Next(values)
}
func (r *faultRows) Close() error {
	err := r.Rows.Close()
	if r.conn.kind == "close" && r.conn.hit() {
		return errInjected
	}
	return err
}

type connector struct{ conn *faultConn }

func (c connector) Connect(context.Context) (driver.Conn, error) { return c.conn, nil }
func (c connector) Driver() driver.Driver                        { return nil }

func TestReadIndexFatalFailureNeverBecomesUnavailableScope(t *testing.T) {
	path := fixture(t, nativeScope(siteA)+`CREATE TABLE "`+inventoryTable(siteA)+`"`+itemSchema+`;CREATE TABLE "`+usersTable(siteA)+`"`+userSchema+`;`)
	uri := url.URL{Scheme: "file", Path: "/" + strings.TrimPrefix(filepath.ToSlash(path), "/")}
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO "` + usersTable(siteA) + `" VALUES(1,'Title','Person');INSERT INTO "` + inventoryTable(siteA) + `" VALUES(1,'unique','path','Name','0',1,1,NULL,NULL,NULL,NULL,NULL);`); err != nil { // #nosec G202 -- synthetic fixture identifiers derive only from literal test constants.
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	open := indexOpen
	t.Cleanup(func() { indexOpen = open })
	for _, kind := range []string{"query", "exec", "next", "scan", "close", "cancel"} {
		for at := 1; at < 150; at++ {
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
				return sql.OpenDB(connector{conn: fault}), nil
			}
			got, err := ReadIndex(ctx, path)
			cancel()
			if fault == nil {
				t.Fatal("fault connection absent")
			}
			if !fault.fired {
				if err != nil {
					t.Fatalf("unarmed read failed: %v", err)
				}
				break
			}
			if err == nil || !reflect.DeepEqual(got, Result{}) || strings.Contains(err.Error(), "private-lists-value") {
				t.Fatalf("%s #%d fatal failure mapped to partial/unavailable success: %#v,%v", kind, at, got, err)
			}
		}
	}
}
