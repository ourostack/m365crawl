package syncer

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/outlookmail"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/workiq"
)

var acquisitionStubBuild sync.Once
var acquisitionStubImage []byte
var acquisitionStubBuildErr error

func workIQAcquisitionArchive(t *testing.T) (string, string) {
	t.Helper()
	isolateTmp(t)
	db := newDB(t)
	t.Setenv("WORKIQ_ACQUISITION_LOCK_PATH", db)
	ctx := context.Background()
	s, err := store.Open(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	account := "outlook/WorkIQSynthetic"
	at := time.Date(2026, 9, 10, 8, 0, 0, 0, time.UTC)
	_, err = s.CommitMail(ctx, store.MailBatch{
		Account: account, ReadAt: at, FreshAt: at, Trusted: true,
		Result: outlookmail.Result{Folders: []outlookmail.Folder{{Key: 1, Name: "Native inbox", Kind: "inbox"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetWorkIQEnabled(ctx, true); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	return db, account
}

func workIQAcquisitionStub(t *testing.T) string {
	t.Helper()
	exe := os.Getenv("M365CRAWL_TEST_ACQUISITION_STUB")
	if exe == "" {
		name := "workiq"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		exe = filepath.Join(t.TempDir(), name)
		acquisitionStubBuild.Do(func() {
			cmd := osexec.Command("go", "build", "-o", exe, "../workiq/testdata/stub")
			cmd.Env = append(os.Environ(), "GOWORK=off", "GOPROXY=off")
			if output, err := cmd.CombinedOutput(); err != nil {
				acquisitionStubBuildErr = fmt.Errorf("build synthetic helper: %w: %s", err, output)
				return
			}
			acquisitionStubImage, acquisitionStubBuildErr = os.ReadFile(filepath.Clean(exe))
		})
		if acquisitionStubBuildErr != nil {
			t.Fatal(acquisitionStubBuildErr)
		}
		if _, err := os.Stat(exe); os.IsNotExist(err) {
			if err := os.WriteFile(exe, acquisitionStubImage, 0o700); err != nil {
				t.Fatal(err)
			}
		} else if err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("M365CRAWL_WORKIQ_BIN", exe)
	transcript := filepath.Join(t.TempDir(), "fetch-transcript.jsonl")
	t.Setenv("WORKIQ_ACQUISITION_TRANSCRIPT", transcript)
	return transcript
}

func TestWorkIQMailAcquisitionUsesActualMCPAndDurableProducers(t *testing.T) {
	db, account := workIQAcquisitionArchive(t)
	transcript := workIQAcquisitionStub(t)
	ctx := context.Background()
	report, err := RunWorkIQ(ctx, WorkIQOptions{DBPath: db, Source: "mail", Account: account,
		Invocation: workiq.Invocation{ExplicitTeamsRoot: true}})
	if err != nil {
		t.Fatal("synthetic explicit acquisition failed", err)
	}
	s, err := store.OpenReadOnly(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	rows, err := s.MailList(ctx, store.MailFilter{})
	if err != nil || len(rows) != 1 || rows[0].Provenance != "workiq_server" {
		t.Fatal("actual acquisition did not admit exactly one canonical server item", err)
	}
	item, err := s.MailGet(ctx, account, rows[0].DetailKey)
	if err != nil || item.BodyText != "retained synthetic server body" {
		t.Fatal("actual acquisition lost its mapped retained body", err)
	}
	if len(report.Accounts) != 1 || report.Accounts[0].Status != "ok" || report.Counts["mail_added"] != 1 || len(report.Gaps) != 0 {
		t.Fatal("acquisition reported a different account/completion than its actual stored traversal")
	}
	f, err := os.Open(filepath.Clean(transcript))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	scanner := bufio.NewScanner(f)
	me, pages := 0, 0
	for scanner.Scan() {
		var call struct {
			Tool, Path string
			Pinned     bool
		}
		if json.Unmarshal(scanner.Bytes(), &call) != nil || call.Tool != "fetch" {
			t.Fatal("acquisition issued a non-fetch tool or malformed transcript")
		}
		if call.Path == "/me" {
			me++
		} else if !call.Pinned {
			t.Fatal("content was fetched before the pinned user was revalidated")
		}
		if strings.Contains(call.Path, "$skiptoken=second") {
			pages++
		}
	}
	if err := scanner.Err(); err != nil || me != 2 || pages != 1 {
		t.Fatal("acquisition skipped bootstrap/revalidation or a real folder-list page", err)
	}
	unlock, err := store.AcquireWriter(ctx, db, store.WriterOrdinarySync)
	if err != nil {
		t.Fatal("acquisition left a writer consumer behind", err)
	}
	unlock()
}

func TestWorkIQMailAcquisitionRetriesOnlyAfterReopeningAndRepinning(t *testing.T) {
	db, account := workIQAcquisitionArchive(t)
	transcript := workIQAcquisitionStub(t)
	t.Setenv("WORKIQ_ACQUISITION_FAIL_ONCE", filepath.Join(t.TempDir(), "failed-once"))
	_, err := RunWorkIQ(context.Background(), WorkIQOptions{DBPath: db, Source: "mail", Account: account,
		Invocation: workiq.Invocation{ExplicitTeamsRoot: true}})
	if err != nil {
		t.Fatal("one transient page did not recover through a fresh pinned session", err)
	}
	raw, err := os.ReadFile(filepath.Clean(transcript))
	if err != nil {
		t.Fatal(err)
	}
	me, page := 0, 0
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var call struct{ Path string }
		if err := json.Unmarshal([]byte(line), &call); err != nil {
			t.Fatal(err)
		}
		if call.Path == "/me" {
			me++
		}
		if call.Path == "/me/mailFolders/folder-inbox/messages/delta()" {
			page++
		}
	}
	if me != 3 || page != 2 {
		t.Fatal("retry reused a closed client, skipped pin verification, or repeated an admitted page")
	}
	s, err := store.OpenReadOnly(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	rows, err := s.MailList(context.Background(), store.MailFilter{})
	if err != nil || len(rows) != 1 {
		t.Fatal("a retried page duplicated or lost canonical data", err)
	}
}
