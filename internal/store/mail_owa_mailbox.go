package store

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"
)

const (
	// owaMailboxIdentityKey + account holds the sealed mailbox identity bound to a Windows Outlook
	// mail account. It is written in the transaction that commits that mailbox's mail, so an
	// account never holds mail of an identity it is not bound to.
	owaMailboxIdentityKey = "outlook_mailbox_identity:"
	// owaMailboxSealKey holds the archive's random key that seals mailbox identities, so a stored
	// binding cannot confirm a guessed address without the archive's own key.
	owaMailboxSealKey = "outlook_mailbox_seal"
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

// scopeArgs selects the profile's account and its mailbox accounts; callers filter exactly.
func scopeArgs(prefix, scope string) []any {
	return []any{prefix + scope, escapeLike(prefix+scope+OWAMailboxInfix) + "%"}
}

func owaMailboxBindings(ctx context.Context, q mailQuerier, scope string) (map[string]string, error) {
	out := map[string]string{}
	err := mailEach(ctx, q, `select key, value from meta where key=? or key like ? escape '\'`, scopeArgs(owaMailboxIdentityKey, scope), func(rows *sql.Rows) error {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return err
		}
		if account := strings.TrimPrefix(key, owaMailboxIdentityKey); inOWAMailboxScope(scope, account) {
			out[account] = value
		}
		return nil
	})
	return out, err
}

// OWAMailboxView is what attributing a profile's mailboxes needs of its archived accounts.
type OWAMailboxView struct {
	Bindings map[string]string          // account → sealed bound mailbox identity
	Holds    map[string]bool            // accounts holding mail rows
	Native   map[string]map[string]bool // account → held native "folder\x00id" and "message\x00id"
	seal     []byte
}

// Sealed is the identity as bindings store it; empty before the archive bound any mailbox.
func (v OWAMailboxView) Sealed(identity string) string {
	if v.seal == nil {
		return ""
	}
	return sealIdentity(v.seal, identity)
}

func sealIdentity(key []byte, identity string) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(identity))
	return hex.EncodeToString(m.Sum(nil))
}

// owaSeal reads the archive's sealing key, or nil when none exists yet.
func owaSeal(ctx context.Context, q mailQuerier) ([]byte, error) {
	var seal []byte
	err := mailEach(ctx, q, `select value from meta where key=?`, []any{owaMailboxSealKey}, func(rows *sql.Rows) error {
		var raw string
		err := rows.Scan(&raw)
		if err == nil {
			seal, err = hex.DecodeString(raw)
		}
		return err
	})
	return seal, err
}

// NativeKey is the OWAMailboxView.Native key of a native identity.
func NativeKey(kind, id string) string { return kind + "\x00" + id }

// OWAMailboxView reads the bindings, mail and native identities of the profile account's scope.
func (s *Store) OWAMailboxView(ctx context.Context, scope string) (OWAMailboxView, error) {
	v := OWAMailboxView{Holds: map[string]bool{}, Native: map[string]map[string]bool{}}
	var err error
	if v.seal, err = owaSeal(ctx, s.db); err != nil {
		return OWAMailboxView{}, err
	}
	if v.Bindings, err = owaMailboxBindings(ctx, s.db, scope); err != nil {
		return OWAMailboxView{}, err
	}
	err = mailEach(ctx, s.db, `select distinct account from mail_messages where account=? or account like ? escape '\'`, scopeArgs("", scope), func(rows *sql.Rows) error {
		var account string
		err := rows.Scan(&account)
		v.Holds[account] = inOWAMailboxScope(scope, account)
		return err
	})
	if err != nil {
		return OWAMailboxView{}, err
	}
	err = mailEach(ctx, s.db, `select account, kind, source_id from mail_source_ids where (account=? or account like ? escape '\') and kind in ('folder','message')`, scopeArgs("", scope), func(rows *sql.Rows) error {
		var account, kind, id string
		if err := rows.Scan(&account, &kind, &id); err != nil {
			return err
		}
		if v.Native[account] == nil {
			v.Native[account] = map[string]bool{}
		}
		v.Native[account][NativeKey(kind, id)] = inOWAMailboxScope(scope, account)
		return nil
	})
	if err != nil {
		return OWAMailboxView{}, err
	}
	return v, nil
}

// bindOWAMailbox records the account's identity inside the commit's transaction. A bound account
// keeps its identity unless the caller proved a rebind, and one identity holds one account.
func bindOWAMailbox(ctx context.Context, tx *sql.Tx, b OWAMailBatch) error {
	if b.Identity == "" {
		return nil
	}
	seal, err := owaSeal(ctx, tx)
	if err == nil && seal == nil {
		seal = make([]byte, 32)
		_, _ = rand.Read(seal) // crypto/rand.Read never fails
		_, err = tx.ExecContext(ctx, `insert into meta(key, value) values(?, ?)`, owaMailboxSealKey, hex.EncodeToString(seal))
	}
	var bound map[string]string
	if err == nil {
		bound, err = owaMailboxBindings(ctx, tx, b.Scope)
	}
	if err != nil {
		return err
	}
	sealed := sealIdentity(seal, b.Identity)
	if !inOWAMailboxScope(b.Scope, b.Account) || bound[b.Account] != "" && bound[b.Account] != sealed && !b.Rebind {
		return ErrOWAMailboxBinding
	}
	for account, identity := range bound {
		if account != b.Account && identity == sealed {
			return ErrOWAMailboxBinding
		}
	}
	_, err = tx.ExecContext(ctx, `insert into meta(key, value) values(?, ?) on conflict(key) do update set value=excluded.value`, owaMailboxIdentityKey+b.Account, sealed)
	return err
}

// OWAMailboxes returns the accounts the profile held at its last attributed read, or nil.
func (s *Store) OWAMailboxes(ctx context.Context, scope string) ([]string, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `select value from meta where key=?`, owaMailboxesKey+scope).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	var out []string
	if err == nil {
		err = json.Unmarshal([]byte(raw), &out)
	}
	return out, err
}

// SetOWAMailboxes records the accounts an attributed read of the profile found.
func (s *Store) SetOWAMailboxes(ctx context.Context, scope string, accounts []string) error {
	sorted := slices.Clone(accounts)
	slices.Sort(sorted)
	b, _ := json.Marshal(slices.Compact(sorted))
	_, err := s.db.ExecContext(ctx, `insert into meta(key, value) values(?, ?) on conflict(key) do update set value=excluded.value`, owaMailboxesKey+scope, string(b))
	return err
}

// OWAProfileMailState combines the mail state of every mailbox account the profile held at its
// last attributed read: the first failure, read only when every mailbox was read (at the oldest
// read), and those accounts.
func (s *Store) OWAProfileMailState(ctx context.Context, scope string) (MailState, []string, error) {
	accounts, err := s.OWAMailboxes(ctx, scope)
	if len(accounts) == 0 {
		accounts = []string{scope}
	}
	out := MailState{Read: true}
	for _, a := range accounts {
		var st MailState
		if err == nil {
			st, err = s.MailState(ctx, a)
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
	if err != nil {
		return MailState{}, nil, err
	}
	if !out.Read || out.Failure != nil {
		out.Read, out.ReadAt = false, time.Time{}
	}
	return out, accounts, nil
}
