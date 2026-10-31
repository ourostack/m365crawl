package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// WorkIQWindowRoute is what the archive's mail windows say about one route interval.
type WorkIQWindowRoute struct {
	// Covered says the union of the account's all-folder window coverage contains the interval.
	Covered bool
	// Twins counts unresolved deferred window members inside the interval that may be twins of
	// mail cached in another folder; it is counted only for a read of named folders.
	Twins int
	// Unproven says the latest finished window containing the interval fetched some folders only by
	// listing; OpenItems is that window's count of items from its start to now, for the proof cost.
	Unproven  bool
	OpenItems int64
}

// WorkIQMailWindowRoute reads window coverage for [since, until). folderKeys and folderKinds are
// the folders a --folder read names, nil for a read of every folder.
func (s *Store) WorkIQMailWindowRoute(ctx context.Context, account string, since, until time.Time, folderKeys []uint32, folderKinds []string) (WorkIQWindowRoute, error) {
	var out WorkIQWindowRoute
	ok, err := s.hasWorkIQWindows(ctx)
	if err != nil || !ok {
		return out, err
	}
	if !since.Before(until) {
		out.Covered = true
		return out, nil
	}
	type interval struct{ since, until time.Time }
	var covered []interval
	if err := mailEach(ctx, s.db, `select since,until from workiq_window_coverage where account_id=? and folder_id='' order by since,until`, []any{account}, func(r *sql.Rows) error {
		var a, b string
		if err := r.Scan(&a, &b); err != nil {
			return err
		}
		start, errA := time.Parse(time.RFC3339Nano, a)
		end, errB := time.Parse(time.RFC3339Nano, b)
		if errA != nil || errB != nil {
			return errors.New("invalid WorkIQ window coverage")
		}
		covered = append(covered, interval{start, end})
		return nil
	}); err != nil {
		return out, err
	}
	reach := since
	for _, c := range covered {
		if c.since.After(reach) {
			break
		}
		if c.until.After(reach) {
			reach = c.until
		}
	}
	out.Covered = !reach.Before(until)
	if out.Covered {
		if folderKeys == nil {
			return out, nil
		}
		keys, _ := json.Marshal(folderKeys)   // integers always encode
		kinds, _ := json.Marshal(folderKinds) // plain strings always encode
		err := s.db.QueryRowContext(ctx, `select count(*) from workiq_window_members m
where m.account_id=? and m.state='deferred' and m.in_window=1 and m.removed=0 and m.received_at>=? and m.received_at<?
and not exists(select 1 from workiq_aliases a where a.kind='mail' and a.account_id=m.account_id and a.namespace='graph' and a.alias=m.graph_id)
and not exists(select 1 from workiq_attempts a join workiq_censuses c on c.attempt_id=a.id where a.family='mail' and a.kind='sync' and a.account_id=m.account_id
 and a.status in ('ok','ok_with_omissions') and c.complete=1 and a.started_at>m.observed_at)
and not exists(select 1 from mail_messages x where x.rowid=m.match_rowid and x.folder_key in (select value from json_each(?)))
and (coalesce((select w.well_known from workiq_folders w where w.account_id=m.account_id and w.graph_id=m.folder_id),'other') in ('other')
 or exists(select 1 from json_each(?) k where k.value=(select w.well_known from workiq_folders w where w.account_id=m.account_id and w.graph_id=m.folder_id)
  or k.value not in ('inbox','sent','deleted','drafts','archive','junk')))`,
			account, workIQTimeValue(&since), workIQTimeValue(&until), string(keys), string(kinds)).Scan(&out.Twins)
		return out, err
	}
	var counts string
	err = s.db.QueryRowContext(ctx, `select a.counts_json from workiq_attempts a where a.family='mail' and a.kind='mail_window' and a.account_id=?
and a.status in ('ok','ok_with_omissions') and a.window_start<=? and a.window_end>=?
and exists(select 1 from workiq_gaps g where g.attempt_id=a.id and g.code='workiq_window_unproven')
and not exists(select 1 from workiq_attempts b where b.family='mail' and b.kind='mail_window' and b.account_id=a.account_id and b.id>a.id
 and b.status in ('ok','ok_with_omissions') and b.window_start<=? and b.window_end>=?)
order by a.id desc limit 1`, account, workIQTimeValue(&since), workIQTimeValue(&until), workIQTimeValue(&since), workIQTimeValue(&until)).Scan(&counts)
	if errors.Is(err, sql.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	var parsed map[string]int64
	if json.Unmarshal([]byte(counts), &parsed) != nil {
		return out, errors.New("invalid WorkIQ window counts")
	}
	out.Unproven, out.OpenItems = true, parsed["window_items_open"]
	return out, nil
}
