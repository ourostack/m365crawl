package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"
)

const (
	// owaMailboxIdentityKey + account holds the mailbox identity (outlookowa.MailboxIdentity) bound
	// to a Windows Outlook mail account. It is written in the transaction that commits that
	// mailbox's mail, so an account never holds mail of an identity it is not bound to.
	owaMailboxIdentityKey = "outlook_mailbox_identity:"
	// owaMailboxesKey + the profile's account lists the accounts whose mailboxes the profile held
	// at its last attributed read; a profile without it has only its own account.
	owaMailboxesKey = "outlook_mailboxes:"
	// OWAMailboxInfix separates a profile's account from one of its additional mailbox accounts.
	OWAMailboxInfix = "/mailbox-"
)

// ErrOWAMailboxBinding refuses mail whose mailbox identity conflicts with the recorded binding.
var ErrOWAMailboxBinding = errors.New("store: outlook mailbox binding conflict")

func inOWAMailboxScope(scope, account string) bool {
	return account == scope || strings.HasPrefix(account, scope+OWAMailboxInfix) && !strings.Contains(strings.TrimPrefix(account, scope+OWAMailboxInfix), "/")
}

// OWAMailboxBindings maps each account of the profile account's scope to its bound mailbox identity.
func (s *Store) OWAMailboxBindings(ctx context.Context, scope string) (map[string]string, error) {
	return owaMailboxBindings(ctx, s.db, scope)
}

func owaMailboxBindings(ctx context.Context, q queryer, scope string) (map[string]string, error) {
	rows, err := q.QueryContext(ctx, `select key, value from meta where key=? or key like ? escape '\'`,
		owaMailboxIdentityKey+scope, escapeLike(owaMailboxIdentityKey+scope+OWAMailboxInfix)+"%")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]string{}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		if account := strings.TrimPrefix(key, owaMailboxIdentityKey); inOWAMailboxScope(scope, account) {
			out[account] = value
		}
	}
	return out, rows.Err()
}

// bindOWAMailbox records the account's identity inside the commit's transaction. A bound account
// keeps its identity unless the caller proved a rebind, and one identity holds one account.
func bindOWAMailbox(ctx context.Context, tx *sql.Tx, b OWAMailBatch) error {
	if b.Identity == "" {
		return nil
	}
	if !inOWAMailboxScope(b.Scope, b.Account) {
		return ErrOWAMailboxBinding
	}
	bound, err := owaMailboxBindings(ctx, tx, b.Scope)
	if err != nil {
		return err
	}
	if old, ok := bound[b.Account]; ok && old != b.Identity && !b.Rebind {
		return ErrOWAMailboxBinding
	}
	for account, identity := range bound {
		if account != b.Account && identity == b.Identity {
			return ErrOWAMailboxBinding
		}
	}
	_, err = tx.ExecContext(ctx, `insert into meta(key, value) values(?, ?) on conflict(key) do update set value=excluded.value`, owaMailboxIdentityKey+b.Account, b.Identity)
	return err
}

// OWAMailboxes returns the accounts the profile held at its last attributed read, or nil.
func (s *Store) OWAMailboxes(ctx context.Context, scope string) ([]string, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `select value from meta where key=?`, owaMailboxesKey+scope).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// SetOWAMailboxes records the accounts an attributed read of the profile found.
func (s *Store) SetOWAMailboxes(ctx context.Context, scope string, accounts []string) error {
	sorted := slices.Clone(accounts)
	slices.Sort(sorted)
	b, _ := json.Marshal(slices.Compact(sorted))
	_, err := s.db.ExecContext(ctx, `insert into meta(key, value) values(?, ?) on conflict(key) do update set value=excluded.value`, owaMailboxesKey+scope, string(b))
	return err
}

// OWAMailSourceOverlap counts the native folder and message identities the account already holds.
// A mailbox keeps its native identities across reads, so a match is evidence of the same mailbox.
func (s *Store) OWAMailSourceOverlap(ctx context.Context, account string, folders, messages []string) (int, error) {
	n := 0
	for kind, ids := range map[string][]string{"folder": folders, "message": messages} {
		for _, id := range ids {
			var hit int
			if err := s.db.QueryRowContext(ctx, `select count(*) from mail_source_ids where account=? and kind=? and source_id=?`, account, kind, id).Scan(&hit); err != nil {
				return 0, err
			}
			n += hit
		}
	}
	return n, nil
}

// OWAMailSourceCount counts the native identities the account holds.
func (s *Store) OWAMailSourceCount(ctx context.Context, account string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `select count(*) from mail_source_ids where account=?`, account).Scan(&n)
	return n, err
}

// OWAProfileMailState combines the mail state of every mailbox account the profile held at its
// last attributed read: the first failure, read only when every mailbox was read (at the oldest
// read), and how many mailboxes there are.
func (s *Store) OWAProfileMailState(ctx context.Context, scope string) (MailState, int, error) {
	accounts, err := s.OWAMailboxes(ctx, scope)
	if err != nil {
		return MailState{}, 0, err
	}
	if len(accounts) == 0 {
		accounts = []string{scope}
	}
	out := MailState{Read: true}
	for _, a := range accounts {
		st, err := s.MailState(ctx, a)
		if err != nil {
			return MailState{}, 0, err
		}
		if st.Failure != nil && out.Failure == nil {
			out.Failure = st.Failure
		}
		if !st.Read {
			out.Read = false
		} else if out.ReadAt.IsZero() || st.ReadAt.Before(out.ReadAt) {
			out.ReadAt = st.ReadAt
		}
	}
	if !out.Read || out.Failure != nil {
		out.Read, out.ReadAt = false, time.Time{}
	}
	return out, len(accounts), nil
}
