package onedrivesync

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"reflect"
	"strings"
	"testing"
)

type duplicateConn struct {
	driver.Conn
	table string
}

func (c *duplicateConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if strings.Contains(query, ` FROM "`+c.table+`" ORDER BY `) {
		selectSQL, _, _ := strings.Cut(query, " ORDER BY ")
		field := map[string]string{
			"od_ScopeInfo_Records": "sourceResourceID", "od_ClientFile_Records": "fileName",
			"od_ClientFolder_Records": "folderName", "od_GraphMetadata_Records": "createdBy",
			"od_ClientPolicy_Records": "libraryTitle", "od_HydrationData": "lastHydrationType",
		}[c.table]
		second := strings.Replace(selectSQL, "+"+field, "CAST("+field+" AS BLOB)", 1)
		query = selectSQL + " UNION ALL " + second + " ORDER BY 1"
	}
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
}

type directConnector struct{ conn driver.Conn }

func (c directConnector) Connect(context.Context) (driver.Conn, error) { return c.conn, nil }
func (c directConnector) Driver() driver.Driver                        { return nil }

func TestReadIndexDuplicateIdentitiesRefuseAll(t *testing.T) {
	path := fixture(t, nativeSchema, nativeRows)
	original := openIndex
	t.Cleanup(func() { openIndex = original })
	for _, table := range []string{"od_ScopeInfo_Records", "od_ClientFile_Records", "od_ClientFolder_Records",
		"od_GraphMetadata_Records", "od_ClientPolicy_Records", "od_HydrationData"} {
		t.Run(table, func(t *testing.T) {
			openIndex = func(name, uri string) (*sql.DB, error) {
				probe, err := sql.Open(name, uri)
				if err != nil {
					return nil, err
				}
				defer func() { _ = probe.Close() }()
				conn, err := probe.Driver().Open(uri)
				if err != nil {
					return nil, err
				}
				return sql.OpenDB(directConnector{conn: &duplicateConn{Conn: conn, table: table}}), nil
			}
			got, err := ReadIndex(context.Background(), path)
			var typed *ReadError
			if !errors.As(err, &typed) || typed.Code != "onedrive_sync_index_ambiguous" || !reflect.DeepEqual(got, Result{}) {
				t.Fatalf("duplicate native key selected, including refused duplicate row: %#v,%v", got, err)
			}
		})
	}
}

var errInjected = errors.New("private-sync-payload")

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
	if (c.kind == "query" || c.kind == "cancel") && c.hit() {
		if c.kind == "cancel" {
			c.cancel()
		}
		return nil, errInjected
	}
	rows, err := c.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
	if err != nil {
		return nil, err
	}
	return &faultRows{Rows: rows, conn: c, data: strings.Contains(query, " ORDER BY ")}, nil
}

func (c *faultConn) Close() error {
	err := c.Conn.Close()
	if c.kind == "dbclose" {
		c.fired = true
		return errInjected
	}
	if c.kind == "cancelclose" {
		c.fired = true
		c.cancel()
	}
	return err
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

func TestReadIndexLateFailureRefusesAll(t *testing.T) {
	path := fixture(t, nativeSchema, nativeRows)
	original := openIndex
	t.Cleanup(func() { openIndex = original })
	for _, kind := range []string{"query", "next", "scan", "close", "cancel", "dbclose", "cancelclose"} {
		for at := 1; at < 250; at++ {
			ctx, cancel := context.WithCancel(context.Background())
			var fault *faultConn
			openIndex = func(name, uri string) (*sql.DB, error) {
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
				return sql.OpenDB(directConnector{conn: fault}), nil
			}
			got, err := ReadIndex(ctx, path)
			cancel()
			if fault == nil {
				t.Fatal("fault connection absent")
			}
			if !fault.fired {
				if err != nil {
					t.Fatalf("%s unarmed read failed: %v", kind, err)
				}
				break
			}
			if err == nil || !reflect.DeepEqual(got, Result{}) || strings.Contains(err.Error(), "private-sync-payload") {
				t.Fatalf("%s #%d published partial source: %v", kind, at, err)
			}
			if strings.HasPrefix(kind, "cancel") && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation identity lost: %v", err)
			}
			if kind == "dbclose" || kind == "cancelclose" {
				break
			}
		}
	}
}
