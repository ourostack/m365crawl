package onedriveusage

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
	fileBytes, fieldBytes, stringBytes                      int64
	recent, documents, collaborators, history, participants int
}

type ReadError struct{ Code string }

func (e *ReadError) Error() string { return "onedrive: " + e.Code }

var (
	indexAbs   = filepath.Abs
	indexOpen  = sql.Open
	indexClose = (*sql.DB).Close
)

func ReadIndex(ctx context.Context, path string) (Result, error) {
	return readIndex(ctx, path, readLimits{
		fileBytes: 512 << 20, fieldBytes: 1 << 20, stringBytes: 64 << 20,
		recent: 1 << 16, documents: 1 << 16, collaborators: 4096, history: 1 << 18, participants: 1 << 18,
	})
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
		return Result{}, &ReadError{Code: "onedrive_usage_index_unreadable"}
	}
	if info.Size() > limits.fileBytes {
		return Result{}, &ReadError{Code: "onedrive_usage_index_too_large"}
	}
	absolute, err := indexAbs(path)
	if err != nil {
		return Result{}, &ReadError{Code: "onedrive_usage_index_unreadable"}
	}
	uri := url.URL{Scheme: "file", Path: "/" + strings.TrimPrefix(filepath.ToSlash(absolute), "/"),
		RawQuery: "mode=ro&immutable=1"}
	db, err := indexOpen("sqlite", uri.String())
	if err != nil {
		return Result{}, &ReadError{Code: "onedrive_usage_index_unreadable"}
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
		return Result{}, &ReadError{Code: "onedrive_usage_index_unrecognized"}
	}
	losses := map[string]int{}
	native := map[string]bool{}
	var retained int64
	keep := func(values ...string) error {
		for _, value := range values {
			retained += int64(len(value))
		}
		if retained > limits.stringBytes {
			return &ReadError{Code: "onedrive_usage_index_too_large"}
		}
		return nil
	}
	err = readTable(ctx, db, "recent_files_spo",
		"ID,ItemId,ListId,WebId,SiteId,LastModifiedDateTime,LastAccessedDateTime,FileModifiedDateTime,LastSharedDateTime,SavedDateTime,Type,Source,Extension,WorkingSetId,HiddenFromShared,IsLocalPlaceHolder",
		limits.recent, limits.fieldBytes, func(row []any) error {
			values := make([]string, 14)
			valid := true
			for i := range values {
				var ok bool
				values[i], ok = nativeText(row[i], i == 0)
				valid = valid && ok
			}
			hidden, hiddenOK := nativeInteger(row[14])
			placeholder, placeholderOK := nativeInteger(row[15])
			if !valid || !hiddenOK || !placeholderOK {
				losses["onedrive_usage_recent_unmapped"]++
				return nil
			}
			if err := keep(values...); err != nil {
				return err
			}
			native[values[0]] = true
			res.Recent = append(res.Recent, RecentObservation{
				ID: values[0], ItemID: values[1], ListID: values[2], WebID: values[3], SiteID: values[4],
				LastModifiedRaw: values[5], LastAccessedRaw: values[6], FileModifiedRaw: values[7], LastSharedRaw: values[8], SavedRaw: values[9],
				Type: values[10], Source: values[11], Extension: values[12], WorkingSetID: values[13],
				HiddenFromShared: hidden, LocalPlaceholder: placeholder,
			})
			return nil
		})
	if err != nil {
		return Result{}, err
	}
	historyCount, participantsCount := 0, 0
	err = readTable(ctx, db, "recent_files_formatted_spo", "ID,Format,FormattedValue", limits.documents, limits.fieldBytes,
		func(row []any) error {
			id, idOK := nativeText(row[0], true)
			format, formatOK := nativeText(row[1], true)
			raw, rawOK := nativeText(row[2], true)
			if !idOK || !formatOK || !rawOK {
				losses["onedrive_usage_document_unmapped"]++
				return nil
			}
			if !native[id] {
				losses["onedrive_usage_document_orphan"]++
				return nil
			}
			document, ok, decodeErr := decodeDocument(id, format, raw)
			if decodeErr != nil {
				return &ReadError{Code: "onedrive_usage_index_too_large"}
			}
			if !ok {
				losses["onedrive_usage_document_unmapped"]++
				return nil
			}
			historyCount += len(document.History)
			for _, share := range document.History {
				participantsCount += len(share.Participants)
			}
			if historyCount > limits.history || participantsCount > limits.participants {
				return &ReadError{Code: "onedrive_usage_index_too_large"}
			}
			if err := keep(documentStrings(document)...); err != nil {
				return err
			}
			res.Documents = append(res.Documents, document)
			return nil
		})
	if err != nil {
		return Result{}, err
	}
	err = readTable(ctx, db, "top_collaborators", "ID,FormattedValue", limits.collaborators, limits.fieldBytes,
		func(row []any) error {
			id, idOK := nativeText(row[0], true)
			raw, rawOK := nativeText(row[1], true)
			if !idOK || !rawOK {
				losses["onedrive_usage_collaborator_unmapped"]++
				return nil
			}
			user, ok, decodeErr := decodeCollaborator(id, raw)
			if decodeErr != nil {
				return &ReadError{Code: "onedrive_usage_index_too_large"}
			}
			if !ok {
				losses["onedrive_usage_collaborator_unmapped"]++
				return nil
			}
			if err := keep(append([]string{user.ID, user.UserID, user.DisplayName, user.Department, user.JobTitle, user.Office}, user.Emails...)...); err != nil {
				return err
			}
			res.Collaborators = append(res.Collaborators, user)
			return nil
		})
	if err != nil {
		return Result{}, err
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
	return &ReadError{Code: "onedrive_usage_index_unreadable"}
}

