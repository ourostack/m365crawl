package cli

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/outlookmail"
	"github.com/ourostack/m365crawl/internal/store"
)

// seedMailAs adds one message to the archive under another mail account.
func seedMailAs(t *testing.T, e *env, account string) {
	t.Helper()
	st, err := store.Open(context.Background(), e.db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	m := mailMsg(901, fInbox, "<w@x.test>", "", "Work only", 1, true)
	m.Account = account
	res := outlookmail.Result{Messages: []outlookmail.Message{m}, Folders: mailFolderList(),
		Coverage: []outlookmail.Coverage{{FolderKey: fInbox, Count: 1, Oldest: m.Received, Newest: m.Received}}}
	read := mailT0.Add(2 * time.Hour)
	if _, err := st.CommitMail(context.Background(), store.MailBatch{Account: account, ReadAt: read, FreshAt: read, Result: res, Trusted: true}); err != nil {
		t.Fatal(err)
	}
}

// --account outlook/<profile> is a mail account: Teams lists say it holds no chats and point to its
// mail, and an Outlook account the archive lacks is named as one.
func TestOutlookAccountNotes(t *testing.T) {
	pinReadsHere(t)
	pinMailSupported(t, true)
	e := newEnv(t)
	seedMail(t, e, seedMessages())
	mailOnly := "--account outlook/Main names an Outlook account, which holds no Teams chats: run m365crawl mail list --account outlook/Main for its mail, or pass a Teams account that m365crawl whoami lists"
	unknown := "the archive holds no data for account outlook/Nope: m365crawl mail folders and m365crawl calendar sources list the Outlook accounts it holds"
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"--account", "outlook/Main", "messages"}, mailOnly},
		{[]string{"--account", "outlook/Main", "conversations"}, mailOnly},
		{[]string{"--account", "outlook/Nope", "messages"}, unknown},
		{[]string{"--account", "outlook/Nope", "calendar"}, unknown},
		{[]string{"--account", "outlook/Main", "calendar"}, "the archive holds no calendar yet: run m365crawl sync, and m365crawl calendar sources if it stays empty"},
	} {
		if got := noteOf(t, e, c.args...); got != c.want {
			t.Errorf("%v: note %q, want %q", c.args, got, c.want)
		}
	}
	if m := overviewSourceOf(t, e, 0, "--account", "outlook/Main"); m["state"] != "empty" || m["note"] != mailOnly {
		t.Errorf("overview chats, outlook/Main: %v", m)
	}
	if m := overviewSourceOf(t, e, 0, "--account", "outlook/Nope"); m["note"] != unknown {
		t.Errorf("overview chats, outlook/Nope: %v", m)
	}
}

// The overview's mail row follows --account: an Outlook account counts only its own mail, one the
// archive holds no mail for says so, and a Teams account says mail covers every Outlook account.
func TestOverviewMailFollowsTheAccount(t *testing.T) {
	pinReadsHere(t)
	pinMailSupported(t, true)
	e := newEnv(t)
	teamsArchive(t, e)
	seedMail(t, e, seedMessages())
	seedMailAs(t, e, "outlook/Work")
	on := []string{"--outlook-root", t.TempDir()} // the Outlook source is on, so no note says it is off
	src := func(flags ...string) map[string]any { return overviewSourceOf(t, e, 1, append(flags, on...)...) }
	all := src()
	if all["messages"] != float64(len(seedMessages())+1) || all["note"] != nil {
		t.Fatalf("every account: %v", all)
	}
	if m := src("--account", "outlook/Work"); m["state"] != "ok" || m["messages"] != float64(1) {
		t.Errorf("outlook/Work: %v", m)
	}
	if m := src("--account", "outlook/Main"); m["messages"] != float64(len(seedMessages())) {
		t.Errorf("outlook/Main: %v", m)
	}
	if m := src("--account", "outlook/Nope"); m["state"] != "empty" || m["note"] != "no mail is archived for account outlook/Nope: m365crawl mail folders lists the Outlook accounts with mail" {
		t.Errorf("outlook/Nope: %v", m)
	}
	if m := src("--account", tenantA+"/"+userA); m["messages"] != all["messages"] || m["note"] != "mail is shown for every Outlook account, and --account outlook/<profile> narrows it" {
		t.Errorf("Teams account: %v", m)
	}
}

// When search leaves mail out because the archive has none, its note gives the same reason the
// overview does: only mail that a sync would read is a reason to sync.
func TestSearchSaysWhyMailIsMissing(t *testing.T) {
	pinReadsHere(t)
	pinMailSupported(t, true)
	e := newEnv(t)
	e.emptyArchive()
	for _, c := range []struct {
		flags []string
		want  string
	}{
		{nil, "mail is not read in this run: the Outlook source is off (--outlook-root none, or --teams-root without --outlook-root)"},
		{[]string{"--outlook-root", t.TempDir()}, "no new Outlook for Mac profile on this machine, so there is no mail to read"},
		{[]string{"--outlook-root", outlookProfiles(t, "Main")}, "mail is not in the archive yet; run m365crawl sync"},
	} {
		if got := noteOf(t, e, append(c.flags, "search", "x", "--source", "mail")...); got != c.want {
			t.Errorf("%v: note %q, want %q", c.flags, got, c.want)
		}
	}
}

// A failure while reading what the archive holds of an Outlook account, or why mail is missing
// from a search, is an error, not a note.
func TestOutlookAccountNoteFailures(t *testing.T) {
	pinReadsHere(t)
	pinMailSupported(t, true)
	brokenMeta := func(t *testing.T) *env {
		e := newEnv(t)
		e.emptyArchive()
		e.exec("drop table meta")
		e.exec("create table meta (x)")
		return e
	}
	for name, args := range map[string][]string{
		"account note": {"--account", "outlook/Main", "messages"},
		"search note":  {"search", "x", "--source", "mail"},
	} {
		t.Run(name, func(t *testing.T) {
			e := brokenMeta(t)
			code, _, errOut := e.run(append([]string{"--json", "--max-age", "0"}, args...)...)
			if code == 0 || errorOf(t, errOut)["code"] != "db_error" {
				t.Fatalf("exit %d, stderr %q", code, errOut)
			}
		})
	}
	t.Run("calendar sources", func(t *testing.T) {
		e := newEnv(t)
		e.emptyArchive()
		old := calendarSourcesOf
		t.Cleanup(func() { calendarSourcesOf = old })
		calendarSourcesOf = func(*store.Store, context.Context, store.CalendarSourcesFilter) (store.CalendarSources, error) {
			return store.CalendarSources{}, errors.New("broken")
		}
		code, _, errOut := e.run("--json", "--max-age", "0", "--account", "outlook/Main", "messages")
		if code == 0 || errorOf(t, errOut)["code"] != "db_error" {
			t.Fatalf("exit %d, stderr %q", code, errOut)
		}
	})
}
