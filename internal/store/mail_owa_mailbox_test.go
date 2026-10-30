package store

import (
	"context"
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
	second := mailAcct + "/mailbox-0123456789abcdef"
	if got, err := s.OWAMailboxBindings(ctx, mailAcct); err != nil || len(got) != 0 {
		t.Fatalf("empty bindings = %v, %v", got, err)
	}
	mustCommitOWA(t, s, owaMailboxBatch(mailAcct, "identity-a", "a", mailT0))
	mustCommitOWA(t, s, owaMailboxBatch(second, "identity-b", "b", mailT0))
	// Another profile whose name extends this one is not in its scope.
	other := mailAcct + "0"
	ob := owaMailboxBatch(other, "identity-a", "a", mailT0)
	ob.Scope = other
	mustCommitOWA(t, s, ob)
	got, err := s.OWAMailboxBindings(ctx, mailAcct)
	if err != nil || !reflect.DeepEqual(got, map[string]string{mailAcct: "identity-a", second: "identity-b"}) {
		t.Fatalf("bindings = %v, %v", got, err)
	}
	if n := countRows(t, s, `select count(*) from mail_messages where account=?`, second); n != 1 {
		t.Fatalf("second mailbox rows = %d", n)
	}
	// A different identity cannot take over a bound account, nor can one identity hold two accounts.
	for name, b := range map[string]OWAMailBatch{
		"changed":   owaMailboxBatch(second, "identity-c", "b", mailT0.Add(time.Hour)),
		"duplicate": owaMailboxBatch(mailAcct+"/mailbox-fedcba9876543210", "identity-a", "c", mailT0.Add(time.Hour)),
	} {
		if _, err := s.CommitOWAMail(ctx, b); err == nil {
			t.Fatalf("%s binding accepted", name)
		}
		if n := countRows(t, s, `select count(*) from mail_messages where account=?`, b.Account); name == "duplicate" && n != 0 {
			t.Fatal("a refused binding committed mail")
		}
	}
	if got, _ := s.OWAMailboxBindings(ctx, mailAcct); got[second] != "identity-b" || len(got) != 2 {
		t.Fatalf("refused bindings changed state: %v", got)
	}
	// An overlap-proven rebind records the account's new identity.
	rb := owaMailboxBatch(second, "identity-c", "b", mailT0.Add(2*time.Hour))
	rb.Rebind = true
	mustCommitOWA(t, s, rb)
	if got, _ := s.OWAMailboxBindings(ctx, mailAcct); got[second] != "identity-c" {
		t.Fatalf("rebind = %v", got)
	}
	// A legacy read without an identity neither binds nor unbinds.
	plain := owaMailboxBatch(mailAcct, "", "a", mailT0.Add(3*time.Hour))
	mustCommitOWA(t, s, plain)
	if got, _ := s.OWAMailboxBindings(ctx, mailAcct); got[mailAcct] != "identity-a" {
		t.Fatalf("plain read changed binding: %v", got)
	}
}

func TestOWAMailboxSetAndSourceOverlap(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	if got, err := s.OWAMailboxes(ctx, mailAcct); err != nil || got != nil {
		t.Fatalf("absent set = %v, %v", got, err)
	}
	set := []string{mailAcct + "/mailbox-b", mailAcct}
	if err := s.SetOWAMailboxes(ctx, mailAcct, set); err != nil {
		t.Fatal(err)
	}
	if got, err := s.OWAMailboxes(ctx, mailAcct); err != nil || !reflect.DeepEqual(got, []string{mailAcct, mailAcct + "/mailbox-b"}) {
		t.Fatalf("set = %v, %v", got, err)
	}
	mustCommitOWA(t, s, owaMailboxBatch(mailAcct, "", "a", mailT0))
	n, err := s.OWAMailSourceOverlap(ctx, mailAcct, []string{"a/inbox", "b/inbox", "a/root"}, []string{"a/message", "b/message"})
	if err != nil || n != 3 {
		t.Fatalf("overlap = %d, %v", n, err)
	}
	if n, err := s.OWAMailSourceOverlap(ctx, mailAcct+"/mailbox-b", []string{"a/inbox"}, []string{"a/message"}); err != nil || n != 0 {
		t.Fatalf("overlap crossed accounts = %d, %v", n, err)
	}
}

func TestOWAProfileMailStateCombinesMailboxes(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	second := mailAcct + "/mailbox-b"
	if st, n, err := s.OWAProfileMailState(ctx, mailAcct); err != nil || n != 1 || st.Read {
		t.Fatalf("unread single = %+v %d %v", st, n, err)
	}
	if err := s.SetMailRead(ctx, mailAcct, mailT0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if st, n, _ := s.OWAProfileMailState(ctx, mailAcct); !st.Read || n != 1 || !st.ReadAt.Equal(mailT0.Add(time.Hour)) {
		t.Fatalf("read single = %+v", st)
	}
	if err := s.SetOWAMailboxes(ctx, mailAcct, []string{mailAcct, second}); err != nil {
		t.Fatal(err)
	}
	if st, n, _ := s.OWAProfileMailState(ctx, mailAcct); st.Read || n != 2 {
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
