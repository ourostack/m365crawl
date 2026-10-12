package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/alecthomas/kong"
	"github.com/openclaw/crawlkit/output"
)

// leafCommands is every runnable command path of the command line ("docs list", "status").
func leafCommands(t *testing.T) []*kong.Node {
	t.Helper()
	var app cliApp
	p, err := kong.New(&app)
	if err != nil {
		t.Fatal(err)
	}
	var out []*kong.Node
	var walk func(n *kong.Node)
	walk = func(n *kong.Node) {
		if n.Type == kong.CommandNode && len(n.Children) == 0 {
			out = append(out, n)
		}
		for _, c := range n.Children {
			walk(c)
		}
	}
	walk(p.Model.Node)
	return out
}

func commandPath(n *kong.Node) string {
	var parts []string
	for ; n != nil && n.Type == kong.CommandNode; n = n.Parent {
		parts = append([]string{n.Name}, parts...)
	}
	return strings.Join(parts, " ")
}

func TestEveryCommandLabelIsAValidLogLabel(t *testing.T) {
	for _, n := range leafCommands(t) {
		var buf bytes.Buffer
		label := logLabel(commandPath(n))
		if err := output.Write(&buf, output.Log, label, map[string]any{"ok": true}); err != nil ||
			!strings.HasPrefix(buf.String(), label+"=") {
			t.Fatalf("%q: log label %q refused: %v", commandPath(n), label, err)
		}
	}
	if got := logLabel("docs list"); got != "docs_list" {
		t.Fatalf("logLabel(docs list) = %q", got)
	}
}

// TestEveryCommandRunsUnderLogFormat runs each command with --format log on a synced fixture
// archive: a command may refuse its placeholder argument, but none may fail with an internal error.
// skill (Markdown) and metadata (the crawlkit manifest) print their document in every format.
func TestEveryCommandRunsUnderLogFormat(t *testing.T) {
	skip := map[string]string{
		"watch":              "runs until interrupted",
		"transcripts signin": "opens a browser window for sign-in",
	}
	e := newEnv(t)
	e.sync()
	ran := 0
	for _, n := range leafCommands(t) {
		path := commandPath(n)
		if skip[path] != "" {
			continue
		}
		args := append([]string{"--format", "log", "--max-age", "0"}, strings.Fields(path)...)
		for _, a := range n.Positional {
			if a.Required {
				args = append(args, "placeholder")
			}
		}
		code, stdout, stderr := e.run(args...)
		if strings.Contains(stderr, `"code":"internal"`) || strings.Contains(stderr, "invalid log label") {
			t.Fatalf("%s under --format log: exit %d: %s", path, code, stderr)
		}
		group := path // a group's default subcommand ("people list") prints the group's label
		if n.Parent != nil && n.Parent.Type == kong.CommandNode {
			group = commandPath(n.Parent)
		}
		if code == 0 && path != "skill" && path != "metadata" && !strings.HasPrefix(stdout, logLabel(path)+"=") && !strings.HasPrefix(stdout, logLabel(group)+"=") {
			t.Fatalf("%s under --format log printed %q", path, firstLine(stdout))
		}
		ran++
	}
	if ran < 30 {
		t.Fatalf("only %d commands ran", ran)
	}
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}
