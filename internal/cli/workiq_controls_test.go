package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openclaw/crawlkit/output"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/syncer"
)

func TestWorkIQTextAndLogResultsKeepScopeGapsAndOutputErrors(t *testing.T) {
	total := int64(4)
	result := workIQResult{Status: "ok_with_omissions", WorkIQReport: syncer.WorkIQReport{
		Kind: "coverage", Enabled: true,
		Accounts: []syncer.WorkIQAccountReport{{Family: "mail", Account: "outlook/One", Status: "ok_with_omissions"}},
		Mail:     []syncer.WorkIQMailCoverage{{FolderID: "unknown"}, {FolderID: "known", ServerTotal: &total}},
		Chats:    []syncer.WorkIQChatCoverage{{Conversation: "chat", ServerCount: 2, ArchiveCount: 1}},
		Probes:   []syncer.WorkIQPathProbe{{Family: "mail", PathClass: "mail_folders", State: "allowed"}},
		Gaps:     map[string]int{"workiq_clock_unknown": 1},
	}}
	for _, format := range []output.Format{output.Text, output.JSON, output.Log} {
		var out bytes.Buffer
		rt := newRuntime(context.Background(), &Globals{}, &out, &bytes.Buffer{})
		rt.format, rt.cmd = format, "workiq coverage"
		if err := rt.write(rt.cmd, result); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "workiq_clock_unknown") || !strings.Contains(out.String(), "coverage") {
			t.Fatal("scope or gaps were lost from an output format", format)
		}
		rt.stdout = failWriter{}
		if err := rt.write(rt.cmd, result); err == nil {
			t.Fatal("failed result output was reported as success")
		}
	}
	rt := newRuntime(context.Background(), &Globals{}, &bytes.Buffer{}, &bytes.Buffer{})
	if err := (workIQToggleResult{Enabled: true}).renderWorkIQ(rt); err != nil {
		t.Fatal(err)
	}
	rt.stdout = failWriter{}
	if err := (workIQToggleResult{}).renderWorkIQ(rt); !errors.Is(err, os.ErrClosed) {
		t.Fatal("failed configuration output was swallowed", err)
	}
}

func TestWorkIQConfigurationAndReadOutputFailuresRemainFailures(t *testing.T) {
	e := newEnv(t)
	e.emptyArchive()
	release, err := store.AcquireWriter(context.Background(), e.db, store.WriterOrdinarySync)
	if err != nil {
		t.Fatal(err)
	}
	code, _, stderr := e.run("--json", "workiq", "enable")
	release()
	if code == 0 || errorOf(t, stderr)["code"] != "locked" {
		t.Fatal("configuration bypassed an ordinary writer", code, stderr)
	}
	workIQCLITransport(t, e.db)
	seedMail(t, e, nil)
	if code, _, stderr := e.run("--json", "workiq", "enable"); code != 0 {
		t.Fatal(stderr)
	}
	t.Setenv("WORKIQ_PROBE_CASE", "folder-fail")
	code, out, stderr := e.run("--json", "--account", mailAccount, "workiq", "coverage", "--source", "mail")
	if code == 0 || decode(t, out)["status"] != "partial" || stderr == "" {
		t.Fatal("failed accounted probe became positive output", code, out, stderr)
	}
	full := []string{"--db", e.db, "--teams-root", e.root, "--json", "--account", mailAccount, "workiq", "status", "--source", "mail"}
	if code := Main(full, failWriter{}, &bytes.Buffer{}); code == 0 {
		t.Fatal("failed stdout became a positive command")
	}
}

func TestWorkIQDoctorDoesNotSpawnWhenDisabledAndFailsClosedOnSettings(t *testing.T) {
	e := newEnv(t)
	e.emptyArchive()
	t.Setenv("M365CRAWL_WORKIQ_BIN", filepath.Join(t.TempDir(), "must-not-start"))
	rt := newRuntime(context.Background(), &Globals{DB: e.db, TeamsRoot: e.root}, &bytes.Buffer{}, &bytes.Buffer{})
	if err := rt.setup(); err != nil {
		t.Fatal(err)
	}
	if checks := rt.workIQDoctorChecks(); len(checks) != 0 {
		t.Fatal("disabled source added a doctor check or probed WorkIQ")
	}
	e.exec(`insert into meta(key,value) values('workiq_enabled','invalid')`)
	if checks := rt.workIQDoctorChecks(); len(checks) != 1 || checks[0].OK {
		t.Fatal("corrupt opt-in was silently treated as disabled")
	}
	e.exec(`update meta set value='1' where key='workiq_enabled'`)
	if checks := rt.workIQDoctorChecks(); len(checks) != 1 || checks[0].OK ||
		checks[0].Detail != "workiq_not_installed" {
		t.Fatal("enabled doctor lost the fixed executable refusal")
	}
	old := openArchiveReadOnly
	t.Cleanup(func() { openArchiveReadOnly = old })
	openArchiveReadOnly = func(context.Context, string) (*store.Store, error) { return nil, store.ErrNoArchive }
	if checks := rt.workIQDoctorChecks(); len(checks) != 0 {
		t.Fatal("missing archive invented optional settings")
	}
	openArchiveReadOnly = func(context.Context, string) (*store.Store, error) { return nil, errors.New("private archive failure") }
	if checks := rt.workIQDoctorChecks(); len(checks) != 1 || checks[0].OK || strings.Contains(checks[0].Detail, "private") {
		t.Fatal("archive failure became positive or leaked its raw diagnostic")
	}
}

func TestWorkIQMetadataDeclaresExplicitEffectsWithoutLocalNetworkClaim(t *testing.T) {
	m := manifest()
	for _, command := range []string{"enable", "disable", "sync", "coverage", "status"} {
		entry, ok := m.Commands["workiq "+command]
		if !ok || !entry.JSON || entry.Mutates != (command == "enable" || command == "disable" || command == "sync") {
			t.Fatal("manifest lost explicit source operation/effect", command)
		}
	}
	for _, scope := range m.Privacy.LocalOnlyScopes {
		if strings.Contains(scope, "workiq") {
			t.Fatal("server source was described as local-only")
		}
	}
}
