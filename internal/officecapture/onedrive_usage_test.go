package officecapture

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/officedocuments"
	"github.com/ourostack/m365crawl/internal/onenote"
	"github.com/ourostack/m365crawl/internal/sourcecapture"
)

const usageFixtureSchema = `
CREATE TABLE recent_files_spo(ID TEXT PRIMARY KEY,ItemId TEXT,ListId TEXT,WebId TEXT,SiteId TEXT,
LastModifiedDateTime,LastAccessedDateTime,FileModifiedDateTime,LastSharedDateTime,SavedDateTime,
Type TEXT,Source TEXT,Extension TEXT,WorkingSetId TEXT,HiddenFromShared INTEGER,IsLocalPlaceHolder INTEGER);
CREATE TABLE recent_files_formatted_spo(ID TEXT,Format TEXT NOT NULL,FormattedValue TEXT NOT NULL,PRIMARY KEY(ID,Format));
CREATE TABLE top_collaborators(ID TEXT PRIMARY KEY,FormattedValue TEXT);
INSERT INTO recent_files_spo VALUES
('synthetic-id','native-item','native-list','native-web','native-site','2026-10-09T12:34:56Z','accessed',NULL,'shared',NULL,
'Word','native-source','docx','working-set',0,1);
INSERT INTO top_collaborators VALUES('native-person','{"Id":"stated-person","DisplayName":"Synthetic Person","EmailAddresses":["person@example.invalid"]}');
`

// usageFormatted is one recent_files_formatted_spo row: the document JSON, escaped as the source stores it.
func usageFormatted(t *testing.T, format string) string {
	t.Helper()
	body := `{"file":{"FileName":"Synthetic usage title","SharePointItem":{"SiteId":"s","WebId":"w","ListId":"l","UniqueId":"u"},` +
		`"AllExtensions":{"SharingHistory":{"Instances":[{"SharedByTime":"raw","Participants":[{"DisplayName":"Synthetic Person","Smtp":"person@example.invalid"}]}]}}}}`
	escaped, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return "INSERT INTO recent_files_formatted_spo VALUES('synthetic-id','" + format + "','" +
		strings.ReplaceAll(strings.Trim(string(escaped), `"`), "'", "''") + "')"
}

func usageSource(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "darwin" && runtime.GOOS != "windows" {
		t.Skip("ordinary input qualification is Darwin/Windows only")
	}
	return sqliteSource(t, usageLeaf, usageFixtureSchema, usageFormatted(t, "variant-a"), usageFormatted(t, "variant-b"))
}

func usageCode(err error) string {
	var fixed *ReadError
	if errors.As(err, &fixed) {
		return fixed.Code
	}
	return ""
}

