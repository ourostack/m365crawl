package calendar

import (
	"context"
	"database/sql"
	"slices"
)

// Find resolves the same joined event group as Agenda, including historical
// timed/date keys. FindWithRows also exposes its actual source-qualified rows.
func Find(ctx context.Context, db *sql.DB, principal, key string) (AgendaItem, bool, error) {
	item, _, ok, err := FindWithRows(ctx, db, principal, key)
	return item, ok, err
}

func FindWithRows(ctx context.Context, db querier, principal, key string) (item AgendaItem, sourceRows []Event, ok bool, err error) {
	principals, err := LoadPrincipals(ctx, db)
	if err != nil {
		return item, nil, false, err
	}
	scope := principals.Accounts(principal)
	cols := selectColumns(false)
	rows, err := queryKeyed(ctx, db, cols, eventQuery{
		sql:  selectSQL(cols, "event_key = ? AND account_id IN ("+placeholders(len(scope))+")"),
		args: append([]any{key}, anys(scope)...),
	})
	if err != nil || len(rows) == 0 {
		return item, nil, false, err
	}
	from, to := rows[0].Start, rows[0].End
	for _, r := range rows {
		if r.Start.Before(from) {
			from = r.Start
		}
		if r.End.After(to) {
			to = r.End
		}
	}
	if to.Before(from) {
		to = from
	}
	from, to = from.UTC().AddDate(0, 0, -1), to.UTC().AddDate(0, 0, 1)
	groups, joined, err := loadGroupsJoined(ctx, db, principals, scope, from, to, from.Format(dateLayout), to.Format(dateLayout))
	if err != nil {
		return item, nil, false, err
	}
	windows, err := loadWindows(ctx, db, scope)
	if err != nil {
		return item, nil, false, err
	}
	fresh, _ := freshness(windows, principals)
	for k, g := range groups {
		if k.principal == principal && (k.key == key || slices.Contains(joined[k], key)) {
			return mergeItem(k, g, fresh[principal], joined[k]), append([]Event(nil), g...), true, nil
		}
	}
	return item, nil, false, nil
}

func anys(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}
