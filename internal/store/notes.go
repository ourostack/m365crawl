package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/onenote"
)

var ErrNoteNotFound = errors.New("notes: note not found")

type NoteFilter struct {
	SourceID     int64
	Query        string
	Since, Until time.Time
	Limit        int
}

type NoteRow struct {
	ID, SourceID, CaptureID                                      int64
	GOID, GUID, GOSID, ParentGOID, GrandparentGOIDs, Title, Text string
	ModifiedAt, CapturedAt, SourceObservedAt                     *time.Time
}

type NoteAncestor struct {
	GOID, Title string
	Type        int64
}

type NoteDetail struct {
	NoteRow
	Elements  []onenote.Element
	Flags     []onenote.Flag
	Ancestors []NoteAncestor
}

const noteTextSQL = `coalesce((select group_concat(text,char(10)) from
(select text from note_elements where entity_rowid=n.rowid order by native_rowid)),'')`

func applyNotes(ctx context.Context, tx *sql.Tx, sourceID, captureID int64, result onenote.Result, partial bool) error {
	admittedPages := map[int64]int64{}
	elementPages := map[int64]int64{}
	for _, entity := range result.Entities {
		var id int64
		err := tx.QueryRowContext(ctx, `select rowid from note_entities where source_id=? and goid=?`, sourceID, entity.GOID).Scan(&id)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if id != 0 && partial {
			continue
		}
		if _, err := tx.ExecContext(ctx, `insert into note_entities(source_id,goid,guid,gosid,parent_goid,grandparent_goids,type,title,modified_at,capture_id)
values(?,?,?,?,?,?,?,?,?,?) on conflict(source_id,goid) do update set guid=excluded.guid,gosid=excluded.gosid,
parent_goid=excluded.parent_goid,grandparent_goids=excluded.grandparent_goids,type=excluded.type,title=excluded.title,
modified_at=excluded.modified_at,capture_id=excluded.capture_id`, sourceID, entity.GOID, entity.GUID, entity.GOSID,
			entity.ParentGOID, entity.GrandparentGOIDs, entity.Type, entity.Title, knowledgeTime(entity.ModifiedAt), captureID); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `select rowid from note_entities where source_id=? and goid=?`, sourceID, entity.GOID).Scan(&id); err != nil {
			return err
		}
		for _, statement := range []string{
			`delete from note_elements where entity_rowid=?`,
			`delete from note_flags where entity_rowid=?`,
			`delete from note_fts where rowid=?`,
		} {
			if _, err := tx.ExecContext(ctx, statement, id); err != nil {
				return err
			}
		}
		if entity.Type == 1 {
			admittedPages[entity.RowID] = id
		}
	}
	for _, element := range result.Elements {
		id, admitted := admittedPages[element.EntityRowID]
		if !admitted {
			continue
		}
		if _, err := tx.ExecContext(ctx, `insert into note_elements(entity_rowid,native_entity_rowid,native_rowid,goid,text,kind,jcid,hashtag_markers,capture_id)
values(?,?,?,?,?,?,?,?,?)`, id, element.EntityRowID, element.RowID, element.GOID, element.Text, element.Kind, element.Jcid, element.HashtagMarkers, captureID); err != nil {
			return err
		}
		elementPages[element.RowID] = id
	}
	for _, flag := range result.Flags {
		id, admitted := elementPages[flag.ElementRowID]
		if !admitted {
			continue
		}
		if _, err := tx.ExecContext(ctx, `insert into note_flags(entity_rowid,native_rowid,element_native_rowid,type,shape,status,label,capture_id)
values(?,?,?,?,?,?,?,?)`, id, flag.RowID, flag.ElementRowID, flag.Type, flag.Shape, flag.Status, flag.Label, captureID); err != nil {
			return err
		}
	}
	for _, id := range admittedPages {
		if _, err := tx.ExecContext(ctx, `insert into note_fts(rowid,title,text) select n.rowid,n.title,`+noteTextSQL+`
from note_entities n where n.rowid=?`, id); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) Notes(ctx context.Context, filter NoteFilter) ([]NoteRow, bool, error) {
	return s.notes(ctx, filter, 0)
}

func (s *Store) notes(ctx context.Context, filter NoteFilter, noteID int64) ([]NoteRow, bool, error) {
	return s.readNotes(ctx, s.db, filter, noteID)
}

