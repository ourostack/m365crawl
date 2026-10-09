package cli

import (
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/calendar"
)

// Every list command has its --fields keys in the one table, and nothing else is in it.
func TestCommandFieldsCoverListCommands(t *testing.T) {
	var names []string
	for name, sets := range commandFields {
		names = append(names, name)
		if len(sets) == 0 || sets[0].when != "" {
			t.Fatalf("%s: the first set must be the plain result's", name)
		}
		for _, s := range sets {
			if len(s.keys()) == 0 {
				t.Fatalf("%s %q: no keys", name, s.when)
			}
		}
	}
	sort.Strings(names)
	want := slices.Clone(listCommands)
	sort.Strings(want)
	if !slices.Equal(names, want) {
		t.Fatalf("commands with --fields keys %v, list commands %v", names, want)
	}
	if fieldsOf("calendar", "--nope") != nil || fieldsOf("nope", "") != nil {
		t.Fatal("an unknown command or mode has keys")
	}
}

// The keys a command's help lists are exactly the keys its check accepts: a listed key passes and
// an unlisted one is refused with the same list.
func TestHelpListsTheKeysTheCheckAccepts(t *testing.T) {
	e := newEnv(t)
	for name, sets := range commandFields {
		// A group's default command has its own help: calendar agenda, transcripts list.
		args := append(strings.Fields(map[string]string{"calendar": "calendar agenda", "transcripts": "transcripts list"}[name]), "--help")
		if len(args) == 1 {
			args = append(strings.Fields(name), "--help")
		}
		code, out, _ := e.run(args...)
		if code != 0 {
			t.Fatalf("%s --help: exit %d", name, code)
		}
		_, section, ok := strings.Cut(out, "\n--fields keys (comma separated; any other key is a usage error):\n")
		if !ok {
			t.Fatalf("%s --help has no --fields section:\n%s", name, out)
		}
		listed := strings.Join(strings.Fields(section), " ")
		for _, s := range sets {
			want := strings.Join(s.keys(), ", ")
			if s.when != "" {
				want = "With " + s.when + ": " + want
			}
			if !strings.Contains(listed, want) {
				t.Fatalf("%s --help lists\n%s\nwant\n%s", name, listed, want)
			}
		}
	}
	// The JSON mode prints the same help text.
	_, out, _ := e.run("calendar", "event", "--help", "--json")
	if !strings.Contains(out, "body_text, body_html, body_type") {
		t.Fatalf("calendar event --help --json:\n%s", out)
	}
	// The agenda names the keys only calendar event has; a command without keys prints no section.
	_, out, _ = e.run("calendar", "--help")
	if strings.Contains(out, "--fields keys") {
		t.Fatalf("the calendar group's help lists keys:\n%s", out)
	}
	_, out, _ = e.run("calendar", "agenda", "--help")
	if !strings.Contains(strings.Join(strings.Fields(out), " "), "Only in `calendar event` (refused here): organizer, attendees,") {
		t.Fatalf("calendar agenda --help:\n%s", out)
	}
	if _, out, _ = e.run("sync", "--help"); strings.Contains(out, "--fields keys") {
		t.Fatalf("sync --help lists keys:\n%s", out)
	}
	// Every line of the section fits the help width.
	_, out, _ = e.run("search", "--help")
	_, section, _ := strings.Cut(out, "\n--fields keys")
	for _, l := range strings.Split(section, "\n") {
		if len(l) > helpWidth {
			t.Fatalf("line wider than %d: %q", helpWidth, l)
		}
	}
	// Each check refuses an unknown key with the list the help shows.
	for name, sets := range commandFields {
		for _, s := range sets {
			rt := &runtime{fields: []string{"no_such_key"}}
			err := checkCommandFields(rt, name, s.when)
			if err == nil || !strings.Contains(err.Error(), strings.Join(s.keys(), ", ")) {
				t.Fatalf("%s %q: %v", name, s.when, err)
			}
			rt.fields = s.keys()
			if err := checkCommandFields(rt, name, s.when); err != nil {
				t.Fatalf("%s %q refuses its own keys: %v", name, s.when, err)
			}
		}
	}
}

// fieldsDoc is the paragraph docs/commands.md carries for a command's --fields keys.
func fieldsDoc(name string) string {
	quote := func(keys []string) string {
		q := make([]string, len(keys))
		for i, k := range keys {
			q[i] = "`" + k + "`"
		}
		return strings.Join(q, ", ")
	}
	var b strings.Builder
	for i, s := range commandFields[name] {
		switch {
		case i == 0:
			b.WriteString("`--fields` keys: " + quote(s.keys()) + ".")
		default:
			b.WriteString(" With `" + s.when + "`: " + quote(s.keys()) + ".")
		}
	}
	if name == "calendar" {
		b.WriteString(" Only in `calendar event`, and refused here: " + quote(eventOnlyKeys) + ".")
	}
	return b.String()
}

