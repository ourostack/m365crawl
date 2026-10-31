package store

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func owaMailboxBatch(account, identity, prefix string, at time.Time) OWAMailBatch {
	m := owaMessage(prefix+"/message", prefix+"/inbox", "<"+prefix+"@example.test>", 1)
	m.Value.Account = account
	b := owaBatch(at, m)
	b.Result.RootID = prefix + "/root"
	b.Result.Folders[0].ID, b.Result.Folders[0].ParentID = prefix+"/inbox", prefix+"/root"
	b.Result.Folders[1].ID, b.Result.Folders[1].ParentID = prefix+"/sent", prefix+"/root"
	b.Account, b.Identity, b.Scope = account, identity, mailAcct
	return b
}

func TestOWAMailboxBindingsAreTransactionalAndScoped(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	second := mailAcct + "/mailbox-2"
	if got := bindingsOf(t, s); len(got) != 0 {
		t.Fatalf("empty bindings = %v", got)
	}
	mustCommitOWA(t, s, owaMailboxBatch(mailAcct, "identity-a", "a", mailT0))
	mustCommitOWA(t, s, owaMailboxBatch(second, "identity-b", "b", mailT0))
	// Another profile whose name extends this one is not in its scope.
	other := mailAcct + "0"
	ob := owaMailboxBatch(other, "identity-a", "a", mailT0)
	ob.Scope = other
	mustCommitOWA(t, s, ob)
	if got := bindingsOf(t, s); !reflect.DeepEqual(got, map[string]string{mailAcct: "identity-a", second: "identity-b"}) {
		t.Fatalf("bindings = %v", got)
	}
	if n := countRows(t, s, `select count(*) from mail_messages where account=?`, second); n != 1 {
		t.Fatalf("second mailbox rows = %d", n)
	}
	// A different identity cannot take over a bound account, nor can one identity hold two
	// accounts, nor can an account outside the profile's scope be bound.
	outside := owaMailboxBatch("outlook/elsewhere", "identity-d", "d", mailT0.Add(time.Hour))
	for name, b := range map[string]OWAMailBatch{
		"changed":   owaMailboxBatch(second, "identity-c", "b", mailT0.Add(time.Hour)),
		"duplicate": owaMailboxBatch(mailAcct+"/mailbox-3", "identity-a", "c", mailT0.Add(time.Hour)),
		"outside":   outside,
	} {
		if _, err := s.CommitOWAMail(ctx, b); !errors.Is(err, ErrOWAMailboxBinding) {
			t.Fatalf("%s binding accepted: %v", name, err)
		}
		if n := countRows(t, s, `select count(*) from mail_messages where account=?`, b.Account); name != "changed" && n != 0 {
			t.Fatal("a refused binding committed mail")
		}
	}
	if got := bindingsOf(t, s); got[second] != "identity-b" || len(got) != 2 {
		t.Fatalf("refused bindings changed state: %v", got)
	}
	// An overlap-proven rebind records the account's new identity.
	rb := owaMailboxBatch(second, "identity-c", "b", mailT0.Add(2*time.Hour))
	rb.Rebind = true
	mustCommitOWA(t, s, rb)
	if got := bindingsOf(t, s); got[second] != "identity-c" {
		t.Fatalf("rebind = %v", got)
	}
	// A legacy read without an identity neither binds nor unbinds.
	plain := owaMailboxBatch(mailAcct, "", "a", mailT0.Add(3*time.Hour))
	mustCommitOWA(t, s, plain)
	if got := bindingsOf(t, s); got[mailAcct] != "identity-a" {
		t.Fatalf("plain read changed binding: %v", got)
	}
}

// bindingsOf reports each binding by the plain identity it seals, or "?" when none matches.
func bindingsOf(t *testing.T, s *Store) map[string]string {
	t.Helper()
	v, err := s.OWAMailboxView(context.Background(), mailAcct)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for account, sealed := range v.Bindings {
		out[account] = "?"
		for _, plain := range []string{"identity-a", "identity-b", "identity-c"} {
			if v.Sealed(plain) == sealed {
				out[account] = plain
			}
		}
	}
	return out
}

func TestOWAMailboxIdentitiesAreSealedPerArchive(t *testing.T) {
	ctx := context.Background()
	var sealed []string
	for range 2 {
		s := newStore(t)
		if v, err := s.OWAMailboxView(ctx, mailAcct); err != nil || v.Sealed("identity-a") != "" {
			t.Fatalf("an archive without bindings sealed an identity: %v", err)
		}
		mustCommitOWA(t, s, owaMailboxBatch(mailAcct, "identity-a", "a", mailT0))
		v, err := s.OWAMailboxView(ctx, mailAcct)
		if err != nil || v.Bindings[mailAcct] != v.Sealed("identity-a") || len(v.Bindings[mailAcct]) != 64 {
			t.Fatalf("sealed binding = %v, %v", v.Bindings, err)
		}
		if countRows(t, s, `select count(*) from meta where value like '%identity-a%'`) != 0 {
			t.Fatal("a plain identity was stored")
		}
		sealed = append(sealed, v.Bindings[mailAcct])
	}
	if sealed[0] == sealed[1] {
		t.Fatal("two archives sealed one identity alike, so a stored binding confirms a guess")
	}
}

