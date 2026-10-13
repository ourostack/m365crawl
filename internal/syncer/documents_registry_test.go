package syncer

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/officecapture"
	"github.com/ourostack/m365crawl/internal/store"
	_ "modernc.org/sqlite"
)

const registryFixtureSQL = `
CREATE TABLE HKEY_CURRENT_USER(node_id INTEGER PRIMARY KEY,parent_id,name TEXT,write_time);
CREATE TABLE HKEY_CURRENT_USER_values(node_id INTEGER,name TEXT,type INTEGER,value);
INSERT INTO HKEY_CURRENT_USER VALUES
(1,-1,'HKEY_CURRENT_USER',NULL),(2,1,'Software',NULL),(3,2,'Microsoft',NULL),
(4,3,'Office',NULL),(5,4,'15.0',NULL),(6,5,'Common',NULL),(7,6,'MruUserData',NULL),
(8,7,'UnsignedUser',NULL),(9,8,'Word',NULL),(10,9,'Local',NULL),
(11,10,'Documents',NULL),(12,11,'synthetic-key',NULL),(13,11,'unmapped-key',NULL);
INSERT INTO HKEY_CURRENT_USER_values VALUES
(12,'FileName',1,'Synthetic registry title'),(12,'DocumentUrl',1,'file:///synthetic/registry-document.docx'),
(12,'Path',1,'Synthetic friendly path'),(12,'Timestamp',1,'2026-10-09T12:34:56Z'),
(12,'Application',1,'synthetic application value'),(12,'FileSizeInBytes',11,42),(12,'IsPinned',4,1);
`

