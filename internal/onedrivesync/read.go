package onedrivesync

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	_ "modernc.org/sqlite"
)

var openIndex = sql.Open
var indexAbs = filepath.Abs

type readLimits struct {
	fileBytes, fieldBytes, stringBytes int64
	rows                               [6]int
	ancestry                           int
}

// ReadIndex reads only a caller-supplied consistent consolidated private copy.
func ReadIndex(ctx context.Context, path string) (Result, error) {
	return readIndex(ctx, path, readLimits{64 << 20, 1 << 20, 64 << 20, [6]int{4096, 262144, 65536, 262144, 4096, 262144}, 256})
}

func readIndex(ctx context.Context, path string, limits readLimits) (result Result, err error) {
	defer func() {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		if err != nil {
			result = Result{}
		}
	}()
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return Result{}, readFailure(ctx)
	}
	if info.Size() > limits.fileBytes {
		return Result{}, &ReadError{Code: "onedrive_sync_index_too_large"}
	}
	absolute, err := indexAbs(path)
	if err != nil {
		return Result{}, &ReadError{Code: "onedrive_sync_index_unreadable"}
	}
	uri := url.URL{Scheme: "file", Path: "/" + strings.TrimPrefix(filepath.ToSlash(absolute), "/")}
	query := url.Values{"mode": {"ro"}, "immutable": {"1"}, "_pragma": {"query_only(1)"}}
	uri.RawQuery = query.Encode()
	db, err := openIndex("sqlite", uri.String())
	if err != nil {
		return Result{}, &ReadError{Code: "onedrive_sync_index_unreadable"}
	}
	db.SetMaxOpenConns(1)
	defer func() {
		if closeErr := db.Close(); closeErr != nil && err == nil {
			err = &ReadError{Code: "onedrive_sync_index_unreadable"}
		}
	}()
	var encoding string
	if err := db.QueryRowContext(ctx, "PRAGMA encoding").Scan(&encoding); err != nil {
		return Result{}, readFailure(ctx)
	}
	if encoding != "UTF-8" {
		return Result{}, &ReadError{Code: "onedrive_sync_index_unsupported"}
	}
	losses := map[string]int{}
	unmapped := func(kind string) error {
		losses["onedrive_sync_"+kind+"_unmapped"]++
		return nil
	}
	itemIdentities := map[string]string{}
	var retained int64
	charge := func(values []string) error {
		for _, value := range values {
			if int64(len(value)) > limits.stringBytes-retained {
				return &ReadError{Code: "onedrive_sync_index_too_large"}
			}
			retained += int64(len(value))
		}
		return nil
	}
	for index, source := range []struct {
		name, columns, order string
		visit                func([]any) error
	}{
		{"od_ScopeInfo_Records", "scopeID,sourceResourceID,siteID,webID,listID,webURL,remotePath,lastKnownFolderPath", "scopeID", func(v []any) error {
			s, ok := textValues(v)
			if !ok {
				return unmapped("scope")
			}
			if err := charge(s[1:]); err != nil {
				return err
			}
			result.Scopes = append(result.Scopes, Scope{s[0], s[1], s[2], s[3], s[4], s[5], s[6], s[7]})
			return nil
		}},
		{"od_ClientFile_Records", "resourceID,parentResourceID,fileName,size,lastChange,serverLastChange,fileStatus,lastKnownPinState", "resourceID", func(v []any) error {
			s, ok := textValues(v[:3])
			n, valid := integerValues(v[3:])
			if !ok || !valid {
				return unmapped("file")
			}
			if err := charge(s[1:]); err != nil {
				return err
			}
			result.Files = append(result.Files, File{s[0], s[1], s[2], n[0], n[1], n[2], n[3], n[4]})
			return nil
		}},
		{"od_ClientFolder_Records", "resourceID,parentResourceID,parentScopeID,folderName", "resourceID", func(v []any) error {
			s, ok := textValues(v)
			if !ok {
				return unmapped("folder")
			}
			if err := charge(s[1:]); err != nil {
				return err
			}
			result.Folders = append(result.Folders, Folder{s[0], s[1], s[2], s[3]})
			return nil
		}},
		{"od_GraphMetadata_Records", "resourceID,createdBy,modifiedBy,spoCompositeID", "resourceID", func(v []any) error {
			s, ok := textValues(v)
			if !ok {
				return unmapped("graph")
			}
			if err := charge(s[1:]); err != nil {
				return err
			}
			result.Graph = append(result.Graph, Graph{s[0], s[1], s[2], s[3]})
			return nil
		}},
		{"od_ClientPolicy_Records", "siteID,webID,listID,graphDriveId,siteTitle,libraryTitle,viewOnlineUrlTemplate,shareUrlTemplate,wacUrlTemplate,davUrlTemplate", "siteID,webID,listID", func(v []any) error {
			s, ok := textValues(v)
			if !ok {
				return unmapped("policy")
			}
			if err := charge(s[3:]); err != nil {
				return err
			}
			result.Policies = append(result.Policies, Policy{s[0], s[1], s[2], s[3], s[4], s[5], s[6], s[7], s[8], s[9]})
			return nil
		}},
		{"od_HydrationData", "resourceID,firstHydrationTime,lastHydrationTime,hydrationCount,lastHydrationType", "resourceID", func(v []any) error {
			id, ok := textValues(v[:1])
			n, valid := integerValues(v[1:4])
			kind, good := textValues(v[4:])
			if !ok || !valid || !good {
				return unmapped("hydration")
			}
			if err := charge(kind); err != nil {
				return err
			}
			result.Hydration = append(result.Hydration, Hydration{id[0], n[0], n[1], n[2], kind[0]})
			return nil
		}},
	} {
		kind := map[string]string{
			"od_ScopeInfo_Records": "scope", "od_ClientFile_Records": "file", "od_ClientFolder_Records": "folder",
			"od_GraphMetadata_Records": "graph", "od_ClientPolicy_Records": "policy", "od_HydrationData": "hydration",
		}[source.name]
		seen := map[[3]string]bool{}
		keyCount := len(strings.Split(source.order, ","))
		visit := func(values []any) error {
			ids, valid := textValues(values[:keyCount])
			if !valid {
				return unmapped(kind)
			}
			var key [3]string
			for i, id := range ids {
				if id == "" {
					return unmapped(kind)
				}
				key[i] = id
			}
			if seen[key] {
				return &ReadError{Code: "onedrive_sync_index_ambiguous"}
			}
			if err := charge(ids); err != nil {
				return err
			}
			seen[key] = true
			if kind == "file" || kind == "folder" {
				if _, exists := itemIdentities[key[0]]; exists {
					return &ReadError{Code: "onedrive_sync_index_ambiguous"}
				}
				itemIdentities[key[0]] = kind
			}
			return source.visit(values)
		}
		if err := capture(ctx, db, source.name, source.columns, source.order, limits.rows[index], limits.fieldBytes, visit); err != nil {
			return Result{}, err
		}
	}
	if err := relationships(ctx, &result, losses, limits.ancestry); err != nil {
		return Result{}, err
	}
	for code, count := range losses {
		result.Losses = append(result.Losses, Loss{Code: code, Count: count})
	}
	sort.Slice(result.Losses, func(i, j int) bool { return result.Losses[i].Code < result.Losses[j].Code })
	return result, nil
}