func TestUsageCaptureRollbackAndLimits(t *testing.T) {
	if usageLimit != 512<<20 || registryLimit != 64<<20 || jsonLimit != 8<<20 {
		t.Fatal("usage, registry or JSON input limit changed")
	}
	path := usageSource(t)
	before, err := os.ReadFile(path) //nolint:gosec // synthetic fixture
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	observed, err := ObserveOneDriveUsage(context.Background(), path)
	if err != nil || observed == "" {
		t.Fatalf("observation: %q, %v", observed, err)
	}
	var private string
	err = WithOneDriveUsageCapture(context.Background(), UsageInput{Path: path}, func(c *UsageCapture) error {
		private = c.Path
		if c.Path == path || filepath.Base(c.Path) != "usage.db" || c.ContentDigest != fmt.Sprintf("%x", sha256.Sum256(before)) ||
			c.AcquisitionSignature != observed || c.AcquiredAt.IsZero() || !c.SourceModified.Equal(info.ModTime()) {
			t.Fatal("capture identity or clocks missing")
		}
		result, err := c.ReadUsage(context.Background())
		if err != nil {
			return err
		}
		if len(result.Recent) != 1 || len(result.Documents) != 2 || result.Documents[0].Format != "variant-a" ||
			result.Documents[1].Format != "variant-b" || len(result.Documents[0].History) != 1 ||
			len(result.Collaborators) != 1 || result.Collaborators[0].Emails[0] != "person@example.invalid" || len(result.Losses) != 0 {
			t.Fatalf("native usage claims lost: %#v", result)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{private, filepath.Dir(private)} {
		if _, err := os.Lstat(name); !os.IsNotExist(err) {
			t.Fatalf("private copy survived: %v", err)
		}
	}
	after, err := os.ReadFile(path) //nolint:gosec // synthetic fixture
	if err != nil || string(after) != string(before) {
		t.Fatal("usage source bytes changed")
	}
	if output := officeWriterProbe(t, path); !strings.Contains(output, "WRITE_OK") {
		t.Fatalf("writer did not recover after usage capture: %s", output)
	}
	limit := usageLimit
	t.Cleanup(func() { usageLimit = limit })
	usageLimit = info.Size()
	if err := WithOneDriveUsageCapture(context.Background(), UsageInput{Path: path}, func(*UsageCapture) error { return nil }); err != nil {
		t.Fatalf("input at the inclusive limit refused: %v", err)
	}
	usageLimit = info.Size() - 1
	called := false
	err = WithOneDriveUsageCapture(context.Background(), UsageInput{Path: path}, func(*UsageCapture) error { called = true; return nil })
	if called || usageCode(err) != "onedrive_usage_index_too_large" {
		t.Fatalf("input one byte over the limit admitted: %v", err)
	}
	usageLimit = limit
	for _, tc := range []struct{ name, code string }{
		{"wal-header", "onedrive_usage_input_unqualified"},
		{"nonempty-journal", "onedrive_usage_source_busy"},
		{"nonempty-wal", "onedrive_usage_input_unqualified"},
		{"shm", "onedrive_usage_input_unqualified"},
		{"linked-companion", "onedrive_usage_input_unqualified"},
		{"hard-link", "onedrive_usage_input_unqualified"},
		{"not-sqlite", "onedrive_usage_input_unqualified"},
		{"directory", "onedrive_usage_input_unqualified"},
		{"relative", "onedrive_usage_input_unqualified"},
		{"missing", "onedrive_usage_input_unqualified"},
		{"empty-journal", ""},
		{"empty-wal", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := usageSource(t)
			write := func(name string, data []byte) {
				if err := os.WriteFile(name, data, 0o600); err != nil { //nolint:gosec // test-owned synthetic path
					t.Fatal(err)
				}
			}
			switch tc.name {
			case "wal-header":
				data, err := os.ReadFile(path) //nolint:gosec // synthetic fixture
				if err != nil {
					t.Fatal(err)
				}
				data[18], data[19] = 2, 2
				write(path, data)
			case "nonempty-journal":
				write(path+"-journal", []byte("journal"))
			case "nonempty-wal":
				write(path+"-wal", []byte("wal"))
			case "shm":
				write(path+"-shm", nil)
			case "linked-companion":
				if err := os.Symlink(filepath.Join(filepath.Dir(path), "absent"), path+"-journal"); err != nil {
					t.Skip("symlinks unavailable")
				}
			case "hard-link":
				if err := os.Link(path, path+".alias"); err != nil {
					t.Fatal(err)
				}
			case "not-sqlite":
				write(path, []byte(strings.Repeat("not a database header ", 8)))
			case "directory":
				path = filepath.Dir(path)
			case "relative":
				path = filepath.Base(path)
			case "missing":
				path += ".missing"
			case "empty-journal":
				write(path+"-journal", nil)
			case "empty-wal":
				write(path+"-wal", nil)
			}
			// A refusal happens on held metadata and the 100-byte header: no content is copied.
			readAt := registryReadAt
			t.Cleanup(func() { registryReadAt = readAt })
			contentReads := 0
			registryReadAt = func(f *os.File, b []byte, offset int64) (int, error) {
				if len(b) != sqliteHeaderBytes {
					contentReads++
				}
				return readAt(f, b, offset)
			}
			called := false
			err := WithOneDriveUsageCapture(context.Background(), UsageInput{Path: path}, func(c *UsageCapture) error {
				called = true
				_, err := c.ReadUsage(context.Background())
				return err
			})
			if tc.code == "" {
				if err != nil || !called || contentReads == 0 {
					t.Fatalf("ordinary zero-length companion refused: %v", err)
				}
				return
			}
			if called || usageCode(err) != tc.code || contentReads != 0 {
				t.Fatalf("unsafe usage input admitted, misreported or read (%d content reads): %v", contentReads, err)
			}
			if tc.name == "relative" || tc.name == "missing" || tc.name == "directory" {
				if signature, err := ObserveOneDriveUsage(context.Background(), path); signature != "" || usageCode(err) != tc.code {
					t.Fatalf("unsafe usage input observed: %q %v", signature, err)
				}
			}
		})
	}
}

// Review focus 2: while a usage capture holds SQLite SHARED protection on a file, a notes, Office
// JSON or registry call that selects the same file must not open and close it, because on POSIX
// that close would release the protection. A separate writer process stays excluded throughout.
func TestUsageSourceDescriptorIsolation(t *testing.T) {
	path := usageSource(t)
	stage := registryStage
	t.Cleanup(func() { registryStage = stage })
	var stages []string
	registryStage = func(name string) {
		stages = append(stages, name)
		if output := officeWriterProbe(t, path); !strings.Contains(output, "WRITE_BLOCKED") {
			t.Errorf("writer admitted after %s validation: %s", name, output)
		}
	}
	blocked := func(what string) {
		t.Helper()
		if output := officeWriterProbe(t, path); !strings.Contains(output, "WRITE_BLOCKED") {
			t.Errorf("%s released usage SHARED protection: %s", what, output)
		}
	}
	err := WithOneDriveUsageCapture(context.Background(), UsageInput{Path: path}, func(c *UsageCapture) error {
		if signature, err := ObserveJSON(shortContext(t), path); err == nil || signature != "" {
			t.Error("JSON observation entered the held source lifetime")
		}
		blocked("JSON observation")
		if inputs, err := DiscoverJSON(shortContext(t), filepath.Dir(path)); err == nil || inputs != nil {
			t.Error("JSON discovery entered the held source lifetime")
		}
		blocked("JSON discovery")
		called := false
		if err := WithJSONCapture(shortContext(t), DocumentInput{Path: path, Surface: officedocuments.Recent, Application: "supplied"},
			func(c *JSONCapture) error { called = true; _, err := c.ReadJSON(context.Background()); return err }); err == nil || called {
			t.Error("JSON capture entered the held source lifetime")
		}
		blocked("JSON format failure")
		if notes, err := onenote.CaptureIndex(shortContext(t), path); err == nil || notes != nil {
			t.Error("notes capture entered the held source lifetime")
			if notes != nil { // release what it took, so the failure is this assertion and not a later hang
				_ = notes.Close()
			}
		}
		blocked("notes capture")
		if _, err := ObserveRegistry(shortContext(t), path); err == nil {
			t.Error("registry observation entered the held source lifetime")
		}
		if err := WithRegistryCapture(shortContext(t), RegistryInput{Path: path}, func(*RegistryCapture) error { return nil }); err == nil {
			t.Error("registry capture entered the held source lifetime")
		}
		blocked("registry refusal")
		if _, err := ObserveOneDriveUsage(shortContext(t), path); err == nil {
			t.Error("nested usage observation entered the held source lifetime")
		}
		if _, err := DiscoverOneDriveUsage(shortContext(t)); err == nil {
			t.Error("nested usage discovery entered the held source lifetime")
		}
		blocked("nested usage calls")
		_, err := c.ReadUsage(context.Background())
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"locked", "copied", "before-reader", "after-reader", "after-callback", "cleanup"}
	if !slices.Equal(stages, want) {
		t.Fatalf("validation stages = %v, want %v", stages, want)
	}
	registryStage = stage
	if output := officeWriterProbe(t, path); !strings.Contains(output, "WRITE_OK") {
		t.Fatalf("writer did not recover after cleanup: %s", output)
	}
	// The lifetime is free again: every family proceeds without deadlock.
	if _, err := ObserveJSON(context.Background(), path); err != nil {
		t.Fatalf("JSON observation after usage cleanup: %v", err)
	}
	if _, err := ObserveRegistry(context.Background(), path); err != nil {
		t.Fatalf("registry observation after usage cleanup: %v", err)
	}
	if err := WithOneDriveUsageCapture(context.Background(), UsageInput{Path: path}, func(*UsageCapture) error { return nil }); err != nil {
		t.Fatalf("usage capture after cleanup: %v", err)
	}
}

// A wait for the source lifetime that ends by timeout opens nothing, and the next capture runs.
func TestUsageGateWaitOpensNothing(t *testing.T) {
	path := usageSource(t)
	bounded, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	release, err := sourcecapture.Acquire(bounded)
	if err != nil {
		t.Fatalf("the source lifetime is still held by an earlier capture: %v", err)
	}
	t.Cleanup(release)
	stat := registryStat
	t.Cleanup(func() { registryStat = stat })
	opened := false
	registryStat = func(f *os.File) (os.FileInfo, error) { opened = true; return stat(f) }
	called := false
	err = WithOneDriveUsageCapture(shortContext(t), UsageInput{Path: path}, func(*UsageCapture) error { called = true; return nil })
	if !errors.Is(err, context.DeadlineExceeded) || usageCode(err) != "onedrive_usage_capture_failed" || called || opened {
		t.Fatalf("a capture that never got the source lifetime opened its source: %v", err)
	}
	if signature, err := ObserveOneDriveUsage(shortContext(t), path); signature != "" || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("an observation that never got the source lifetime published evidence: %v", err)
	}
	release()
	registryStat = stat
	if err := WithOneDriveUsageCapture(context.Background(), UsageInput{Path: path}, func(*UsageCapture) error { return nil }); err != nil {
		t.Fatalf("capture after the lifetime was released: %v", err)
	}
}

// A platform without input qualification refuses every usage input with the fixed code before
// opening it, and has no default discovery.
func TestUsageUnqualifiedPlatformRefusal(t *testing.T) {
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		t.Skip("this platform qualifies ordinary inputs")
	}
	path := sqliteSource(t, usageLeaf, usageFixtureSchema)
	if signature, err := ObserveOneDriveUsage(context.Background(), path); signature != "" || usageCode(err) != "onedrive_usage_input_unqualified" {
		t.Fatalf("unqualified platform observed a usage input: %q %v", signature, err)
	}
	called := false
	err := WithOneDriveUsageCapture(context.Background(), UsageInput{Path: path}, func(*UsageCapture) error { called = true; return nil })
	if called || usageCode(err) != "onedrive_usage_input_unqualified" {
		t.Fatalf("unqualified platform captured a usage input: %v", err)
	}
	if discovery, err := DiscoverOneDriveUsage(context.Background()); len(discovery.Inputs) != 0 || usageCode(err) != "onedrive_usage_default_unsupported" {
		t.Fatalf("unqualified platform discovered usage inputs: %v", err)
	}
	if output := officeWriterProbe(t, path); !strings.Contains(output, "WRITE_OK") {
		t.Fatalf("a refused input left a writer held out: %s", output)
	}
}

// Every usage entry point has one 30-second deadline of its own that covers the wait for the
// source lifetime: a caller without a deadline does not wait forever for another capture.
func TestUsageEntryPointsHaveTheirOwnDeadline(t *testing.T) {
	if usageDeadline != 30*time.Second {
		t.Fatalf("usage deadline = %v, want 30s", usageDeadline)
	}
	path := usageSource(t)
	bounded, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	release, err := sourcecapture.Acquire(bounded)
	if err != nil {
		t.Fatalf("the source lifetime is still held by an earlier capture: %v", err)
	}
	t.Cleanup(release)
	deadline, mac := usageDeadline, discoveryMac
	t.Cleanup(func() { usageDeadline, discoveryMac = deadline, mac })
	usageDeadline, discoveryMac = 50*time.Millisecond, true
	for name, call := range map[string]func() error{
		"capture": func() error {
			return WithOneDriveUsageCapture(context.Background(), UsageInput{Path: path}, func(*UsageCapture) error { return nil })
		},
		"observation": func() error { _, err := ObserveOneDriveUsage(context.Background(), path); return err },
		"discovery":   func() error { _, err := DiscoverOneDriveUsage(context.Background()); return err },
	} {
		done := make(chan error, 1)
		go func() { done <- call() }()
		select {
		case err := <-done:
			if !errors.Is(err, context.DeadlineExceeded) || !strings.HasPrefix(usageCode(err), "onedrive_usage_") {
				t.Fatalf("%s without a caller deadline, behind a held source lifetime: %v", name, err)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("%s has no deadline of its own: it is still waiting for the source lifetime", name)
		}
	}
}
