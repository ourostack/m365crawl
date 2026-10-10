//go:build acceptance

package acceptance

import (
	"context"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/store"
)

func TestOWAArchiveStateExactCumulativeProjection(t *testing.T) {
	st := owaArchive(t, owaBudgetArchive(t))
	var total int64
	for _, table := range []string{"mail_messages", "mail_folders", "mail_recipients", "mail_attachments", "mail_source_ids", "mail_absent", "mail_fts"} {
		total += owaIndependentProjectedBytes(t, st, "select rowid,* from "+table+" order by rowid")
	}
	if total <= 1 {
		t.Fatal("synthetic_projection_measurement_invalid")
	}
	if _, err := owaArchiveStateBudget(owaWorkContext(t), st, total); err != nil {
		t.Fatal("exact_cumulative_projection_refused")
	}
	if _, err := owaArchiveStateBudget(owaWorkContext(t), st, total-1); err == nil || err.Error() != "archive_state_budget" {
		t.Fatal("cumulative_projection_budget_excess_accepted")
	}
}

// This test independently reproduces the reviewer's SQL projection measurement; production accounting must carry its own single admitted measurement, not call this oracle.
func owaIndependentProjectedBytes(t *testing.T, st *store.Store, q string) int64 {
	t.Helper()
	ctx := owaWorkContext(t)
	cols, _, _, err := st.SQL(ctx, "select * from ("+q+") limit 0", 0)
	if err != nil || len(cols) == 0 {
		t.Fatal("synthetic_projection_schema_failed")
	}
	lengths := make([]string, len(cols))
	for i, col := range cols {
		lengths[i] = `coalesce(length(cast("` + strings.ReplaceAll(col, `"`, `""`) + `" as blob)),0)`
	}
	_, rows, truncated, err := st.SQL(ctx, "select coalesce(sum("+strings.Join(lengths, "+")+"),0) from ("+q+")", 1)
	if err != nil || truncated || len(rows) != 1 || len(rows[0]) != 1 {
		t.Fatal("synthetic_projection_measurement_failed")
	}
	n, ok := rows[0][0].(int64)
	if !ok || n < 0 {
		t.Fatal("synthetic_projection_measurement_invalid")
	}
	return n
}

func TestOWAMeasuredQueryIncludesNumericProjectedBytes(t *testing.T) {
	st := owaArchive(t, owaBudgetArchive(t))
	q := `select 12345 as numeric_value, 'abc' as string_value, null as missing_value`
	rows, measured, err := owaQueryMeasured(context.Background(), st, q, 8)
	if err != nil || measured != 8 || len(rows) != 1 {
		t.Fatal("numeric_projection_measurement_not_carried")
	}
	if _, measured, err := owaQueryMeasured(context.Background(), st, q, 7); err == nil || measured != 0 {
		t.Fatal("refused_query_returned_admitted_measurement")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, measured, err := owaQueryMeasured(ctx, st, q, 8); err == nil || measured != 0 || err.Error() != "query_context_refused" {
		t.Fatal("measured_query_context_refusal_changed")
	}
}
