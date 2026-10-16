package store

import (
	"context"
	"database/sql"
	"errors"
)

// AttendeeBootstrapRequest recovers an admitted original request, never a retry selector or path guess.
func (s *Store) AttendeeBootstrapRequest(ctx context.Context, sourceID int64) (AttendeeRequest, error) {
	var request AttendeeRequest
	if sourceID <= 0 {
		return request, ErrKnowledgeSourceUnknown
	}
	err := s.attendeeView(ctx, func(q knowledgeReader, available bool) error {
		if !available {
			return ErrKnowledgeSourceUnknown
		}
		var locatorID int64
		err := q.QueryRowContext(ctx, `select s.locator_id from knowledge_sources s
join outlook_attendee_sources n on n.source_id=s.id where s.id=?`, sourceID).Scan(&locatorID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrKnowledgeSourceUnknown
		}
		if err != nil {
			return err
		}
		var attemptID int64
		err = q.QueryRowContext(ctx, `select m.attempt_id from outlook_attendee_members m
join knowledge_attempts a on a.id=m.attempt_id
join outlook_attendee_requests r on r.attempt_id=a.id
where m.snapshot_id in (select rowid from outlook_attendee_snapshots where source_id=?)
and a.locator_id=? and json_extract(r.request_json,'$.root')<>''
and json_extract(r.request_json,'$.source_id')='0'
order by m.attempt_id desc limit 1`, sourceID, locatorID).Scan(&attemptID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		saved, err := readAttendeeRequest(ctx, q, attemptID)
		if err != nil {
			return err
		}
		request = AttendeeRequest{Root: saved.Root, Discover: saved.Discover}
		return nil
	})
	return request, err
}
