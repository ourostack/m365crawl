package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"path/filepath"
	"strings"
	"testing"
)

type noteSnapshotConnector struct {
	faultConnector
	barrier func()
	trigger string
}

func (c noteSnapshotConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.faultConnector.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &noteSnapshotConn{faultConn: conn.(*faultConn), barrier: c.barrier, trigger: c.trigger}, nil
}

type noteSnapshotConn struct {
	*faultConn
	barrier func()
	trigger string
}

func (c *noteSnapshotConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	rows, err := c.faultConn.QueryContext(ctx, query, args)
	if err != nil {
		return nil, err
	}
	if strings.Contains(query, c.trigger) {
		return &noteSnapshotRows{Rows: rows, barrier: c.barrier}, nil
	}
	return rows, nil
}

type noteSnapshotRows struct {
	driver.Rows
	barrier func()
}

func (r *noteSnapshotRows) Close() error {
	err := r.Rows.Close()
	r.barrier()
	return err
}

func TestNotesReviewedDetailUsesOneCommittedReadSnapshot(t *testing.T) {
	for _, trigger := range []string{"from note_entities n", "from note_elements where entity_rowid"} {
		t.Run(trigger, func(t *testing.T) { notesReadSnapshotPublication(t, trigger) })
	}
}

func notesReadSnapshotPublication(t *testing.T, trigger string) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "data", "archive.db")
	writer, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = writer.Close() }()
	first := beginNotes(t, writer, "A", base)
	commitNotes(t, writer, first, "complete", "A", noteFixture("notebook", "Original", "originalword"))
	rows, _, err := writer.Notes(ctx, NoteFilter{})
	if err != nil {
		t.Fatal(err)
	}
	id := rows[0].ID
	reader, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	committed := false
	barrier := func() {
		if committed {
			return
		}
		committed = true
		replacement := noteFixture("notebook", "Replacement", "replacementword")
		replacement.Elements[0].GOID = "new-element"
		replacement.Flags[0].Label = "New flag"
		replacement.Entities[1].Title = "New section"
		commitNotes(t, writer, beginNotes(t, writer, "B", base), "complete", "B", replacement)
	}
	inj := &injector{seen: map[string]int{}}
	connector := noteSnapshotConnector{faultConnector: faultConnector{dsn: sqliteFileURI(path, "mode=ro"), inj: inj}, barrier: barrier, trigger: trigger}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(1)
	defer func() { _ = db.Close() }()
	reader.db = db
	detail, err := reader.Note(ctx, id)
	if err != nil || !committed {
		t.Fatalf("deterministic publication barrier not exercised: %t/%v", committed, err)
	}
	if detail.Title != "Original" || detail.CaptureID != first.ID ||
		detail.Elements[0].GOID != "paragraph" || detail.Flags[0].Label != "Follow up" || detail.Ancestors[0].Title != "Section" {
		t.Fatalf("detail mixed captures: title=%s capture=%d element=%s flag=%s ancestor=%s",
			detail.Title, detail.CaptureID, detail.Elements[0].GOID, detail.Flags[0].Label, detail.Ancestors[0].Title)
	}
}
