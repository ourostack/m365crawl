package onenote

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
	fileBytes, stringBytes, fieldBytes  int64
	entities, elements, flags, hashtags int64
}

func ReadIndex(ctx context.Context, indexPath string) (Result, error) {
	return readIndex(ctx, indexPath, readLimits{
		fileBytes: 64 << 20, stringBytes: 32 << 20, fieldBytes: 1 << 20,
		entities: 1 << 16, elements: 1 << 18, flags: 1 << 18, hashtags: 1 << 18,
	})
}

type ReadError struct {
	Code string
}

var (
	indexAbs   = filepath.Abs
	indexOpen  = sql.Open
	indexClose = (*sql.DB).Close
)

func (e *ReadError) Error() string { return "onenote: " + e.Code }

func readIndex(ctx context.Context, indexPath string, limits readLimits) (res Result, err error) {
	defer func() {
		if contextErr := ctx.Err(); contextErr != nil {
			res = Result{}
			err = contextErr
		}
	}()
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	info, err := os.Stat(indexPath)
	if err != nil || !info.Mode().IsRegular() {
		return Result{}, &ReadError{Code: "onenote_index_unreadable"}
	}
	if info.Size() > limits.fileBytes {
		return Result{}, &ReadError{Code: "onenote_index_too_large"}
	}
	path, err := indexAbs(indexPath)
	if err != nil {
		return Result{}, &ReadError{Code: "onenote_index_unreadable"}
	}
	uri := url.URL{
		Scheme: "file", Path: "/" + strings.TrimPrefix(filepath.ToSlash(path), "/"),
		RawQuery: "mode=ro&immutable=1",
	}
	db, err := indexOpen("sqlite", uri.String())
	if err != nil {
		return Result{}, &ReadError{Code: "onenote_index_unreadable"}
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
		return Result{}, &ReadError{Code: "onenote_index_unrecognized"}
	}
	losses := map[string]int{}
	pages := map[int64]bool{}
	goids := map[string]bool{}
	elements := map[int64]int{}
	var retained int64
	keep := func(values ...string) error {
		for _, value := range values {
			retained += int64(len(value))
		}
		if retained > limits.stringBytes {
			return &ReadError{Code: "onenote_index_too_large"}
		}
		return nil
	}
	err = readTable(ctx, db, "Entities",
		"rowid,Type,GOID,GUID,GOSID,ParentGOID,GrandparentGOIDs,LastModifiedTime,Title",
		limits.entities, limits.fieldBytes, func(v []any) error {
			id, idOK := v[0].(int64)
			kind, kindOK := v[1].(int64)
			goid, goidOK := nativeString(v[2], true)
			if goidOK {
				if goids[goid] {
					return &ReadError{Code: "onenote_index_unrecognized"}
				}
				if err := keep(goid); err != nil {
					return err
				}
				goids[goid] = true
			}
			guid, guidOK := nativeString(v[3], true)
			gosid, gosidOK := nativeString(v[4], false)
			parent, parentOK := nativeString(v[5], false)
			ancestors, ancestorsOK := nativeString(v[6], false)
			title, titleOK := nativeString(v[8], false)
			if !idOK || !kindOK || !goidOK || !guidOK || !gosidOK || !parentOK || !ancestorsOK || !titleOK {
				losses["onenote_entity_unmapped"]++
				return nil
			}
			if kind < 1 || kind > 4 {
				losses["onenote_entity_unsupported"]++
				return nil
			}
			e := Entity{RowID: id, Type: kind, GOID: goid, GUID: guid, GOSID: gosid,
				ParentGOID: parent, GrandparentGOIDs: ancestors, Title: title}
			if v[7] != nil {
				ticks, ok := v[7].(int64)
				if ok {
					e.ModifiedAt, ok = filetime(ticks)
				}
				if !ok {
					losses["onenote_modified_unknown"]++
				}
			}
			if err := keep(guid, gosid, parent, ancestors, title); err != nil {
				return err
			}
			pages[id] = kind == 1
			res.Entities = append(res.Entities, e)
			return nil
		})
	if err != nil {
		return Result{}, err
	}
	err = readTable(ctx, db, "PageElements", "rowid,GOID,Text,Jcid,EntityRowId",
		limits.elements, limits.fieldBytes, func(v []any) error {
			id, idOK := v[0].(int64)
			goid, goidOK := nativeString(v[1], true)
			text, textOK := nativeString(v[2], false)
			jcid, jcidOK := v[3].(int64)
			parent, parentOK := v[4].(int64)
			if !idOK || !goidOK || !textOK || !jcidOK || !parentOK {
				losses["onenote_element_unmapped"]++
				return nil
			}
			kind := ""
			switch jcid {
			case 393230:
				kind = "rich_text"
			case 393233:
				kind = "ocr"
			default:
				losses["onenote_element_unsupported"]++
				return nil
			}
			if !pages[parent] {
				losses["onenote_element_orphan"]++
				return nil
			}
			if err := keep(goid, text); err != nil {
				return err
			}
			elements[id] = len(res.Elements)
			res.Elements = append(res.Elements, Element{
				RowID: id, EntityRowID: parent, Jcid: jcid, GOID: goid, Text: text, Kind: kind,
			})
			return nil
		})
	if err != nil {
		return Result{}, err
	}
	err = readTable(ctx, db, "NoteFlags", "rowid,Type,Shape,Status,Label,PageElementRowId",
		limits.flags, limits.fieldBytes, func(v []any) error {
			id, idOK := v[0].(int64)
			kind, kindOK := v[1].(int64)
			shape, shapeOK := v[2].(int64)
			status, statusOK := v[3].(int64)
			label, labelOK := nativeString(v[4], false)
			parent, parentOK := v[5].(int64)
			if !idOK || !kindOK || !shapeOK || !statusOK || !labelOK || !parentOK {
				losses["onenote_flag_unmapped"]++
				return nil
			}
			if _, exists := elements[parent]; !exists {
				losses["onenote_flag_orphan"]++
				return nil
			}
			if err := keep(label); err != nil {
				return err
			}
			res.Flags = append(res.Flags, Flag{
				RowID: id, ElementRowID: parent, Type: kind, Shape: shape, Status: status, Label: label,
			})
			return nil
		})
	if err != nil {
		return Result{}, err
	}
	err = readTable(ctx, db, "Hashtags", "rowid,PageElementRowId", limits.hashtags,
		limits.fieldBytes, func(v []any) error {
			_, idOK := v[0].(int64)
			parent, parentOK := v[1].(int64)
			if !idOK || !parentOK {
				losses["onenote_hashtag_unmapped"]++
				return nil
			}
			if index, exists := elements[parent]; exists {
				res.Elements[index].HashtagMarkers++
			} else {
				losses["onenote_hashtag_orphan"]++
			}
			return nil
		})
	if err != nil {
		return Result{}, err
	}
	for _, code := range slices.Sorted(maps.Keys(losses)) {
		res.Losses = append(res.Losses, Loss{Code: code, Count: losses[code]})
	}
	res.Ordering = "index_rowid_not_document_layout"
	return res, nil
}

