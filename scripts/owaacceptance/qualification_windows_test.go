package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/scripts/owaacceptance/protocol"
	"golang.org/x/sys/windows"
)

func testDigest(t *testing.T, path string) string {
	t.Helper()
	// #nosec G304 G703 -- Explicit current test or private compiled artifact, never a source/cache file or payload-derived filename.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("test_binary_read_failed")
	}
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

func TestSyntheticReceiptWaitsForNonemptyPublication(t *testing.T) {
	path := filepath.Join(t.TempDir(), "result")
	if os.WriteFile(path, nil, 0o600) != nil {
		t.Fatal("synthetic_empty_receipt_failed")
	}
	published := make(chan error, 1)
	time.AfterFunc(30*time.Millisecond, func() {
		// #nosec G703 -- Exact synthetic t.TempDir synchronization receipt, not a payload or user-derived filename.
		published <- os.WriteFile(path, []byte("refused"), 0o600)
	})
	t.Cleanup(func() {
		if <-published != nil {
			t.Error("synthetic_publication_failed")
		}
	})
	if string(waitSyntheticReceipt(t, path)) != "refused" {
		t.Fatal("empty_receipt_accepted_before_publication")
	}
}

func TestQualificationRejectsZeroExitWithoutActualTestWitness(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal("test_executable_failed")
	}
	receipt := filepath.Join(t.TempDir(), "receipt")
	_, err = runQualified(exe, testDigest(t, exe), qualifiedFixtureRoot(t), t.TempDir(), receipt, 5*time.Second)
	if err == nil {
		t.Error("unmatched_zero_exit_qualified")
	}
	data, readErr := protocol.ReadProtected(filepath.Join(receipt, protocol.OwnerName))
	if readErr != nil {
		t.Fatal("durable_prelaunch_ownership_missing")
	}
	owner, err := protocol.DecodeOwnership(data)
	if err != nil || owner.Adopted {
		t.Fatal("prelaunch_record_invalid")
	}
	closeData, err := protocol.ReadProtected(filepath.Join(receipt, protocol.CloseoutName))
	if err != nil {
		t.Fatal("durable_failed_closeout_missing")
	}
	closeout, err := protocol.DecodeCloseout(closeData)
	if err != nil || closeout.Qualified || !closeout.ConsumersReleased || closeout.RootState != "absent" {
		t.Fatal("failed_run_recovery_facts_invalid")
	}
}

func TestQualificationBinaryMismatchPrelaunchRefused(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal("test_executable_failed")
	}
	if _, err := runQualified(exe, "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff", t.TempDir(), t.TempDir(), filepath.Join(t.TempDir(), "receipt"), time.Second); err == nil {
		t.Fatal("binary_mismatch_accepted")
	}
}

func TestProtectedRecordRefusesOverwriteAndBroadACL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, protocol.OwnerName)
	if protocol.WriteProtectedNew(path, []byte("{}")) == nil {
		t.Fatal("broad_receipt_parent_accepted")
	}
	if protocol.MakePrivateDirectory(dir) != nil {
		t.Fatal("private_directory_failed")
	}
	if protocol.WriteProtectedNew(path, []byte("{}")) != nil {
		t.Fatal("private_record_failed")
	}
	if protocol.WriteProtectedNew(path, []byte("{}")) == nil {
		t.Fatal("unowned_overwrite_accepted")
	}
	if _, err := protocol.ReadProtected(path); err != nil {
		t.Fatal("protected_record_read_failed")
	}
}

func TestQualifiedSyntheticActualBinary(t *testing.T) {
	binary := os.Getenv("OWA_ACCEPTANCE_TEST_BINARY")
	if binary == "" {
		t.Skip("explicit compiled synthetic acceptance artifact required")
	}
	if !filepath.IsAbs(binary) {
		t.Fatal("test_binary_path_refused")
	}
	source := t.TempDir()
	copySyntheticFixture(t, filepath.Join("..", "..", "testdata", "outlook-owa-fixture", "mail"), filepath.Join(source, "Default"))
	receipt := filepath.Join(t.TempDir(), "receipt")
	witness, err := runQualified(binary, testDigest(t, binary), source, t.TempDir(), receipt, 30*time.Second)
	if err != nil || witness.Observed != 1 || witness.Unstable != 0 || witness.Counts["messages"] != 5 || witness.Counts["recipients"] != 4 || witness.Counts["folders"] != 7 {
		t.Fatal("actual_synthetic_qualification_missing")
	}
	data, err := protocol.ReadProtected(filepath.Join(receipt, protocol.CloseoutName))
	if err != nil {
		t.Fatal("passed_closeout_missing")
	}
	closeout, err := protocol.DecodeCloseout(data)
	if err != nil || !closeout.Qualified || closeout.RootState != "absent" || !closeout.ConsumersReleased {
		t.Fatal("qualified_closeout_invalid")
	}
}

