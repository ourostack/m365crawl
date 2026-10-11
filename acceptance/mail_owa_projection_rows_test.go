//go:build acceptance

package acceptance

import (
	"context"
	"strconv"
	"testing"

	"github.com/ourostack/m365crawl/internal/v8"
	gl "github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"
)

func TestOWAProjectedRecipientRowsAbovePrimaryLimit(t *testing.T) {
	path := owaBudgetArchive(t)
	// Actual private recipient table; no source fixture, schema or importer alteration.
	owaCorrupt(t, path, `delete from mail_recipients`)
	owaCorrupt(t, path, `with recursive n(x) as (select 0 union all select x+1 from n where x<19999) insert into mail_recipients(message_rowid,ord,name,address,kind_raw,role) select (select min(rowid) from mail_messages),x,'Synthetic','synthetic@example.test',1,'to' from n`)
	st := owaArchive(t, path)
	rows, measured, err := owaQueryMeasured(owaWorkContext(t), st, `select rowid,* from mail_recipients order by rowid`, 512<<20)
	if err != nil || len(rows) != 20000 || measured <= 0 || measured >= 2<<20 {
		t.Fatal("bounded_projected_child_rows_refused_or_truncated")
	}
	state, err := owaArchiveStateBudget(owaWorkContext(t), st, 512<<20)
	if err != nil || len(state["mail_recipients"]) != 20000 {
		t.Fatal("bounded_child_state_rows_refused_or_truncated")
	}
}

func TestOWAProjectedRowBoundariesAndByteRefusals(t *testing.T) {
	if owaProjectionBudget(131072, 1, 1) != nil {
		t.Fatal("exact_projected_row_boundary_refused")
	}
	for _, tc := range []struct{ rows, bytes, max int64 }{
		{131073, 0, 512 << 20}, {-1, 0, 512 << 20}, {1, -1, 512 << 20},
		{1, 0, -1}, {1, 0, (512 << 20) + 1}, {1, 2, 1},
	} {
		if err := owaProjectionBudget(tc.rows, tc.bytes, tc.max); err == nil || err.Error() != "query_projection_budget" {
			t.Fatal("invalid_projection_bound_accepted")
		}
	}
	st := owaArchive(t, owaBudgetArchive(t))
	q := `with recursive n(x) as (select 1 union all select x+1 from n where x<131073) select x as ord from n`
	if rows, bytes, err := owaQueryMeasured(context.Background(), st, q, 512<<20); err == nil || err.Error() != "query_projection_budget" || rows != nil || bytes != 0 {
		t.Fatal("actual_excess_projected_rows_materialized")
	}
	if rows, _, err := owaQueryMeasured(context.Background(), st, `select 'abc' as address`, 3); err != nil || len(rows) != 1 {
		t.Fatal("small_exact_projection_byte_boundary_refused")
	}
	for _, max := range []int64{2, -1, (512 << 20) + 1} {
		if rows, bytes, err := owaQueryMeasured(context.Background(), st, `select 'abc' as address`, max); err == nil || rows != nil || bytes != 0 {
			t.Fatal("actual_projection_byte_override_accepted")
		}
	}
	if rows, _, err := owaQueryMeasured(context.Background(), st, `select missing from absent_table`, 512<<20); err == nil || rows != nil {
		t.Fatal("unknown_query_accepted")
	}
}

func TestOWAPrimaryObjectAndArrayLimitsRemain16384(t *testing.T) {
	for _, n := range []int{16384, 16385} {
		keys := make([]string, n)
		values := make([]any, n)
		for i := range keys {
			keys[i] = strconv.Itoa(i)
		}
		f := owaFields{}
		f.get(&v8.Object{Keys: keys, Values: values}, "missing")
		if n == 16384 && f.err != nil || n == 16385 && (f.err == nil || f.err.Error() != "census_resource_refused") {
			t.Fatal("primary_object_limit_expanded")
		}
		f = owaFields{}
		got := f.list(owaObject("recipients", make([]any, n)), "recipients")
		if n == 16384 && (f.err != nil || len(got) != 16384) || n == 16385 && (f.err == nil || f.err.Error() != "census_resource_refused" || got != nil) {
			t.Fatal("primary_array_limit_changed")
		}
	}

}

func TestOWAPrimaryRawRecordLimitRemains16384(t *testing.T) {
	c := owaCapture(t, owaFixtureProfile("mail", "Synthetic"))
	owaSyntheticTable(t, c, nil, false)
	db, err := gl.OpenFile(c.profile.Source.LevelDBDir, &opt.Options{})
	if err != nil {
		t.Fatal("synthetic_primary_record_open_failed")
	}
	batch := new(gl.Batch)
	// The unchanged fixture has 7 folders, 5 headers and 4 bodies; add 16,368 valid orphan bodies to reach exactly 16,384 primary records.
	for i := 0; i < 16368; i++ {
		id := "SyntheticPrimary" + strconv.Itoa(i)
		value := owaObject("ItemId", owaIdentity(id))
		batch.Put(owaSyntheticKey(3, id), append([]byte{1, 0xff, 0x10, 0xff, 15}, owaEncodeSynthetic(t, value)...))
	}
	writeErr := db.Write(batch, nil)
	closeErr := db.Close()
	if writeErr != nil || closeErr != nil {
		t.Fatal("synthetic_primary_records_write_failed")
	}
	census, err := owaCensusContext(c.ctx, c.profile.Source.LevelDBDir, c.profile.Source.BlobDir)
	if err != nil || len(census.bodies) != 16372 || len(census.messages) != 5 || len(census.folders) != 7 {
		t.Fatal("exact_primary_record_limit_refused")
	}
	db, err = gl.OpenFile(c.profile.Source.LevelDBDir, &opt.Options{})
	if err != nil {
		t.Fatal("synthetic_primary_record_reopen_failed")
	}
	value := owaObject("ItemId", owaIdentity("SyntheticPrimaryExcess"))
	writeErr = db.Put(owaSyntheticKey(3, "SyntheticPrimaryExcess"), append([]byte{1, 0xff, 0x10, 0xff, 15}, owaEncodeSynthetic(t, value)...), nil)
	closeErr = db.Close()
	if writeErr != nil || closeErr != nil {
		t.Fatal("synthetic_primary_excess_write_failed")
	}
	if _, err := owaCensusContext(c.ctx, c.profile.Source.LevelDBDir, c.profile.Source.BlobDir); err == nil || err.Error() != "census_resource_refused" {
		t.Fatal("primary_record_limit_expanded")
	}
}
