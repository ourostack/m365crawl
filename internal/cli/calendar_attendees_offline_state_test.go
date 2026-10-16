package cli

import (
	"database/sql"
	"os"
	"testing"
)

func TestAttendeeMissingOldAndPartialArchiveReads(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		e := newEnv(t)
		for _, args := range [][]string{{"calendar", "attendees", "sources"}, {"calendar", "attendees", "list", "not-archived"}} {
			code, out, stderr := e.run(append([]string{"--max-age", "1ns"}, args...)...)
			if code != 0 || !containsJSONKey(out, "native_attendee_evidence") {
				t.Fatal(code, out, stderr)
			}
		}
		if _, err := os.Stat(e.db); !os.IsNotExist(err) {
			t.Fatal("offline missing archive was created")
		}
		if code, _, _ := e.run("calendar", "attendees", "show", "attendee-observation:1"); code == 0 {
			t.Fatal("missing native observation invented")
		}
	})
	t.Run("old", func(t *testing.T) {
		e := newEnv(t)
		ref := attendeeCLIArchive(t, e, 1, false)
		db, err := sql.Open("sqlite", e.db)
		if err != nil {
			t.Fatal(err)
		}
		for _, query := range []string{`pragma foreign_keys=off`, `update schema_migrations set version=18`, `drop table outlook_attendee_members`, `drop table outlook_attendee_snapshots`, `drop table outlook_attendee_bindings`, `drop table outlook_attendee_occurrences`} {
			if _, err := db.Exec(query); err != nil {
				t.Fatal(err)
			}
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		before, err := os.ReadFile(e.db)
		if err != nil {
			t.Fatal(err)
		}
		for _, args := range [][]string{{"calendar", "attendees", "sources"}, {"calendar", "attendees", "list", ref}} {
			code, out, stderr := e.run(append([]string{"--max-age", "1ns"}, args...)...)
			if code != 0 || len(items(t, decode(t, out))) != 0 {
				t.Fatal("old optional namespace not read-only empty", code, out, stderr)
			}
		}
		after, err := os.ReadFile(e.db)
		if err != nil || string(before) != string(after) {
			t.Fatal("old optional read migrated/repaired archive", err)
		}
	})
	t.Run("partial_current", func(t *testing.T) {
		e := newEnv(t)
		attendeeCLIArchive(t, e, 1, false)
		db, err := sql.Open("sqlite", e.db)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`drop table outlook_attendee_members`); err != nil {
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		code, _, stderr := e.run("--max-age", "1ns", "calendar", "attendees", "sources", "--source-id", "999")
		if code == 0 || errorOf(t, stderr)["code"] != "outlook_attendee_archive_corrupt" {
			t.Fatal("projection concealed current namespace corruption", code, stderr)
		}
	})
}
