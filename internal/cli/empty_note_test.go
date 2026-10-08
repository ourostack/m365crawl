package cli

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/teamsdesktop"
)

// noteT0 is when the one message of the synthetic Teams archive was sent.
var noteT0 = time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)

// teamsArchive builds an archive holding one account, one chat and one message, without a sync.
func teamsArchive(t *testing.T, e *env) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, e.db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	a := teamsdesktop.Account{TenantID: tenantA, UserID: userA}
	if err := st.ApplyAccount(ctx, a); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ApplyConversations(ctx, []teamsdesktop.Conversation{{TenantID: tenantA, UserID: userA, ID: "19:chat", Kind: "Chat", DisplayName: "Chat", Raw: []byte(`{}`)}}); err != nil {
		t.Fatal(err)
	}
	m := teamsdesktop.Message{TenantID: tenantA, UserID: userA, ConversationID: "19:chat", ID: "1", SenderID: "8:orgid:x", SenderName: "Ann",
		SentAt: noteT0, MessageType: "Text", ContentText: "hello", Version: 1, Raw: []byte(`{"id":"1"}`)}
	if _, err := st.ApplyMessages(ctx, []teamsdesktop.Message{m}); err != nil {
		t.Fatal(err)
	}
}

// pinReadsHere makes the notes and search run as on a Mac, which reads Teams and mail, whatever
// the host is.
func pinReadsHere(t *testing.T) {
	t.Helper()
	oldTeams, oldSearch := teamsReadHere, searchMailPlatform
	teamsReadHere, searchMailPlatform = func() bool { return true }, "darwin"
	t.Cleanup(func() { teamsReadHere, searchMailPlatform = oldTeams, oldSearch })
	pinMailPlatform(t)
}

// noteOf runs a read command in JSON and returns its note.
func noteOf(t *testing.T, e *env, args ...string) string {
	t.Helper()
	code, out, errOut := e.run(append([]string{"--json", "--max-age", "0"}, args...)...)
	if code != 0 {
		t.Fatalf("%v: exit %d: %s", args, code, errOut)
	}
	m := decode(t, out)
	if m["count"] != float64(0) {
		t.Fatalf("%v: want an empty list, got %v", args, m)
	}
	note, _ := m["note"].(string)
	return note
}

// Every Teams list command says why it is empty.
func TestEmptyTeamsListsSayWhy(t *testing.T) {
	pinReadsHere(t)
	e := newEnv(t)
	for _, args := range [][]string{{"messages"}, {"search", "x", "--source", "chats"}, {"conversations"}, {"teams"}, {"people"}, {"activity"}, {"stores"}, {"unread"}, {"thread", "c", "r"}, {"calendar"}, {"calendar", "actions"}, {"calendar", "sources"}} {
		if got := noteOf(t, e, args...); got != "no archive yet: run m365crawl sync" {
			t.Errorf("no archive, %v: note %q", args, got)
		}
	}
	e.emptyArchive()
	if got := noteOf(t, e, "messages"); got != "the archive holds no Teams messages yet: run m365crawl sync, and m365crawl doctor if it stays empty" {
		t.Errorf("empty archive: %q", got)
	}
	for _, args := range [][]string{{"calendar"}, {"calendar", "actions"}, {"calendar", "sources"}} {
		if got := noteOf(t, e, args...); got == "" || !contains([]string{
			"the archive holds no calendar yet: run m365crawl sync, and m365crawl calendar sources if it stays empty",
			"the archive holds no calendar yet: run m365crawl sync, and m365crawl doctor if it stays empty",
		}, got) {
			t.Errorf("no calendar, %v: %q", args, got)
		}
	}

	e = newEnv(t)
	teamsArchive(t, e)
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"messages", "--since", "2026-04-01"}, "nothing matched: the time range is outside the archived Teams messages, which run from 2026-03-10 to 2026-03-10"},
		{[]string{"search", "hello", "--source", "chats", "--until", "2026-03-01"}, "nothing matched: the time range is outside the archived Teams messages, which run from 2026-03-10 to 2026-03-10"},
		{[]string{"messages", "--from", "nobody"}, "nothing matched the filters"},
		{[]string{"search", "absent", "--source", "chats"}, "no message matched the search words and filters"},
		{[]string{"conversations", "--kind", "Space"}, "nothing matched the filters"},
		{[]string{"people", "--query", "nobody"}, "nothing matched the filters"},
		{[]string{"activity"}, noneOfThisKind},
		{[]string{"activity", "--unread"}, "nothing matched the filters"},
		{[]string{"teams"}, "the archive holds no teams: this account's Teams cache lists none"},
		{[]string{"stores"}, "the archive holds no generic stores yet: run m365crawl sync"},
		{[]string{"records", "--database", "nothing"}, "nothing matched the filters"},
		{[]string{"unread"}, "nothing is unread in chats and meetings; --include-channels adds channels"},
		{[]string{"unread", "--include-channels"}, "nothing is unread"},
		{[]string{"thread", "19:chat", "404"}, "no message of this thread is archived: check the conversation id and root message id (the thread column of search and messages text output gives both; conversation_id and reply_chain_id or id in JSON), or pass a Teams message link"},
	} {
		if got := noteOf(t, e, c.args...); got != c.want {
			t.Errorf("%v: note %q, want %q", c.args, got, c.want)
		}
	}

	old := teamsReadHere
	teamsReadHere = func() bool { return false }
	t.Cleanup(func() { teamsReadHere = old })
	if got := noteOf(t, e, "messages", "--from", "nobody"); got != "nothing matched the filters" {
		t.Errorf("data on a platform without Teams: %q", got)
	}
	e = newEnv(t)
	e.emptyArchive()
	if got := noteOf(t, e, "messages", "--from", "nobody"); got != "Teams is read on macOS and Windows only, so this archive holds no Teams data on this operating system" {
		t.Errorf("unsupported platform: %q", got)
	}
}

