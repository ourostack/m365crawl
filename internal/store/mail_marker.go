package store

import (
	"context"
	"strings"
)

// mailMarkerPrefix starts the meta key a sync sets once it has read an account's mail.
const mailMarkerPrefix = "outlook_mail_read:"

// MailReadAccounts lists the accounts whose mail a sync has read (the account part of each
// outlook_mail_read marker), in name order. An archive no sync wrote a marker to returns none.
func (s *Store) MailReadAccounts(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `select key from meta where key like ? order by key`, mailMarkerPrefix+"%")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var key string
		_ = rows.Scan(&key) // the key column is never null
		out = append(out, strings.TrimPrefix(key, mailMarkerPrefix))
	}
	return out, rows.Err()
}
