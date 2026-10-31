package cli

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/outlookmail"
	"github.com/ourostack/m365crawl/internal/store"
)

// The coverage fixture's clock: two hours after mailT0, so mailT0's day (2026-09-10, UTC) is today.
var covNow = mailT0.Add(2 * time.Hour)

const (
	covTenant, covUser = "t1", "u1"
	covTeams           = covTenant + "/" + covUser
	covSelf            = "8:orgid:" + covUser
	covTeamsSource     = "WV2Profile_tfw|https_teams.microsoft.com_0"
)

func fixClock(t *testing.T, at time.Time) {
	t.Helper()
	old := runtimeClock
	runtimeClock = func() time.Time { return at }
	t.Cleanup(func() { runtimeClock = old })
}

func covDay(daysAgo int) string { return mailT0.AddDate(0, 0, -daysAgo).Format("2006-01-02") }

// covSent is a Sent Items message sent daysAgo, whose draft was started draftDays earlier.
func covSent(dk uint32, msgID string, daysAgo, draftDays int) outlookmail.Message {
	m := mailMsg(dk, fSent, msgID, "", "sent "+msgID, daysAgo, false)
	m.Sent = m.Received
	m.Received = m.Sent.AddDate(0, 0, -draftDays)
	return m
}

// commitCovMail commits one complete read of the account at read (store written then).
func commitCovMail(t *testing.T, st *store.Store, read time.Time, msgs ...outlookmail.Message) {
	t.Helper()
	cov := map[uint32]*outlookmail.Coverage{}
	for _, m := range msgs {
		c := cov[m.FolderKey]
		if c == nil {
			c = &outlookmail.Coverage{FolderKey: m.FolderKey, Oldest: m.Received, Newest: m.Received}
			cov[m.FolderKey] = c
		}
		c.Count++
		c.Oldest, c.Newest = minTime(c.Oldest, m.Received), maxTime(c.Newest, m.Received)
	}
	res := outlookmail.Result{Messages: msgs, Folders: mailFolderList()}
	for _, f := range mailFolderList() {
		if c := cov[f.Key]; c != nil {
			res.Coverage = append(res.Coverage, *c)
		}
	}
	if _, err := st.CommitMail(context.Background(), store.MailBatch{Account: mailAccount, ReadAt: read, FreshAt: read, Result: res, Trusted: true}); err != nil {
		t.Fatal(err)
	}
}

func minTime(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}

// covMailMessages: Inbox from 20 days ago to yesterday, Sent Items with a message sent 10 days ago
// whose draft started a day before, and one sent 3 days ago; a Projects message from long ago,
// which must not extend any range.
func covMailMessages() []outlookmail.Message {
	return []outlookmail.Message{
		mailMsg(1, fInbox, "<i20@x.test>", "", "i20", 20, false),
		mailMsg(2, fInbox, "<i5a@x.test>", "", "i5a", 5, false),
		mailMsg(3, fInbox, "<i5b@x.test>", "", "i5b", 5, false),
		mailMsg(4, fInbox, "<i1@x.test>", "", "i1", 1, false),
		covSent(5, "<s10@x.test>", 10, 1),
		covSent(6, "<s3@x.test>", 3, 0),
		mailMsg(7, fProjects, "<p@x.test>", "", "p", 400, false),
	}
}

// covTeamsSeed adds a Teams account read every three days from 15 days ago until an hour after
// mailT0, a 1:1 chat with two messages 5 days ago (one the account's) and a call event, and a
// meeting chat whose sequence numbers skip two numbers 2 days ago.
func covTeamsSeed(t *testing.T, db string) {
	t.Helper()
	conn, err := sql.Open("sqlite", db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	ts := func(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }
	day := func(n int) time.Time { return mailT0.AddDate(0, 0, -n) }
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := conn.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`insert into accounts(tenant_id,user_id,first_seen_at,last_synced_at) values(?,?,?,?)`, covTenant, covUser, ts(day(15)), ts(mailT0.Add(time.Hour)))
	for n := 15; n >= 0; n -= 3 {
		exec(`insert into sync_runs(started_at,finished_at,source,status,source_accounts_json) values(?,?,?,?,?)`, ts(day(n).Add(time.Hour)), ts(day(n).Add(time.Hour)), covTeamsSource, "ok", `["`+covTeams+`"]`)
	}
	msg := func(conv, id, sender, typ string, at time.Time, seq int) {
		exec(`insert into messages(tenant_id,user_id,conversation_id,id,sender_id,message_type,sent_at,raw_json,updated_at) values(?,?,?,?,?,?,?,?,?)`,
			covTenant, covUser, conv, id, sender, typ, ts(at), fmt.Sprintf(`{"sequenceId":%d}`, seq), ts(mailT0))
	}
	exec(`insert into conversations(tenant_id,user_id,id,kind,last_message_at,updated_at) values(?,?,?,?,?,?)`, covTenant, covUser, "19:a@unq.gbl.spaces", "Chat", ts(day(5).Add(3*time.Hour)), ts(mailT0))
	msg("19:a@unq.gbl.spaces", "a1", covSelf, "RichText/Html", day(5).Add(time.Hour), 1)
	msg("19:a@unq.gbl.spaces", "a2", "8:orgid:other", "Text", day(5).Add(2*time.Hour), 2)
	msg("19:a@unq.gbl.spaces", "a3", covSelf, "Event/Call", day(5).Add(3*time.Hour), 3)
	exec(`insert into conversations(tenant_id,user_id,id,kind,last_message_at,updated_at) values(?,?,?,?,?,?)`, covTenant, covUser, "19:meeting_m@thread.v2", "Meeting", ts(day(2).Add(5*time.Hour)), ts(mailT0))
	msg("19:meeting_m@thread.v2", "m1", covSelf, "Text", day(2).Add(time.Hour), 10)
	msg("19:meeting_m@thread.v2", "m4", "8:orgid:other", "Text", day(2).Add(5*time.Hour), 13)
}

