package store

import (
	"bytes"
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/teamsdesktop"
)

func beginSession(t *testing.T, s *Store) *Session {
	t.Helper()
	x, err := s.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(x.Rollback)
	return x
}

// The Rows variants of Apply report, per item, the row it ended up in: the incoming hash when the
// row was inserted or updated, the stored one when it was left alone.
func TestApplyRowsReportWhereEachItemLanded(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	x := beginSession(t, s)
	must0(x.ApplyAccount(ctx, acctA))

	cs := []teamsdesktop.Conversation{conv(acctA, "c1", "Chat", "One"), conv(acctA, "c2", "Chat", "Two")}
	_, crows, err := x.ApplyConversationsRows(ctx, cs)
	must0(err)
	ms := []teamsdesktop.Message{msg(acctA, "c1", "m1", "hello", base), msg(acctA, "c1", "m2", "again", base)}
	_, _, mrows, err := x.ApplyMessagesRows(ctx, ms)
	must0(err)
	acts := []teamsdesktop.Activity{{TenantID: acctA.TenantID, UserID: acctA.UserID, ID: "a1", Type: "mention", At: base}}
	_, _, arows, err := x.ApplyActivityRows(ctx, acts)
	must0(err)
	for name, rows := range map[string][]RowState{"conversations": crows, "messages": mrows, "activity": arows} {
		for i, r := range rows {
			if r.Rowid == 0 || len(r.Hash) != 64 || !r.Changed {
				t.Fatalf("%s[%d] after an insert = %+v", name, i, r)
			}
		}
	}
	if len(crows) != 2 || len(mrows) != 2 || len(arows) != 1 {
		t.Fatalf("rows: %d %d %d", len(crows), len(mrows), len(arows))
	}

	// The same items again: the same rows and hashes, reported as left alone.
	_, crows2, _ := x.ApplyConversationsRows(ctx, cs)
	_, _, mrows2, _ := x.ApplyMessagesRows(ctx, ms)
	_, _, arows2, _ := x.ApplyActivityRows(ctx, acts)
	for i := range crows {
		if crows2[i].Rowid != crows[i].Rowid || crows2[i].Hash != crows[i].Hash || crows2[i].Changed {
			t.Fatalf("conversation %d again: %+v then %+v", i, crows[i], crows2[i])
		}
	}
	for i := range mrows {
		if mrows2[i].Rowid != mrows[i].Rowid || mrows2[i].Hash != mrows[i].Hash || mrows2[i].Changed {
			t.Fatalf("message %d again: %+v then %+v", i, mrows[i], mrows2[i])
		}
	}
	if arows2[0].Rowid != arows[0].Rowid || arows2[0].Hash != arows[0].Hash || arows2[0].Changed {
		t.Fatalf("activity again: %+v then %+v", arows[0], arows2[0])
	}

	// An older version of a message is left alone: the row keeps the stored hash, not the incoming one.
	old := ms[0]
	old.Version = 0
	old.ContentText = "stale"
	_, _, rows, _ := x.ApplyMessagesRows(ctx, []teamsdesktop.Message{old})
	if rows[0].Changed || rows[0].Hash != mrows[0].Hash {
		t.Fatalf("older version = %+v, want the stored row %+v", rows[0], mrows[0])
	}
	// A changed item is reported changed with its new hash.
	ms[1].ContentText = "edited"
	ms[1].Version = 2
	_, _, rows, _ = x.ApplyMessagesRows(ctx, ms[1:])
	if !rows[0].Changed || rows[0].Hash == mrows[1].Hash || rows[0].Rowid != mrows[1].Rowid {
		t.Fatalf("edited message = %+v, was %+v", rows[0], mrows[1])
	}
	// An updated activity item keeps its rowid.
	acts[0].IsRead = true
	_, _, rows, _ = x.ApplyActivityRows(ctx, acts)
	if !rows[0].Changed || rows[0].Rowid != arows[0].Rowid {
		t.Fatalf("updated activity = %+v, was %+v", rows[0], arows[0])
	}
}

func TestRefOf(t *testing.T) {
	good := "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"
	ref, ok := RefOf(RowMessage, RowState{Rowid: 7, Hash: good})
	if !ok || ref.Kind != RowMessage || ref.Rowid != 7 || ref.Hash[0] != 0x00 || ref.Hash[15] != 0xff {
		t.Fatalf("RefOf = %+v %v", ref, ok)
	}
	for _, bad := range []string{"", "zz", "0011"} {
		if _, ok := RefOf(RowMessage, RowState{Rowid: 1, Hash: bad}); ok {
			t.Fatalf("RefOf accepted hash %q", bad)
		}
	}
}

