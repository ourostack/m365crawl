package store

import "context"

// WorkIQMailComplete reports whether account has a recorded whole-account, all-folder, gap-free
// WorkIQ mail sync of any age, so its retained server mail reaches past the native cache.
func (s *Store) WorkIQMailComplete(ctx context.Context, account string) (bool, error) {
	ok, err := s.hasWorkIQ(ctx)
	if err != nil || !ok {
		return false, err
	}
	var done bool
	err = s.db.QueryRowContext(ctx, `select exists(select 1 from workiq_mail_receipts r
join workiq_attempts a on a.id=r.attempt_id
where r.account_id=? and a.account_id=r.account_id and a.family='mail' and a.kind='sync'
and a.status='ok' and a.all_folders=1 and a.gap_count=0 and r.enumeration_key<>'')`, account).Scan(&done)
	return done, err
}
