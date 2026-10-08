package cli

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/teamsdesktop"
)

func TestParseTeamsLink(t *testing.T) {
	const ctx = "?context=%7B%22contextType%22%3A%22chat%22%7D"
	if !contains(teamsHosts, "gov.teams.microsoft.us") || !contains(teamsHosts, "dod.teams.microsoft.us") {
		t.Fatalf("hosts %v", teamsHosts)
	}
	for _, host := range teamsHosts {
		base := "https://" + host
		for _, c := range []struct {
			name, link, conv, msg, parent string
		}{
			{"channel, encoded", base + "/l/channel/19%3AabcDEF%40thread.skype/Channel%20Name?groupId=00000000-0000-4000-8000-000000000001&tenantId=00000000-0000-4000-8000-000000000002&ngc=true", "19:abcDEF@thread.skype", "", ""},
			{"channel tacv2, plain", base + "/l/channel/19:abc@thread.tacv2/General", "19:abc@thread.tacv2", "", ""},
			{"channel, no name", base + "/l/channel/19%3Aabc%40thread.tacv2", "19:abc@thread.tacv2", "", ""},
			{"group chat, encoded", base + "/l/chat/19%3Aabc%40thread.v2/conversations" + ctx, "19:abc@thread.v2", "", ""},
			{"one-to-one chat, plain", base + "/l/chat/19:00000000-0000-4000-8000-0000000000a1_00000000-0000-4000-8000-0000000000ff@unq.gbl.spaces/conversations", "19:00000000-0000-4000-8000-0000000000a1_00000000-0000-4000-8000-0000000000ff@unq.gbl.spaces", "", ""},
			{"message, plain", base + "/l/message/19:abc@unq.gbl.spaces/1791487288555" + ctx, "19:abc@unq.gbl.spaces", "1791487288555", ""},
			{"message, encoded", base + "/l/message/19%3Aabc%40thread.v2/17?tenantId=t", "19:abc@thread.v2", "17", ""},
			{"channel reply", base + "/l/message/19%3Aabc%40thread.tacv2/1700000046000?tenantId=t&parentMessageId=1700000045000", "19:abc@thread.tacv2", "1700000046000", "1700000045000"},
			{"message, trailing slash", base + "/l/message/19%3Aabc%40thread.v2/17/", "19:abc@thread.v2", "17", ""},
			{"meeting, lower-case escapes", base + "/l/meetup-join/19%3ameeting_ZmFrZQ%40thread.v2/0?context=%7b%7d", "19:meeting_ZmFrZQ@thread.v2", "", ""},
			{"pasted in angle brackets", " <" + base + "/l/chat/19%3Aabc%40thread.v2/0> ", "19:abc@thread.v2", "", ""},
			{"pasted in quotes", "\"" + base + "/l/chat/19%3Aabc%40thread.v2/0\"", "19:abc@thread.v2", "", ""},
			{"pasted in backticks", "`" + base + "/l/chat/19%3Aabc%40thread.v2/0`", "19:abc@thread.v2", "", ""},
			{"upper-case scheme", strings.Replace(base, "https", "HTTPS", 1) + "/l/chat/19%3Aabc%40thread.v2/0", "19:abc@thread.v2", "", ""},
			{"message link ending a sentence", base + "/l/message/19%3Aabc%40thread.v2/1700000000002).", "19:abc@thread.v2", "1700000000002", ""},
			{"chat link ending a sentence", base + "/l/chat/19%3Ax%40thread.v2.", "19:x@thread.v2", "", ""},
			{"team link", base + "/l/team/19%3Ageneral%40thread.tacv2/conversations?groupId=00000000-0000-4000-8000-000000000001&tenantId=t", "19:general@thread.tacv2", "", ""},
			{"upper-case host and http", strings.Replace(base, "https://"+host, "http://"+strings.ToUpper(host), 1) + "/l/chat/19%3Aabc%40thread.v2/0", "19:abc@thread.v2", "", ""},
		} {
			conv, msg, parent, err := parseTeamsLink(c.link)
			if err != nil || conv != c.conv || msg != c.msg || parent != c.parent {
				t.Errorf("%s %s: %q %q %q %v", host, c.name, conv, msg, parent, err)
			}
		}
	}
}