func TestRowHashesOf(t *testing.T) {
	ctx := context.Background()
	x := beginSession(t, newStore(t))
	must0(x.ApplyAccount(ctx, acctA))
	_, crows, _ := x.ApplyConversationsRows(ctx, []teamsdesktop.Conversation{conv(acctA, "c1", "Chat", "One")})
	_, _, mrows, _ := x.ApplyMessagesRows(ctx, []teamsdesktop.Message{msg(acctA, "c1", "m1", "hello", base), msg(acctA, "c1", "m2", "again", base)})
	_, _, arows, _ := x.ApplyActivityRows(ctx, []teamsdesktop.Activity{{TenantID: acctA.TenantID, UserID: acctA.UserID, ID: "a1", Type: "mention", At: base}})
	ids := func(rows []RowState) (out []int64) {
		for _, r := range rows {
			out = append(out, r.Rowid)
		}
		return
	}
	for kind, rows := range map[byte][]RowState{RowConversation: crows, RowMessage: mrows, RowActivity: arows} {
		got, err := x.RowHashesOf(kind, ids(rows))
		if err != nil || len(got) != len(rows) {
			t.Fatalf("kind %d: %v, %v", kind, got, err)
		}
		for _, r := range rows {
			ref, _ := RefOf(kind, r)
			if got[r.Rowid] != ref.Hash {
				t.Fatalf("kind %d row %d: %x, want %x", kind, r.Rowid, got[r.Rowid], ref.Hash)
			}
		}
	}
	// Only the rows asked for come back; a rowid with no row is simply absent.
	got, err := x.RowHashesOf(RowMessage, []int64{mrows[1].Rowid, 9999})
	if err != nil || len(got) != 1 {
		t.Fatalf("asked for one row: %v, %v", got, err)
	}
	if got, err := x.RowHashesOf(RowMessage, nil); err != nil || len(got) != 0 {
		t.Fatalf("asked for nothing: %v, %v", got, err)
	}
	// More rowids than one query takes are asked in several.
	many := make([]int64, 0, 2*rowHashChunk+3)
	for i := int64(0); i < int64(2*rowHashChunk+3); i++ {
		many = append(many, 1000+i)
	}
	many = append(many, mrows[0].Rowid, mrows[1].Rowid)
	if got, err := x.RowHashesOf(RowMessage, many); err != nil || len(got) != 2 {
		t.Fatalf("chunked: %v, %v", got, err)
	}
	// A row whose stored hash is unusable is left out: it matches no reference.
	if _, err := x.tx.Exec(`update messages set content_hash='ab' where rowid=?`, mrows[0].Rowid); err != nil {
		t.Fatal(err)
	}
	got, err = x.RowHashesOf(RowMessage, ids(mrows))
	if _, ok := got[mrows[0].Rowid]; err != nil || ok || len(got) != 1 {
		t.Fatalf("after damage: %v, %v", got, err)
	}
	if _, err := x.RowHashesOf(99, nil); err == nil {
		t.Fatal("unknown row kind accepted")
	}
	// A failing query and a failing scan are errors.
	y := beginSession(t, newStore(t))
	_, _ = y.tx.Exec(`drop table messages`)
	if _, err := y.RowHashesOf(RowMessage, []int64{1}); err == nil {
		t.Fatal("query error ignored")
	}
	w := beginSession(t, newStore(t))
	_, _ = w.tx.Exec(`drop table conversations`)
	_, _ = w.tx.Exec(`create table conversations(a, content_hash)`)
	_, _ = w.tx.Exec(`insert into conversations(a, content_hash) values(1, null)`)
	if _, err := w.RowHashesOf(RowConversation, []int64{1}); err == nil {
		t.Fatal("scan error ignored")
	}
}

