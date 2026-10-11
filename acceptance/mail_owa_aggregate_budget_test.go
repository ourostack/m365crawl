//go:build acceptance

package acceptance

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	gl "github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"
)

func TestOWAAggregatePayloadAboveOldCeiling(t *testing.T) {
	c := owaCapture(t, owaFixtureProfile("mail", "Synthetic"))
	owaSyntheticTable(t, c, nil, false)
	db, err := gl.OpenFile(c.profile.Source.LevelDBDir, &opt.Options{})
	if err != nil {
		t.Fatal("synthetic_aggregate_open_failed")
	}
	body := strings.Repeat("X", 1<<20)
	for i := 0; i < 65; i++ {
		id := strings.Repeat("A", i+1)
		value := owaObject("ItemId", owaIdentity(id), "NormalizedBody", owaObject("Value", body))
		if db.Put(owaSyntheticKey(3, id), append([]byte{1, 0xff, 0x10, 0xff, 15}, owaEncodeSynthetic(t, value)...), nil) != nil {
			if db.Close() != nil {
				t.Error("synthetic_aggregate_close_failed")
			}
			t.Fatal("synthetic_aggregate_write_failed")
		}
	}
	if db.Close() != nil {
		t.Fatal("synthetic_aggregate_close_failed")
	}
	census, err := owaCensusContext(c.ctx, c.profile.Source.LevelDBDir, c.profile.Source.BlobDir)
	if err != nil || len(census.bodies) != 69 || len(census.messages) != 5 {
		t.Fatal("legitimate_aggregate_payload_refused")
	}
}

func TestOWAProjectedQueryAboveOldCeiling(t *testing.T) {
	st := owaArchive(t, owaBudgetArchive(t))
	q := `with recursive n(x) as (select 1 union all select x+1 from n where x<33) select zeroblob(1048576) as payload from n`
	rows, err := owaQueryContext(owaWorkContext(t), st, q)
	if err != nil || len(rows) != 33 {
		t.Fatal("bounded_query_above_old_ceiling_refused")
	}
}

func owaBudgetArchive(t *testing.T) string {
	t.Helper()
	c := owaObserve(t, owaFixtureProfile("mail", "Synthetic"))
	path := filepath.Join(owaScratch(t), "budget.db")
	owaExercise(t, path, c)
	return path
}

func TestOWAAggregateBudgetBoundaries(t *testing.T) {
	for _, limit := range []int64{owaMaxDecodedBytes, owaMaxStateBytes, owaMaxQueryBytes} {
		if limit != 512<<20 {
			t.Error("aggregate_operational_limit_not_512mib")
		}
		if owaProjectionBudget(1, limit, limit) != nil || owaProjectionBudget(1, limit+1, limit) == nil || owaProjectionBudget(owaMaxProjectionRows+1, 0, limit) == nil || owaProjectionBudget(1, 0, limit+1) == nil {
			t.Error("projection_budget_boundary_unproved")
		}
	}
	if owaDecodedBudget((512<<20)-(8<<20), 8<<20) != nil || owaDecodedBudget((512<<20)-(8<<20)+1, 8<<20) == nil || owaDecodedBudget(0, (8<<20)+1) == nil || owaDecodedBudget((512<<20)+1, 0) == nil {
		t.Error("decoded_budget_boundary_unproved")
	}
	if err := owaDecodedBudget(512<<20, 1); err == nil || err.Error() != "aggregate_payload_budget" {
		t.Error("aggregate_refusal_not_distinguished")
	}
	st := owaArchive(t, owaBudgetArchive(t))
	q := `select zeroblob(32) as payload`
	if _, err := owaQueryBudget(context.Background(), st, q, 32); err != nil {
		t.Fatal("actual_projection_exact_budget_refused")
	}
	if _, err := owaQueryBudget(context.Background(), st, q, 31); err == nil {
		t.Fatal("actual_projection_over_budget_materialized")
	}
	if _, err := owaArchiveStateBudget(owaWorkContext(t), st, 1); err == nil {
		t.Fatal("actual_archive_state_over_budget_accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := owaArchiveStateBudget(ctx, st, 512<<20); err == nil || err.Error() != "archive_state_refused" {
		t.Fatal("state_context_refusal_misclassified")
	}
	if _, err := owaArchiveStateBudget(owaWorkContext(t), st, (512<<20)+1); err == nil {
		t.Fatal("archive_state_bound_override_accepted")
	}
}

func TestOWAArchiveStateAboveOldAggregateCeiling(t *testing.T) {
	path := owaBudgetArchive(t)
	// Exact synthetic archive mutation only; five 8 MiB values in each table remain within the unchanged individual body limit.
	owaCorrupt(t, path, `update mail_messages set body_text=cast(zeroblob(8388608) as text)`)
	owaCorrupt(t, path, `update mail_fts set body_text=cast(zeroblob(8388608) as text)`)
	st := owaArchive(t, path)
	state, err := owaArchiveStateBudget(owaWorkContext(t), st, owaMaxStateBytes)
	if err != nil {
		t.Fatal("bounded_archive_state_above_old_limit_refused")
	}
	var bytes int64
	for _, rows := range state {
		for _, row := range rows {
			for _, v := range row {
				bytes += int64(len(asString(v)))
			}
		}
	}
	if bytes <= 64<<20 || bytes > 512<<20 {
		t.Fatal("actual_state_size_not_proved")
	}
	if _, err := owaArchiveStateBudget(owaWorkContext(t), st, 64<<20); err == nil {
		t.Fatal("actual_state_old_limit_not_enforced")
	}
}
