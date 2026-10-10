package officeregistry

import (
	"context"
	"database/sql"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	_ "modernc.org/sqlite"
)

type readLimits struct {
	fileBytes, fieldBytes, stringBytes int64
	nodes, values, depth               int
}

type ReadError struct{ Code string }

func (e *ReadError) Error() string { return "office: " + e.Code }

var (
	indexAbs   = filepath.Abs
	indexOpen  = sql.Open
	indexClose = (*sql.DB).Close
)

func ReadIndex(ctx context.Context, path string) (Result, error) {
	return readIndex(ctx, path, readLimits{
		fileBytes: 64 << 20, fieldBytes: 1 << 20, stringBytes: 16 << 20,
		nodes: 1 << 16, values: 1 << 18, depth: 128,
	})
}

func readIndex(ctx context.Context, source string, limits readLimits) (res Result, err error) {
	defer func() {
		if contextErr := ctx.Err(); contextErr != nil {
			res, err = Result{}, contextErr
		}
	}()
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	info, err := os.Stat(source)
	if err != nil || !info.Mode().IsRegular() {
		return Result{}, &ReadError{Code: "office_registry_index_unreadable"}
	}
	if info.Size() > limits.fileBytes {
		return Result{}, &ReadError{Code: "office_registry_index_too_large"}
	}
	absolute, err := indexAbs(source)
	if err != nil {
		return Result{}, &ReadError{Code: "office_registry_index_unreadable"}
	}
	uri := url.URL{Scheme: "file", Path: "/" + strings.TrimPrefix(filepath.ToSlash(absolute), "/"),
		RawQuery: "mode=ro&immutable=1"}
	db, err := indexOpen("sqlite", uri.String())
	if err != nil {
		return Result{}, &ReadError{Code: "office_registry_index_unreadable"}
	}
	defer func() {
		if closeErr := indexClose(db); closeErr != nil {
			res = Result{}
			if err == nil {
				err = safeReadError(ctx)
			}
		}
	}()
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, "PRAGMA query_only=ON"); err != nil {
		return Result{}, safeReadError(ctx)
	}
	var encoding string
	if err := db.QueryRowContext(ctx, "PRAGMA encoding").Scan(&encoding); err != nil {
		return Result{}, safeReadError(ctx)
	}
	if encoding != "UTF-8" {
		return Result{}, &ReadError{Code: "office_registry_index_unrecognized"}
	}
	nodes := map[int64]nativeNode{}
	targets := map[int64]string{}
	values := map[int64]map[string]any{}
	invalid := map[int64]bool{}
	losses := map[string]int{}
	var retained int64
	keep := func(text string) error {
		retained += int64(len(text))
		if retained > limits.stringBytes {
			return &ReadError{Code: "office_registry_index_too_large"}
		}
		return nil
	}
	err = readTable(ctx, db, "HKEY_CURRENT_USER", "node_id,parent_id,name", limits.nodes, limits.fieldBytes, nil,
		func(row []any) error {
			id, idOK := row[0].(int64)
			parent, parentOK := row[1].(int64)
			name, nameOK := nativeText(row[2])
			if !idOK || !parentOK || !nameOK || name == "" {
				if idOK {
					nodes[id] = nativeNode{malformed: true}
				}
				losses["office_registry_ancestry_unmapped"]++
				return nil
			}
			if err := keep(name); err != nil {
				return err
			}
			nodes[id] = nativeNode{parent: parent, name: name}
			return nil
		})
	if err != nil {
		return Result{}, err
	}
	for _, id := range slices.Sorted(maps.Keys(nodes)) {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		app, valid := applicationFor(nodes, id, limits.depth)
		if !valid {
			losses["office_registry_ancestry_unmapped"]++
			continue
		}
		if app != "" {
			targets[id] = app
			values[id] = map[string]any{}
		}
	}
	err = readTable(ctx, db, "HKEY_CURRENT_USER_values", "node_id,name,type,value", limits.values, limits.fieldBytes, slices.Sorted(maps.Keys(targets)),
		func(row []any) error {
			id, idOK := row[0].(int64)
			if !idOK || targets[id] == "" {
				return nil
			}
			name, nameOK := nativeText(row[1])
			if !nameOK {
				return nil
			}
			if _, duplicate := values[id][name]; duplicate {
				return &ReadError{Code: "office_registry_index_unrecognized"}
			}
			kind, kindOK := row[2].(int64)
			value, valueOK := registryValue(name, kind, row[3])
			values[id][name] = value
			if !kindOK || !valueOK {
				invalid[id] = true
				return nil
			}
			if text, ok := value.(string); ok {
				return keep(text)
			}
			return nil
		})
	if err != nil {
		return Result{}, err
	}
	for _, id := range slices.Sorted(maps.Keys(targets)) {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		v := values[id]
		title, _ := v["FileName"].(string)
		uri, _ := v["DocumentUrl"].(string)
		if invalid[id] || strings.TrimSpace(title) == "" || strings.TrimSpace(uri) == "" {
			losses["office_registry_document_unmapped"]++
			continue
		}
		app, _ := v["Application"].(string)
		path, _ := v["Path"].(string)
		opened, _ := v["Timestamp"].(string)
		document := Observation{NodeID: id, ApplicationPath: targets[id], ApplicationValue: app,
			Title: title, URL: uri, FriendlyPath: path, OpenedRaw: opened}
		if size, ok := v["FileSizeInBytes"].(int64); ok {
			document.Size = &size
		}
		if pin, ok := v["IsPinned"].(int64); ok {
			known := pin == 1
			document.Pinned = &known
		}
		res.Documents = append(res.Documents, document)
	}
	for _, code := range slices.Sorted(maps.Keys(losses)) {
		res.Losses = append(res.Losses, Loss{Code: code, Count: losses[code]})
	}
	return res, nil
}