func TestParseTeamsLinkRejects(t *testing.T) {
	for _, c := range []struct {
		link, why string
		people    bool
	}{
		{"https://[::1", "not a valid URL", false},
		{"http://%", "not a valid URL", false},
		{"https://example.com/l/chat/19%3Aabc%40thread.v2/0", "not a Teams link", false},
		{"https://teams.microsoft.com.example.com/l/chat/19%3Aabc%40thread.v2/0", "not a Teams link", false},
		{"https://evil.example/l/message/19%3Aabc/1", "not a Teams link", false},
		{"ftp://teams.microsoft.com/l/chat/19%3Aabc%40thread.v2/0", "not a Teams link", false},
		{"https://teams.microsoft.com/", "not a Teams channel, chat, message or meeting link", false},
		{"https://teams.microsoft.com/v2/", "not a Teams channel, chat, message or meeting link", false},
		{"https://teams.microsoft.com/l/entity/00000000-0000-4000-8000-000000000003/tab", "not a Teams channel, chat, message or meeting link", false},
		{"https://teams.microsoft.com/l/chat", "no conversation id", false},
		{"https://teams.microsoft.com/l/channel/", "no conversation id", false},
		{"https://teams.microsoft.com/l/message//1", "no conversation id", false},
		{"https://teams.microsoft.com/l/meetup-join/%zz/0", "not a valid URL", false},
		{"https://teams.microsoft.com/l/message/%zz/1", "not a valid URL", false},
		{"https://teams.microsoft.com/l/message/19%3Aabc/%zz", "not a valid URL", false},
		{"https://teams.microsoft.com/l/message/19%3Aabc", "no message id", false},
		{"https://teams.microsoft.com/l/message/19%3Aabc/", "no message id", false},
		{"https://teams.microsoft.com/l/chat/0/0?users=a@example.com,b@example.com", "names people", true},
		{"https://teams.live.com/l/chat/0/0?users=a@example.com", "names people", true},
	} {
		_, _, _, err := parseTeamsLink(c.link)
		if err == nil || !strings.Contains(err.Error(), c.why) || err.people != c.people {
			t.Errorf("%q: %v", c.link, err)
			continue
		}
		coded := err.coded("own fix")
		if c.people != (coded.Fix == peopleLinkFix) || (!c.people && coded.Fix != "own fix") || coded.Code != errs.CodeUsage {
			t.Errorf("%q: coded %+v", c.link, coded)
		}
	}
	if !strings.Contains(peopleLinkFix, "m365crawl conversations --query <name>") {
		t.Fatalf("people fix %q", peopleLinkFix)
	}
}

func TestIsLink(t *testing.T) {
	for s, want := range map[string]bool{
		"https://teams.microsoft.com/l/chat/x/0":    true,
		"HTTPS://teams.microsoft.com/l/chat/x/0":    true,
		"  http://teams.microsoft.com/l/chat/x/0 ":  true,
		"<https://teams.microsoft.com/l/chat/x/0>":  true,
		"'https://teams.microsoft.com/l/chat/x/0'":  true,
		"`https://teams.microsoft.com/l/chat/x/0`.": true,
		"19:abc@thread.v2":                          false,
		"Fixture chat 1":                            false,
		"":                                          false,
	} {
		if got := isLink(s); got != want {
			t.Errorf("isLink(%q) = %v", s, got)
		}
	}
}

func TestJoinNotes(t *testing.T) {
	for _, c := range [][3]string{{"", "", ""}, {"a", "", "a"}, {"", "b", "b"}, {"a", "b", "a; b"}} {
		if got := joinNotes(c[0], c[1]); got != c[2] {
			t.Errorf("%q + %q = %q", c[0], c[1], got)
		}
	}
}

// linkEnv is a synced fixture archive. Both sources point at fixtures, so nothing real is read.
func linkEnv(t *testing.T) *env {
	t.Helper()
	e := newEnv(t)
	outlook, err := filepath.Abs("../../testdata/outlook-fixture")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("M365CRAWL_TEAMS_ROOT", e.root)
	t.Setenv("M365CRAWL_OUTLOOK_ROOT", outlook)
	e.sync()
	return e
}