// A list that is not empty carries no note, and text output prints the note of an empty one.
func TestEmptyNoteOnlyOnEmptyLists(t *testing.T) {
	e := newEnv(t)
	teamsArchive(t, e)
	code, out, _ := e.run("--json", "--max-age", "0", "messages")
	if m := decode(t, out); code != 0 || m["count"] != float64(1) || m["note"] != nil {
		t.Fatalf("a full list has no note: %d %v", code, m)
	}
	_, out, _ = e.run("--format", "text", "--max-age", "0", "messages", "--from", "nobody")
	if !contains(splitLines(out), "note: nothing matched the filters") {
		t.Fatalf("text output lacks the note:\n%s", out)
	}
}

// A failure while explaining an empty list is reported, not hidden.
func TestEmptyNoteReportsAnArchiveFailure(t *testing.T) {
	e := newEnv(t)
	teamsArchive(t, e)
	e.exec("drop table messages")
	e.exec("create table messages (x)")
	code, _, errOut := e.run("--json", "--max-age", "0", "stores")
	if code == 0 {
		t.Fatalf("exit 0, stderr %q", errOut)
	}
	if body := errorOf(t, errOut); body["code"] != "db_error" {
		t.Fatalf("error = %v", body)
	}
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

// A failure while reading the calendar sources that explain an empty agenda is reported.
func TestCalendarEmptyNoteReportsAFailure(t *testing.T) {
	e := newEnv(t)
	e.emptyArchive()
	old := calendarSourcesOf
	calendarSourcesOf = func(*store.Store, context.Context, store.CalendarSourcesFilter) (store.CalendarSources, error) {
		return store.CalendarSources{}, errors.New("sources failed")
	}
	t.Cleanup(func() { calendarSourcesOf = old })
	for _, args := range [][]string{{"calendar"}, {"calendar", "actions"}} {
		code, _, errOut := e.run(append([]string{"--json", "--max-age", "0"}, args...)...)
		if code == 0 || errorOf(t, errOut)["code"] != "db_error" {
			t.Fatalf("%v: exit %d, stderr %q", args, code, errOut)
		}
	}
}

// The thread column of search and messages text output is what `thread` takes, whole.
func TestTextThreadColumnRunsThread(t *testing.T) {
	pinReadsHere(t)
	e := newEnv(t)
	teamsArchive(t, e)
	for _, args := range [][]string{{"messages"}, {"search", "hello"}} {
		_, out, _ := e.run(append([]string{"--format", "text", "--max-age", "0"}, args...)...)
		if !strings.Contains(out, "thread") || !strings.Contains(out, "19:chat 1") {
			t.Fatalf("%v: no thread target:\n%s", args, out)
		}
	}
	code, out, _ := e.run("--json", "--max-age", "0", "thread", "19:chat", "1")
	if m := decode(t, out); code != 0 || m["count"] != float64(1) {
		t.Fatalf("thread: %d %v", code, m)
	}
	// For mail, the thread column holds the id that mail thread takes.
	e = newEnv(t)
	seedMail(t, e, seedMessages())
	_, out, _ = e.run("--format", "text", "--max-age", "0", "search", "Quarterly", "--source", "mail")
	var id string
	for _, w := range strings.Fields(out) {
		if strings.HasPrefix(w, "outlook/") {
			id = w
			break
		}
	}
	if code, out, errOut := e.run("--json", "--max-age", "0", "mail", "thread", id); code != 0 || decode(t, out)["count"] == float64(0) {
		t.Fatalf("mail thread %q: %d %s %s", id, code, out, errOut)
	}
	if got := threadTarget("19:c", "", "5"); got != "19:c 5" {
		t.Fatalf("no reply chain: %q", got)
	}
	if got := threadTarget("19:c", "4", "5"); got != "19:c 4" {
		t.Fatalf("reply chain: %q", got)
	}
}

// An empty search names only the sources it read: a mail-only search never blames Teams.
func TestSearchEmptyNoteNamesTheSourcesRead(t *testing.T) {
	pinReadsHere(t)
	noTeams := "the archive holds no Teams messages yet: run m365crawl sync, and m365crawl doctor if it stays empty"
	outlookOff := "mail is not read in this run: the Outlook source is off (--outlook-root none, or --teams-root without --outlook-root)"
	e := newEnv(t)
	e.emptyArchive()
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"search", "x", "--source", "mail"}, outlookOff},
		{[]string{"search", "x", "--source", "chats"}, noTeams},
		{[]string{"search", "x"}, outlookOff + "; " + noTeams},
	} {
		if got := noteOf(t, e, c.args...); got != c.want {
			t.Errorf("empty archive, %v: note %q, want %q", c.args, got, c.want)
		}
	}

	e = newEnv(t)
	seedMail(t, e, seedMessages())
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"search", "zzzqqq", "--source", "mail"}, "no mail matched the search words and filters"},
		{[]string{"search", "zzzqqq"}, "Teams chats: " + noTeams + "; no mail matched the search words and filters"},
	} {
		got := noteOf(t, e, c.args...)
		if got != c.want {
			t.Errorf("mail only, %v: note %q, want %q", c.args, got, c.want)
		}
		if c.args[len(c.args)-1] == "mail" && strings.Contains(got, "Teams") {
			t.Errorf("a mail-only search blamed Teams: %q", got)
		}
	}

	e = searchMailEnv(t)
	if got := noteOf(t, e, "search", "zzzqqq"); got != "no chat message or mail matched the search words and filters" {
		t.Errorf("both sources: note %q", got)
	}
}