func copySyntheticFixture(t *testing.T, src, dst string) {
	t.Helper()
	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatal("fixture_list_failed")
	}
	if os.MkdirAll(dst, 0o700) != nil {
		t.Fatal("fixture_directory_failed")
	}
	for _, entry := range entries {
		from, to := filepath.Join(src, entry.Name()), filepath.Join(dst, entry.Name())
		if entry.IsDir() {
			copySyntheticFixture(t, from, to)
			continue
		}
		// #nosec G304 -- Only the unchanged committed synthetic fixture is copied.
		data, err := os.ReadFile(from)
		if err != nil {
			t.Fatal("fixture_read_failed")
		}
		// #nosec G703 -- Exact t.TempDir synthetic destination, no host cache or dynamic private names.
		if os.WriteFile(to, data, 0o600) != nil {
			t.Fatal("fixture_write_failed")
		}
	}
}

func qualifiedFixtureRoot(t *testing.T) string {
	t.Helper()
	source := t.TempDir()
	copySyntheticFixture(t, filepath.Join("..", "..", "testdata", "outlook-owa-fixture", "mail"), filepath.Join(source, "Default"))
	return source
}

func TestQualificationSyntheticNonzeroAndTimeout(t *testing.T) {
	binary := os.Getenv("OWA_ACCEPTANCE_TEST_BINARY")
	if binary == "" {
		t.Skip("explicit compiled synthetic acceptance artifact required")
	}
	for _, mode := range []string{"nonzero", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			source := t.TempDir()
			variant := "multiple"
			timeout := 30 * time.Second
			if mode == "timeout" {
				variant = "mail"
				timeout = time.Millisecond
			}
			copySyntheticFixture(t, filepath.Join("..", "..", "testdata", "outlook-owa-fixture", variant), filepath.Join(source, "Default"))
			receipt := filepath.Join(t.TempDir(), "receipt")
			_, err := runQualified(binary, testDigest(t, binary), source, t.TempDir(), receipt, timeout)
			if err == nil {
				t.Fatal("failed_child_qualified")
			}
			if mode == "timeout" && !errors.Is(err, errTimeout) {
				t.Fatal("timeout_not_reported")
			}
			data, err := protocol.ReadProtected(filepath.Join(receipt, protocol.CloseoutName))
			if err != nil {
				t.Fatal("failure_recovery_record_missing")
			}
			record, err := protocol.DecodeCloseout(data)
			if err != nil || record.Qualified || record.RootState != "absent" || !record.ConsumersReleased || !record.NativeCloseVerified {
				t.Fatal("failure_reconciliation_unproved")
			}
		})
	}

}

func TestQualificationCleanupRefusalHasDurableOwnedRecovery(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal("test_executable_failed")
	}
	w, q, err := newQualifiedRun(exe, testDigest(t, exe), qualifiedFixtureRoot(t), t.TempDir(), filepath.Join(t.TempDir(), "receipt"))
	if err != nil {
		t.Fatal("qualified_workspace_failed")
	}
	path := filepath.Join(w.dir, "retained-synthetic")
	if os.WriteFile(path, []byte("synthetic-only"), 0o600) != nil {
		t.Fatal("synthetic_write_failed")
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal("synthetic_path_failed")
	}
	if windows.SetFileAttributes(name, windows.FILE_ATTRIBUTE_READONLY) != nil {
		t.Fatal("synthetic_readonly_failed")
	}
	exit, result := w.run(exe, []string{"-test.run=^" + protocol.TestName + "$", "-test.timeout=4m"}, qualifiedEnv(os.Environ(), q), 5*time.Second)
	if result == nil || q.finish(exit, result) == nil {
		t.Error("cleanup_refusal_qualified")
	}
	data, err := protocol.ReadProtected(filepath.Join(q.receipt, protocol.CloseoutName))
	if err != nil {
		t.Fatal("retained_closeout_missing")
	}
	record, err := protocol.DecodeCloseout(data)
	if err != nil || record.Qualified || record.RootState != "retained_owned" || record.ObservedRootNative != w.id || !record.ConsumersReleased {
		t.Fatal("retained_ownership_unproved")
	}
	// The exact named test object is restored only after the recorded consumer barrier. Recovery uses the unchanged native owned inventory, never a prefix sweep.
	if windows.SetFileAttributes(name, windows.FILE_ATTRIBUTE_NORMAL) != nil {
		t.Fatal("synthetic_attribute_restore_failed")
	}
	if w.cleanup() != nil {
		t.Fatal("exact_owned_recovery_failed")
	}
	if _, err := os.Lstat(w.dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("recovered_root_not_absent")
	}
}

