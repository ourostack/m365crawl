package cli

import (
	"bytes"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/alecthomas/kong"

	"github.com/ourostack/m365crawl/internal/errs"
)

// The embedded guide is the repository's SKILL.md, printed raw in every output mode.
func TestSkillPrintsTheEmbeddedGuide(t *testing.T) {
	want, err := os.ReadFile("../../.agents/skills/m365crawl/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"skill"}, {"skill", "--json"}, {"skill", "--format", "text"}, {"--format", "log", "skill"}} {
		var out, errb bytes.Buffer
		if code := Main(args, &out, &errb); code != 0 || errb.Len() != 0 {
			t.Fatalf("%v: exit %d, stderr %q", args, code, errb.String())
		}
		if out.String() != string(want) {
			t.Fatalf("%v: output differs from SKILL.md", args)
		}
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, os.ErrClosed }

func TestSkillReportsAWriteFailure(t *testing.T) {
	var errb bytes.Buffer
	if code := Main([]string{"skill"}, failWriter{}, &errb); code == 0 {
		t.Fatal("a failed write must not exit 0")
	}
}

// skillMaxLines bounds the guide: what --help says is not repeated in it.
const skillMaxLines = 60

func skillText(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../../.agents/skills/m365crawl/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// codeSpans are the `backticked` spans of a Markdown text.
func codeSpans(s string) []string {
	parts := strings.Split(s, "`")
	var out []string
	for i := 1; i < len(parts); i += 2 {
		out = append(out, parts[i])
	}
	return out
}

// shellWords splits a command line on spaces, keeping a "double-quoted" word whole, and stands a
// value in for each <placeholder>.
func shellWords(s string) []string {
	var out []string
	var cur strings.Builder
	quoted, any := false, false
	for _, r := range s {
		switch {
		case r == '"':
			quoted, any = !quoted, true
		case r == ' ' && !quoted:
			if any {
				out = append(out, cur.String())
			}
			cur.Reset()
			any = false
		default:
			cur.WriteRune(r)
			any = true
		}
	}
	if any {
		out = append(out, cur.String())
	}
	for i, w := range out {
		if strings.HasPrefix(w, "<") && strings.HasSuffix(w, ">") {
			out[i] = "x"
		}
	}
	return out
}

// skillKeys are the JSON keys and error codes the guide may name.
func skillKeys() []string {
	keys := []string{errs.CodeMailUnsupportedPlatform, errs.CodeSigninRequired}
	for _, typ := range []reflect.Type{reflect.TypeFor[listResult](), reflect.TypeFor[meta](), reflect.TypeFor[messageItem](), reflect.TypeFor[mailListItem](),
		reflect.TypeFor[mailShowItem](), reflect.TypeFor[calendarItem](), reflect.TypeFor[errorBody](), reflect.TypeFor[overviewResult](), reflect.TypeFor[overviewSource](),
		reflect.TypeFor[calendarExtras](), reflect.TypeFor[calendarChat](), reflect.TypeFor[relatedMail](), reflect.TypeFor[personItem]()} {
		keys = append(keys, jsonKeys(typ)...)
	}
	return keys
}

var (
	snakeWord = regexp.MustCompile(`^[a-z]+(_[a-z]+)+$|^(note|fix|truncated)$`)
	// nestedField is a field inside another, such as chat.recent_messages.
	nestedField = regexp.MustCompile(`^[a-z_]+\.[a-z_]+$`)
)

// The guide is short, and every command, flag, --fields key and field it names exists.
func TestSkillNamesOnlyWhatExists(t *testing.T) {
	text := skillText(t)
	if n := strings.Count(strings.TrimSuffix(text, "\n"), "\n") + 1; n > skillMaxLines {
		t.Errorf("SKILL.md has %d lines, more than %d", n, skillMaxLines)
	}
	helps, keys := helpTexts(t), skillKeys()
	flagExists := func(flag string) bool {
		for where := range helps {
			if where == flag || strings.HasSuffix(where, " "+flag) {
				return true
			}
		}
		// --user-agreed belongs to transcripts signin, which the fetch change (#130) adds; the guide
		// already carries that change's wording so the two merge cleanly.
		return flag == "--help" || flag == "--user-agreed"
	}
	commands := map[string]bool{}
	for _, span := range codeSpans(text) {
		switch {
		case strings.HasPrefix(span, "m365crawl"):
			var args []string
			for _, w := range shellWords(span)[1:] {
				if w != "--help" { // help ends the parse; the rest of the line is what is checked
					args = append(args, w)
				}
			}
			var app cliApp
			p, err := kong.New(&app)
			if err != nil {
				t.Fatal(err)
			}
			kctx, err := p.Parse(args)
			if err != nil {
				t.Errorf("`%s` does not parse: %v", span, err)
				continue
			}
			commands[commandName(kctx)] = true
			for i, a := range args {
				if a == "--fields" && i+1 < len(args) {
					for _, k := range splitFields(args[i+1]) {
						if !contains(keys, k) {
							t.Errorf("`%s`: no field %q", span, k)
						}
					}
				}
			}
		case strings.HasPrefix(span, "--"):
			if name, _, _ := strings.Cut(span, " "); !flagExists(name) {
				t.Errorf("no flag %s", name)
			}
		case nestedField.MatchString(span):
			for _, k := range strings.Split(span, ".") {
				if !contains(keys, k) {
					t.Errorf("no field %q in %q", k, span)
				}
			}
		case snakeWord.MatchString(span):
			if !contains(keys, span) {
				t.Errorf("no field or code %q", span)
			}
		}
	}
	for _, c := range []string{"overview", "mail list", "mail show", "mail thread", "mail folders", "mail unread", "search", "calendar", "calendar event", "transcripts", "transcripts show"} {
		if !commands[c] {
			t.Errorf("SKILL.md names no `m365crawl %s` command", c)
		}
	}
}

// Each of the five jobs (triage, recall, threads, cross-source with meeting prep, attachments) has
// a command line in the Start here block or in the guide's table of jobs.
func TestFiveJobsHaveCommands(t *testing.T) {
	rows := map[string]string{}
	for _, line := range strings.Split(skillText(t), "\n") {
		if cells := strings.Split(line, "|"); len(cells) > 3 && strings.Contains(cells[2], "`m365crawl ") {
			rows[strings.TrimSpace(cells[1])] = cells[2]
		}
	}
	start := startHereText(startHere)
	for job, want := range map[string]string{
		"Triage":                        "unread",
		"Recall":                        "search",
		"Threads":                       "thread",
		"Cross-source and meeting prep": "calendar event",
		"Attachments":                   "--has-attachments",
	} {
		found := strings.Contains(start, want) && job == "Attachments"
		for name, cmds := range rows {
			found = found || strings.HasPrefix(name, job) && strings.Contains(cmds, want)
		}
		if !found {
			t.Errorf("job %q has no command with %q", job, want)
		}
	}
}

// A channel reply's own id reads only that reply, so the guide gives thread the root: the
// message's reply_chain_id, else its id.
func TestSkillThreadTakesTheRoot(t *testing.T) {
	text := skillText(t)
	for _, span := range codeSpans(text) {
		if strings.HasPrefix(span, "m365crawl thread ") && span != "m365crawl thread <conversation_id> <root_id>" {
			t.Errorf("`%s`: thread takes <conversation_id> <root_id>", span)
		}
	}
	if n := strings.Count(text, "`reply_chain_id`, else `id`"); n < 2 {
		t.Errorf("the guide says %d times that the root is `reply_chain_id`, else `id`; want both thread lines to", n)
	}
}

// The guide names the transcript commands, and that fetch alone uses the network.
func TestSkillNamesTranscriptsCommands(t *testing.T) {
	text := skillText(t)
	for _, want := range []string{"`m365crawl transcripts`", "`m365crawl transcripts show <meeting>`", "`transcripts fetch`", "--source transcripts`", "`transcripts fetch` and `transcripts signin` are the only commands that use the network"} {
		if !strings.Contains(text, want) {
			t.Errorf("SKILL.md does not say %s", want)
		}
	}
}
