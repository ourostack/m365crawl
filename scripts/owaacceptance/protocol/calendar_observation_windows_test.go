package protocol

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestCalendarObservationCheckedProcessIdentity(t *testing.T) {
	pid := int64(windows.GetCurrentProcessId())
	value, err := guardProcessIdentity(int(pid))
	if err != nil || int64(value.PID) != pid || value.Created == 0 {
		t.Fatal("actual_process_identity_refused")
	}
	for _, invalid := range []int64{-1, 0, 1 << 32, 1<<32 + pid} {
		if int64(int(invalid)) != invalid {
			continue // a narrower native int cannot represent this oversized input
		}
		if _, err := guardProcessIdentity(int(invalid)); err == nil {
			t.Fatal("invalid_pid_wrapped_or_accepted")
		}
	}
}

// A real native process context with fake empty sources, never an M365 cache.
func observationContext(t *testing.T, test string) (Ownership, string) {
	t.Helper()
	for _, entry := range os.Environ() {
		key, value, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(strings.ToUpper(key), "M365CRAWL_") {
			t.Setenv(key, value)
			if os.Unsetenv(key) != nil {
				t.Fatal("synthetic_environment_refused")
			}
		}
	}
	root, source, receipt := t.TempDir(), t.TempDir(), t.TempDir()
	for _, dir := range []string{root, receipt} {
		if MakePrivateDirectory(dir) != nil {
			t.Fatal("synthetic_private_directory_refused")
		}
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal("synthetic_binary_path_refused")
	}
	// #nosec G304 -- Current validation executable only, never a private source/cache.
	binary, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal("synthetic_binary_read_refused")
	}
	hash := sha256.Sum256(binary)
	binaryID, err := NativeIdentity(exe, false)
	if err != nil {
		t.Fatal("synthetic_binary_identity_refused")
	}
	rootID, err := NativeIdentity(root, true)
	if err != nil {
		t.Fatal("synthetic_root_identity_refused")
	}
	child, err := ProcessIdentity(windows.GetCurrentProcessId())
	if err != nil {
		t.Fatal("synthetic_child_identity_refused")
	}
	parent := os.Getppid()
	if parent <= 0 || int64(parent) > 0xffffffff {
		t.Fatal("synthetic_parent_pid_refused")
	}
	// #nosec G115 -- Positive uint32 PID range is checked immediately above.
	supervisor, err := ProcessIdentity(uint32(parent))
	if err != nil {
		t.Fatal("synthetic_parent_identity_refused")
	}
	owner := Ownership{
		Version: Version, Test: test, Nonce: strings.Repeat("a", 64), BinaryHash: hex.EncodeToString(hash[:]),
		BinaryNative: binaryID, Root: root, RootNative: rootID, Source: source, Supervisor: supervisor, Child: child, Adopted: true,
	}
	initial := owner
	initial.Adopted, initial.Child = false, Process{}
	for name, value := range map[string]Ownership{OwnerName: initial, AdoptedName: owner} {
		data, err := Encode(value)
		if err != nil || WriteProtectedNew(filepath.Join(receipt, name), data) != nil {
			t.Fatal("synthetic_owner_write_refused")
		}
	}
	for _, key := range []string{"TEMP", "TMP", "TMPDIR", "HOME", "USERPROFILE"} {
		t.Setenv(key, root)
	}
	for key, value := range map[string]string{
		"LOCALAPPDATA":         filepath.Join(root, "AppData", "Local"),
		"APPDATA":              filepath.Join(root, "AppData", "Roaming"),
		"M365CRAWL_REAL_CACHE": "1", "M365CRAWL_OUTLOOK_ROOT": source, "M365CRAWL_TEAMS_ROOT": filepath.Join(root, "no-teams"),
		OwnerEnv: filepath.Join(receipt, AdoptedName),
	} {
		t.Setenv(key, value)
	}
	originalArgs := os.Args
	t.Cleanup(func() { os.Args = originalArgs })
	os.Args = []string{exe, "-test.run=^" + test + "$", "-test.timeout=4m"}
	return owner, receipt
}

func TestCalendarObservationNativeGuardAndEmission(t *testing.T) {
	owner, receipt := observationContext(t, CalendarObservationTest)
	got, err := CurrentCalendarObservationGuard()
	if err != nil || got != owner {
		t.Fatal("valid_calendar_native_context_refused")
	}
	if _, err := CurrentGuard(); err == nil {
		t.Fatal("calendar_context_became_mail_context")
	}
	counts := calendarObserved().Counts
	if EmitCalendarObservation(owner, counts, 1, 0, true) != nil {
		t.Fatal("calendar_observation_emission_refused")
	}
	path := filepath.Join(receipt, CalendarObservationName)
	wire, err := ReadProtected(path)
	if err != nil {
		t.Fatal("private_calendar_observation_unreadable")
	}
	value, err := DecodeCalendarObservation(wire)
	if err != nil || MatchCalendarObservation(owner, value) != nil {
		t.Fatal("native_calendar_observation_mismatch")
	}
	if EmitCalendarObservation(owner, counts, 1, 0, true) == nil {
		t.Fatal("calendar_observation_replaced")
	}
	if EmitQualification(owner, validWitness().Counts, 1, 0) == nil {
		t.Fatal("calendar_context_emitted_mail_qualification")
	}
}