func safeReadError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return &ReadError{Code: "onenote_index_unreadable"}
}

func nativeString(value any, required bool) (string, bool) {
	if value == nil {
		return "", !required
	}
	text, ok := value.(string)
	return text, ok && utf8.ValidString(text) && !strings.ContainsRune(text, 0) &&
		(!required || strings.TrimSpace(text) != "")
}

func readTable(ctx context.Context, db *sql.DB, table, columns string, maxRows, maxField int64, visit func([]any) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var relation string
	if err := db.QueryRowContext(ctx, "SELECT type FROM pragma_table_list WHERE schema='main' AND name=?", table).Scan(&relation); err != nil || relation != "table" {
		return &ReadError{Code: "onenote_index_unrecognized"}
	}
	var rowIDs, primaryKeys int64
	if err := db.QueryRowContext(ctx, `
SELECT count(*) FILTER (WHERE name='rowid' AND upper(type)='INTEGER' AND pk=1 AND hidden=0),
       count(*) FILTER (WHERE pk>0)
FROM pragma_table_xinfo(?)`, table).Scan(&rowIDs, &primaryKeys); err != nil || rowIDs != 1 || primaryKeys != 1 {
		return &ReadError{Code: "onenote_index_unrecognized"}
	}
	check, err := db.QueryContext(ctx, "SELECT "+columns+" FROM "+table+" LIMIT 0") // #nosec G202 -- identifiers are fixed literals at the four internal call sites.
	if err != nil {
		return &ReadError{Code: "onenote_index_unrecognized"}
	}
	if err := check.Close(); err != nil {
		return safeReadError(ctx)
	}
	var count int64
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&count); err != nil {
		return safeReadError(ctx)
	}
	if count > maxRows {
		return &ReadError{Code: "onenote_index_too_large"}
	}
	for _, column := range strings.Split(columns, ",") {
		var size sql.NullInt64
		if err := db.QueryRowContext(ctx, "SELECT max(length(CAST("+column+" AS BLOB))) FROM "+table).Scan(&size); err != nil {
			return safeReadError(ctx)
		}
		if size.Valid && size.Int64 > maxField {
			return &ReadError{Code: "onenote_index_too_large"}
		}
	}
	rows, err := db.QueryContext(ctx, "SELECT "+columns+" FROM "+table+" ORDER BY rowid") // #nosec G202 -- identifiers are fixed literals at the four internal call sites.
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
