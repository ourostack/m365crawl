package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/alecthomas/kong"
)

// The command table help --json prints must be the table the actions declaration generated.
func TestHelpCommandsMatchGeneratedCommandsJSON(t *testing.T) {
	raw, err := os.ReadFile("../../actions/generated/commands.json")
	if err != nil {
		t.Fatal(err)
	}
	var generated struct {
		Commands []struct {
			Name     string `json:"name"`
			Effect   string `json:"effect"`
			ReadOnly bool   `json:"readOnly"`
		} `json:"commands"`
	}
	if err := json.Unmarshal(raw, &generated); err != nil {
		t.Fatal(err)
	}
	var app cliApp
	parser, err := kong.New(&app, kong.Name("m365crawl"))
	if err != nil {
		t.Fatal(err)
	}
	registered := commandsOf(parser.Model.Node)
	if len(registered) != len(generated.Commands) {
		t.Fatalf("the CLI registers %d commands, commands.json declares %d", len(registered), len(generated.Commands))
	}
	byName := map[string]helpCommand{}
	for _, c := range registered {
		byName[c.Name] = c
	}
	for _, want := range generated.Commands {
		got, ok := byName[want.Name]
		if !ok {
			t.Errorf("commands.json declares %q, which the CLI does not register", want.Name)
			continue
		}
		if got.Effect != want.Effect || got.ReadOnly != want.ReadOnly {
			t.Errorf("%q: CLI says effect %s readOnly %t, commands.json says %s %t", want.Name, got.Effect, got.ReadOnly, want.Effect, want.ReadOnly)
		}
	}
}

func TestHelpPrintsCommandsAndExitCodesAsJSON(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var out, errb bytes.Buffer
	if code := Main([]string{"help", "--json"}, &out, &errb); code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, errb.String())
	}
	var doc helpDocument
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatalf("help --json is not one JSON document: %v\n%s", err, out.String())
	}
	effects := map[string]string{}
	for _, c := range doc.Commands {
		effects[c.Name] = c.Effect
	}
	if effects["writes send-mail"] != "others" || effects["mail list"] != "private" {
		t.Fatalf("effects = %v, want writes send-mail others and mail list private", effects)
	}
	if len(doc.ExitCodes) == 0 || doc.ExitCodes[0].Code != 0 {
		t.Fatalf("exit codes = %+v, want the documented list starting at 0", doc.ExitCodes)
	}
}

func TestHelpTextListsCommands(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var out, errb bytes.Buffer
	if code := Main([]string{"--format", "text", "help"}, &out, &errb); code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, errb.String())
	}
	if !strings.Contains(out.String(), "mail list  ") {
		t.Fatalf("text help = %q, want one line per command", out.String())
	}
}

func TestAmbiguousVerbsNameTheCommandsThatAct(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, verb := range []string{"edit", "delete", "remove"} {
		var out, errb bytes.Buffer
		if code := Main([]string{verb}, &out, &errb); code != 2 {
			t.Fatalf("%s: exit code = %d, want 2", verb, code)
		}
		if !strings.Contains(errb.String(), "m365crawl writes ") || out.Len() != 0 {
			t.Fatalf("%s: stdout = %q, stderr = %q; want nothing on stdout and a command named on stderr", verb, out.String(), errb.String())
		}
	}
}
