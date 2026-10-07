package syncer

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/teamsdesktop"
)

// openRaw opens the archive file directly, for the tests that age it the way alpha.1 left it.
func openRaw(t *testing.T, db string) *sql.DB {
	t.Helper()
	d, err := sql.Open("sqlite", db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// ageToAlpha1 rewrites an archive's derived fields as alpha.1 derived them: message text is the
// content markup as text (no card, call or thread-activity text), the sender name is imDisplayName
// alone, an untitled chat has no display name, the content hash covers those old values, the text
// indexes follow, and the archive carries no derivation version.
func ageToAlpha1(t *testing.T, db string) {
	t.Helper()
	d := openRaw(t, db)
	rows, err := d.Query(`select rowid, tenant_id, user_id, conversation_id, id, content_html, coalesce(json_extract(raw_json,'$.imDisplayName'),'') from messages`)
	if err != nil {
		t.Fatal(err)
	}
	type row struct {
		rowid             int64
		key, html, sender string
	}
	var all []row
	for rows.Next() {
		var r row
		var tn, us, cv, id string
		if err := rows.Scan(&r.rowid, &tn, &us, &cv, &id, &r.html, &r.sender); err != nil {
			t.Fatal(err)
		}
		r.key = strings.Join([]string{tn, us, cv, id}, "|")
		all = append(all, r)
	}
	_ = rows.Close()
	for _, r := range all {
		text := teamsdesktop.HTMLToText(r.html)
		exec(t, d, `update messages set content_text=?, sender_name=?, content_hash='old' where rowid=?`, text, r.sender, r.rowid)
		exec(t, d, `delete from message_fts where rowid=?`, r.rowid)
		exec(t, d, `insert into message_fts(rowid, message_key, content) values(?,?,?)`, r.rowid, r.key, text)
	}
	exec(t, d, `update conversations set content_hash='old', display_name='' where title='' and topic=''`)
	exec(t, d, `delete from meta`)
}

func exec(t *testing.T, d *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := d.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

// snapshot is every derived field of the archive, with the text indexes.
func snapshot(t *testing.T, db string) string {
	t.Helper()
	d := openRaw(t, db)
	var b strings.Builder
	for _, q := range []string{
		`select tenant_id, user_id, conversation_id, id, sender_name, content_text, content_hash from messages order by 1,2,3,4`,
		`select tenant_id, user_id, id, display_name, content_hash from conversations order by 1,2,3`,
		`select tenant_id, id, display_name from people order by 1,2`,
		`select message_key, content from message_fts order by 1`,
		`select c.id, f.title from conversation_fts f join conversations c on c.rowid=f.rowid order by 1`,
	} {
		rows, err := d.Query(q)
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := rows.Columns()
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			fmt.Fprintln(&b, vals...)
		}
		_ = rows.Close()
	}
	return b.String()
}

func TestAlpha1ArchiveMigratesToWhatAFreshSyncMakes(t *testing.T) {
	root := fixtureCopy(t)
	fresh := newDB(t)
	run(t, Options{Root: root, DBPath: fresh})
	want := snapshot(t, fresh)

	old := newDB(t)
	run(t, Options{Root: root, DBPath: old})
	ageToAlpha1(t, old)
	if snapshot(t, old) == want {
		t.Fatal("the aged archive should differ from a fresh one")
	}

	// The cache has not changed, so only the migration can bring the old rows up to date.
	rep, changes := run(t, Options{Root: root, DBPath: old})
	if rep.Status != StatusUnchanged {
		t.Errorf("status = %q", rep.Status)
	}
	if m := rep.Migrated; m == nil || m.From != 1 || m.To != store.DerivationVersion || m.Rows == 0 {
		t.Fatalf("migrated = %+v", m)
	}
	if rep.Messages.Updated != 0 || rep.Conversations.Updated != 0 || len(changes) != 0 {
		t.Errorf("a migration is not a change: %+v %+v %v", rep.Messages, rep.Conversations, changes)
	}
	if got := snapshot(t, old); got != want {
		t.Errorf("migrated archive differs from a fresh sync:\n%s\n---\n%s", got, want)
	}

	// Once migrated, a cache touch reports only real changes: here none.
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(logFile(t, root), later, later); err != nil {
		t.Fatal(err)
	}
	rep, changes = run(t, Options{Root: root, DBPath: old})
	if rep.Migrated != nil {
		t.Errorf("migrated again: %+v", rep.Migrated)
	}
	if rep.Messages.Updated != 0 || rep.Messages.Unchanged != fixtureMessages || rep.Conversations.Updated != 0 || len(changes) != 0 {
		t.Errorf("after migration: %+v %+v %v", rep.Messages, rep.Conversations, changes)
	}
}

func TestNewArchiveReportsNoMigration(t *testing.T) {
	root := fixtureCopy(t)
	db := newDB(t)
	if rep, _ := run(t, Options{Root: root, DBPath: db}); rep.Migrated != nil {
		t.Errorf("a new archive migrated: %+v", rep.Migrated)
	}
	if rep, _ := run(t, Options{Root: root, DBPath: db}); rep.Migrated != nil {
		t.Errorf("a current archive migrated: %+v", rep.Migrated)
	}
}

func TestMigrationFailureFailsTheSync(t *testing.T) {
	root, db := syncedStart(t)
	exec(t, openRaw(t, db), `update meta set value='x' where key='derivation_version'`)
	if _, _, err := Run(context.Background(), Options{Root: root, DBPath: db}); err == nil {
		t.Fatal("a corrupt derivation version should fail the sync")
	}
}

func TestNewerArchiveIsNeverWritten(t *testing.T) {
	root, db := syncedStart(t)
	d := openRaw(t, db)
	exec(t, d, `update meta set value='99' where key='derivation_version'`)
	exec(t, d, `update messages set content_text='sentinel' where rowid=(select min(rowid) from messages)`)
	before, runs := snapshot(t, db), runCount(t, d)
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(logFile(t, root), later, later); err != nil {
		t.Fatal(err)
	}
	_, _, err := Run(context.Background(), Options{Root: root, DBPath: db})
	var coded *errs.Coded
	if !errors.As(err, &coded) || coded.Code != errs.CodeArchiveNewer || coded.Exit != errs.ExitEnvironment || !strings.Contains(coded.Message, "99") {
		t.Fatalf("err = %v", err)
	}
	if snapshot(t, db) != before || runCount(t, d) != runs {
		t.Error("a refused sync wrote to the archive")
	}
}

func runCount(t *testing.T, d *sql.DB) (n int) {
	t.Helper()
	if err := d.QueryRow(`select count(*) from sync_runs`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
