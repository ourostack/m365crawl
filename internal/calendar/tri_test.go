package calendar

import (
	"database/sql"
	"strings"
	"testing"
)

func TestTriZeroValueIsUnknown(t *testing.T) {
	var zero Tri
	if zero != TriUnknown || zero.Known() || zero.Is(true) || zero.Is(false) || zero.String() != "unknown" {
		t.Fatalf("zero = %v", zero)
	}
	if (Event{}).Cancelled.Known() || (Event{}).AllDay.Known() {
		t.Fatal("an event whose mapper set no flag must report every flag unknown")
	}
}

func TestTriOfAndIs(t *testing.T) {
	if TriOf(true) != TriTrue || TriOf(false) != TriFalse {
		t.Fatal("TriOf")
	}
	for _, tc := range []struct {
		tri      Tri
		known    bool
		isT, isF bool
		str      string
	}{
		{TriUnknown, false, false, false, "unknown"},
		{TriFalse, true, false, true, "false"},
		{TriTrue, true, true, false, "true"},
	} {
		if tc.tri.Known() != tc.known || tc.tri.Is(true) != tc.isT || tc.tri.Is(false) != tc.isF || tc.tri.String() != tc.str {
			t.Errorf("%v: known %v is(true) %v is(false) %v %q", tc.tri, tc.tri.Known(), tc.tri.Is(true), tc.tri.Is(false), tc.tri.String())
		}
	}
}

func TestTriSQLRoundTripNullZeroOne(t *testing.T) {
	db := openDB(t)
	w := window(t, SourceTeams, octStart, octEnd, "2026-10-02T00:00:00Z")
	var events []Event
	for i, tri := range []Tri{TriUnknown, TriFalse, TriTrue} {
		e := timed(t, SourceTeams, string(rune('a'+i)), "Flags", "2026-10-05T16:00:00Z")
		e.IsPrivate, e.HasAttachments = tri, tri
		events = append(events, e)
	}
	apply(t, db, w, "2026-10-02T01:00:00Z", events...)
	for i, want := range []sql.NullInt64{{}, {Int64: 0, Valid: true}, {Int64: 1, Valid: true}} {
		var got sql.NullInt64
		if err := db.QueryRow(`SELECT is_private FROM calendar_source_events WHERE source_id=?`, string(rune('a'+i))).Scan(&got); err != nil || got != want {
			t.Fatalf("source %c: stored %+v, want %+v (%v)", 'a'+i, got, want, err)
		}
	}
	items, _ := agenda(t, db, "2026-10-05T00:00:00Z", "2026-10-06T00:00:00Z")
	got := map[string]Tri{}
	for _, it := range items {
		got[it.SourceID] = it.IsPrivate
	}
	if got["a"] != TriUnknown || got["b"] != TriFalse || got["c"] != TriTrue {
		t.Fatalf("read back %v", got)
	}
}

func TestTriFromSQLRejectsDamage(t *testing.T) {
	if _, err := triFromSQL(sql.NullInt64{Int64: 7, Valid: true}); err == nil {
		t.Fatal("a flag of 7 is damage")
	}
	db := openDB(t)
	w := window(t, SourceTeams, octStart, octEnd, "2026-10-02T00:00:00Z")
	apply(t, db, w, "2026-10-02T01:00:00Z", timed(t, SourceTeams, "a", "A", "2026-10-05T16:00:00Z"))
	exec(t, db, `UPDATE calendar_source_events SET cancelled=5`)
	_, err := Agenda(ctx, db, AgendaQuery{From: mustTime(t, "2026-10-05T00:00:00Z"), To: mustTime(t, "2026-10-06T00:00:00Z")})
	if err == nil || !strings.Contains(err.Error(), "cancelled") {
		t.Fatalf("want an error naming the column, got %v", err)
	}
}

func TestSchemaFlagsNullable(t *testing.T) {
	db := openDB(t)
	for _, col := range []string{"all_day", "is_organizer", "is_private", "cancelled", "is_online_meeting", "has_attachments"} {
		var notNull int
		var dflt sql.NullString
		if err := db.QueryRow(`SELECT "notnull", dflt_value FROM pragma_table_info('calendar_source_events') WHERE name=?`, col).Scan(&notNull, &dflt); err != nil {
			t.Fatalf("%s: %v", col, err)
		}
		if notNull != 0 || dflt.Valid {
			t.Errorf("%s must be nullable with no default: notnull=%d default=%v", col, notNull, dflt)
		}
	}
}

func TestSchemaHasUnknownFieldsColumn(t *testing.T) {
	db := openDB(t)
	var notNull int
	var dflt sql.NullString
	if err := db.QueryRow(`SELECT "notnull", dflt_value FROM pragma_table_info('calendar_source_events') WHERE name='unknown_fields'`).Scan(&notNull, &dflt); err != nil {
		t.Fatal(err)
	}
	if notNull != 1 || dflt.String != "''" {
		t.Fatalf("unknown_fields: notnull=%d default=%v", notNull, dflt)
	}
}