func covEnv(t *testing.T) (*env, string) {
	t.Helper()
	fixClock(t, covNow)
	e := textEnv(t)
	e.emptyArchive()
	st, err := store.Open(context.Background(), e.db)
	if err != nil {
		t.Fatal(err)
	}
	commitCovMail(t, st, mailT0.Add(time.Hour), covMailMessages()...)
	_ = st.Close()
	covTeamsSeed(t, e.db)
	return e, t.TempDir()
}

// covRun runs coverage with the Outlook source on (an empty Outlook root) and decodes it.
func covRun(t *testing.T, e *env, outlook string, args ...string) map[string]any {
	t.Helper()
	code, out, errOut := e.run(append([]string{"--no-sync", "--json", "--outlook-root", outlook, "coverage", "--tz", "UTC"}, args...)...)
	if code != 0 {
		t.Fatalf("%v: exit %d: %s", args, code, errOut)
	}
	return decode(t, out)
}

// entryOf finds the entry of a source and scope on a day of a coverage result.
func entryOf(t *testing.T, m map[string]any, day, source, scope string) map[string]any {
	t.Helper()
	for _, d := range m["days"].([]any) {
		dm := d.(map[string]any)
		if dm["day"] != day {
			continue
		}
		for _, x := range dm["entries"].([]any) {
			e := x.(map[string]any)
			if e["source"] == source && (scope == "" && e["scope"] == nil || e["scope"] == scope) {
				return e
			}
		}
	}
	t.Fatalf("no %s %s entry on %s in %v", source, scope, day, m)
	return nil
}

func reasonCodes(e map[string]any) string {
	var codes []string
	for _, r := range e["reasons"].([]any) {
		codes = append(codes, r.(map[string]any)["code"].(string))
	}
	return strings.Join(codes, ",")
}

func wantEntry(t *testing.T, e map[string]any, state string, items int, codes string) {
	t.Helper()
	if e["state"] != state || e["items"] != float64(items) || reasonCodes(e) != codes {
		t.Fatalf("want %s, %d items, reasons %q; got %v", state, items, codes, e)
	}
}

// A quiet day inside a complete read's range is covered with zero items; a day before the cache
// is not covered and says why; another folder's old mail does not extend Sent or Inbox.
func TestCoverageTellsAQuietDayFromAnUncoveredOne(t *testing.T) {
	e, outlook := covEnv(t)
	m := covRun(t, e, outlook, "--day", covDay(4), "--source", "mail")
	sent := entryOf(t, m, covDay(4), "mail", "sent")
	wantEntry(t, sent, "covered", 0, "")
	if sent["basis"] != "desktop_cache" || sent["marked_items"] != float64(0) || sent["account"] != mailAccount || sent["fix"] != nil {
		t.Fatalf("sent %v", sent)
	}
	wantEntry(t, entryOf(t, m, covDay(4), "mail", "inbox"), "covered", 0, "")

	m = covRun(t, e, outlook, "--day", covDay(5), "--source", "mail:inbox")
	wantEntry(t, entryOf(t, m, covDay(5), "mail", "inbox"), "covered", 2, "")

	m = covRun(t, e, outlook, "--day", covDay(100), "--source", "mail")
	for _, role := range []string{"sent", "inbox"} {
		x := entryOf(t, m, covDay(100), "mail", role)
		wantEntry(t, x, "not_covered", 0, "before_cache")
		if x["covered_from"] != nil || !strings.Contains(x["fix"].(string), "WorkIQ") {
			t.Fatalf("%s %v", role, x)
		}
	}
}