func safeReadError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return &ReadError{Code: "office_registry_index_unreadable"}
}

func nativeText(value any) (string, bool) {
	text, ok := value.(string)
	return text, ok && utf8.ValidString(text) && !strings.ContainsRune(text, 0)
}

func registryValue(name string, kind int64, value any) (any, bool) {
	if value == nil {
		return nil, true
	}
	switch name {
	case "FileSizeInBytes":
		number, ok := value.(int64)
		return number, kind == 11 && ok && number >= 0
	case "IsPinned":
		number, ok := value.(int64)
		return number, kind == 4 && ok && (number == 0 || number == 1)
	default:
		text, ok := nativeText(value)
		return text, kind == 1 && ok
	}
}

func readTable(ctx context.Context, db *sql.DB, table, columns string, maxRows int, maxField int64, ids []int64, visit func([]any) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var relation string
	if err := db.QueryRowContext(ctx, "SELECT type FROM pragma_table_list WHERE schema='main' AND name=?", table).Scan(&relation); err != nil || relation != "table" {
		return &ReadError{Code: "office_registry_index_unrecognized"}
	}
	if table == "HKEY_CURRENT_USER" {
		var identity, primaryKeys int64
		if err := db.QueryRowContext(ctx, `
SELECT count(*) FILTER (WHERE name='node_id' AND upper(type)='INTEGER' AND pk=1 AND hidden=0),
       count(*) FILTER (WHERE pk>0)
FROM pragma_table_xinfo(?)`, table).Scan(&identity, &primaryKeys); err != nil || identity != 1 || primaryKeys != 1 {
			return &ReadError{Code: "office_registry_index_unrecognized"}
		}
	}
	check, err := db.QueryContext(ctx, "SELECT "+columns+" FROM "+table+" LIMIT 0") // #nosec G202 -- both callers supply only fixed internal identifiers.
	if err != nil {
		return &ReadError{Code: "office_registry_index_unrecognized"}
	}
	if err := check.Close(); err != nil {
		return safeReadError(ctx)
	}
	var count int64
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&count); err != nil {
		return safeReadError(ctx)
	}
	if count > int64(maxRows) {
		return &ReadError{Code: "office_registry_index_too_large"}
	}
	selections := [][]int64{nil}
	if table == "HKEY_CURRENT_USER_values" {
		selections = nil
		for first := 0; first < len(ids); first += 256 {
			selections = append(selections, ids[first:min(first+256, len(ids))])
		}
	}
	for _, selection := range selections {
		where := ""
		var args []any
		if table == "HKEY_CURRENT_USER_values" {
			where = " WHERE name COLLATE BINARY IN ('FileName','DocumentUrl','Path','Timestamp','Application','FileSizeInBytes','IsPinned') AND node_id IN (" +
				strings.TrimSuffix(strings.Repeat("?,", len(selection)), ",") + ")"
			for _, id := range selection {
				args = append(args, id)
			}
		}
		if err := readSelection(ctx, db, table, columns, where, args, maxField, visit); err != nil {
			return err
		}
	}
	return nil
}

func readSelection(ctx context.Context, db *sql.DB, table, columns, where string, args []any, maxField int64, visit func([]any) error) error {
	for _, column := range strings.Split(columns, ",") {
		var size sql.NullInt64
		if err := db.QueryRowContext(ctx, "SELECT max(length(CAST("+column+" AS BLOB))) FROM "+table+where, args...).Scan(&size); err != nil {
			return safeReadError(ctx)
		}
		if size.Valid && size.Int64 > maxField {
			return &ReadError{Code: "office_registry_index_too_large"}
		}
	}
	rows, err := db.QueryContext(ctx, "SELECT "+columns+" FROM "+table+where+" ORDER BY node_id", args...) // #nosec G202 -- identifiers/filter are internal; native IDs use bound parameters.
	if err != nil {
		return safeReadError(ctx)
	}
	defer func() { _ = rows.Close() }()
	values := make([]any, len(strings.Split(columns, ",")))
	dest := make([]any, len(values))
	for i := range values {
		dest[i] = &values[i]
	}
	for rows.Next() {
		if err := rows.Scan(dest...); err != nil {
			return safeReadError(ctx)
		}
		if err := visit(values); err != nil {
			return err
		}
	}
	if rows.Err() != nil {
		return safeReadError(ctx)
	}
	return nil
}
