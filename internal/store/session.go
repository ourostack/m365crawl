package store

import (
	"context"
	"database/sql"

	"github.com/ourostack/teamscrawl/internal/teamsdesktop"
)

// Session is one write transaction spanning any number of Apply calls. Nothing it wrote is
// visible to other connections, or kept, until Commit; Rollback (or a cancelled context) discards
// all of it. The Apply methods behave exactly like the Store's own.
type Session struct{ tx *sql.Tx }

// Begin starts a write transaction.
func (s *Store) Begin(ctx context.Context) (*Session, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	return &Session{tx: tx}, nil
}

// Commit makes the session's writes permanent.
func (x *Session) Commit() error { return x.tx.Commit() }

// Rollback discards the session's writes. It does nothing after Commit.
func (x *Session) Rollback() { _ = x.tx.Rollback() }

// ApplyAccount is Store.ApplyAccount inside the session.
func (x *Session) ApplyAccount(ctx context.Context, a teamsdesktop.Account) error {
	return applyAccount(ctx, x.tx, a)
}

// ApplyConversations is Store.ApplyConversations inside the session.
func (x *Session) ApplyConversations(ctx context.Context, cs []teamsdesktop.Conversation) (Counts, error) {
	return applyConversations(ctx, x.tx, cs)
}

// ApplyPeople is Store.ApplyPeople inside the session.
func (x *Session) ApplyPeople(ctx context.Context, ps []teamsdesktop.Person) (Counts, error) {
	return applyPeople(ctx, x.tx, ps)
}

// ApplyMessages is Store.ApplyMessages inside the session.
func (x *Session) ApplyMessages(ctx context.Context, ms []teamsdesktop.Message) (Counts, error) {
	n, _, err := applyMessages(ctx, x.tx, ms)
	return n, err
}

// ApplyMessagesChanges is Store.ApplyMessagesChanges inside the session.
func (x *Session) ApplyMessagesChanges(ctx context.Context, ms []teamsdesktop.Message) (Counts, []Change, error) {
	return applyMessages(ctx, x.tx, ms)
}

// ApplyActivityChanges is Store.ApplyActivityChanges inside the session.
func (x *Session) ApplyActivityChanges(ctx context.Context, as []teamsdesktop.Activity) (Counts, []Change, error) {
	return applyActivity(ctx, x.tx, as)
}

// RecordRun is Store.RecordRun inside the session, so a run is recorded only if its data is kept.
func (x *Session) RecordRun(ctx context.Context, r Run) error { return recordRun(ctx, x.tx, r) }
