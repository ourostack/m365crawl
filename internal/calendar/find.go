package calendar

import (
	"context"
	"database/sql"
	"slices"
)

// Find returns the merged event of one principal that is stored under key, or that a read joined
// the key's group into (JoinedKeys): a timed key that later became all-day names the same event as
// its twin's date key. It loads the group the way Agenda does, around the key's own rows, so the
// result is the item an agenda over the event's days returns. ok is false when no row of the
// principal's accounts holds the key.
func Find(ctx context.Context, db *sql.DB, principal, key string) (item AgendaItem, ok bool, err error) {
	principals, err := LoadPrincipals(ctx, db)
	if err != nil {
		return item, false, err
	}
	scope := principals.Accounts(principal)
	cols := selectColumns(false)
	rows, err := queryKeyed(ctx, db, cols, eventQuery{
		sql:  selectSQL(cols, "event_key = ? AND account_id IN ("+placeholders(len(scope))+")"),
		args: append([]any{key}, anys(scope)...),
	})
	if err != nil || len(rows) == 0 {
		return item, false, err
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
	// A day either side reaches the twin's rows; the load widens all-day dates by another day.
	from, to = from.UTC().AddDate(0, 0, -1), to.UTC().AddDate(0, 0, 1)
	groups, joined, err := loadGroupsJoined(ctx, db, principals, scope, from, to, from.Format(dateLayout), to.Format(dateLayout))
	if err != nil {
		return item, false, err
	}
	windows, err := loadWindows(ctx, db, scope)
	if err != nil {
		return item, false, err
	}
	fresh, _ := freshness(windows, principals)
	for k, g := range groups {
		if k.principal == principal && (k.key == key || slices.Contains(joined[k], key)) {
			return mergeItem(k, g, fresh[principal], joined[k]), true, nil
		}
	}
	return item, false, nil
}

func anys(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}
