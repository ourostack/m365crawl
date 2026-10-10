package onedrivelists

import (
	"context"
	"database/sql"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	_ "modernc.org/sqlite"
)

type readLimits struct {
	fileBytes, fieldBytes, stringBytes int64
	scopes, items, users               int
}

type ReadError struct{ Code string }

func (e *ReadError) Error() string { return "onedrive: " + e.Code }

var (
	indexAbs   = filepath.Abs
	indexOpen  = sql.Open
	indexClose = (*sql.DB).Close
	nativeGUID = regexp.MustCompile(`^[a-fA-F0-9]{8}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{12}$`)
)

func ReadIndex(ctx context.Context, path string) (Result, error) {
	return readIndex(ctx, path, readLimits{fileBytes: 768 << 20, fieldBytes: 1 << 20, stringBytes: 64 << 20,
		scopes: 4096, items: 1 << 18, users: 1 << 18})
}

func readIndex(ctx context.Context, path string, limits readLimits) (res Result, err error) {
	defer func() {
		if contextErr := ctx.Err(); contextErr != nil {
			res, err = Result{}, contextErr
		}
	}()
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return Result{}, &ReadError{Code: "onedrive_lists_index_unreadable"}
	}
	if info.Size() > limits.fileBytes {
		return Result{}, &ReadError{Code: "onedrive_lists_index_too_large"}
	}
	absolute, err := indexAbs(path)
	if err != nil {
		return Result{}, &ReadError{Code: "onedrive_lists_index_unreadable"}
	}
	uri := url.URL{Scheme: "file", Path: "/" + strings.TrimPrefix(filepath.ToSlash(absolute), "/"),
		RawQuery: "mode=ro&immutable=1"}
	db, err := indexOpen("sqlite", uri.String())
	if err != nil {
		return Result{}, &ReadError{Code: "onedrive_lists_index_unreadable"}
	}
	defer func() {
		if closeErr := indexClose(db); closeErr != nil {
			res = Result{}
			if err == nil {
				err = safeError(ctx)
			}
		}
	}()
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, "PRAGMA query_only=ON"); err != nil {
		return Result{}, safeError(ctx)
	}
	var encoding string
	if err := db.QueryRowContext(ctx, "PRAGMA encoding").Scan(&encoding); err != nil {
		return Result{}, safeError(ctx)
	}
	if encoding != "UTF-8" {
		return Result{}, &ReadError{Code: "onedrive_lists_index_unrecognized"}
	}
	losses := map[string]int{}
	var retained int64
	keep := func(values ...string) error {
		for _, text := range values {
			retained += int64(len(text))
		}
		if retained > limits.stringBytes {
			return &ReadError{Code: "onedrive_lists_index_too_large"}
		}
		return nil
	}
	scopes := 0
	available, err := table(ctx, db, "lists", "listID,webID,siteID,driveID,title,siteUrl,listUrl,lastSyncTime",
		"", "listID,siteID", &scopes, limits.scopes, limits.fieldBytes, func(row []any) error {
			values, ok := texts(row[:7])
			synced, syncOK := integer(row[7])
			if !ok || !syncOK || !nativeGUID.MatchString(values[0]) || !nativeGUID.MatchString(values[2]) {
				losses["onedrive_lists_scope_unmapped"]++
				return nil
			}
			if err := keep(values...); err != nil {
				return err
			}
			res.Scopes = append(res.Scopes, Scope{ListID: values[0], WebID: values[1], SiteID: values[2],
				DriveID: values[3], Title: values[4], SiteURL: values[5], ListURL: values[6], LastSyncRaw: synced})
			return nil
		})
	if err != nil {
		return Result{}, err
	}
	if !available {
		return Result{}, &ReadError{Code: "onedrive_lists_index_unrecognized"}
	}
	slices.SortFunc(res.Scopes, func(a, b Scope) int {
		if n := strings.Compare(a.SiteID, b.SiteID); n != 0 {
			return n
		}
		return strings.Compare(a.ListID, b.ListID)
	})
	sites := map[string]map[int64]bool{}
	itemRows, userRows := 0, 0
	for i := range res.Scopes {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		scope := &res.Scopes[i]
		if _, seen := sites[scope.SiteID]; !seen {
			known := map[int64]bool{}
			values := map[int64]map[string]string{}
			userTable := "site_" + scope.SiteID + "_users"
			usersAvailable, err := table(ctx, db, userTable, "id,key,value",
				" WHERE key COLLATE BINARY IN ('Title','EMail','Name','SipAddress')", "id,key",
				&userRows, limits.users, limits.fieldBytes, func(row []any) error {
					id, idOK := row[0].(int64)
					text, textOK := texts(row[1:])
					if !idOK || !textOK {
						losses["onedrive_lists_user_unmapped"]++
						return nil
					}
					if err := keep(text...); err != nil {
						return err
					}
					if values[id] == nil {
						values[id] = map[string]string{}
					}
					values[id][text[0]] = text[1]
					known[id] = true
					return nil
				})
			if err != nil {
				return Result{}, err
			}
			if !usersAvailable {
				losses["onedrive_lists_users_unavailable"]++
			}
			for _, id := range slices.Sorted(maps.Keys(values)) {
				v := values[id]
				res.Users = append(res.Users, User{SiteID: scope.SiteID, ID: id, Title: v["Title"], Email: v["EMail"], Name: v["Name"], SIP: v["SipAddress"]})
			}
			sites[scope.SiteID] = known
		}
		ids, uniques := map[int64]bool{}, map[string]bool{}
		inventory := "list_" + scope.ListID + "_" + scope.SiteID + "_rows"
		itemsAvailable, err := table(ctx, db, inventory,
			"ID,UniqueId,FileRef,FileLeafRef,FSObjType,Author,Editor,Created,Modified,Last_x0020_Modified,ServerUrl,File_x0020_Type",
			"", "ID", &itemRows, limits.items, limits.fieldBytes, func(row []any) error {
				id, idOK := row[0].(int64)
				if idOK {
					if ids[id] {
						return &ReadError{Code: "onedrive_lists_index_unrecognized"}
					}
					ids[id] = true
				}
				unique, uniqueOK := text(row[1])
				if uniqueOK && unique != "" {
					if uniques[unique] {
						return &ReadError{Code: "onedrive_lists_index_unrecognized"}
					}
					if err := keep(unique); err != nil {
						return err
					}
					uniques[unique] = true
				}
				head, headOK := texts(row[2:5])
				tail, tailOK := texts(row[7:])
				author, authorOK := integer(row[5])
				editor, editorOK := integer(row[6])
				if !idOK || !uniqueOK || unique == "" || !headOK || !tailOK || !authorOK || !editorOK ||
					(head[2] != "0" && head[2] != "1") {
					losses["onedrive_lists_item_unmapped"]++
					return nil
				}
				if err := keep(append(head, tail...)...); err != nil {
					return err
				}
				kind := "file"
				if head[2] == "1" {
					kind = "folder"
				}
				for _, person := range []*int64{author, editor} {
					if person != nil && !sites[scope.SiteID][*person] {
						losses["onedrive_lists_user_reference_unknown"]++
					}
				}
				res.Items = append(res.Items, Item{SiteID: scope.SiteID, WebID: scope.WebID, ListID: scope.ListID,
					ID: id, UniqueID: unique, Path: head[0], Name: head[1], Kind: kind, AuthorID: author, EditorID: editor,
					CreatedRaw: tail[0], ModifiedRaw: tail[1], LastModifiedRaw: tail[2], ServerURL: tail[3], Extension: tail[4]})
				return nil
			})
		if err != nil {
			return Result{}, err
		}
		scope.InventoryPresent = itemsAvailable
		if !itemsAvailable {
			losses["onedrive_lists_inventory_unavailable"]++
		}
	}
	for _, code := range slices.Sorted(maps.Keys(losses)) {
		res.Losses = append(res.Losses, Loss{Code: code, Count: losses[code]})
	}
	return res, nil
}

