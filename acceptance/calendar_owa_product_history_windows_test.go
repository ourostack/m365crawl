//go:build acceptance && windows

package acceptance

import (
	"context"
	"errors"

	"github.com/ourostack/m365crawl/internal/store"
)

var errOWACalendarAccountScope = errors.New("calendar_product_account_scope_refused")

func owaCalendarProductHistory(ctx context.Context, st *store.Store, account string) ([][]any, int64, error) {
	rows, err := owaQueryContext(ctx, st, `select source,account_id,source_id,first_seen_at,field_clocks_json,detail_as_of,removed_at
		from calendar_source_events order by source,account_id,source_id`)
	if err != nil {
		return nil, 0, err
	}
	var count int64
	for _, row := range rows {
		if len(row) != 7 {
			return nil, 0, errors.New("calendar_product_history_shape_refused")
		}
		if row[0] != "outlook" || row[1] != account {
			return nil, 0, errOWACalendarAccountScope
		}
		count++
	}
	return rows, count, nil
}