const (
	fixtureChannel = "19:topicchannel1@thread.tacv2"
	fixtureChat    = "19:00000000-0000-4000-8000-0000000000a1_00000000-0000-4000-8000-0000000000ff@unq.gbl.spaces"
	fixtureMeeting = "19:meeting_FIXTURE1@thread.v2"
)

// linkTo escapes a conversation id the way Teams does in a link.
func linkTo(conv string) string {
	return strings.NewReplacer(":", "%3A", "@", "%40").Replace(conv)
}

// conversationsOf runs a read and returns the conversation ids of its items and its note.
func conversationsOf(t *testing.T, e *env, args ...string) (map[string]bool, string) {
	t.Helper()
	code, stdout, stderr := e.run(append([]string{"--json", "--max-age", "0", "--account", tenantA + "/" + userA}, args...)...)
	if code != 0 {
		t.Fatalf("%v: exit %d: %s", args, code, stderr)
	}
	m := decode(t, stdout)
	got := map[string]bool{}
	for _, it := range items(t, m) {
		got[it["conversation_id"].(string)] = true
	}
	note, _ := m["note"].(string)
	return got, note
}

func TestConversationFlagTakesTeamsLinks(t *testing.T) {
	e := linkEnv(t)
	for _, c := range []struct{ link, conv string }{
		{"https://teams.microsoft.com/l/channel/" + linkTo(fixtureChannel) + "/General?groupId=00000000-0000-4000-8000-000000000001&tenantId=" + tenantA, fixtureChannel},
		{"https://teams.cloud.microsoft/l/chat/" + linkTo(fixtureChat) + "/conversations?context=%7B%22contextType%22%3A%22chat%22%7D", fixtureChat},
		{"https://teams.live.com/l/chat/" + fixtureChat + "/conversations", fixtureChat},
		{" <HTTPS://gov.teams.microsoft.us/l/chat/" + linkTo(fixtureChat) + "/conversations>.", fixtureChat},
		{"https://teams.microsoft.com/l/team/" + linkTo(fixtureChannel) + "/conversations?groupId=00000000-0000-4000-8000-000000000001", fixtureChannel},
		{"https://teams.microsoft.com/l/meetup-join/" + strings.NewReplacer(":", "%3a", "@", "%40").Replace(fixtureMeeting) + "/0?context=%7b%7d", fixtureMeeting},
	} {
		for _, flag := range []string{"-c", "--conversation"} {
			got, note := conversationsOf(t, e, "messages", flag, c.link, "--limit", "500")
			if len(got) != 1 || !got[c.conv] || note != "" {
				t.Errorf("messages %s %s: %v, note %q", flag, c.link, got, note)
			}
		}
		if got, _ := conversationsOf(t, e, "search", "--conversation", c.link, "--source", "chats", "--limit", "500"); len(got) != 1 || !got[c.conv] {
			t.Errorf("search --conversation %s: %v", c.link, got)
		}
	}
	// unread takes a link too; the chat has unread messages in the fixture or none, but never another's.
	got, _ := conversationsOf(t, e, "unread", "-c", "https://teams.microsoft.com/l/chat/"+linkTo(fixtureChat)+"/0", "--limit", "500")
	if len(got) > 1 || (len(got) == 1 && !got[fixtureChat]) {
		t.Errorf("unread -c: %v", got)
	}
}

func TestConversationFlagWithAMessageLink(t *testing.T) {
	e := linkEnv(t)
	link := "https://teams.microsoft.com/l/message/" + linkTo(fixtureChannel) + "/1700000046000?tenantId=" + tenantA + "&parentMessageId=1700000045000"
	got, note := conversationsOf(t, e, "messages", "-c", link, "--limit", "500")
	if len(got) != 1 || !got[fixtureChannel] {
		t.Fatalf("messages -c <message link>: %v", got)
	}
	if !strings.Contains(note, "the link named one message") || !strings.Contains(note, "m365crawl thread '"+link+"'") {
		t.Fatalf("note %q", note)
	}
	// An empty result keeps its own note and adds the link's.
	_, note = conversationsOf(t, e, "messages", "-c", link, "--from", "nobody-at-all")
	if !strings.HasPrefix(note, "nothing matched the filters; the link named one message") {
		t.Fatalf("empty note %q", note)
	}
}

