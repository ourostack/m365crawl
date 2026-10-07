package cli

import (
	"bytes"
	"os"
	"testing"
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
