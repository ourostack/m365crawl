package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/openclaw/crawlkit/control"
)

func TestMetadataGolden(t *testing.T) {
	e := newEnv(t)
	code, stdout, stderr := e.run("metadata", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	checkGolden(t, "metadata.json", stdout)
}

func TestMetadataParsesAsManifest(t *testing.T) {
	e := newEnv(t)
	for _, args := range [][]string{{"metadata", "--json"}, {"metadata"}, {"--format", "text", "metadata"}} {
		code, stdout, stderr := e.run(args...)
		if code != 0 || stderr != "" {
			t.Fatalf("%v: exit %d, stderr %q", args, code, stderr)
		}
		var m control.Manifest
		if err := json.Unmarshal([]byte(stdout), &m); err != nil {
			t.Fatalf("%v: not a manifest: %v\n%s", args, err, stdout)
		}
		if m.ID != "m365crawl" || !m.Commands["sync"].Mutates {
			t.Fatalf("%v: manifest = %+v", args, m)
		}
		if m.Commands["status"].Mutates || !m.Commands["status"].JSON {
			t.Fatalf("%v: status command = %+v", args, m.Commands["status"])
		}
	}
}

func TestMetadataCommandsResolve(t *testing.T) {
	for name, c := range manifest().Commands {
		if len(c.Argv) < 2 || c.Argv[0] != "m365crawl" {
			t.Fatalf("%s: argv = %v", name, c.Argv)
		}
		var app cliApp
		p, err := newParser(&app)
		if err != nil {
			t.Fatal(err)
		}
		kctx, err := p.Parse(c.Argv[1:])
		if err != nil {
			t.Fatalf("%s: %v does not parse: %v", name, c.Argv, err)
		}
		if got := commandName(kctx); got != name {
			t.Fatalf("%s: argv resolves to command %q", name, got)
		}
	}
}

func TestMetadataNeverSyncs(t *testing.T) {
	e := newEnv(t)
	e.sync()
	code, _, stderr := e.run("--max-age", "1ns", "metadata", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("metadata must not sync: exit %d, stderr %q", code, stderr)
	}
	if n := syncRunCount(t, e); n != 1 {
		t.Fatalf("sync runs = %d, want 1", n)
	}
}

func TestMetadataReportsAWriteFailure(t *testing.T) {
	if code := Main([]string{"metadata"}, failWriter{}, &strings.Builder{}); code == 0 {
		t.Fatal("a failed write must not exit 0")
	}
}