func TestDeleteTypedMemo(t *testing.T) {
	x := beginSession(t, newStore(t))
	for _, k := range []string{"a", "b", "c"} {
		must0(x.PutTypedMemo(srcA, "db1", k, TypedMemo{Digest: []byte{1}, Effects: []byte{1}}))
	}
	must0(x.PutTypedMemo(srcA, "db2", "a", TypedMemo{Digest: []byte{1}, Effects: []byte{1}}))
	must0(x.PutTypedMemo("other", "db1", "a", TypedMemo{Digest: []byte{1}, Effects: []byte{1}}))
	must0(x.DeleteTypedMemoKeys(srcA, "db1", []string{"a", "c", "missing"}))
	if got, _ := x.LoadTypedMemo(srcA, "db1"); len(got) != 1 || got["b"].Digest == nil {
		t.Fatalf("after deleting keys: %v", got)
	}
	count := func(source string) (n int) {
		must0(x.tx.QueryRow(`select count(*) from typed_memo where source=?`, source).Scan(&n))
		return
	}
	must0(x.DeleteTypedMemoOutside(srcA, []string{"db2"}))
	if count(srcA) != 1 || count("other") != 1 {
		t.Fatalf("after deleting outside db2: %d, %d", count(srcA), count("other"))
	}
	must0(x.DeleteTypedMemoOutside(srcA, nil))
	if count(srcA) != 0 || count("other") != 1 {
		t.Fatalf("after deleting everything of one source: %d, %d", count(srcA), count("other"))
	}
	y := beginSession(t, newStore(t))
	_, _ = y.tx.Exec(`drop table typed_memo`)
	if y.DeleteTypedMemoKeys(srcA, "d", []string{"a"}) == nil || y.DeleteTypedMemoOutside(srcA, []string{"d"}) == nil {
		t.Fatal("a failing delete was ignored")
	}
}

// A removed record keeps no digest, and removed_at is set once and stays.
func TestRemovedRecordsKeepNoDigest(t *testing.T) {
	x := beginSession(t, newStore(t))
	r1, r2, r3 := grec(&acctA, dbA, "events", `"k1"`, `1`), grec(&acctA, dbA, "events", `"k2"`, `2`), grec(&acctA, dbA, "events", `"k3"`, `3`)
	for _, r := range []*teamsdesktop.GenericRecord{&r1, &r2, &r3} {
		r.Digest, r.ValueRedacted = []byte{7}, 2
	}
	must(x.UpsertRecords(srcA, []teamsdesktop.GenericRecord{r1, r2, r3}, base))
	later := base.Add(time.Hour)
	must(x.MarkRecordsRemoved(srcA, dbA, map[string]map[string]struct{}{"events": {`"k1"`: {}}}, later))
	check := func(key string, wantDigest bool) {
		t.Helper()
		var d []byte
		var red int
		var removed sql.NullString
		must0(x.tx.QueryRow(`select raw_digest, value_redacted, removed_at from records where key_json=?`, key).Scan(&d, &red, &removed))
		if (d != nil) != wantDigest || (wantDigest != (red == 2)) || removed.Valid == wantDigest {
			t.Fatalf("%s: digest %v redacted %d removed %v", key, d, red, removed)
		}
	}
	check(`"k1"`, true)
	check(`"k2"`, false)
	check(`"k3"`, false)
	var removedAt string
	must0(x.tx.QueryRow(`select removed_at from records where key_json='"k2"'`).Scan(&removedAt))
	must(x.MarkRecordsRemoved(srcA, dbA, map[string]map[string]struct{}{}, later.Add(time.Hour)))
	var again string
	must0(x.tx.QueryRow(`select removed_at from records where key_json='"k2"'`).Scan(&again))
	if again != removedAt {
		t.Fatalf("removed_at moved from %s to %s", removedAt, again)
	}
	// The whole database going away clears digests the same way.
	r4 := grec(&acctA, "Teams:other-manager:react-web-client:00000000-0000-4000-8000-0000000000a1:00000000-0000-4000-8000-0000000000b1:en-us", "events", `"k4"`, `4`)
	r4.Digest = []byte{7}
	must(x.UpsertRecords(srcA, []teamsdesktop.GenericRecord{r4}, base))
	must(x.MarkDatabasesRemoved(srcA, nil, nil, later))
	var n int
	must0(x.tx.QueryRow(`select count(*) from records where raw_digest is not null`).Scan(&n))
	if n != 0 {
		t.Fatalf("%d rows of removed databases still carry a digest", n)
	}
}

