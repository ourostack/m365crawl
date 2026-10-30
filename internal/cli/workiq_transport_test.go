package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ourostack/m365crawl/internal/store"
)

var workIQCLIHelperOnce sync.Once
var workIQCLIHelperImage []byte
var workIQCLIHelperErr error

func workIQCLITransport(t *testing.T, db string) string {
	t.Helper()
	helper := os.Getenv("M365CRAWL_TEST_ACQUISITION_STUB")
	if helper == "" {
		helper = filepath.Join(t.TempDir(), "synthetic-workiq.exe")
		workIQCLIHelperOnce.Do(func() {
			cmd := exec.Command("go", "build", "-o", helper, "../workiq/testdata/stub") //nolint:gosec // G204: fixed Go/source command; only the private test output path varies.
			cmd.Env = goBuildEnv
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("build synthetic transport: %v: %s", err, output)
			}
			workIQCLIHelperImage, workIQCLIHelperErr = os.ReadFile(helper) //nolint:gosec // G304: the synthetic executable just built in t.TempDir.
		})
		if workIQCLIHelperErr != nil {
			t.Fatal(workIQCLIHelperErr)
		}
		if err := os.WriteFile(helper, workIQCLIHelperImage, 0o700); err != nil { //nolint:gosec // G306: synthetic executable in t.TempDir, not a data export.
			t.Fatal(err)
		}
	}
	t.Setenv("M365CRAWL_WORKIQ_BIN", helper)
	t.Setenv("WORKIQ_ACQUISITION_LOCK_PATH", db)
	transcript := filepath.Join(t.TempDir(), "fetch-only.jsonl")
	t.Setenv("WORKIQ_ACQUISITION_TRANSCRIPT", transcript)
	return transcript
}

func TestWorkIQActualCLITransportKeepsSyncCountsAndProbeEffectsSeparate(t *testing.T) {
	e := newEnv(t)
	seedMail(t, e, nil)
	transcript := workIQCLITransport(t, e.db)
	t.Setenv("HOME", t.TempDir())
	for _, command := range []string{"enable", "sync", "coverage", "status", "disable"} {
		args := []string{"--json", "--account", mailAccount, "workiq", command}
		if command != "enable" && command != "disable" {
			args = append(args, "--source", "mail")
		}
		code, out, stderr := e.run(args...)
		if code != 0 || stderr != "" {
			t.Fatal("actual synthetic command failed", command, code, stderr)
		}
		doc := decode(t, out)
		if command == "sync" && (doc["enabled"] != true || doc["kind"] != "sync" ||
			doc["counts"].(map[string]any)["mail_added"] != float64(1)) {
			t.Fatal("sync result hid its actual opt-in or committed count")
		}
		if command == "coverage" && len(doc["mail"].([]any)) != 7 {
			t.Fatal("actual CLI lost folder totals")
		}
	}
	st, err := store.OpenReadOnly(context.Background(), e.db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	_, rows, _, err := st.SQL(context.Background(), `select kind,status,all_folders from workiq_attempts order by id`, 20)
	if err != nil || len(rows) != 3 || rows[0][0] != "sync" || rows[1][0] != "coverage" || rows[2][0] != "status" ||
		rows[0][2] != int64(1) || rows[1][2] != int64(0) || rows[2][2] != int64(0) {
		t.Fatal("counts or status became data completion", err)
	}
	raw, err := os.ReadFile(transcript) //nolint:gosec // G304: own synthetic fetch transcript returned by the test helper.
	if err != nil || strings.Contains(string(raw), "create_entity") {
		t.Fatal("actual CLI did not remain fetch-only", err)
	}
	code, out, stderr := e.run("--json", "--max-age", "0", "mail", "list")
	if code != 0 || stderr != "" || len(decode(t, out)["items"].([]any)) != 1 {
		t.Fatal("disabled acquisition hid retained server-only mail", code, stderr)
	}
}

func TestWorkIQDisabledStatusUsesActualCLIWithoutPersistingData(t *testing.T) {
	e := newEnv(t)
	seedMail(t, e, nil)
	workIQCLITransport(t, e.db)
	t.Setenv("HOME", t.TempDir())
	code, out, stderr := e.run("--json", "--account", mailAccount, "workiq", "status", "--source", "mail")
	if code != 0 || stderr != "" || decode(t, out)["enabled"] != false {
		t.Fatal("disabled explicit probe was treated as data acquisition", code, stderr)
	}
	st, err := store.OpenReadOnly(context.Background(), e.db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	_, rows, _, err := st.SQL(context.Background(), `select count(*) from workiq_attempts`, 1)
	if err != nil || len(rows) != 1 || rows[0][0] != int64(0) {
		t.Fatal("disabled status persisted an attempt", err)
	}
}

func TestWorkIQEnabledOrdinaryCommandsNeverStartTheOptionalExecutable(t *testing.T) {
	e := newEnv(t)
	e.emptyArchive()
	transcript := workIQCLITransport(t, e.db)
	t.Setenv("HOME", t.TempDir())
	if code, _, stderr := e.run("--json", "workiq", "enable"); code != 0 {
		t.Fatal(stderr)
	}
	for _, args := range [][]string{{"--help"}, {"metadata"}, {"skill"}, {"--max-age", "0", "status"},
		{"--max-age", "0", "whoami"}, {"--max-age", "0", "messages"}} {
		if code, _, stderr := e.run(args...); code != 0 {
			t.Fatal("ordinary command failed", args, code, stderr)
		}
	}
	if _, err := os.Stat(transcript); !os.IsNotExist(err) {
		t.Fatal("ordinary command opened an optional server session")
	}
}