func TestCalendarObservationDoesNotWidenLegacyNativeGuard(t *testing.T) {
	owner, _ := observationContext(t, TestName)
	if got, err := CurrentGuard(); err != nil || got != owner {
		t.Fatal("legacy_native_context_regressed")
	}
	if _, err := CurrentCalendarObservationGuard(); err == nil {
		t.Fatal("mail_context_became_calendar_context")
	}
	if EmitCalendarObservation(owner, calendarObserved().Counts, 1, 0, true) == nil {
		t.Fatal("mail_context_emitted_calendar_observation")
	}
	if EmitQualification(owner, validWitness().Counts, 1, 0) != nil {
		t.Fatal("legacy_emission_regressed")
	}
}

func TestCalendarObservationNativeEmissionRejectsUnprovedValues(t *testing.T) {
	owner, receipt := observationContext(t, CalendarObservationTest)
	for _, mode := range []string{"readers", "counters", "unstable", "owner", "oversize"} {
		counts, supplied, unstable, released := calendarObserved().Counts, owner, int64(0), true
		switch mode {
		case "readers":
			released = false
		case "counters":
			counts["records"]++
		case "unstable":
			unstable = 1
		case "owner":
			supplied.Child.Created++
		case "oversize":
			counts[strings.Repeat("synthetic-private", MaxRecordBytes)] = 1
		}
		if err := EmitCalendarObservation(supplied, counts, 1, unstable, released); err == nil || strings.Contains(err.Error(), "synthetic-private") {
			t.Fatal("unproved_observation_emitted_or_disclosed")
		}
	}

	if _, err := os.Stat(filepath.Join(receipt, CalendarObservationName)); !os.IsNotExist(err) {
		t.Fatal("refusal_created_observation")
	}
}

func TestCalendarObservationNativeGuardRejectsTamperedContext(t *testing.T) {
	for _, mode := range []string{
		"argv", "basename", "absent", "scope", "unadopted", "initial-missing", "initial-forged", "initial-adopted", "initial-mismatch",
		"root-missing", "root-id", "source-placement", "child-stale", "parent-stale", "parent-pid", "binary-hash", "temp-env", "appdata-env", "extra-env",
	} {
		t.Run(mode, func(t *testing.T) {
			owner, directory := observationContext(t, CalendarObservationTest)
			initial := owner
			initial.Adopted, initial.Child = false, Process{}
			switch mode {
			case "argv":
				os.Args = append(os.Args, "-test.v")
			case "basename":
				t.Setenv(OwnerEnv, filepath.Join(directory, OwnerName))
			case "absent":
				t.Setenv(OwnerEnv, filepath.Join(t.TempDir(), AdoptedName))
			case "scope":
				owner.Test, initial.Test = TestName, TestName
			case "unadopted":
				owner.Adopted, owner.Child = false, Process{}
			case "initial-adopted":
				initial = owner
			case "initial-mismatch":
				initial.Nonce = strings.Repeat("b", 64)
			case "root-missing":
				owner.Root = filepath.Join(owner.Root, "missing")
				initial.Root = owner.Root
			case "root-id":
				owner.RootNative[2]++
				initial.RootNative = owner.RootNative
			case "source-placement":
				owner.Source, initial.Source = owner.Root, owner.Root
				t.Setenv("M365CRAWL_OUTLOOK_ROOT", owner.Source)
			case "child-stale":
				owner.Child.Created++
			case "parent-stale":
				owner.Supervisor.Created++
				initial.Supervisor = owner.Supervisor
			case "parent-pid":
				owner.Supervisor.PID = 1
				if initial.Supervisor.PID == 1 || owner.Child.PID == 1 {
					owner.Supervisor.PID = 2
				}
				initial.Supervisor = owner.Supervisor
			case "binary-hash":
				owner.BinaryHash = strings.Repeat("c", 64)
				initial.BinaryHash = owner.BinaryHash
			case "temp-env":
				t.Setenv("TEMP", owner.Source)
			case "appdata-env":
				t.Setenv("LOCALAPPDATA", owner.Source)
			case "extra-env":
				t.Setenv("M365CRAWL_UNAPPROVED", "synthetic-private")
			}
			for name, value := range map[string]Ownership{OwnerName: initial, AdoptedName: owner} {
				data, err := Encode(value)
				if err != nil || os.WriteFile(filepath.Join(directory, name), data, 0o600) != nil {
					t.Fatal("synthetic_tamper_setup_refused")
				}
			}
			switch mode {
			case "initial-missing":
				if os.Rename(filepath.Join(directory, OwnerName), filepath.Join(directory, OwnerName+".retained")) != nil {
					t.Fatal("synthetic_record_rename_refused")
				}
			case "initial-forged":
				if os.WriteFile(filepath.Join(directory, OwnerName), []byte("{}"), 0o600) != nil {
					t.Fatal("synthetic_record_corruption_refused")
				}
			}
			if _, err := CurrentCalendarObservationGuard(); err == nil || strings.Contains(err.Error(), "synthetic-private") {
				t.Fatal("tampered_native_context_accepted_or_disclosed")
			}
		})
	}
}