func TestTypedMemoRoundTrip(t *testing.T) {
	s := newStore(t)
	x := beginSession(t, s)
	got, err := x.LoadTypedMemo(srcA, "db1")
	if err != nil || len(got) != 0 || got == nil {
		t.Fatalf("empty = %v, %v", got, err)
	}
	must0(x.PutTypedMemo(srcA, "db1", `["k1"]`, TypedMemo{Digest: []byte{1}, Effects: []byte{2}}))
	must0(x.PutTypedMemo(srcA, "db1", `["k2"]`, TypedMemo{Digest: []byte{3}, Effects: []byte{4}}))
	must0(x.PutTypedMemo(srcA, "db2", `["k1"]`, TypedMemo{Digest: []byte{5}, Effects: []byte{6}}))
	must0(x.PutTypedMemo("other-source", "db1", `["k1"]`, TypedMemo{Digest: []byte{7}, Effects: []byte{8}}))
	// Putting a key again replaces it.
	must0(x.PutTypedMemo(srcA, "db1", `["k1"]`, TypedMemo{Digest: []byte{9}, Effects: []byte{10}}))
	got, err = x.LoadTypedMemo(srcA, "db1")
	if err != nil || len(got) != 2 || !bytes.Equal(got[`["k1"]`].Digest, []byte{9}) || !bytes.Equal(got[`["k1"]`].Effects, []byte{10}) || !bytes.Equal(got[`["k2"]`].Digest, []byte{3}) {
		t.Fatalf("db1 = %+v, %v", got, err)
	}
	// A rollback forgets it with the rows.
	x.Rollback()
	y := beginSession(t, s)
	if got, _ := y.LoadTypedMemo(srcA, "db1"); len(got) != 0 {
		t.Fatalf("memory survived a rollback: %v", got)
	}
}

func TestTypedMemoErrors(t *testing.T) {
	x := beginSession(t, newStore(t))
	_, _ = x.tx.Exec(`drop table typed_memo`)
	if _, err := x.LoadTypedMemo(srcA, "db"); err == nil {
		t.Fatal("load error ignored")
	}
	if err := x.PutTypedMemo(srcA, "db", "k", TypedMemo{}); err == nil {
		t.Fatal("put error ignored")
	}
	// A row that does not scan (a NULL digest) is an error too.
	y := beginSession(t, newStore(t))
	_, _ = y.tx.Exec(`drop table typed_memo`)
	_, _ = y.tx.Exec(`create table typed_memo(source, database, key_json, digest, effects)`)
	_, _ = y.tx.Exec(`insert into typed_memo values('s','d',null,x'01',x'02')`)
	if _, err := y.LoadTypedMemo("s", "d"); err == nil {
		t.Fatal("scan error ignored")
	}
}

func TestRecordMemos(t *testing.T) {
	x := beginSession(t, newStore(t))
	live := grec(&acctA, dbA, "events", `"live"`, `1`)
	live.Digest, live.ValueRedacted = []byte{1, 2}, 3
	gone := grec(&acctA, dbA, "events", `"gone"`, `2`)
	gone.Digest = []byte{4}
	plain := grec(&acctA, dbA, "events", `"plain"`, `3`) // no digest
	other := grec(&acctA, dbA+"2", "events", `"other"`, `4`)
	other.Digest = []byte{5}
	must(x.UpsertRecords(srcA, []teamsdesktop.GenericRecord{live, gone, plain, other}, base))
	must(x.MarkRecordsRemoved(srcA, dbA, map[string]map[string]struct{}{"events": {`"live"`: {}, `"plain"`: {}}}, base))
	got, err := x.LoadRecordMemos(srcA, dbA)
	if err != nil || len(got) != 1 {
		t.Fatalf("memos = %v, %v; want only the live row with a digest", got, err)
	}
	m := got[[2]string{"events", `"live"`}]
	if !bytes.Equal(m.Digest, []byte{1, 2}) || m.Redacted != 3 {
		t.Fatalf("memo = %+v", m)
	}
	// A cleared row (denied now) carries no memory.
	must(x.PurgeDenied(srcA, func(n string) bool { return n == "events" }, base))
	if got, _ := x.LoadRecordMemos(srcA, dbA); len(got) != 0 {
		t.Fatalf("a purged row still carries memory: %v", got)
	}
}

func TestRecordMemosErrors(t *testing.T) {
	x := beginSession(t, newStore(t))
	_, _ = x.tx.Exec(`drop table records`)
	if _, err := x.LoadRecordMemos(srcA, dbA); err == nil {
		t.Fatal("load error ignored")
	}
	y := beginSession(t, newStore(t))
	_, _ = y.tx.Exec(`drop table records`)
	_, _ = y.tx.Exec(`create table records(source, database, store, key_json, raw_digest, value_redacted, removed_at, value_json)`)
	_, _ = y.tx.Exec(`insert into records values('s','d','st','k',x'01','notanumber',null,'v')`)
	if _, err := y.LoadRecordMemos("s", "d"); err == nil {
		t.Fatal("scan error ignored")
	}
}