// registryFileFixture writes one ordinary synthetic registry SQLite file. extra statements run last.
func registryFileFixture(t *testing.T, name string, extra ...string) string {
	t.Helper()
	if runtime.GOOS != "darwin" && runtime.GOOS != "windows" {
		t.Skip("native source admission is Darwin/Windows")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, name)
	uri := url.URL{Scheme: "file", Path: "/" + strings.TrimPrefix(filepath.ToSlash(path), "/")}
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range append([]string{registryFixtureSQL}, extra...) {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func sourceByLocator(t *testing.T, sources []store.KnowledgeSource, locator string) store.KnowledgeSource {
	t.Helper()
	for _, source := range sources {
		if source.Locator == locator {
			return source
		}
	}
	t.Fatalf("source %s missing from %+v", filepath.Base(locator), sources)
	return store.KnowledgeSource{}
}

func stubDocumentDiscovery(t *testing.T, jsonInputs func() ([]officecapture.DocumentInput, error), registryInputs func() ([]officecapture.RegistryInput, error)) {
	t.Helper()
	discover, registry, usage := documentsDiscover, documentsRegistryDiscover, documentsUsageDiscover
	t.Cleanup(func() {
		documentsDiscover, documentsRegistryDiscover, documentsUsageDiscover = discover, registry, usage
	})
	// The usage family finds nothing and reports nothing here; its own tests stub it after this.
	documentsUsageDiscover = func(context.Context) (officecapture.UsageDiscovery, error) {
		return officecapture.UsageDiscovery{}, nil
	}
	documentsDiscover = func(_ context.Context, root string) ([]officecapture.DocumentInput, error) {
		if root != "" {
			return discover(context.Background(), root)
		}
		return jsonInputs()
	}
	documentsRegistryDiscover = func(context.Context) ([]officecapture.RegistryInput, error) { return registryInputs() }
}

func TestRegistrySyncSelectionAndIndependentFailures(t *testing.T) {
	good := registryFileFixture(t, "MicrosoftRegistrationDB_good.reg")
	broken := filepath.Join(filepath.Dir(good), "MicrosoftRegistrationDB_broken.reg")
	if err := os.WriteFile(broken, []byte(strings.Repeat("not a database ", 16)), 0o600); err != nil {
		t.Fatal(err)
	}
	root := documentRootFixture(t)
	path := newDB(t)
	ctx := context.Background()
	for _, options := range []DocumentOptions{
		{DBPath: path, RegistryFile: good, Root: root, Explicit: true, Discover: true},
		{DBPath: path, RegistryFile: good, SourceID: 1, Explicit: true, Discover: true},
		{DBPath: path, RegistryFile: good, Explicit: true},
		{DBPath: path, RegistryFile: good},
	} {
		if _, err := SyncDocuments(ctx, options); err == nil {
			t.Fatalf("non-exclusive selection accepted: %+v", options)
		}
	}
	noDiscovery := func(family string) {
		stubDocumentDiscovery(t, func() ([]officecapture.DocumentInput, error) {
			t.Errorf("%s discovered JSON by default", family)
			return nil, nil
		},
			func() ([]officecapture.RegistryInput, error) {
				t.Errorf("%s discovered the default registry", family)
				return nil, nil
			})
	}
	noDiscovery("--registry-file")
	report, err := SyncDocuments(ctx, DocumentOptions{DBPath: path, RegistryFile: good, Explicit: true, Discover: true})
	if err != nil || report.Captured != 1 || report.Failed != 0 || len(report.Sources) != 1 || len(report.Discovery) != 0 ||
		report.Sources[0].Kind != store.RegistryDocumentKind || report.Sources[0].Coverage != "partial" ||
		report.Sources[0].Losses["office_registry_document_unmapped"] != 1 || report.Sources[0].Qualification != store.RegistryDocumentQualification {
		t.Fatalf("supplied registry file capture: %+v %v", report, err)
	}
	registered := report.Sources[0]
	noDiscovery("--root")
	report, err = SyncDocuments(ctx, DocumentOptions{DBPath: path, Root: root, Explicit: true, Discover: true})
	if err != nil || report.Captured != 2 || len(report.Sources) != 2 {
		t.Fatalf("JSON root after registry registration: %+v %v", report, err)
	}
	for _, source := range report.Sources {
		if source.Kind == store.RegistryDocumentKind {
			t.Fatal("JSON-root selection refreshed a registry source")
		}
	}
	// Default discovery: the JSON family is refused, the registry family finds a good and a broken leaf.
	stubDocumentDiscovery(t, func() ([]officecapture.DocumentInput, error) {
		return nil, &officecapture.ReadError{Code: "office_json_input_unqualified"}
	}, func() ([]officecapture.RegistryInput, error) {
		return []officecapture.RegistryInput{{Path: broken}, {Path: good}}, nil
	})
	report, err = SyncDocuments(ctx, DocumentOptions{DBPath: path, Explicit: true, Discover: true})
	if err != nil || report.Failed != 1 || report.Unchanged != 3 || report.Captured != 0 || len(report.Sources) != 4 ||
		len(report.Discovery) != 1 || report.Discovery[0] != (DocumentDiscovery{Kind: "office_json", Code: "office_json_input_unqualified"}) {
		t.Fatalf("one refused family or one failed source suppressed its peers: %+v %v", report, err)
	}
	failed := sourceByLocator(t, report.Sources, broken)
	if failed.ID != 0 || failed.AdmittedID != 0 || failed.Status != "failed" || failed.Reason != "office_registry_input_unqualified" ||
		failed.Kind != store.RegistryDocumentKind {
		t.Fatalf("first failed registry capture invented an admitted source: %+v", failed)
	}
	healthy := sourceByLocator(t, report.Sources, good)
	if healthy.ID != registered.ID || healthy.Status != "unchanged" || healthy.AdmittedID <= registered.AdmittedID {
		t.Fatalf("healthy registry peer not refreshed independently: %+v", healthy)
	}
	// The registry family is unavailable: the registered registry source and the JSON family still run.
	stubDocumentDiscovery(t, func() ([]officecapture.DocumentInput, error) { return nil, nil },
		func() ([]officecapture.RegistryInput, error) {
			return nil, &officecapture.ReadError{Code: "office_registry_unavailable"}
		})
	report, err = SyncDocuments(ctx, DocumentOptions{DBPath: path, Explicit: true, Discover: true})
	if err != nil || len(report.Discovery) != 1 || report.Discovery[0] != (DocumentDiscovery{Kind: store.RegistryDocumentKind, Code: "office_registry_unavailable"}) ||
		report.Unchanged != 3 || report.Failed != 1 {
		t.Fatalf("unavailable registry family suppressed registered sources: %+v %v", report, err)
	}
	// A named registered source refreshes alone.
	before := sourceByLocator(t, report.Sources, broken)
	report, err = SyncDocuments(ctx, DocumentOptions{DBPath: path, SourceID: healthy.ID, Explicit: true})
	if err != nil || len(report.Sources) != 1 || report.Unchanged != 1 || report.Sources[0].ID != healthy.ID {
		t.Fatalf("named registry source refresh: %+v %v", report, err)
	}
	s, err := store.OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	sources, err := s.DocumentSources(ctx)
	rows, _, rowsErr := s.DocumentObservations(ctx, store.DocumentFilter{SourceID: healthy.ID})
	if closeErr := s.Close(); err != nil || rowsErr != nil || closeErr != nil {
		t.Fatal(err, rowsErr, closeErr)
	}
	if sourceByLocator(t, sources, broken).AttemptID != before.AttemptID {
		t.Fatal("named refresh touched an unselected failed peer")
	}
	if len(rows) != 1 || rows[0].Variant != store.RegistryDocumentKind || rows[0].Registry.Title != "Synthetic registry title" ||
		rows[0].OpenedAt == nil || !rows[0].OpenedAt.Equal(time.Date(2026, 10, 9, 12, 34, 56, 0, time.UTC)) ||
		rows[0].CaptureID != registered.AdmittedID {
		t.Fatalf("registry observation or its original capture clock changed: %+v", rows)
	}
	// Ordinary reads refresh registered sources only and never discover.
	noDiscovery("an ordinary read")
	refresh, err := SyncDocuments(ctx, DocumentOptions{DBPath: path, MaxAge: time.Hour})
	if err != nil || refresh.Skipped != 4 || refresh.Failed != 0 || len(refresh.Sources) != 4 {
		t.Fatalf("ordinary read: %+v %v", refresh, err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	stubDocumentDiscovery(t, func() ([]officecapture.DocumentInput, error) { return nil, nil },
		func() ([]officecapture.RegistryInput, error) { cancel(); return nil, context.Canceled })
	if _, err := SyncDocuments(cancelled, DocumentOptions{DBPath: path, Explicit: true, Discover: true}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled discovery became a source outcome: %v", err)
	}
}

func TestRegistryRunnerFailureBoundaries(t *testing.T) {
	for _, fault := range []string{"read", "empty-capture", "panic", "refused", "cancel-capture", "commit", "stale"} {
		t.Run(fault, func(t *testing.T) {
			file := registryFileFixture(t, "MicrosoftRegistrationDB_fault.reg")
			path := newDB(t)
			ctx := context.Background()
			options := DocumentOptions{DBPath: path, RegistryFile: file, Explicit: true, Discover: true}
			now, capture := documentsNow, documentsRegistryCapture
			t.Cleanup(func() { documentsNow, documentsRegistryCapture = now, capture })
			documentsRegistryCapture = func(ctx context.Context, input officecapture.RegistryInput, consume func(*officecapture.RegistryCapture) error) error {
				switch fault {
				case "read":
					return capture(ctx, input, func(c *officecapture.RegistryCapture) error {
						_ = c.Close()
						return consume(c)
					})
				case "empty-capture":
					return nil
				case "panic":
					panic("private source payload")
				case "refused":
					return &officecapture.ReadError{Code: "office_registry_source_busy"}
				case "cancel-capture":
					return context.Canceled
				case "stale":
					if err := capture(ctx, input, consume); err != nil {
						return err
					}
					st, err := store.Open(ctx, path)
					if err != nil {
						return err
					}
					_, err = st.BeginRegistryDocumentAttempt(ctx, store.KnowledgeInput{Kind: store.RegistryDocumentKind,
						Platform: documentsPlatform, Locator: input.Path, At: now()})
					return errors.Join(err, st.Close())
				}
				return capture(ctx, input, consume)
			}
			if fault == "commit" {
				calls := 0
				documentsNow = func() time.Time {
					if calls++; calls > 1 {
						return time.Time{}
					}
					return now()
				}
			}
			report, err := SyncDocuments(ctx, options)
			if fault == "commit" || fault == "stale" {
				if err == nil || (fault == "stale") != errors.Is(err, store.ErrStaleKnowledgeAttempt) {
					t.Fatalf("archive fault became a source outcome: %+v %v", report, err)
				}
				return
			}
			want := map[string][2]string{"read": {"failed", "office_registry_capture_failed"}, "empty-capture": {"failed", "office_registry_capture_failed"},
				"panic": {"failed", "office_registry_capture_failed"}, "refused": {"failed", "office_registry_source_busy"},
				"cancel-capture": {"cancelled", "office_registry_cancelled"}}[fault]
			if err != nil || report.Failed != 1 || len(report.Sources) != 1 || report.Sources[0].Status != want[0] ||
				report.Sources[0].Reason != want[1] || report.Sources[0].ID != 0 {
				t.Fatalf("registry source failure: %+v %v", report, err)
			}
		})
	}
}

func TestRegistryRunnerEmptyIndexFamilySelectionAndDiscoveryFault(t *testing.T) {
	empty := registryFileFixture(t, "MicrosoftRegistrationDB_empty.reg",
		`DELETE FROM HKEY_CURRENT_USER_values; DELETE FROM HKEY_CURRENT_USER WHERE node_id IN (12,13);`)
	root := documentRootFixture(t)
	path := newDB(t)
	ctx := context.Background()
	if _, err := SyncDocuments(ctx, DocumentOptions{DBPath: path, Root: root, Explicit: true, Discover: true}); err != nil {
		t.Fatal(err)
	}
	// With JSON sources registered, a supplied registry file still selects only itself.
	report, err := SyncDocuments(ctx, DocumentOptions{DBPath: path, RegistryFile: empty, Explicit: true, Discover: true})
	if err != nil || report.Captured != 1 || len(report.Sources) != 1 || report.Sources[0].Status != "complete" ||
		report.Sources[0].Reason != RegistryListedNoDocuments || len(report.Sources[0].Losses) != 0 {
		t.Fatalf("registry index that lists no documents: %+v %v", report, err)
	}
	stubDocumentDiscovery(t, func() ([]officecapture.DocumentInput, error) { return nil, os.ErrPermission },
		func() ([]officecapture.RegistryInput, error) {
			t.Error("registry discovery ran after a fatal fault")
			return nil, nil
		})
	if _, err := SyncDocuments(ctx, DocumentOptions{DBPath: path, Explicit: true, Discover: true}); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("an unclassified discovery fault became a family outcome: %v", err)
	}
}

func TestRegistryFileMustBeAnOrdinaryFileOtherThanTheArchive(t *testing.T) {
	good := registryFileFixture(t, "MicrosoftRegistrationDB_good.reg")
	path := newDB(t)
	ctx := context.Background()
	if _, err := SyncDocuments(ctx, DocumentOptions{DBPath: path, RegistryFile: good, Explicit: true, Discover: true}); err != nil {
		t.Fatal(err)
	}
	capture := documentsRegistryCapture
	t.Cleanup(func() { documentsRegistryCapture = capture })
	documentsRegistryCapture = func(context.Context, officecapture.RegistryInput, func(*officecapture.RegistryCapture) error) error {
		t.Error("an unusable registry file reached capture")
		return nil
	}
	for name, file := range map[string]string{"missing": good + ".missing", "directory": filepath.Dir(good), "archive": path,
		"archive-wal": path + "-wal", "archive-shm": path + "-shm", "archive-journal": path + "-journal", "archive-lock": path + ".lock",
		"archive-other-spelling": filepath.Dir(path) + string(filepath.Separator) + "." + string(filepath.Separator) + filepath.Base(path)} {
		report, err := SyncDocuments(ctx, DocumentOptions{DBPath: path, RegistryFile: file, Explicit: true, Discover: true})
		if !errors.Is(err, ErrRegistryFileUnusable) || len(report.Sources) != 0 {
			t.Fatalf("%s: %+v %v", name, report, err)
		}
	}
	s, err := store.OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	sources, err := s.DocumentSources(ctx)
	if closeErr := s.Close(); err != nil || closeErr != nil || len(sources) != 1 {
		t.Fatalf("an unusable registry file registered a source: %+v %v %v", sources, err, closeErr)
	}
}