func safeError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return &ReadError{Code: "onedrive_lists_index_unreadable"}
}

func text(value any) (string, bool) {
	if value == nil {
		return "", true
	}
	s, ok := value.(string)
	return s, ok && utf8.ValidString(s) && !strings.ContainsRune(s, 0)
}

func texts(values []any) ([]string, bool) {
	res := make([]string, len(values))
	valid := true
	for i, value := range values {
		var ok bool
		res[i], ok = text(value)
		valid = valid && ok
	}
	return res, valid
}

func integer(value any) (*int64, bool) {
	if value == nil {
		return nil, true
	}
	i, ok := value.(int64)
	return &i, ok
}

func table(ctx context.Context, db *sql.DB, name, columns, where, order string, total *int, maxRows int, maxField int64, visit func([]any) error) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	var kind string
	if err := db.QueryRowContext(ctx, "SELECT type FROM pragma_table_list WHERE schema='main' AND name=?", name).Scan(&kind); err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, safeError(ctx)
	}
	if kind != "table" {
		return false, nil
	}
	if name == "lists" || strings.HasSuffix(name, "_users") {
		first, second, firstType := "listID", "siteID", "TEXT"
		if name != "lists" {
			first, second, firstType = "id", "key", "INTEGER"
		}
		var count, valid int
		if err := db.QueryRowContext(ctx, `
SELECT count(*) FILTER (WHERE pk>0),
count(*) FILTER (WHERE hidden=0 AND ((name=? AND upper(type)=? AND pk=1) OR (name=? AND upper(type)='TEXT' AND pk=2)))
FROM pragma_table_xinfo(?)`, first, firstType, second, name).Scan(&count, &valid); err != nil {
			return false, safeError(ctx)
		}
		if count != 2 || valid != 2 {
			return false, nil
		}
	}
	for _, column := range strings.Split(columns, ",") {
		var present int
		if err := db.QueryRowContext(ctx, "SELECT count(*) FROM pragma_table_xinfo(?) WHERE name=? AND hidden=0", name, column).Scan(&present); err != nil {
			return false, safeError(ctx)
		}
		if present != 1 {
			return false, nil
		}
	}
	quoted := `"` + name + `"`
	check, err := db.QueryContext(ctx, "SELECT "+columns+" FROM "+quoted+" LIMIT 0") // #nosec G202 -- fixed columns; table names derive only from validated GUID metadata or fixed literals.
	if err != nil {
		return false, safeError(ctx)
	}
	if err := check.Close(); err != nil {
		return false, safeError(ctx)
	}
	var count int64
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM "+quoted).Scan(&count); err != nil {
		return false, safeError(ctx)
	}
	if count > int64(maxRows-*total) {
		return false, &ReadError{Code: "onedrive_lists_index_too_large"}
	}
	*total += int(count)
	for _, column := range strings.Split(columns, ",") {
		var size sql.NullInt64
		if err := db.QueryRowContext(ctx, "SELECT max(length(CAST("+column+" AS BLOB))) FROM "+quoted+where).Scan(&size); err != nil {
			return false, safeError(ctx)
		}
		if size.Valid && size.Int64 > maxField {
			return false, &ReadError{Code: "onedrive_lists_index_too_large"}
		}
	}
	rows, err := db.QueryContext(ctx, "SELECT +"+strings.ReplaceAll(columns, ",", ",+")+" FROM "+quoted+where+" ORDER BY "+order) // #nosec G202 -- fixed selection/order; validated metadata names, never arbitrary SQL.
	if err != nil {
		return false, safeError(ctx)
	}
	defer func() { _ = rows.Close() }()
	values := make([]any, len(strings.Split(columns, ",")))
	dest := make([]any, len(values))
	for i := range values {
		dest[i] = &values[i]
	}
	for rows.Next() {
		if err := rows.Scan(dest...); err != nil {
			return false, safeError(ctx)
		}
		if err := visit(values); err != nil {
			return false, err
		}
	}
	if rows.Err() != nil {
		return false, safeError(ctx)
	}
	return true, nil
}