// UpsertRecords keeps the digest of the bytes a row was made from in step with the row.
func TestUpsertRecordsKeepsTheDigestInStep(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	x := beginSession(t, s)
	digestOf := func() (d []byte, red int) {
		must0(x.tx.QueryRowContext(ctx, `select raw_digest, value_redacted from records where key_json='"k"'`).Scan(&d, &red))
		return d, red
	}
	with := func(value string, digest byte, red int) teamsdesktop.GenericRecord {
		r := grec(&acctA, dbA, "events", `"k"`, value)
		if digest != 0 {
			r.Digest, r.ValueRedacted = []byte{digest}, red
		}
		return r
	}
	step := func(r teamsdesktop.GenericRecord) Counts {
		return must(x.UpsertRecords(srcA, []teamsdesktop.GenericRecord{r}, base))
	}

	if c := step(with(`1`, 1, 2)); c.Inserted != 1 {
		t.Fatalf("insert: %+v", c)
	}
	if d, red := digestOf(); !bytes.Equal(d, []byte{1}) || red != 2 {
		t.Fatalf("after insert: %v %d", d, red)
	}
	// Same value, other bytes (a re-encoding): unchanged, and the digest follows.
	if c := step(with(`1`, 2, 5)); c.Unchanged != 1 {
		t.Fatalf("unchanged: %+v", c)
	}
	if d, red := digestOf(); !bytes.Equal(d, []byte{2}) || red != 5 {
		t.Fatalf("after unchanged: %v %d", d, red)
	}
	// A record with no digest leaves what the row remembers alone.
	if c := step(with(`1`, 0, 0)); c.Unchanged != 1 {
		t.Fatalf("unchanged, no digest: %+v", c)
	}
	if d, _ := digestOf(); !bytes.Equal(d, []byte{2}) {
		t.Fatalf("a record without a digest rewrote it: %v", d)
	}
	// A changed value takes its own digest, or none.
	if c := step(with(`2`, 3, 0)); c.Updated != 1 {
		t.Fatalf("update: %+v", c)
	}
	if d, _ := digestOf(); !bytes.Equal(d, []byte{3}) {
		t.Fatalf("after update: %v", d)
	}
	if c := step(with(`3`, 0, 0)); c.Updated != 1 {
		t.Fatalf("update, no digest: %+v", c)
	}
	if d, _ := digestOf(); d != nil {
		t.Fatalf("an update without a digest left a stale one: %v", d)
	}
	// A row that came back after removal gets the digest of the bytes it came back with.
	must(x.MarkRecordsRemoved(srcA, dbA, map[string]map[string]struct{}{"events": {}}, base))
	if c := step(with(`3`, 4, 1)); c.Updated != 1 {
		t.Fatalf("back from removal: %+v", c)
	}
	if d, red := digestOf(); !bytes.Equal(d, []byte{4}) || red != 1 {
		t.Fatalf("after coming back: %v %d", d, red)
	}
	// Nothing left to write: the same digest and count do not rewrite the row.
	if c := step(with(`3`, 4, 1)); c.Unchanged != 1 {
		t.Fatalf("again: %+v", c)
	}
}

// A records table that cannot take the new columns makes the migration fail, not silently skip.
func TestMigrateRecordsColumnFailureIsReported(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	for _, q := range []string{`drop table records`, `create view records as select 1 as source`} {
		if _, err := s.db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.migrate(ctx); err == nil {
		t.Fatal("migrate over a records view must fail")
	}
}

// A failing write of the digest on an otherwise unchanged row fails the upsert.
func TestUpsertRecordsDigestWriteFailure(t *testing.T) {
	ctx := context.Background()
	x := beginSession(t, newStore(t))
	r := grec(&acctA, dbA, "events", `"k"`, `1`)
	r.Digest = []byte{1}
	must(x.UpsertRecords(srcA, []teamsdesktop.GenericRecord{r}, base))
	if _, err := x.tx.ExecContext(ctx, `create trigger no_digest before update of raw_digest on records begin select raise(abort, 'no'); end`); err != nil {
		t.Fatal(err)
	}
	r.Digest = []byte{2}
	if _, err := x.UpsertRecords(srcA, []teamsdesktop.GenericRecord{r}, base); err == nil {
		t.Fatal("a failed digest write was ignored")
	}
}
