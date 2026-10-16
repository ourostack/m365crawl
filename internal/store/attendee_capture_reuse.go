package store

import "context"

func (s *Store) AttendeeCaptureReused(ctx context.Context, attempt KnowledgeAttempt) (bool, error) {
	var reused bool
	err := s.attendeeView(ctx, func(q knowledgeReader, available bool) error {
		if !available {
			return ErrKnowledgeSourceUnknown
		}
		return q.QueryRowContext(ctx, `select p.first_attempt_id<>m.attempt_id from outlook_attendee_members m
join outlook_attendee_snapshots p on p.rowid=m.snapshot_id join knowledge_attempts a on a.id=m.attempt_id
where m.attempt_id=? and a.locator_id=?`, attempt.ID, attempt.LocatorID).Scan(&reused)
	})
	return reused, err
}
