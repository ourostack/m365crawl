package cli

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/teamscrawl/internal/errs"
)

func TestCommandsRejectBadFlagsBeforeReadingTheArchive(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string // text the usage message must contain
	}{
		{"search --fields", []string{"search", "x", "--fields", "nope"}, "--fields"},
		{"search --until", []string{"search", "x", "--until", "garbage"}, "--until"},
		{"search --limit", []string{"search", "x", "--limit", "0"}, "--limit"},
		{"messages --fields", []string{"messages", "--fields", "nope"}, "--fields"},
		{"messages --since", []string{"messages", "--since", "garbage"}, "--since"},
		{"messages --until", []string{"messages", "--until", "garbage"}, "--until"},
		{"unread --fields", []string{"unread", "--fields", "nope"}, "--fields"},
		{"unread by-conversation --fields", []string{"unread", "--by-conversation", "--fields", "nope"}, "--fields"},
		{"unread --limit", []string{"unread", "--limit", "0"}, "--limit"},
		{"thread --fields", []string{"thread", "c", "r", "--fields", "nope"}, "--fields"},
		{"thread --limit", []string{"thread", "c", "r", "--limit", "0"}, "--limit"},
		{"conversations --fields", []string{"conversations", "--fields", "nope"}, "--fields"},
		{"conversations --limit", []string{"conversations", "--limit", "0"}, "--limit"},
		{"people --fields", []string{"people", "--fields", "nope"}, "--fields"},
		{"people --limit", []string{"people", "--limit", "0"}, "--limit"},
		{"activity --fields", []string{"activity", "--fields", "nope"}, "--fields"},
		{"activity --limit", []string{"activity", "--limit", "0"}, "--limit"},
		{"activity --since", []string{"activity", "--since", "garbage"}, "--since"},
		{"sql --limit", []string{"sql", "select 1", "--limit", "0"}, "--limit"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			code, stdout, stderr := e.run(c.args...)
			if code != errs.ExitUsage {
				t.Fatalf("exit %d, want %d; stdout %q stderr %q", code, errs.ExitUsage, stdout, stderr)
			}
			body := errorOf(t, stderr)
			if body["code"] != errs.CodeUsage || !strings.Contains(body["message"].(string), c.want) {
				t.Fatalf("error = %v, want a usage error mentioning %q", body, c.want)
			}
			if stdout != "" {
				t.Fatalf("a usage error must not print a result: %q", stdout)
			}
		})
	}
}

// TestDamagedArchivesSurfaceAsDatabaseErrors drops one table at a time and checks that the command
// that needs it reports db_error (exit 1) instead of printing a partial result.
func TestDamagedArchivesSurfaceAsDatabaseErrors(t *testing.T) {
	cases := []struct {
		name   string
		damage string
		args   []string
	}{
		{"search", "drop table message_fts", []string{"search", "Fixture"}},
		{"messages", "drop table messages", []string{"messages"}},
		{"messages html", "alter table messages drop column content_html", []string{"messages", "--html"}},
		{"search html", "alter table messages drop column content_html", []string{"search", "Fixture", "--html"}},
		{"unread", "drop table messages", []string{"unread"}},
		{"unread by conversation", "drop table messages", []string{"unread", "--by-conversation"}},
		{"thread", "drop table messages", []string{"thread", "19:abc@thread.v2", "1"}},
		{"conversations", "drop table conversations", []string{"conversations"}},
		{"people", "drop table people", []string{"people"}},
		{"activity", "drop table activity", []string{"activity"}},
		{"status", "drop table activity", []string{"status"}},
		{"whoami accounts", "drop table people", []string{"whoami"}},
		{"whoami status", "drop table activity", []string{"whoami"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			e.sync()
			e.exec(c.damage)
			code, stdout, stderr := e.run(append([]string{"--max-age", "0"}, c.args...)...)
			if code != errs.ExitRuntime || errorOf(t, stderr)["code"] != errs.CodeDBError {
				t.Fatalf("exit %d, stdout %q, stderr %s", code, stdout, stderr)
			}
			if stdout != "" {
				t.Fatalf("a failed read must not print a result: %q", stdout)
			}
		})
	}
}

func TestSQLInterruptedMidQueryIsCoded(t *testing.T) {
	e := newEnv(t)
	e.sync()
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(300*time.Millisecond, cancel)
	defer cancel()
	var out, errb syncBuf
	code := runCLI(ctx, []string{"--db", e.db, "--teams-root", e.root, "--max-age", "0", "--json", "sql",
		"with recursive c(x) as (select 1 union all select x+1 from c) select count(*) from c"}, &out, &errb)
	if code != errs.ExitRuntime || errorOf(t, errb.String())["code"] != errs.CodeInterrupted {
		t.Fatalf("exit %d, stderr %s", code, errb.String())
	}
}

func TestSQLShapesEmptyAndTruncatedResults(t *testing.T) {
	e := newEnv(t)
	e.sync()
	code, stdout, stderr := e.run("--max-age", "0", "sql", "select 1 as one where 0")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	m := decode(t, stdout)
	if rows, ok := m["rows"].([]any); !ok || len(rows) != 0 || m["count"].(float64) != 0 || m["truncated"] != false {
		t.Fatalf("empty result = %v", m)
	}
	code, stdout, stderr = e.run("--max-age", "0", "sql", "values (1),(2),(3)", "--limit", "2")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	m = decode(t, stdout)
	if len(m["rows"].([]any)) != 2 || m["count"].(float64) != 2 || m["truncated"] != true {
		t.Fatalf("truncated result = %v", m)
	}
}

func TestStatusAndWhoamiFallBackToTheDefaultRoot(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var out, errb syncBuf
	dbPath := t.TempDir() + "/a.db"
	code := Main([]string{"--db", dbPath, "--max-age", "0", "status"}, &out, &errb)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if m := decode(t, out.String()); m["archive_exists"] != false {
		t.Fatalf("status = %v", m)
	}
}

func TestThreadWithoutAnArchiveIsAnEmptyList(t *testing.T) {
	e := newEnv(t)
	code, stdout, stderr := e.run("--max-age", "0", "thread", "19:abc@thread.v2", "1")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	m := decode(t, stdout)
	if len(items(t, m)) != 0 || m["needs_sync"] != true {
		t.Fatalf("thread without archive = %v", m)
	}
}

func TestSQLErrorsAreUsageErrorsWithTheDatabaseMessage(t *testing.T) {
	e := newEnv(t)
	e.sync()
	code, _, stderr := e.run("--max-age", "0", "sql", "select * from no_such_table")
	body := errorOf(t, stderr)
	if code != errs.ExitUsage || body["code"] != errs.CodeUsage || !strings.Contains(body["message"].(string), "no_such_table") {
		t.Fatalf("exit %d, error %v", code, body)
	}
}