func (s *Store) readNotes(ctx context.Context, reader knowledgeReader, filter NoteFilter, noteID int64) ([]NoteRow, bool, error) {
	available, err := hasKnowledgeSchema(ctx, reader)
	if err != nil || !available {
		return []NoteRow{}, false, err
	}
	if filter.SourceID != 0 {
		if _, err := s.KnowledgeLocators(ctx, filter.SourceID); err != nil {
			return nil, false, err
		}
	}
	where := `n.type=1`
	args := []any{}
	if noteID > 0 {
		where += ` and n.rowid=?`
		args = append(args, noteID)
	}
	if filter.SourceID > 0 {
		where += ` and n.source_id=?`
		args = append(args, filter.SourceID)
	}
	if filter.Query != "" {
		query := buildFTSQuery(filter.Query)
		if query == "" {
			return nil, false, errs.Usage("notes query requires searchable text")
		}
		where += ` and n.rowid in (select rowid from note_fts where note_fts match ?)`
		args = append(args, query)
	}
	if !filter.Since.IsZero() {
		where += ` and n.modified_at>=?`
		args = append(args, knowledgeTime(filter.Since))
	}
	if !filter.Until.IsZero() {
		where += ` and n.modified_at<?`
		args = append(args, knowledgeTime(filter.Until))
	}
	limit := filter.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	args = append(args, limit+1)
	rows, err := reader.QueryContext(ctx, `select n.rowid,n.source_id,n.capture_id,n.goid,n.guid,n.gosid,
n.parent_goid,n.grandparent_goids,n.title,`+noteTextSQL+`,n.modified_at,a.finished_at,ks.source_observed_at
from note_entities n join knowledge_sources ks on ks.id=n.source_id
join knowledge_attempts a on a.id=n.capture_id where `+where+`
order by n.modified_at desc,n.rowid asc limit ?`, args...)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = rows.Close() }()
	out := []NoteRow{}
	for rows.Next() {
		var note NoteRow
		var modified, captured, observed sql.NullString
		if err := rows.Scan(&note.ID, &note.SourceID, &note.CaptureID, &note.GOID, &note.GUID, &note.GOSID,
			&note.ParentGOID, &note.GrandparentGOIDs, &note.Title, &note.Text, &modified, &captured, &observed); err != nil {
			return nil, false, err
		}
		times, err := knowledgeTimes(modified, captured, observed)
		if err != nil {
			return nil, false, err
		}
		note.ModifiedAt, note.CapturedAt, note.SourceObservedAt = times[0], times[1], times[2]
		out = append(out, note)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	truncated := len(out) > limit
	if truncated {
		out = out[:limit]
	}
	return out, truncated, nil
}

func (s *Store) Note(ctx context.Context, id int64) (NoteDetail, error) {
	if id <= 0 {
		return NoteDetail{}, ErrNoteNotFound
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return NoteDetail{}, err
	}
	defer func() { _ = tx.Rollback() }()
	notes, _, err := s.readNotes(ctx, tx, NoteFilter{Limit: 1}, id)
	if err != nil {
		return NoteDetail{}, err
	}
	if len(notes) == 0 {
		return NoteDetail{}, ErrNoteNotFound
	}
	detail := NoteDetail{NoteRow: notes[0], Elements: []onenote.Element{}, Flags: []onenote.Flag{}, Ancestors: []NoteAncestor{}}
	rows, err := tx.QueryContext(ctx, `select native_rowid,native_entity_rowid,goid,text,kind,jcid,hashtag_markers from note_elements where entity_rowid=? order by native_rowid`, id)
	if err != nil {
		return NoteDetail{}, err
	}
	for rows.Next() {
		var element onenote.Element
		if err := rows.Scan(&element.RowID, &element.EntityRowID, &element.GOID, &element.Text, &element.Kind, &element.Jcid, &element.HashtagMarkers); err != nil {
			_ = rows.Close()
			return NoteDetail{}, err
		}
		detail.Elements = append(detail.Elements, element)
	}
	err = errors.Join(rows.Err(), rows.Close())
	if err != nil {
		return NoteDetail{}, err
	}
	rows, err = tx.QueryContext(ctx, `select native_rowid,element_native_rowid,type,shape,status,label from note_flags where entity_rowid=? order by native_rowid`, id)
	if err != nil {
		return NoteDetail{}, err
	}
	for rows.Next() {
		var flag onenote.Flag
		if err := rows.Scan(&flag.RowID, &flag.ElementRowID, &flag.Type, &flag.Shape, &flag.Status, &flag.Label); err != nil {
			_ = rows.Close()
			return NoteDetail{}, err
		}
		detail.Flags = append(detail.Flags, flag)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return NoteDetail{}, err
	}
	seen := map[string]bool{}
	for parent := detail.ParentGOID; parent != ""; {
		if seen[parent] {
			break
		}
		seen[parent] = true
		var ancestor NoteAncestor
		var next string
		err := tx.QueryRowContext(ctx, `select goid,title,type,parent_goid from note_entities where source_id=? and goid=?`,
			detail.SourceID, parent).Scan(&ancestor.GOID, &ancestor.Title, &ancestor.Type, &next)
		if errors.Is(err, sql.ErrNoRows) {
			break
		}
		if err != nil {
			return NoteDetail{}, err
		}
		detail.Ancestors = append(detail.Ancestors, ancestor)
		parent = strings.TrimSpace(next)
	}
	if err := tx.Commit(); err != nil {
		return NoteDetail{}, err
	}
	return detail, nil
}