func TestQualificationBadWitnessRefusedWithSafeRecovery(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal("test_executable_failed")
	}
	for _, mode := range []string{"malformed", "oversized", "trailing", "extra", "negative", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			w, q, err := newQualifiedRun(exe, testDigest(t, exe), qualifiedFixtureRoot(t), t.TempDir(), filepath.Join(t.TempDir(), "receipt"))
			if err != nil {
				t.Fatal("qualified_workspace_failed")
			}
			counts := map[string]int64{}
			for _, key := range protocol.CountFields {
				counts[key] = 0
			}
			witness := protocol.Qualification{Version: protocol.Version, Nonce: q.owner.Nonce, Test: q.owner.Test, BinaryHash: q.owner.BinaryHash, BinaryNative: q.owner.BinaryNative, RootNative: q.owner.RootNative, Supervisor: q.owner.Supervisor, Child: protocol.Process{PID: 1, Created: 1}, Outcome: "passed", Observed: 1, Counts: counts}
			data, _ := protocol.Encode(witness)
			switch mode {
			case "malformed":
				data = []byte("{")
			case "oversized":
				data = make([]byte, protocol.MaxRecordBytes+1)
			case "trailing":
				data = append(data, []byte("{}")...)
			case "extra":
				data = append(data[:len(data)-1], []byte(`,"extra":"not-output"}`)...)
			case "negative":
				counts["messages"] = -1
				data, _ = protocol.Encode(witness)
			case "unknown":
				counts["private_not_output"] = 0
				data, _ = protocol.Encode(witness)
			}
			path := filepath.Join(q.receipt, protocol.WitnessName)
			if mode == "oversized" {
				if protocol.WriteProtectedNew(path, []byte("{}")) != nil {
					t.Fatal("private_record_failed")
				}
				if os.WriteFile(path, data, 0o600) != nil {
					t.Fatal("oversized_synthetic_write_failed")
				}
			} else if protocol.WriteProtectedNew(path, data) != nil {
				t.Fatal("synthetic_witness_write_failed")
			}
			exit, result := w.run(exe, []string{"-test.run=^" + protocol.TestName + "$", "-test.timeout=4m"}, qualifiedEnv(os.Environ(), q), 5*time.Second)
			if result == nil || q.finish(exit, result) == nil {
				t.Error("bad_witness_qualified")
			}
			closeData, err := protocol.ReadProtected(filepath.Join(q.receipt, protocol.CloseoutName))
			if err != nil {
				t.Fatal("refused_closeout_missing")
			}
			record, err := protocol.DecodeCloseout(closeData)
			if err != nil || record.Qualified || record.RootState != "absent" || !record.ConsumersReleased || !record.NativeCloseVerified {
				t.Fatal("bad_witness_closeout_unproved")
			}
		})
	}
}