func capture(ctx context.Context, db *sql.DB, name, columns, order string, maxRows int, maxField int64, visit func([]any) error) error {
	if err := admitTable(ctx, db, name, columns, order); err != nil {
		return err
	}
	var count int64
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM "`+name+`"`).Scan(&count); err != nil {
		return readFailure(ctx)
	}
	if count > int64(maxRows) {
		return &ReadError{Code: "onedrive_sync_index_too_large"}
	}
	for _, column := range strings.Split(columns, ",") {
		var size sql.NullInt64
		if err := db.QueryRowContext(ctx, `SELECT max(length(CAST(`+column+` AS BLOB))) FROM "`+name+`"`).Scan(&size); err != nil {
			return readFailure(ctx)
		}
		if size.Valid && size.Int64 > maxField {
			return &ReadError{Code: "onedrive_sync_index_too_large"}
		}
	}
	rows, err := db.QueryContext(ctx, `SELECT +`+strings.ReplaceAll(columns, ",", ",+")+` FROM "`+name+`" ORDER BY `+order) // #nosec G202 -- names, columns and ordering are fixed literals in ReadIndex.
	if err != nil {
		return readFailure(ctx)
	}
	defer func() { _ = rows.Close() }()
	values := make([]any, len(strings.Split(columns, ",")))
	dest := make([]any, len(values))
	for i := range values {
		dest[i] = &values[i]
	}
	for rows.Next() {
		if err := rows.Scan(dest...); err != nil {
			return readFailure(ctx)
		}
		if err := visit(values); err != nil {
			return err
		}
	}
	if rows.Err() != nil {
		return readFailure(ctx)
	}
	return nil
}

func admitTable(ctx context.Context, db *sql.DB, name, columns, keys string) error {
	unsupported := &ReadError{Code: "onedrive_sync_index_unsupported"}
	var kind string
	if err := db.QueryRowContext(ctx, "SELECT type FROM pragma_table_list WHERE schema='main' AND name=?", name).Scan(&kind); err != nil {
		if err == sql.ErrNoRows {
			return unsupported
		}
		return readFailure(ctx)
	}
	if kind != "table" {
		return unsupported
	}
	keyColumns := strings.Split(keys, ",")
	var count int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM pragma_table_xinfo(?) WHERE pk>0", name).Scan(&count); err != nil {
		return readFailure(ctx)
	}
	if count != len(keyColumns) {
		return unsupported
	}
	for i, column := range keyColumns {
		if err := db.QueryRowContext(ctx, "SELECT count(*) FROM pragma_table_xinfo(?) WHERE name=? AND upper(type)='TEXT' AND pk=? AND hidden=0", name, column, i+1).Scan(&count); err != nil {
			return readFailure(ctx)
		}
		if count != 1 {
			return unsupported
		}
	}
	for _, column := range strings.Split(columns, ",") {
		if err := db.QueryRowContext(ctx, "SELECT count(*) FROM pragma_table_xinfo(?) WHERE name=? AND hidden=0", name, column).Scan(&count); err != nil {
			return readFailure(ctx)
		}
		if count != 1 {
			return unsupported
		}
	}
	return nil
}

func readFailure(ctx context.Context) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return &ReadError{Code: "onedrive_sync_index_unreadable"}
}

func textValues(values []any) ([]string, bool) {
	out := make([]string, len(values))
	for i, value := range values {
		if value == nil {
			continue
		}
		text, ok := value.(string)
		if !ok || !utf8.ValidString(text) {
			return nil, false
		}
		out[i] = text
	}
	return out, true
}

func integerValues(values []any) ([]*int64, bool) {
	out := make([]*int64, len(values))
	for i, value := range values {
		if value == nil {
			continue
		}
		number, ok := value.(int64)
		if !ok {
			return nil, false
		}
		out[i] = &number
	}
	return out, true
}
