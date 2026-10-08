package cli

import (
	"sort"
	"strings"
	"testing"

	"github.com/alecthomas/kong"
)

// helpTexts is every help: string of the command line, keyed by where it is: a command path, or
// a command path and a flag or argument ("search --mentions-me", "thread <target>", "--teams-root").
func helpTexts(t *testing.T) map[string]string {
	t.Helper()
	var app cliApp
	p, err := kong.New(&app)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	var walk func(n *kong.Node, path string)
	walk = func(n *kong.Node, path string) {
		if path != "" {
			out[path] = n.Help
		}
		prefix := path
		if prefix != "" {
			prefix += " "
		}
		for _, f := range n.Flags {
			out[prefix+"--"+f.Name] = f.Help
		}
		for _, a := range n.Positional {
			out[prefix+"<"+a.Name+">"] = a.Help
		}
		for _, c := range n.Children {
			walk(c, prefix+c.Name)
		}
	}
	walk(p.Model.Node, "")
	return out
}

// teamsHelp lists every help string that may say "Teams", and why: the command or flag is
// Teams-only, or the text names Teams beside Outlook. Every other help string speaks of the archive.
var teamsHelp = map[string]string{
	"--account":                      "names both: a Teams account or an Outlook account",
	"--outlook-account":              "names both: links an Outlook profile to a Teams account",
	"--teams-root":                   "Teams-only: the Teams cache directory",
	"doctor":                         "names both: checks Teams and Outlook",
	"sync":                           "names both: reads Teams chats, Outlook mail and the calendar",
	"status":                         "Teams-only part: other Teams origins",
	"calendar":                       "names both: the Teams and Outlook caches",
	"messages":                       "Teams-only: says so and points to mail list",
	"unread":                         "Teams-only: says so and points to mail unread",
	"thread":                         "Teams-only: a Teams message link; points to mail thread",
	"thread <target>":                "Teams-only: a Teams message link",
	"watch":                          "Teams-only: watches the Teams cache",
	"watch --min-interval":           "Teams-only: the Teams cache's pace",
	"records --database":             "Teams-only: Teams' own database names",
	"records --include-removed":      "Teams-only: Teams' cache",
	"activity --include-system":      "Teams-only: Teams' system conversations",
	"conversations --include-system": "Teams-only: Teams' system conversations",
	"messages --include-system":      "Teams-only: Teams' system conversations",
	"search":                         "names both: Teams chats and Outlook mail",
	"search --source":                "names both: chats (Teams), mail (Outlook) or all",
	"search --folder":                "names both: mail only, so it leaves Teams chats out",
	"search --include-deleted":       "Teams-only: says Teams chats only",
	"search --mentions-me":           "Teams-only: says Teams chats only",
	"search --direct-mentions":       "Teams-only: says Teams chats only",
	"search --html":                  "Teams-only: says Teams chats only",
	"search --include-system":        "Teams-only: Teams' system conversations",
	"thread --include-system":        "Teams-only: Teams' system conversations",
	"unread --include-system":        "Teams-only: Teams' system conversations",
}

// No help string says "Teams" where it means the archive: only the strings of teamsHelp may, and
// each of those still does (the list holds no stale entry).
func TestHelpSaysTeamsOnlyWhenItMeansTeams(t *testing.T) {
	texts := helpTexts(t)
	var bad []string
	for where, help := range texts {
		if strings.Contains(help, "Teams") && teamsHelp[where] == "" {
			bad = append(bad, where+": "+help)
		}
	}
	sort.Strings(bad)
	if len(bad) > 0 {
		t.Errorf("help that says Teams but is not in teamsHelp:\n%s", strings.Join(bad, "\n"))
	}
	for where := range teamsHelp {
		if !strings.Contains(texts[where], "Teams") {
			t.Errorf("teamsHelp lists %q, whose help no longer says Teams", where)
		}
	}
}

// The Teams-only reading commands say so and name their mail counterpart.
func TestTeamsOnlyCommandsPointToMail(t *testing.T) {
	texts := helpTexts(t)
	for cmd, mail := range map[string]string{"messages": "m365crawl mail list", "unread": "m365crawl mail unread", "thread": "m365crawl mail thread"} {
		if h := texts[cmd]; !strings.Contains(h, "Teams chats and channels; for Outlook mail use `"+mail) {
			t.Errorf("%s: %q", cmd, h)
		}
	}
	if h := texts["mail show"]; !strings.Contains(h, "name, size and type") {
		t.Errorf("mail show: %q", h)
	}
}