func nativeText(value any, required bool) (string, bool) {
	if value == nil {
		return "", !required
	}
	text, ok := value.(string)
	return text, ok && utf8.ValidString(text) && !strings.ContainsRune(text, 0) && (!required || strings.TrimSpace(text) != "")
}

func nativeInteger(value any) (*int64, bool) {
	if value == nil {
		return nil, true
	}
	integer, ok := value.(int64)
	return &integer, ok
}

func documentStrings(d DocumentObservation) []string {
	values := []string{d.ID, d.Format, d.Title, d.URL, d.Extension, d.Owner, d.CreatedRaw, d.ModifiedRaw, d.SiteID, d.WebID, d.ListID, d.UniqueID}
	shares := d.History
	if d.Latest != nil {
		shares = append(slices.Clone(shares), *d.Latest)
	}
	for _, s := range shares {
		values = append(values, s.DisplayName, s.SMTP, s.AadID, s.AtRaw, s.ConversationID, s.Subject, s.AttachmentID,
			s.MeetingStartRaw, s.ICalUID, s.MeetingSubject)
		for _, p := range s.Participants {
			values = append(values, p.DisplayName, p.SMTP)
		}
	}
	return values
}

func readTable(ctx context.Context, db *sql.DB, table, columns string, maxRows int, maxField int64, visit func([]any) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var relation string
	if err := db.QueryRowContext(ctx, "SELECT type FROM pragma_table_list WHERE schema='main' AND name=?", table).Scan(&relation); err != nil || relation != "table" {
		return &ReadError{Code: "onedrive_usage_index_unrecognized"}
	}
	expected := 1
	if table == "recent_files_formatted_spo" {
		expected = 2
	}
	var keys, validKeys int
	if err := db.QueryRowContext(ctx, `
SELECT count(*) FILTER (WHERE pk>0),
count(*) FILTER (WHERE upper(type)='TEXT' AND hidden=0 AND ((name='ID' AND pk=1) OR (name='Format' AND pk=2)))
FROM pragma_table_xinfo(?)`, table).Scan(&keys, &validKeys); err != nil || keys != expected || validKeys != expected {
		return &ReadError{Code: "onedrive_usage_index_unrecognized"}
	}
	check, err := db.QueryContext(ctx, "SELECT "+columns+" FROM "+table+" LIMIT 0") // #nosec G202 -- fixed internal identifiers at three callers.
	if err != nil {
		return &ReadError{Code: "onedrive_usage_index_unrecognized"}
	}
	if err := check.Close(); err != nil {
		return safeReadError(ctx)
	}
	var count int64
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&count); err != nil {
		return safeReadError(ctx)
	}
	if count > int64(maxRows) {
		return &ReadError{Code: "onedrive_usage_index_too_large"}
	}
	for _, column := range strings.Split(columns, ",") {
		var size sql.NullInt64
		if err := db.QueryRowContext(ctx, "SELECT max(length(CAST("+column+" AS BLOB))) FROM "+table).Scan(&size); err != nil {
			return safeReadError(ctx)
		}
		if size.Valid && size.Int64 > maxField {
			return &ReadError{Code: "onedrive_usage_index_too_large"}
		}
	}
	order := "ID COLLATE BINARY"
	if expected == 2 {
		order += ",Format COLLATE BINARY"
	}
	rawColumns := "+" + strings.ReplaceAll(columns, ",", ",+")
	// Unary plus preserves SQLite storage values while removing declared-date driver coercion.
	rows, err := db.QueryContext(ctx, "SELECT "+rawColumns+" FROM "+table+" ORDER BY "+order) // #nosec G202 -- identifiers and ordering are fixed internal literals.
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