func TestConversationLinkNotArchived(t *testing.T) {
	e := linkEnv(t)
	missing := "https://teams.microsoft.com/l/chat/19%3Amissing%40thread.v2/conversations"
	for _, args := range [][]string{
		{"messages", "-c", missing},
		{"unread", "-c", missing, "--by-conversation"},
		{"thread", "https://teams.microsoft.com/l/message/19%3Amissing%40thread.v2/17"},
	} {
		got, note := conversationsOf(t, e, args...)
		if len(got) != 0 || note != uncachedNote("19:missing@thread.v2", true) || !strings.Contains(note, "for this account") {
			t.Errorf("%v: %v, note %q", args, got, note)
		}
	}
	if n := uncachedNote("x", false); strings.Contains(n, "for this account") || !strings.Contains(n, "Teams desktop app may not have cached it") || !strings.Contains(n, "open it once in Teams, then run m365crawl sync") {
		t.Fatalf("note %q", n)
	}
	// Without --account the note does not name one.
	_, stdout, _ := e.run("--json", "--max-age", "0", "messages", "-c", missing)
	if note := decode(t, stdout)["note"]; note != uncachedNote("19:missing@thread.v2", false) {
		t.Fatalf("no --account: note %q", note)
	}
	// A conversation the archive holds keeps the usual note when a filter empties it.
	_, note := conversationsOf(t, e, "messages", "-c", "https://teams.microsoft.com/l/chat/"+linkTo(fixtureChat)+"/0", "--from", "nobody-at-all")
	if note != "nothing matched the filters" {
		t.Fatalf("held conversation: note %q", note)
	}
}

// A failure while checking whether the archive holds the link's conversation is reported.
func TestConversationLinkCheckFailure(t *testing.T) {
	e := linkEnv(t)
	old := hasConversationOf
	t.Cleanup(func() { hasConversationOf = old })
	hasConversationOf = func(*store.Store, context.Context, *teamsdesktop.Account, string) (bool, error) {
		return false, errors.New("broken")
	}
	code, _, stderr := e.run("--json", "--max-age", "0", "messages", "-c", "https://teams.microsoft.com/l/chat/19%3Amissing%40thread.v2/0")
	if code == 0 || errorOf(t, stderr)["code"] != "db_error" {
		t.Fatalf("exit %d: %s", code, stderr)
	}
}

func TestConversationFlagRejectsBadLinks(t *testing.T) {
	e := newEnv(t)
	for _, c := range []struct{ link, fix string }{
		{"https://example.com/l/chat/19%3Aabc%40thread.v2/0", linkFix},
		{"https://teams.microsoft.com/l/chat/0/0?users=a@example.com", peopleLinkFix},
		{"https://teams.microsoft.com/l/message/19%3Aabc", linkFix},
	} {
		for _, cmd := range []string{"messages", "unread", "search"} {
			code, _, stderr := e.run("--json", "--max-age", "0", cmd, "-c", c.link)
			er := errorOf(t, stderr)
			if code != 2 || er["code"] != "usage" || er["fix"] != c.fix || !strings.HasPrefix(er["message"].(string), "--conversation: ") {
				t.Errorf("%s -c %s: exit %d %v", cmd, c.link, code, er)
			}
		}
	}
}

