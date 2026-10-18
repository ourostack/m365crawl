package store

import (
	"context"
	"time"

	"github.com/ourostack/m365crawl/internal/teamsdesktop"
)

// One private obligation per source survives unchanged raw digests and narrower reads.
func calendarReplayKey(source string) string { return "calendar_replay/" + source }

func (s *Store) CalendarReplayPending(ctx context.Context, source string) (bool, error) {
	var pending bool
	err := s.db.QueryRowContext(ctx, `select exists(select 1 from meta where key=?)`, calendarReplayKey(source)).Scan(&pending)
	return pending, err
}

// DeriveCalendarObservation defers incomplete positives without granting absence or freshness.
// A complete unfiltered replay must apply every retained group before discharging the obligation.
func (x *Session) DeriveCalendarObservation(ctx context.Context, source string, at time.Time, zone *time.Location, present []string, complete, filtered bool) (CalendarResult, error) {
	key := calendarReplayKey(source)
	var pending bool
	if err := x.tx.QueryRowContext(ctx, `select exists(select 1 from meta where key=?)`, key).Scan(&pending); err != nil {
		return CalendarResult{}, err
	}
	mark := func() error {
		_, err := x.tx.ExecContext(ctx, `insert into meta(key,value) values(?,'1') on conflict(key) do update set value='1'`, key)
		return err
	}
	if !complete {
		if err := mark(); err != nil {
			return CalendarResult{}, err
		}
		pending = true
	}
	read := map[[2]string]bool{}
	if complete {
		for _, name := range present {
			if _, a, ok := teamsdesktop.ParseDatabaseName(name); ok {
				read[[2]string{a.TenantID, a.UserID}] = true
			}
		}
	}
	mode := calendarMode{source: source, since: at.UTC().Format(timeLayout), at: at, zone: zone, read: read}
	if pending && complete {
		mode.since = ""
		mode.replayAll = !filtered
	}
	res, err := isolated(ctx, x.tx, func() (CalendarResult, error) {
		return deriveCalendar(ctx, x.tx, mode)
	})
	if err != nil {
		return CalendarResult{}, err
	}
	if res.failure != nil {
		if err := mark(); err != nil {
			return CalendarResult{}, err
		}
		pending = true
	}
	if pending && complete && !filtered && res.failure == nil &&
		res.Omissions[OmitCalendarFailed]+res.Omissions[OmitCalendarUnmapped]+res.Omissions[OmitCalendarRefused] == 0 {
		if _, err := x.tx.ExecContext(ctx, `delete from meta where key=?`, key); err != nil {
			return CalendarResult{}, err
		}
		pending = false
	}
	res.ReplayPending = pending
	return res, nil
}
