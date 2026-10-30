package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/store"
)

func TestWorkIQConfigurationIsExplicitIdempotentAndDoesNotSpawn(t *testing.T) {
	e := newEnv(t)
	e.emptyArchive()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("M365CRAWL_WORKIQ_BIN", filepath.Join(t.TempDir(), "must-not-start"))
	for _, command := range []string{"enable", "enable", "disable", "disable"} {
		code, out, stderr := e.run("--json", "workiq", command)
		if code != 0 || stderr != "" {
			t.Fatal("explicit configuration tried source discovery or a subprocess", command, code, stderr)
		}
		want := command == "enable"
		if decode(t, out)["enabled"] != want {
			t.Fatal("configuration did not return its persisted choice")
		}
		s, err := store.OpenReadOnly(context.Background(), e.db)
		if err != nil {
			t.Fatal(err)
		}
		got, err := s.WorkIQEnabled(context.Background())
		closeErr := s.Close()
		if err != nil || closeErr != nil || got != want {
			t.Fatal("configuration did not persist idempotently", err, closeErr)
		}
	}
}

func TestWorkIQCommandsAreNotImplicitRefreshesAndRefuseDisabledData(t *testing.T) {
	e := newEnv(t)
	e.emptyArchive()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("M365CRAWL_WORKIQ_BIN", filepath.Join(t.TempDir(), "must-not-start"))
	for _, command := range []string{"sync", "coverage"} {
		code, out, stderr := e.run("--json", "--account", "outlook/AlreadyArchived", "workiq", command, "--source", "mail")
		if code != 3 || out != "" || errorOf(t, stderr)["code"] != "workiq_disabled" {
			t.Fatal("disabled data command refreshed caches or started WorkIQ", command, code, out, stderr)
		}
	}
}

func TestWorkIQCommandHelpExplainsExplicitSourceAndSeparateQualification(t *testing.T) {
	e := newEnv(t)
	for _, command := range []string{"sync", "coverage", "status"} {
		code, out, stderr := e.run("workiq", command, "--help")
		if code != 0 || stderr != "" || !strings.Contains(out, "--source") || !strings.Contains(out, "WorkIQ") {
			t.Fatal("new explicit command has no usable help", command, code, stderr)
		}
	}
}

func TestWorkIQReadCommandsRejectConflictingChatAndLinkOptions(t *testing.T) {
	e := newEnv(t)
	e.emptyArchive()
	t.Setenv("M365CRAWL_WORKIQ_BIN", filepath.Join(t.TempDir(), "must-not-start"))
	for _, args := range [][]string{
		{"workiq", "sync", "--source", "mail", "--since", "2w"},
		{"workiq", "coverage", "--source", "mail", "--max-chats", "200"},
		{"workiq", "sync", "--max-chats", "0"},
		{"workiq", "sync", "--since", "unparseable"},
		{"--outlook-account", "none", "workiq", "enable"},
		{"--outlook-profile", "Main", "workiq", "sync"},
		{"--account", "outlook/Main", "workiq", "sync", "--source", "chats"},
	} {
		code, out, _ := e.run(append([]string{"--json"}, args...)...)
		if code != 2 || out != "" {
			t.Fatal("conflicting source selection was ignored or reached acquisition", args, code, out)
		}
	}
}

func TestWorkIQDoctorUsesOnlyVersionWhenEnabled(t *testing.T) {
	e := newEnv(t)
	e.emptyArchive()
	helper := os.Getenv("M365CRAWL_TEST_ACQUISITION_STUB")
	if helper == "" {
		helper = filepath.Join(t.TempDir(), "synthetic-workiq.exe")
		cmd := exec.Command("go", "build", "-o", helper, "../workiq/testdata/stub") //nolint:gosec // G204: fixed Go/source command; only the private test output path varies.
		cmd.Env = goBuildEnv
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build synthetic transport: %v: %s", err, output)
		}
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("M365CRAWL_WORKIQ_BIN", helper)
	transcript := filepath.Join(t.TempDir(), "must-not-fetch.jsonl")
	t.Setenv("WORKIQ_ACQUISITION_TRANSCRIPT", transcript)
	if code, _, stderr := e.run("--json", "workiq", "enable"); code != 0 {
		t.Fatal(stderr)
	}
	_, out, _ := e.run("--json", "doctor")
	doc := decode(t, out)
	found := false
	for _, raw := range doc["checks"].([]any) {
		check := raw.(map[string]any)
		if check["name"] == "workiq" {
			found = true
			if check["ok"] != true || !strings.Contains(check["detail"].(string), "1.0.1") {
				t.Fatal("doctor did not return its bounded version-only observation")
			}
		}
	}
	if !found {
		t.Fatal("enabled optional source has no doctor version check")
	}
	if _, err := os.Stat(transcript); !os.IsNotExist(err) {
		t.Fatal("doctor opened an MCP session or fetched server content")
	}
}
