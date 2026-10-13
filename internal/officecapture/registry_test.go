package officecapture

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/officedocuments"
)

const registryFixtureSQL = `
CREATE TABLE HKEY_CURRENT_USER(node_id INTEGER PRIMARY KEY,parent_id,name TEXT,write_time);
CREATE TABLE HKEY_CURRENT_USER_values(node_id INTEGER,name TEXT,type INTEGER,value);
INSERT INTO HKEY_CURRENT_USER VALUES
(1,-1,'HKEY_CURRENT_USER',NULL),(2,1,'Software',NULL),(3,2,'Microsoft',NULL),
(4,3,'Office',NULL),(5,4,'15.0',NULL),(6,5,'Common',NULL),(7,6,'MruUserData',NULL),
(8,7,'UnsignedUser',NULL),(9,8,'Word',NULL),(10,9,'Local',NULL),
(11,10,'Documents',NULL),(12,11,'synthetic-key',NULL);
INSERT INTO HKEY_CURRENT_USER_values VALUES
(12,'FileName',1,'Synthetic title'),(12,'DocumentUrl',1,'file:///synthetic/document.docx'),
(12,'Path',1,'Synthetic friendly path'),(12,'Timestamp',1,'2026-10-09T12:34:56Z'),
(12,'Application',1,'synthetic application value'),(12,'FileSizeInBytes',11,42),(12,'IsPinned',4,1);
`

func registrySource(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "darwin" && runtime.GOOS != "windows" {
		t.Skip("ordinary input qualification is Darwin/Windows only")
	}
	return sqliteSource(t, "MicrosoftRegistrationDB_synthetic.reg", registryFixtureSQL)
}

func registryCode(err error) string {
	var fixed *ReadError
	if errors.As(err, &fixed) {
		return fixed.Code
	}
	return ""
}

func TestRegistryCapturePrivateCopyAndCleanup(t *testing.T) {
	path := registrySource(t)
	before, err := os.ReadFile(path) //nolint:gosec // synthetic fixture
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	observed, err := ObserveRegistry(context.Background(), path)
	if err != nil || observed == "" {
		t.Fatalf("observation: %q, %v", observed, err)
	}
	var private string
	err = WithRegistryCapture(context.Background(), RegistryInput{Path: path}, func(c *RegistryCapture) error {
		private = c.Path
		if c.Path == path || c.ContentDigest != fmt.Sprintf("%x", sha256.Sum256(before)) ||
			c.AcquisitionSignature != observed || c.AcquiredAt.IsZero() || !c.SourceModified.Equal(info.ModTime()) {
			t.Fatal("capture identity or clocks missing")
		}
		result, err := c.ReadRegistry(context.Background())
		if err != nil {
			return err
		}
		if len(result.Documents) != 1 || result.Documents[0].Title != "Synthetic title" ||
			result.Documents[0].ApplicationPath != "Word" || *result.Documents[0].Size != 42 || !*result.Documents[0].Pinned {
			t.Fatalf("native registry claims lost: %#v", result)
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
		t.Fatal("registry source bytes changed")
	}
	if output := officeWriterProbe(t, path); !strings.Contains(output, "WRITE_OK") {
		t.Fatalf("writer did not recover after registry capture: %s", output)
	}
}

func TestRegistryInclusiveSizeLimit(t *testing.T) {
	if registryLimit != 64<<20 {
		t.Fatal("registry input limit is not 64 MiB")
	}
	path := registrySource(t)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	limit := registryLimit
	t.Cleanup(func() { registryLimit = limit })
	registryLimit = info.Size()
	if err := WithRegistryCapture(context.Background(), RegistryInput{Path: path}, func(*RegistryCapture) error { return nil }); err != nil {
		t.Fatalf("input at the inclusive limit refused: %v", err)
	}
	registryLimit = info.Size() - 1
	called := false
	err = WithRegistryCapture(context.Background(), RegistryInput{Path: path}, func(*RegistryCapture) error { called = true; return nil })
	if called || registryCode(err) != "office_registry_index_too_large" {
		t.Fatalf("input one byte over the limit admitted: %v", err)
	}
}

func TestRegistryRollbackHeaderCompanionsAndLinks(t *testing.T) {
	for _, tc := range []struct{ name, code string }{
		{"wal-header", "office_registry_input_unqualified"},
		{"nonempty-journal", "office_registry_source_busy"},
		{"nonempty-wal", "office_registry_input_unqualified"},
		{"shm", "office_registry_input_unqualified"},
		{"linked-companion", "office_registry_input_unqualified"},
		{"hard-link", "office_registry_input_unqualified"},
		{"not-sqlite", "office_registry_input_unqualified"},
		{"directory", "office_registry_input_unqualified"},
		{"relative", "office_registry_input_unqualified"},
		{"empty-journal", ""},
		{"empty-wal", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := registrySource(t)
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
			case "empty-journal":
				write(path+"-journal", nil)
			case "empty-wal":
				write(path+"-wal", nil)
			}
			called := false
			err := WithRegistryCapture(context.Background(), RegistryInput{Path: path}, func(c *RegistryCapture) error {
				called = true
				_, err := c.ReadRegistry(context.Background())
				return err
			})
			if tc.code == "" {
				if err != nil || !called {
					t.Fatalf("ordinary zero-length companion refused: %v", err)
				}
				return
			}
			if called || registryCode(err) != tc.code {
				t.Fatalf("unsafe registry input admitted or misreported: %v", err)
			}
		})
	}
}

func TestRegistryJSONSharedLifetimeWithRealCapture(t *testing.T) {
	path := registrySource(t)
	err := WithRegistryCapture(context.Background(), RegistryInput{Path: path}, func(c *RegistryCapture) error {
		_, _ = ObserveJSON(shortContext(t), path)
		_ = WithJSONCapture(shortContext(t), DocumentInput{Path: path, Surface: officedocuments.Recent, Application: "supplied"},
			func(*JSONCapture) error { return nil })
		if _, err := ObserveRegistry(shortContext(t), path); err == nil {
			t.Error("nested registry observation entered the held source lifetime")
		}
		if output := officeWriterProbe(t, path); !strings.Contains(output, "WRITE_BLOCKED") {
			t.Errorf("JSON calls released registry SHARED protection: %s", output)
		}
		_, err := c.ReadRegistry(context.Background())
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}