var docHeading = regexp.MustCompile(`(?m)^#{2,3} `)

// docs/commands.md lists each list command's --fields keys from the same table; -update rewrites
// the paragraph, at the end of the command's section.
func TestCommandsDocListsFields(t *testing.T) {
	const path = "../../docs/commands.md"
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)
	for name := range commandFields {
		head := regexp.MustCompile(`(?m)^#{2,3} ` + regexp.QuoteMeta(name) + `\n`)
		loc := head.FindStringIndex(doc)
		if loc == nil {
			t.Fatalf("docs/commands.md has no section for %s", name)
		}
		end := len(doc)
		if next := docHeading.FindStringIndex(doc[loc[1]:]); next != nil {
			end = loc[1] + next[0]
		}
		section := doc[loc[1]:end]
		want := fieldsDoc(name)
		if strings.Contains(section, want+"\n") {
			continue
		}
		if !*update {
			t.Fatalf("docs/commands.md section %s lacks (run with -update):\n%s", name, want)
		}
		lines := strings.Split(strings.TrimRight(section, "\n"), "\n")
		lines = slices.DeleteFunc(lines, func(l string) bool { return strings.HasPrefix(l, "`--fields` keys: ") })
		for len(lines) > 0 && lines[len(lines)-1] == "" {
			lines = lines[:len(lines)-1]
		}
		section = strings.Join(lines, "\n") + "\n\n" + want + "\n\n"
		doc = doc[:loc[1]] + section + doc[end:]
	}
	if *update {
		if err := os.WriteFile(path, []byte(doc), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// The Teams cache notice is given only when the whole range is newer than an account's last refresh,
// and names the account only when there are several.
func TestTeamsCacheNotice(t *testing.T) {
	fresh := time.Date(2026, 1, 11, 9, 0, 0, 0, time.UTC)
	one := &listResult{}
	one.setCoverage(nil, []calendar.AccountCoverage{{AccountID: "t/u", TeamsCacheFreshAt: fresh}}, time.Time{})
	if one.Accounts[0].TeamsCacheFreshAt == nil || !one.Accounts[0].TeamsCacheFreshAt.Equal(fresh) {
		t.Fatalf("%+v", one.Accounts)
	}
	noteTeamsCacheAge(one, fresh.Add(-time.Hour)) // today's agenda, refreshed today: no notice
	if len(one.Notices) != 0 {
		t.Fatalf("%v", one.Notices)
	}
	noteTeamsCacheAge(one, fresh.Add(24*time.Hour))
	if len(one.Notices) != 1 || !strings.Contains(one.Notices[0], "service 2026-01-11T09:00:00Z, before this range starts") {
		t.Fatalf("%v", one.Notices)
	}
	two := &listResult{}
	two.setCoverage(nil, []calendar.AccountCoverage{{AccountID: "outlook/Main"}, {AccountID: "t/u", TeamsCacheFreshAt: fresh}}, time.Time{})
	noteTeamsCacheAge(two, fresh.Add(24*time.Hour))
	if len(two.Notices) != 1 || !strings.Contains(two.Notices[0], "service t/u at 2026-01-11T09:00:00Z,") || strings.Contains(two.Notices[0], "outlook/Main") {
		t.Fatalf("%v", two.Notices)
	}
}

// A likely synonym of a command suggests that command, in the message and in the fix, and stays an error.
func TestUnknownCommandSuggestsSynonyms(t *testing.T) {
	e := newEnv(t)
	for words, want := range map[string]string{
		"chats":         "conversations",
		"chat --help":   "conversations",
		"Email list":    "mail list",
		"inbox":         "mail",
		"events":        "calendar",
		"meetings":      "calendar",
		"transcript":    "transcripts",
		"contacts":      "people",
		"serch":         "search",
		"recordings xy": "transcripts",
	} {
		code, _, errOut := e.run(append([]string{"--json"}, strings.Fields(words)...)...)
		got := errorOf(t, errOut)
		word := strings.Fields(words)[0]
		if code != 2 || got["code"] != "usage" || got["message"] != `unknown command "`+word+`", did you mean "`+want+`"?` ||
			got["fix"] != "Run `m365crawl "+want+" --help` to see what it does and its flags." {
			t.Fatalf("%s: exit %d, %v", words, code, got)
		}
	}
	for word, cmd := range commandSynonyms {
		if _, ok := commandFields[cmd]; !ok && cmd != "mail" {
			t.Errorf("%s suggests %s, which is no list command", word, cmd)
		}
	}
}
