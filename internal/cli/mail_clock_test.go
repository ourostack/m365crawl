package cli

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/outlookmail"
)

func clockMsg(dk, folder uint32, msgID, received, sent string) outlookmail.Message {
	m := mailMsg(dk, folder, msgID, "", "Clock "+msgID, 0, false)
	r, err := time.Parse(time.RFC3339, received)
	if err != nil {
		panic(err)
	}
	s, err := time.Parse(time.RFC3339, sent)
	if err != nil {
		panic(err)
	}
	m.Received, m.Sent = r, s
	return m
}

// Sent Items is dated by when the message was sent, in the local zone, and every other folder by
// when it was received, Drafts included: a message sent late on Aug 31 Pacific whose stored
// received time is Sep 1 lists on Aug 31, and a message sent to oneself just before midnight on
// the day daylight saving ends lists in Sent Items on that day and in Inbox on the next.
func TestMailListDatesSentItemsBySentTimeInTheLocalZone(t *testing.T) {
	la, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}
	setZone(t, la)
	e := textEnv(t)
	seedMail(t, e, []outlookmail.Message{
		clockMsg(1, fSent, "<late@x.test>", "2026-09-01T17:19:31Z", "2026-08-31T23:05:56Z"),
		clockMsg(2, fInbox, "<in@x.test>", "2026-09-01T10:00:00Z", "2026-08-31T22:00:00Z"),
		clockMsg(3, fDrafts, "<draft@x.test>", "2026-09-01T07:00:30Z", "2026-09-01T06:59:30Z"),
		// 23:30 PST on Nov 1, a 25-hour day; the Inbox copy arrives at 00:05 on Nov 2.
		clockMsg(4, fSent, "<self@x.test>", "2026-11-02T07:30:00Z", "2026-11-02T07:30:00Z"),
		clockMsg(5, fInbox, "<self@x.test>", "2026-11-02T08:05:00Z", "2026-11-02T07:30:00Z"),
		// 00:30 PDT on Nov 1, before the clocks go back.
		clockMsg(6, fSent, "<early@x.test>", "2026-10-31T07:00:00Z", "2026-11-01T07:30:00Z"),
	})
	day := func(d string, extra ...string) string {
		return strings.Join(ids(items(t, mailJSON(t, e, append([]string{"mail", "list", "--since", d, "--until", d}, extra...)...))), " ")
	}
	for d, want := range map[string]string{
		"2026-08-31": "outlook/Main:1",
		"2026-09-01": "outlook/Main:2 outlook/Main:3",
		"2026-10-31": "",
		"2026-11-01": "outlook/Main:4 outlook/Main:6",
		"2026-11-02": "outlook/Main:5",
	} {
		if got := day(d); got != want {
			t.Errorf("%s: %q, want %q", d, got, want)
		}
	}
	if got := day("2026-09-01", "--folder", "sent"); got != "" {
		t.Errorf("nothing was sent on Sep 1: %q", got)
	}
	it := items(t, mailJSON(t, e, "mail", "list", "--folder", "sent", "--since", "2026-08-31", "--until", "2026-08-31"))[0]
	if it["dated_at"] != "2026-08-31T23:05:56Z" || it["sent_at"] != "2026-08-31T23:05:56Z" || it["received_at"] != "2026-09-01T17:19:31Z" || it["received_at_disagrees"] != false {
		t.Fatalf("%v", it)
	}
	code, out, _ := e.mail("--format", "text", "--no-color", "mail", "list", "--folder", "sent", "--since", "2026-08-31", "--until", "2026-08-31")
	if code != 0 || !strings.Contains(out, "date") || strings.Contains(out, "received") || !strings.Contains(out, "2026-08-31 23:05") {
		t.Fatalf("the text table shows the date the message is listed by: %d %s", code, out)
	}
}

// The folder-copy check names the clock it selected messages by, and its notice says it.
func TestMailListFolderCopiesNameTheClock(t *testing.T) {
	e := copyEnv(t)
	m := mailJSON(t, e, append([]string{"mail", "list", "--folder", "sent"}, dayFlags(3)...)...)
	c := m["folder_copies"].([]any)[0].(map[string]any)
	notices, _ := m["notices"].([]any)
	if c["dated_by"] != "sent_at" || len(notices) != 1 || !strings.Contains(notices[0].(string), "(messages sent in it)") {
		t.Fatalf("%v %v", c, notices)
	}
	m = mailJSON(t, e, append([]string{"mail", "list", "--folder", "inbox"}, dayFlags(25)...)...)
	if c := m["folder_copies"].([]any)[0].(map[string]any); c["dated_by"] != "received_at" {
		t.Fatalf("%v", c)
	}
}

// mail show says when copies of the message disagree on received time, and the date it is listed by.
func TestMailShowSaysWhenCopiesDisagree(t *testing.T) {
	e := copyEnv(t)
	db, err := sql.Open("sqlite", e.db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`update mail_messages set received_at='2026-08-20T08:00:00.000Z' where detail_key=2001`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	code, out, _ := e.mail("--format", "text", "--no-color", "mail", "show", "outlook/Main:2001")
	if code != 0 || !strings.Contains(out, "2026-08-20 08:00 UTC (copies disagree; dated 2026-09-07 07:59 UTC)") {
		t.Fatalf("%d %s", code, out)
	}
	if m := mailJSON(t, e, "mail", "show", "outlook/Main:2001"); m["received_at_disagrees"] != true || m["dated_at"] != "2026-09-07T07:59:00Z" {
		t.Fatalf("%v", m)
	}
}