func TestThreadTakesLinks(t *testing.T) {
	e := linkEnv(t)
	// A channel reply's link reads the whole thread, by its root.
	link := "https://teams.microsoft.com/l/message/" + linkTo(fixtureChannel) + "/1700000046000?tenantId=" + tenantA + "&parentMessageId=1700000045000"
	_, stdout, errOut := e.run("--json", "--max-age", "0", "--account", tenantA+"/"+userA, "thread", link)
	its := items(t, decode(t, stdout))
	if len(its) == 0 || its[0]["id"] != "1700000045000" {
		t.Fatalf("thread <reply link>: %v %s", its, errOut)
	}
	// Plain and encoded message links name the same thread.
	for _, l := range []string{
		"https://teams.cloud.microsoft/l/message/" + fixtureChannel + "/1700000045000",
		"https://teams.live.com/l/message/" + linkTo(fixtureChannel) + "/1700000045000?context=%7B%7D",
	} {
		_, out, _ := e.run("--json", "--max-age", "0", "--account", tenantA+"/"+userA, "thread", l)
		if got := items(t, decode(t, out)); len(got) != len(its) {
			t.Errorf("thread %s: %d items, want %d", l, len(got), len(its))
		}
	}
	// A link pasted with brackets, spaces, an upper-case scheme or a full stop reads the same thread.
	for _, l := range []string{" <" + link + "> ", strings.Replace(link, "https", "HTTPS", 1) + "."} {
		_, out, _ := e.run("--json", "--max-age", "0", "--account", tenantA+"/"+userA, "thread", l)
		if got := items(t, decode(t, out)); len(got) != len(its) {
			t.Errorf("thread %q: %d items, want %d", l, len(got), len(its))
		}
	}
	// A root after a link is a usage error.
	code, _, stderr := e.run("--json", "--max-age", "0", "thread", link, "1700000045000")
	if er := errorOf(t, stderr); code != 2 || er["code"] != "usage" || !strings.Contains(er["message"].(string), "not both") || !strings.Contains(er["fix"].(string), "without the root") {
		t.Fatalf("thread <link> <root>: exit %d %v", code, er)
	}
	// A channel or chat link names no message: thread points to messages --conversation.
	for _, l := range []string{
		"https://teams.microsoft.com/l/channel/" + linkTo(fixtureChannel) + "/General",
		"https://teams.microsoft.com/l/chat/" + linkTo(fixtureChat) + "/conversations",
		"https://teams.microsoft.com/l/meetup-join/" + linkTo(fixtureMeeting) + "/0",
	} {
		code, _, stderr := e.run("--json", "--max-age", "0", "thread", l)
		er := errorOf(t, stderr)
		fix, _ := er["fix"].(string)
		if code != 2 || er["code"] != "usage" || !strings.Contains(fix, "m365crawl messages --conversation '"+l+"'") || !strings.Contains(fix, "message link") {
			t.Errorf("thread %s: exit %d %v", l, code, er)
		}
	}
	// A link that names people keeps its own fix.
	code, _, stderr = e.run("--json", "--max-age", "0", "thread", "https://teams.microsoft.com/l/chat/0/0?users=a@example.com")
	if er := errorOf(t, stderr); code != 2 || er["fix"] != peopleLinkFix {
		t.Fatalf("thread <people link>: exit %d %v", code, er)
	}
}

func TestTranscriptsTakesAMeetingLink(t *testing.T) {
	for _, c := range []struct{ arg, want string }{
		{"call-1", "call-1"},
		{"https://teams.microsoft.com/l/meetup-join/19%3ameeting_ZmFrZQ%40thread.v2/0?context=%7b%7d", "19:meeting_ZmFrZQ@thread.v2"},
		{"https://teams.microsoft.com/l/chat/19%3Ameeting_ZmFrZQ%40thread.v2/conversations", "19:meeting_ZmFrZQ@thread.v2"},
		{"https://teams.microsoft.com/l/message/19%3Ameeting_ZmFrZQ%40thread.v2/17", "19:meeting_ZmFrZQ@thread.v2"},
		{" <HTTPS://teams.microsoft.com/l/meetup-join/19%3ameeting_ZmFrZQ%40thread.v2/0>", "19:meeting_ZmFrZQ@thread.v2"},
	} {
		if got, err := meetingRef(c.arg); err != nil || got != c.want {
			t.Errorf("%s: %q %v", c.arg, got, err)
		}
	}
	_, err := meetingRef("https://teams.microsoft.com/l/chat/0/0?users=a@example.com")
	var coded *errs.Coded
	if !errors.As(err, &coded) || coded.Fix != peopleLinkFix || !strings.Contains(coded.Message, "names people") {
		t.Fatalf("people link: %v", err)
	}
}
