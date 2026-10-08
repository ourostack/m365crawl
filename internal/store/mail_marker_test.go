package store

import (
	"context"
	"testing"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), writableArchivePath(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestMailReadAccounts(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	got, err := s.MailReadAccounts(ctx)
	if err != nil || len(got) != 0 {
		t.Fatalf("empty = %v, %v", got, err)
	}
	for _, a := range []string{"outlook/B", "outlook/A"} {
		if _, err := s.db.Exec(`insert into meta(key, value) values(?, 'x')`, mailMarkerPrefix+a); err != nil {
			t.Fatal(err)
		}
	}
	got, err = s.MailReadAccounts(ctx)
	if err != nil || len(got) != 2 || got[0] != "outlook/A" || got[1] != "outlook/B" {
		t.Fatalf("accounts = %v, %v", got, err)
	}
	_ = s.Close()
	if _, err := s.MailReadAccounts(ctx); err == nil {
		t.Fatal("closed store must fail")
	}
	s2 := openTest(t)
	if _, err := s2.db.Exec(`drop table meta`); err != nil {
		t.Fatal(err)
	}
	if _, err := s2.MailReadAccounts(ctx); err == nil {
		t.Fatal("missing table must fail")
	}
}
