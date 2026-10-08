package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/outlookmail"
	"github.com/ourostack/m365crawl/internal/teamsdesktop"
)

func peopleKeys(rows []PersonRow) string {
	var out []string
	for _, r := range rows {
		out = append(out, r.ID+"|"+r.DisplayName+"|"+r.Email+"|"+strings.Join(r.Sources, "+"))
	}
	return strings.Join(out, "\n")
}

// peopleArchive holds two Teams people and a mailbox of three correspondents.
func peopleArchive(t *testing.T) *Store {
	t.Helper()
	ctx := context.Background()
	s := newStore(t)
	must(s.ApplyPeople(ctx, []teamsdesktop.Person{
		{TenantID: acctA.TenantID, ID: "8:orgid:1", DisplayName: "Ann Teams", SeenAt: mailT0.Add(-time.Hour)},
		{TenantID: acctA.TenantID, ID: "8:orgid:2", DisplayName: "Bob Teams", SeenAt: mailT0.Add(-10 * 24 * time.Hour)},
	}))
	// Ann sends twice (the newer copy carries no name), Bob receives, Cy is a recipient without an
	// address, and a gone message from Gus is left out.
	m1 := mailMsg(1, fInbox, "<1@x>", "One", 5)
	m2 := mailMsg(2, fInbox, "<2@x>", "Two", 0)
	m2.SenderName, m2.SenderAddress = "", "ANN@example.test"
	m2.Recipients = append(m2.Recipients, outlookmail.Recipient{Name: "Cy NoAddress"})
	m3 := mailMsg(3, fInbox, "<3@x>", "Three", 1)
	m3.SenderName, m3.SenderAddress = "Gus Gone", "gus@example.test"
	m3.Recipients = nil
	m4 := mailMsg(4, fInbox, "<4@x>", "Four", 20)
	m4.SenderName, m4.SenderAddress = "", "nameless@example.test"
	m4.Recipients = nil
	mustCommitMail(t, s, mailBatch(mailT0, m1, m2, m3, m4))
	if _, err := s.db.ExecContext(ctx, `update mail_messages set gone_at=? where detail_key=3`, fmtTime(mailT0)); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestPeopleListsMailCorrespondents(t *testing.T) {
	ctx := context.Background()
	s := peopleArchive(t)
	got, trunc := must2(s.People(ctx, "", Filter{}))
	// Newest seen first across both sources; a mail row's name is the newest one it was given.
	want := strings.Join([]string{
		"mail:ann@example.test|Ann Sender|ann@example.test|mail",
		"mail:bob@example.test|Bob Reader|bob@example.test|mail",
		"8:orgid:1|Ann Teams||chats",
		"8:orgid:2|Bob Teams||chats",
		"mail:nameless@example.test|nameless@example.test|nameless@example.test|mail",
	}, "\n")
	if trunc || peopleKeys(got) != want {
		t.Fatalf("got\n%s\nwant\n%s", peopleKeys(got), want)
	}
	if ann := got[0]; !ann.FirstSeenAt.Equal(mailT0.AddDate(0, 0, -5)) || !ann.LastSeenAt.Equal(mailT0) || ann.TenantID != "" {
		t.Fatalf("ann %+v", ann)
	}
	// Text matches a name or an address of either source; a mail id matches exactly.
	for q, want := range map[string]string{
		"ann":                        "mail:ann@example.test,8:orgid:1",
		"BOB@EXAMPLE":                "mail:bob@example.test",
		"mail:nameless@example.test": "mail:nameless@example.test",
		"8:orgid:2":                  "8:orgid:2",
		"gus":                        "",
	} {
		rows, _ := must2(s.People(ctx, q, Filter{}))
		var ids []string
		for _, r := range rows {
			ids = append(ids, r.ID)
		}
		if strings.Join(ids, ",") != want {
			t.Errorf("%q: %v", q, ids)
		}
	}
}

func TestPeopleLimitCountsBothSources(t *testing.T) {
	ctx := context.Background()
	s := peopleArchive(t)
	for _, c := range []struct {
		limit int
		ids   string
		total int
	}{
		{2, "mail:ann@example.test,mail:bob@example.test", 5}, // mail had more
		{4, "mail:ann@example.test,mail:bob@example.test,8:orgid:1,8:orgid:2", 5},
		{1, "mail:ann@example.test", 5}, // both had more
	} {
		var total int
		rows, trunc := must2(s.People(ctx, "", Filter{Limit: c.limit, Total: &total}))
		var ids []string
		for _, r := range rows {
			ids = append(ids, r.ID)
		}
		if !trunc || strings.Join(ids, ",") != c.ids || total != c.total {
			t.Errorf("limit %d: %v %v total %d", c.limit, ids, trunc, total)
		}
	}
	// Teams more than the limit, mail none.
	var total int
	if _, trunc := must2(s.People(ctx, "teams", Filter{Limit: 1, Total: &total})); !trunc || total != 2 {
		t.Errorf("teams only: %v %d", trunc, total)
	}
}

func TestPeopleWithoutMailTables(t *testing.T) {
	ctx := context.Background()
	s := peopleArchive(t)
	if _, err := s.db.ExecContext(ctx, `drop table mail_recipients`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.People(ctx, "", Filter{}); err == nil {
		t.Fatal("a broken mail archive read succeeded")
	}
	if _, err := s.db.ExecContext(ctx, `drop table mail_messages`); err != nil {
		t.Fatal(err)
	}
	got, _ := must2(s.People(ctx, "", Filter{}))
	if peopleKeys(got) != "8:orgid:1|Ann Teams||chats\n8:orgid:2|Bob Teams||chats" {
		t.Fatalf("%s", peopleKeys(got))
	}
}

// Two people seen at the same time with the same name sort by id.
func TestPeopleTieBreaksOnID(t *testing.T) {
	ctx := context.Background()
	s := peopleArchive(t)
	must(s.ApplyPeople(ctx, []teamsdesktop.Person{{TenantID: acctA.TenantID, ID: "8:orgid:9", DisplayName: "Ann Sender", SeenAt: mailT0}}))
	got, _ := must2(s.People(ctx, "ann sender", Filter{}))
	if len(got) != 2 || got[0].ID != "8:orgid:9" || got[1].ID != "mail:ann@example.test" {
		t.Fatalf("%s", peopleKeys(got))
	}
}

// A recipient row whose name cannot be read fails the read instead of printing a wrong row.
func TestPeopleReportsAnUnreadableMailRow(t *testing.T) {
	ctx := context.Background()
	s := peopleArchive(t)
	for _, q := range []string{
		`drop table mail_recipients`,
		`create table mail_recipients(message_rowid integer not null, ord integer not null, name text, address text, kind_raw integer not null default 0)`,
		`insert into mail_recipients(message_rowid, ord, name, address) select rowid, 0, null, 'null@example.test' from mail_messages where detail_key=1`,
	} {
		if _, err := s.db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := s.People(ctx, "null@", Filter{}); err == nil {
		t.Fatal("a NULL name was read")
	}
}

// "mail:<address>" names one address exactly, even when it sits inside another.
func TestPeopleMailIDMatchesExactly(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	a := mailMsg(1, fInbox, "<1@x>", "One", 1)
	a.SenderName, a.SenderAddress, a.Recipients = "Ann", "ann@example.test", nil
	b := mailMsg(2, fInbox, "<2@x>", "Two", 2)
	b.SenderName, b.SenderAddress, b.Recipients = "Joann", "joann@example.test", nil
	mustCommitMail(t, s, mailBatch(mailT0, a, b))
	for q, want := range map[string]string{
		"mail:ann@example.test": "mail:ann@example.test",
		"MAIL:Ann@Example.test": "mail:ann@example.test",
		"mail:ann@":             "",
		"ann@":                  "mail:ann@example.test,mail:joann@example.test",
	} {
		rows, _ := must2(s.People(ctx, q, Filter{}))
		var ids []string
		for _, r := range rows {
			ids = append(ids, r.ID)
		}
		if strings.Join(ids, ",") != want {
			t.Errorf("%q: %v", q, ids)
		}
	}
}
