package store

import (
	"context"
	"testing"
)

func TestMailFailuresIncludesUnreadAccountsAndScopesThem(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	for _, account := range []string{"outlook/A", "outlook/B"} {
		if err := s.SetMailFailure(ctx, account, OutlookFailure{Code: "fixture_failure", Message: "Fixture failure"}); err != nil {
			t.Fatal(err)
		}
	}
	all, err := s.MailFailures(ctx, "")
	if err != nil || len(all) != 2 || all[0].Account != "outlook/A" || all[1].Account != "outlook/B" {
		t.Fatalf("failures without mail rows: %+v %v", all, err)
	}
	one, err := s.MailFailures(ctx, "outlook/B")
	if err != nil || len(one) != 1 || one[0].Code != "fixture_failure" {
		t.Fatalf("scoped failure: %+v %v", one, err)
	}
	if err := s.SetMailRead(ctx, "outlook/B", mailT0); err != nil {
		t.Fatal(err)
	}
	one, err = s.MailFailures(ctx, "outlook/B")
	if err != nil || len(one) != 0 {
		t.Fatalf("successful read did not clear the warning: %+v %v", one, err)
	}
}

func TestMailFailuresPropagatesReadErrors(t *testing.T) {
	for _, setup := range []string{
		`insert into meta(key,value) values('outlook_mail_failure:outlook/A','invalid')`,
		`drop table meta; create table meta(key text,value text); insert into meta values('outlook_mail_failure:outlook/A',null)`,
		`drop table meta; create table meta(x)`,
	} {
		s := newStore(t)
		if _, err := s.db.Exec(setup); err != nil {
			t.Fatal(err)
		}
		if _, err := s.MailFailures(context.Background(), ""); err == nil {
			t.Fatalf("failed state read became an empty success: %s", setup)
		}
	}
	s := newStore(t)
	_ = s.Close()
	if _, err := s.MailFailures(context.Background(), ""); err == nil {
		t.Fatal("closed database became an empty success")
	}
}

func TestMailFailuresWithoutLegacyMetaIsEmpty(t *testing.T) {
	s := newStore(t)
	if _, err := s.db.Exec("drop table meta"); err != nil {
		t.Fatal(err)
	}
	failures, err := s.MailFailures(context.Background(), "")
	if err != nil || len(failures) != 0 {
		t.Fatalf("legacy archive gained an error: %+v %v", failures, err)
	}
	if rowCount(t, s, "select count(*) from sqlite_master where name='meta'") != 0 {
		t.Fatal("read created failure storage")
	}
}
