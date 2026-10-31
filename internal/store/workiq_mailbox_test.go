package store

import (
	"context"
	"errors"
	"testing"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/outlookowa"
	"github.com/ourostack/m365crawl/internal/workiq"
)

func workIQCodeOf(err error) string {
	var coded *errs.Coded
	if errors.As(err, &coded) {
		return coded.Code
	}
	return ""
}

// Review: a Windows profile's further mailboxes (#267) carry only a sealed mailbox identity. WorkIQ
// binds such an account only when the signed-in user's own addresses seal to that identity; a
// mailbox of someone else mismatches, and an account without a binding is never bootstrapped to
// whoever WorkIQ is signed in as.
func TestWorkIQMailboxAccountsBindOnlyTheirOwnMailbox(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	must0(s.SetWorkIQEnabled(ctx, true))
	own, shared, unbound := mailAcct+"/mailbox-2", mailAcct+"/mailbox-3", mailAcct+"/mailbox-4"
	mustCommitOWA(t, s, owaMailboxBatch(own, outlookowa.MailboxIdentity("Person@Example.test", "person.upn@example.test"), "b", mailT0))
	mustCommitOWA(t, s, owaMailboxBatch(shared, outlookowa.MailboxIdentity("shared@example.test", "person.upn@example.test"), "c", mailT0))
	mustCommitOWA(t, s, owaMailboxBatch(unbound, "", "d", mailT0))
	facts := must(s.WorkIQAccounts(ctx))
	user := workiq.User{ID: "user-1", Mail: "person@example.test", UPN: "person.upn@example.test"}
	binding, err := workiq.SelectMailAccount(user, facts, own)
	if err != nil || binding.Bootstrap || binding.Address != "person@example.test" || binding.Verify(user) != nil {
		t.Fatal("the user's own mailbox did not bind to its sealed identity", err, binding.Bootstrap)
	}
	other := workiq.User{ID: "user-2", Mail: "other@example.test", UPN: "other@example.test"}
	for _, tc := range []struct {
		user    workiq.User
		account string
		code    string
	}{
		{other, own, "workiq_identity_mismatch"},
		{user, shared, "workiq_identity_mismatch"},
		{user, unbound, "workiq_account_invalid"},
	} {
		if _, err := workiq.SelectMailAccount(tc.user, facts, tc.account); workIQCodeOf(err) != tc.code {
			t.Fatalf("%s: %v, want %s", tc.account, err, tc.code)
		}
	}
	for _, fact := range facts {
		if fact.ID == own {
			if address, err := fact.PinAddress(); address != "" || err != nil {
				t.Fatal("a sealed mailbox pinned an address or refused before WorkIQ named its user", address, err)
			}
		}
	}
	for account, want := range map[string]bool{own: true, shared: true, unbound: false, mailAcct: true} {
		if got := must(s.WorkIQMailBindable(ctx, account)); got != want {
			t.Fatalf("%s bindable %v, want %v", account, got, want)
		}
	}
	// Every failing read of the bindings, seal or accounts is the caller's error.
	sweepKind(t, s, injectFaults(t, s), "query", false, func(ctx context.Context, s *Store) error {
		if _, err := s.WorkIQMailBindable(ctx, unbound); err != nil {
			return err
		}
		_, err := s.WorkIQAccounts(ctx)
		return err
	}, nil)
}