// A failure while reading why a search found nothing is reported, not hidden behind a note.
func TestSearchEmptyNoteReportsAFailure(t *testing.T) {
	pinReadsHere(t)
	e := newEnv(t)
	teamsArchive(t, e)
	old := messageWindowOf
	t.Cleanup(func() { messageWindowOf = old })
	messageWindowOf = func(*store.Store, context.Context, *teamsdesktop.Account) (time.Time, time.Time, error) {
		return time.Time{}, time.Time{}, errors.New("broken")
	}
	code, _, errOut := e.run("--json", "--max-age", "0", "search", "zzzqqq", "--source", "chats")
	if code == 0 {
		t.Fatalf("exit 0, stderr %q", errOut)
	}
	if body := errorOf(t, errOut); body["code"] != "db_error" {
		t.Fatalf("error = %v", body)
	}
}

// Where Teams or mail is not read, an empty search says so instead of asking for a sync.
func TestSearchEmptyNoteOnOtherPlatforms(t *testing.T) {
	pinReadsHere(t)
	noTeamsHere := "Teams is read on macOS and Windows only, so this archive holds no Teams data on this operating system"
	e := newEnv(t)
	e.emptyArchive()

	// Linux: no Teams cache to read.
	teamsReadHere = func() bool { return false }
	if got := noteOf(t, e, "search", "x", "--source", "chats"); got != noTeamsHere {
		t.Errorf("linux, --source chats: note %q", got)
	}
	seedMail(t, e, seedMessages())
	if got := noteOf(t, e, "search", "zzzqqq"); got != "Teams chats: "+noTeamsHere+"; no mail matched the search words and filters" {
		t.Errorf("linux, both sources: note %q", got)
	}

	// Windows: Teams is read, mail is not yet.
	teamsReadHere, searchMailPlatform = func() bool { return true }, "windows"
	e = newEnv(t)
	got := noteOf(t, e, "search", "x")
	if !strings.Contains(got, "mail is not yet read on Windows; searched Teams chats and meeting transcripts only") || !strings.Contains(got, "no archive yet: run m365crawl sync") {
		t.Errorf("windows, no archive: note %q", got)
	}
	e.emptyArchive()
	code, _, errOut := e.run("--json", "--max-age", "0", "search", "x", "--source", "mail")
	if body := errorOf(t, errOut); code != 3 || body["code"] != "mail_unsupported_platform" {
		t.Errorf("windows, --source mail: exit %d, %v", code, body)
	}
}