func TestQualifiedSyntheticGuardRejectsRecordedMismatchBeforeDiscovery(t *testing.T) {
	binary := os.Getenv("OWA_ACCEPTANCE_TEST_BINARY")
	if binary == "" {
		t.Skip("explicit compiled synthetic acceptance artifact required")
	}
	for _, mode := range []string{"nonce", "parent_creation", "root_native", "binary_hash", "test_name"} {
		t.Run(mode, func(t *testing.T) {
			source := t.TempDir()
			copySyntheticFixture(t, filepath.Join("..", "..", "testdata", "outlook-owa-fixture", "mail"), filepath.Join(source, "Default"))
			w, q, err := newQualifiedRun(binary, testDigest(t, binary), source, t.TempDir(), filepath.Join(t.TempDir(), "receipt"))
			if err != nil {
				t.Fatal("qualified_workspace_failed")
			}
			switch mode {
			case "nonce":
				q.owner.Nonce = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
			case "parent_creation":
				q.owner.Supervisor.Created++
			case "root_native":
				q.owner.RootNative[2]++
			case "binary_hash":
				q.owner.BinaryHash = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
			case "test_name":
				q.owner.Test = "unmatched"
			}
			if mode != "nonce" {
				// Coherent but false synthetic records reach the native/live/binary guard, rather than merely failing initial/adopted equality.
				data, err := protocol.Encode(q.owner)
				if err != nil {
					t.Fatal("synthetic_owner_encode_failed")
				}
				// #nosec G703 -- Intentional mutation of this test's exact protected record before its copied-synthetic child starts.
				if os.WriteFile(filepath.Join(q.receipt, protocol.OwnerName), data, 0o600) != nil {
					t.Fatal("synthetic_owner_mutation_failed")
				}
			}
			exit, result := w.run(binary, []string{"-test.run=^" + protocol.TestName + "$", "-test.timeout=4m"}, qualifiedEnv(os.Environ(), q), 30*time.Second)
			if q.finish(exit, result) == nil || exit == 0 {
				t.Fatal("forged_guard_child_qualified")
			}
			if _, err := os.Lstat(filepath.Join(q.receipt, protocol.WitnessName)); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("forged_guard_produced_witness")
			}
			// The archive and private snapshot were never created before permit refusal.
			if !q.cleanupVerified {
				t.Fatal("guard_refusal_cleanup_unproved")
			}
		})
	}
}

func TestQualificationSanitizesInheritedUnrequestedEnvironment(t *testing.T) {
	q := &qualifiedRun{owner: protocol.Ownership{Source: `C:\synthetic`, Root: `C:\private`}}
	env := qualifiedEnv([]string{"m365crawl_outlook_profile=unrequested", "M365CRAWL_REAL_CACHE=0", "OWA_ACCEPTANCE_TEST_BINARY=unrequested", "GOCOVERDIR=unrequested", "GOFLAGS=unrequested", "GODEBUG=unrequested", "GOTRACEBACK=unrequested", "KEEP=safe"}, q)
	if len(env) != 5 || env[0] != "KEEP=safe" {
		t.Fatal("unrequested_environment_retained")
	}

}

func TestQualificationEarlyExitBinaryCannotQualify(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "main.go")
	if os.WriteFile(path, []byte("package main\nfunc main() {}\n"), 0o600) != nil {
		t.Fatal("synthetic_early_exit_source_failed")
	}
	binary := filepath.Join(dir, "early-exit.exe")
	goBinary, err := exec.LookPath("go")
	if err != nil {
		t.Fatal("loaded_sdk_missing")
	}
	// #nosec G204 G702 -- Loaded validation SDK, fixed build command, exact private synthetic source/artifact; no shell or dependency change.
	cmd := exec.CommandContext(t.Context(), goBinary, "build", "-o", binary, path)
	cmd.Env = append(os.Environ(), "GOWORK=off", "M365CRAWL_REAL_CACHE=0")
	if cmd.Run() != nil {
		t.Fatal("synthetic_early_exit_build_failed")
	}
	receipt := filepath.Join(t.TempDir(), "receipt")
	if _, err := runQualified(binary, testDigest(t, binary), qualifiedFixtureRoot(t), t.TempDir(), receipt, 5*time.Second); err == nil {
		t.Fatal("early_exit_binary_qualified")
	}
	data, err := protocol.ReadProtected(filepath.Join(receipt, protocol.CloseoutName))
	if err != nil {
		t.Fatal("early_exit_closeout_missing")
	}
	record, err := protocol.DecodeCloseout(data)
	if err != nil || record.Qualified || record.Exit != 0 || record.RootState != "absent" || !record.ConsumersReleased || !record.NativeCloseVerified {
		t.Fatal("early_exit_recovery_unproved")
	}
}
