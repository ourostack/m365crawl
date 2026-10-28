package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

const WorkIQDefaultChatQualification = "workiq-v1:default365"
const WorkIQAbsoluteChatQualification = "workiq-v1:absolute"

type WorkIQChatWindow struct {
	Start, End    time.Time
	Resumed       bool
	Qualification string
}

func (s *Store) ResolveWorkIQChatWindow(ctx context.Context, account string, now time.Time, requested ...time.Time) (WorkIQChatWindow, error) {
	out := WorkIQChatWindow{Start: now.Add(-365 * 24 * time.Hour), End: now, Qualification: WorkIQDefaultChatQualification}
	if now.IsZero() || len(requested) > 1 {
		return WorkIQChatWindow{}, errors.New("invalid fixed chat window request")
	}
	filter := ""
	if len(requested) == 1 {
		if requested[0].IsZero() || requested[0].After(now) {
			return WorkIQChatWindow{}, errors.New("invalid absolute chat window")
		}
		out.Start, out.Qualification = requested[0], WorkIQAbsoluteChatQualification
		filter = scopeTime(requested[0])
	}
	facts, err := s.WorkIQAccounts(ctx)
	if err != nil {
		return WorkIQChatWindow{}, err
	}
	known := false
	for _, fact := range facts {
		known = known || fact.Family == "chats" && fact.ID == account
	}
	if !known {
		return WorkIQChatWindow{}, errors.New("chat window owner is not archived")
	}
	var start, end, scope, family, originalAccount, qualification string
	err = s.db.QueryRowContext(ctx, `select coalesce(o.window_start,''),coalesce(o.window_end,o.started_at),f.scope,o.family,o.account_id,o.qualification
from workiq_frontiers f join workiq_pages p on p.frontier_id=f.id
join workiq_attempts o on o.id=p.attempt_id
where f.family='chats' and f.account_id=? and f.qualification=? and f.retired=0
and f.revision>f.admitted_revision and p.state='staged' and p.expected_revision>=f.admitted_revision
and (?='' or f.scope=?) order by p.id desc limit 1`,
		account, out.Qualification, filter, filter).Scan(&start, &end, &scope, &family, &originalAccount, &qualification)
	if errors.Is(err, sql.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return WorkIQChatWindow{}, err
	}
	startAt, err := workIQStoredTime(sql.NullString{String: start, Valid: start != ""})
	if err != nil {
		return WorkIQChatWindow{}, err
	}
	endAt, err := workIQStoredTime(sql.NullString{String: end, Valid: true})
	if err != nil {
		return WorkIQChatWindow{}, err
	}
	if family != "chats" || originalAccount != account || qualification != out.Qualification || scope != start ||
		startAt == nil || startAt.IsZero() || endAt == nil || endAt.IsZero() || endAt.Before(*startAt) || endAt.After(now) {
		return WorkIQChatWindow{}, errors.New("invalid retained fixed chat window")
	}
	out.Start, out.End, out.Resumed = *startAt, *endAt, true
	return out, nil
}
