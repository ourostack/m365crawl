package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/openclaw/crawlkit/output"
	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/syncer"
)

func TestWorkIQResultUsesTheSyncersResumableAsIs(t *testing.T) {
	e := newEnv(t)
	e.emptyArchive()
	old := runWorkIQSyncer
	t.Cleanup(func() { runWorkIQSyncer = old })
	limited := &errs.Coded{Code: "workiq_page_limit", Exit: 1, Message: "page budget"}
	for _, resumable := range []bool{true, false} {
		runWorkIQSyncer = func(context.Context, syncer.WorkIQOptions) (syncer.WorkIQReport, error) {
			return syncer.WorkIQReport{Resumable: resumable, Accounts: []syncer.WorkIQAccountReport{{Family: "mail", Account: mailAccount,
				Status: "failed", Gaps: map[string]int{"workiq_page_limit": 1}}}}, limited
		}
		_, out, _ := e.run("--json", "--account", mailAccount, "workiq", "sync", "--source", "mail")
		if doc := decode(t, out); doc["status"] != "partial" || doc["resumable"] != resumable {
			t.Fatalf("resumable=%t became %v", resumable, doc["resumable"])
		}
	}
}

func TestWorkIQStartupFailureWithAnAccountIsNotResumable(t *testing.T) {
	e := newEnv(t)
	seedMail(t, e, nil)
	workIQCLITransport(t, e.db)
	t.Setenv("HOME", t.TempDir())
	if code, _, stderr := e.run("--json", "workiq", "enable"); code != 0 {
		t.Fatal(stderr)
	}
	t.Setenv("WORKIQ_ACQUISITION_ME", "fail")
	code, out, stderr := e.run("--json", "--account", mailAccount, "workiq", "sync", "--source", "mail")
	if code == 0 || stderr == "" {
		t.Fatal("startup failure succeeded", code)
	}
	doc := decode(t, out)
	if doc["status"] != "partial" || doc["resumable"] != false {
		t.Fatalf("a startup failure invited a blind retry: %v", doc)
	}
}

func TestWorkIQRetryGuidanceNamesOnlyTheResumableSignal(t *testing.T) {
	e := routeEnv(t)
	steps := routesOf(t, mailJSON(t, e, "mail", "list", "--since", "2026-01-01"))[0]["next"].([]any)
	does := steps[2].(map[string]any)["does"].(string)
	if strings.Contains(does, "partial") || !strings.Contains(does, "resumable: true") || !strings.Contains(does, "fix") {
		t.Fatalf("sync step guidance = %q", does)
	}
	guide := skillText(t)
	if strings.Contains(guide, "again while it ends `partial`") || !strings.Contains(guide, "resumable: true") {
		t.Fatal("the agent guide still retries any partial WorkIQ result")
	}
}

func TestWorkIQResumableTextSaysToRunAgain(t *testing.T) {
	for _, resumable := range []bool{true, false} {
		var out bytes.Buffer
		rt := newRuntime(t.Context(), &Globals{}, &out, &bytes.Buffer{})
		rt.format, rt.cmd = output.Text, "workiq sync"
		if err := rt.write(rt.cmd, workIQResult{Status: "partial", Resumable: resumable, WorkIQReport: syncer.WorkIQReport{Kind: "sync"}}); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out.String(), "run the same command again") != resumable {
			t.Fatalf("resumable=%t text:\n%s", resumable, out.String())
		}
	}
}

func TestWorkIQAccountRequiredProbeNamesItsExactCommand(t *testing.T) {
	e := newEnv(t)
	e.emptyArchive()
	old := runWorkIQSyncer
	t.Cleanup(func() { runWorkIQSyncer = old })
	runWorkIQSyncer = func(context.Context, syncer.WorkIQOptions) (syncer.WorkIQReport, error) {
		return syncer.WorkIQReport{Gaps: map[string]int{"workiq_account_required": 1}, Probes: []syncer.WorkIQPathProbe{{Family: "mail",
			Account: mailAccount, PathClass: "mail_folders", State: "account_required", Code: "workiq_account_required", Scope: "initial_page_only"}}}, nil
	}
	code, out, stderr := e.run("--json", "workiq", "status")
	doc := decode(t, out)
	probe := doc["probes"].([]any)[0].(map[string]any)
	if code != 0 || doc["status"] != "ok_with_omissions" || !strings.Contains(probe["next"].(string), "--account outlook/Main workiq status --source mail") {
		t.Fatal("account-required probe has no exact command", code, stderr, probe)
	}
	if _, text, _ := e.run("--format", "text", "workiq", "status"); !strings.Contains(text, "next: m365crawl ") {
		t.Fatal("text output lost the account-required command", text)
	}
}

func TestWorkIQNarrowedScopeNoticeIsShown(t *testing.T) {
	e := newEnv(t)
	e.emptyArchive()
	old := runWorkIQSyncer
	t.Cleanup(func() { runWorkIQSyncer = old })
	runWorkIQSyncer = func(context.Context, syncer.WorkIQOptions) (syncer.WorkIQReport, error) {
		return syncer.WorkIQReport{Notices: []string{"the account outlook/Main selects mail only; chats were not checked"}}, nil
	}
	_, out, _ := e.run("--json", "--account", mailAccount, "workiq", "status")
	if notices, _ := decode(t, out)["notices"].([]any); len(notices) != 1 {
		t.Fatal("JSON lost the narrowed-scope notice", out)
	}
	if _, text, _ := e.run("--format", "text", "--account", mailAccount, "workiq", "status"); !strings.Contains(text, "note: the account outlook/Main selects mail only") {
		t.Fatal("text output lost the narrowed-scope notice", text)
	}
}