// Sent Items start later than their received-time range by the longest draft-to-send delay: the
// day the oldest Sent message's draft started is not covered for Sent, although Inbox is.
func TestCoverageSentStartsAfterTheLongestDraft(t *testing.T) {
	e, outlook := covEnv(t)
	m := covRun(t, e, outlook, "--day", covDay(11), "--source", "mail")
	sent := entryOf(t, m, covDay(11), "mail", "sent")
	wantEntry(t, sent, "not_covered", 0, "before_cache")
	if !strings.Contains(sent["reasons"].([]any)[0].(map[string]any)["detail"].(string), "drafted up to 24h0m0s before it was sent") {
		t.Fatalf("sent %v", sent)
	}
	wantEntry(t, entryOf(t, m, covDay(11), "mail", "inbox"), "covered", 0, "")
	m = covRun(t, e, outlook, "--day", covDay(10), "--source", "mail:sent")
	x := entryOf(t, m, covDay(10), "mail", "sent")
	wantEntry(t, x, "partial", 1, "before_cache")
	if x["covered_from"] != mailT0.AddDate(0, 0, -10).Format(time.RFC3339) {
		t.Fatalf("sent %v", x)
	}
}

// The covered range ends at the newest message, not at the store's write time; today is never
// covered; a future day is not covered.
func TestCoverageEndsAtTheNewestMessageAndTodayIsInProgress(t *testing.T) {
	e, outlook := covEnv(t)
	m := covRun(t, e, outlook, "--since", covDay(1), "--until", "2026-09-12", "--source", "mail:inbox")
	y := entryOf(t, m, covDay(1), "mail", "inbox")
	wantEntry(t, y, "partial", 1, "after_newest_mail")
	if y["covered_until"] != mailT0.AddDate(0, 0, -1).Format(time.RFC3339) {
		t.Fatalf("yesterday %v", y)
	}
	wantEntry(t, entryOf(t, m, covDay(0), "mail", "inbox"), "not_covered", 0, "in_progress,after_last_read")
	wantEntry(t, entryOf(t, m, "2026-09-12", "mail", "inbox"), "not_covered", 0, "future")
	sum := m["summary"].([]any)
	if len(sum) != 1 || sum[0].(map[string]any)["state"] != "not_covered" || sum[0].(map[string]any)["items"] != float64(1) {
		t.Fatalf("summary %v", sum)
	}
	if r := m["range"].(map[string]any); r["zone"] != "UTC" || r["from"] != covDay(1)+"T00:00:00Z" || r["to"] != "2026-09-13T00:00:00Z" {
		t.Fatalf("range %v", r)
	}
}

// Chats are covered from the archive's first to its last successful Teams read; the account's own
// messages are counted apart, system events not at all; a meeting chat with skipped sequence
// numbers is partial; a day long before the first read is not covered.
func TestCoverageOfChatsAndMeetings(t *testing.T) {
	e, outlook := covEnv(t)
	m := covRun(t, e, outlook, "--day", covDay(5), "--source", "chats,meetings,channels")
	c := entryOf(t, m, covDay(5), "chats", "chats")
	wantEntry(t, c, "covered", 2, "")
	if c["my_items"] != float64(1) || c["deleted_items"] != float64(0) || c["account"] != covTeams || c["basis"] != "desktop_cache" {
		t.Fatalf("chats %v", c)
	}
	wantEntry(t, entryOf(t, m, covDay(5), "chats", "meetings"), "covered", 0, "")
	wantEntry(t, entryOf(t, m, covDay(5), "chats", "channels"), "covered", 0, "")

	m = covRun(t, e, outlook, "--day", covDay(2), "--source", "meetings")
	x := entryOf(t, m, covDay(2), "chats", "meetings")
	wantEntry(t, x, "partial", 2, "sequence_gap")
	if x["possibly_missing"] != float64(2) || x["next"] != nil {
		t.Fatalf("meetings %v", x)
	}

	m = covRun(t, e, outlook, "--day", covDay(40), "--source", "chats")
	x = entryOf(t, m, covDay(40), "chats", "chats")
	wantEntry(t, x, "not_covered", 0, "before_archive_start")
	if len(x["next"].([]any)) == 0 {
		t.Fatalf("chats %v", x)
	}
	m = covRun(t, e, outlook, "--day", covDay(15), "--source", "chats")
	wantEntry(t, entryOf(t, m, covDay(15), "chats", "chats"), "partial", 0, "before_archive_start")
	m = covRun(t, e, outlook, "--day", covDay(0), "--source", "chats")
	wantEntry(t, entryOf(t, m, covDay(0), "chats", "chats"), "partial", 0, "in_progress,after_last_read")
}

// The calendar is covered on a day a calendar cache showed, and says so per principal.
func TestCoverageOfTheCalendar(t *testing.T) {
	e, outlook := covEnv(t)
	m := covRun(t, e, outlook, "--day", covDay(5), "--source", "calendar")
	wantEntry(t, entryOf(t, m, covDay(5), "calendar", ""), "not_covered", 0, "never_read")
}
