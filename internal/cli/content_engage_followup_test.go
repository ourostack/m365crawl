package cli

import (
	"bytes"
	"context"
	"github.com/ourostack/m365crawl/internal/engagecontent"
	"os"
	"strings"
	"testing"
)

func TestEngageGeneratedFullSQLKeepsSelectedArchive(t *testing.T) {
	e := contentFollowupEnv(t)
	engageControlled(t, &engageFixturePage{})
	saved := observeEngageHome
	t.Cleanup(func() { observeEngageHome = saved })
	observeEngageHome = func(context.Context, engagecontent.Driver) (engagecontent.Result, error) {
		return engageCLIResult("A"), nil
	}
	if code, _, stderr := e.run("content", "fetch", "engage-home"); code != 0 {
		t.Fatal(stderr)
	}
	code, out, stderr := e.run("content", "show", "engage-observation:1")
	if code != 0 {
		t.Fatal(stderr)
	}
	item := decode(t, out)["items"].([]any)[0].(map[string]any)
	command, ok := item["full_text_command"].(string)
	if !ok {
		t.Fatal("no scoped executable full-record follow-up")
	}
	words := requireContentFollowupArchive(t, e, command)
	if !strings.Contains(command, "--no-sync") || words[len(words)-1] != item["full_text_sql"] {
		t.Fatal("full SQL intent changed:", command)
	}
	before, err := os.ReadFile(e.db)
	if err != nil {
		t.Fatal(err)
	}
	stat, err := os.Stat(e.db)
	if err != nil {
		t.Fatal(err)
	}
	if code, full, stderr := replayContentFollowup(t, e, command); code != 0 || !strings.Contains(full, "syntheticword") {
		t.Fatalf("actual emitted SQL replay: %s %s", full, stderr)
	}
	after, err := os.ReadFile(e.db)
	if err != nil {
		t.Fatal(err)
	}
	end, err := os.Stat(e.db)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) || !stat.ModTime().Equal(end.ModTime()) {
		t.Fatal("emitted offline SQL mutated selected archive")
	}
}