func TestOWAMailboxSetAndView(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	if got, err := s.OWAMailboxes(ctx, mailAcct); err != nil || got != nil {
		t.Fatalf("absent set = %v, %v", got, err)
	}
	set := []string{mailAcct + "/mailbox-2", mailAcct}
	if err := s.SetOWAMailboxes(ctx, mailAcct, set); err != nil {
		t.Fatal(err)
	}
	if got, err := s.OWAMailboxes(ctx, mailAcct); err != nil || !reflect.DeepEqual(got, []string{mailAcct, mailAcct + "/mailbox-2"}) {
		t.Fatalf("set = %v, %v", got, err)
	}
	mustCommitOWA(t, s, owaMailboxBatch(mailAcct, "identity-a", "a", mailT0))
	other := owaMailboxBatch(mailAcct+"0", "identity-a", "z", mailT0)
	other.Scope = mailAcct + "0"
	mustCommitOWA(t, s, other)
	v, err := s.OWAMailboxView(ctx, mailAcct)
	if err != nil || !reflect.DeepEqual(v.Bindings, map[string]string{mailAcct: v.Sealed("identity-a")}) || !reflect.DeepEqual(v.Holds, map[string]bool{mailAcct: true}) {
		t.Fatalf("view = %+v, %v", v, err)
	}
	if len(v.Native) != 1 || !v.Native[mailAcct][NativeKey("folder", "a/inbox")] || !v.Native[mailAcct][NativeKey("message", "a/message")] || v.Native[mailAcct][NativeKey("folder", "z/inbox")] {
		t.Fatalf("native = %v", v.Native)
	}
	if _, err := s.db.Exec(`update meta set value='{' where key=?`, owaMailboxesKey+mailAcct); err != nil {
		t.Fatal(err)
	}
	if _, err := s.OWAMailboxes(ctx, mailAcct); err == nil {
		t.Fatal("a damaged mailbox set was accepted")
	}
	if _, _, err := s.OWAProfileMailState(ctx, mailAcct); err == nil {
		t.Fatal("a damaged mailbox set hid the profile state")
	}
}

func TestOWAMailboxReadsAndBindingFaults(t *testing.T) {
	setup := func(t *testing.T, s *Store) {
		mustCommitOWA(t, s, owaMailboxBatch(mailAcct, "identity-a", "a", mailT0))
		if err := s.SetOWAMailboxes(context.Background(), mailAcct, []string{mailAcct}); err != nil {
			t.Fatal(err)
		}
		if err := s.SetMailRead(context.Background(), mailAcct, mailT0); err != nil {
			t.Fatal(err)
		}
	}
	sweepReadFaults(t, setup, func(ctx context.Context, s *Store) error {
		_, err := s.OWAMailboxView(ctx, mailAcct)
		return err
	})
	sweepReadFaults(t, setup, func(ctx context.Context, s *Store) error {
		_, _, err := s.OWAProfileMailState(ctx, mailAcct)
		return err
	}, 2) // the mail state's own columns are nullable
	bindSecond := func(ctx context.Context, s *Store) error {
		_, err := s.CommitOWAMail(ctx, owaMailboxBatch(mailAcct+"/mailbox-2", "identity-b", "b", mailT0.Add(time.Hour)))
		return err
	}
	sweepFaults(t, setup, bindSecond)
	// The first binding also creates the archive's sealing key.
	sweepFaults(t, nil, bindSecond)
}

func TestOWAProfileMailStateCombinesMailboxes(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	second := mailAcct + "/mailbox-2"
	if st, n, err := s.OWAProfileMailState(ctx, mailAcct); err != nil || len(n) != 1 || st.Read {
		t.Fatalf("unread single = %+v %v %v", st, n, err)
	}
	if err := s.SetMailRead(ctx, mailAcct, mailT0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if st, n, _ := s.OWAProfileMailState(ctx, mailAcct); !st.Read || len(n) != 1 || !st.ReadAt.Equal(mailT0.Add(time.Hour)) {
		t.Fatalf("read single = %+v", st)
	}
	if err := s.SetOWAMailboxes(ctx, mailAcct, []string{mailAcct, second}); err != nil {
		t.Fatal(err)
	}
	if st, n, _ := s.OWAProfileMailState(ctx, mailAcct); st.Read || len(n) != 2 {
		t.Fatalf("one unread mailbox reported read: %+v", st)
	}
	if err := s.SetMailRead(ctx, second, mailT0); err != nil {
		t.Fatal(err)
	}
	if st, _, _ := s.OWAProfileMailState(ctx, mailAcct); !st.Read || !st.ReadAt.Equal(mailT0) {
		t.Fatalf("oldest read = %+v", st)
	}
	if err := s.SetMailFailure(ctx, second, OutlookFailure{Code: "outlook_mail_layout_unsupported"}); err != nil {
		t.Fatal(err)
	}
	if st, _, _ := s.OWAProfileMailState(ctx, mailAcct); st.Read || st.Failure == nil || st.Failure.Code != "outlook_mail_layout_unsupported" {
		t.Fatalf("failed mailbox hidden: %+v", st)
	}
}
