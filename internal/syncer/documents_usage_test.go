package syncer

import (
	"context"
	"database/sql"
	"encoding/json"
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
)

const usageFixtureSQL = `
CREATE TABLE recent_files_spo(ID TEXT PRIMARY KEY,ItemId TEXT,ListId TEXT,WebId TEXT,SiteId TEXT,
LastModifiedDateTime,LastAccessedDateTime,FileModifiedDateTime,LastSharedDateTime,SavedDateTime,
Type TEXT,Source TEXT,Extension TEXT,WorkingSetId TEXT,HiddenFromShared INTEGER,IsLocalPlaceHolder INTEGER);
CREATE TABLE recent_files_formatted_spo(ID TEXT,Format TEXT NOT NULL,FormattedValue TEXT NOT NULL,PRIMARY KEY(ID,Format));
CREATE TABLE top_collaborators(ID TEXT PRIMARY KEY,FormattedValue TEXT);
INSERT INTO recent_files_spo VALUES
('synthetic-id','native-item','native-list','native-web','native-site','2026-10-10T10:00:00Z','2026-10-11T11:00:00Z','2026-10-09T12:34:56Z','2026-10-08T09:00:00Z',NULL,
'Word','native-source','docx','working-set',0,1),
('recent-only','native-item-2','native-list','native-web','native-site',NULL,NULL,'not a clock',NULL,NULL,'Word','native-source','docx','working-set',NULL,NULL);
INSERT INTO top_collaborators VALUES('native-person','{"Id":"stated-person","DisplayName":"Synthetic Person","EmailAddresses":["person@example.invalid"]}');
`

// usageFormattedSQL is one recent_files_formatted_spo row: the document JSON, escaped as the source stores it.
func usageFormattedSQL(t *testing.T, id, format, title string) string {
	t.Helper()
	body := `{"file":{"FileName":"` + title + `","SharePointItem":{"SiteId":"s","WebId":"w","ListId":"l","UniqueId":"u"},` +
		`"AllExtensions":{"SharingHistory":{"Instances":[{"SharedByTime":"2026-10-08T09:00:00Z","Participants":[{"DisplayName":"Synthetic Person","Smtp":"person@example.invalid"}]}]}}}}`
	escaped, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return "INSERT INTO recent_files_formatted_spo VALUES('" + id + "','" + format + "','" +
		strings.ReplaceAll(strings.Trim(string(escaped), `"`), "'", "''") + "')"
}

func usageExec(t *testing.T, path string, statements ...string) {
	t.Helper()
	uri := url.URL{Scheme: "file", Path: "/" + strings.TrimPrefix(filepath.ToSlash(path), "/")}
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

// usageFileFixture writes one ordinary synthetic usage SQLite file with two recent rows, two
// formatted variants of the first and one collaborator. extra statements run last.
func usageFileFixture(t *testing.T, name string, extra ...string) string {
	t.Helper()
	if runtime.GOOS != "darwin" && runtime.GOOS != "windows" {
		t.Skip("native source admission is Darwin/Windows")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, name)
	usageExec(t, path, append([]string{usageFixtureSQL, usageFormattedSQL(t, "synthetic-id", "variant-a", "Synthetic usage title"),
		usageFormattedSQL(t, "synthetic-id", "variant-b", "Synthetic usage title")}, extra...)...)
	return path
}

func stubUsageDiscovery(t *testing.T, usage func() (officecapture.UsageDiscovery, error)) {
	t.Helper()
	discover := documentsUsageDiscover
	t.Cleanup(func() { documentsUsageDiscover = discover })
	documentsUsageDiscover = func(context.Context) (officecapture.UsageDiscovery, error) { return usage() }
}

func usageRowsOf(t *testing.T, path string, sourceID int64) []store.DocumentObservationRow {
	t.Helper()
	s, err := store.OpenReadOnly(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	rows, _, err := s.DocumentObservations(context.Background(), store.DocumentFilter{SourceID: sourceID})
	if closeErr := s.Close(); err != nil || closeErr != nil {
		t.Fatal(err, closeErr)
	}
	return rows
}

func TestUsageSyncIndependentAndRetry(t *testing.T) {
	good := usageFileFixture(t, "Microsoft.FileUsageSync.db")
	broken := filepath.Join(filepath.Dir(good), "broken.db")
	if err := os.WriteFile(broken, []byte(strings.Repeat("not a database ", 16)), 0o600); err != nil {
		t.Fatal(err)
	}
	registry := registryFileFixture(t, "MicrosoftRegistrationDB_good.reg")
	root := documentRootFixture(t)
	path := newDB(t)
	ctx := context.Background()
	for _, options := range []DocumentOptions{
		{DBPath: path, UsageFile: good, Root: root, Explicit: true, Discover: true},
		{DBPath: path, UsageFile: good, RegistryFile: registry, Explicit: true, Discover: true},
		{DBPath: path, UsageFile: good, SourceID: 1, Explicit: true, Discover: true},
		{DBPath: path, UsageFile: good, Explicit: true},
		{DBPath: path, UsageFile: good},
	} {
		if _, err := SyncDocuments(ctx, options); err == nil {
			t.Fatalf("non-exclusive selection accepted: %+v", options)
		}
	}
	noDiscovery := func(selection string) {
		stubDocumentDiscovery(t, func() ([]officecapture.DocumentInput, error) {
			t.Errorf("%s discovered JSON by default", selection)
			return nil, nil
		}, func() ([]officecapture.RegistryInput, error) {
			t.Errorf("%s discovered the default registry", selection)
			return nil, nil
		})
		stubUsageDiscovery(t, func() (officecapture.UsageDiscovery, error) {
			t.Errorf("%s discovered default usage files", selection)
			return officecapture.UsageDiscovery{}, nil
		})
	}
	// A supplied usage file acquires only itself.
	noDiscovery("--onedrive-usage-file")
	report, err := SyncDocuments(ctx, DocumentOptions{DBPath: path, UsageFile: good, Explicit: true, Discover: true})
	if err != nil || report.Captured != 1 || report.Failed != 0 || len(report.Sources) != 1 || len(report.Discovery) != 0 ||
		report.Sources[0].Kind != store.UsageDocumentKind || report.Sources[0].Coverage != "observations" || report.Sources[0].Status != "complete" ||
		report.Sources[0].Qualification != store.UsageDocumentQualification || report.Sources[0].ID == 0 {
		t.Fatalf("supplied usage file capture: %+v %v", report, err)
	}
	registered := report.Sources[0]
	rows := usageRowsOf(t, path, registered.ID)
	types := map[string]int{}
	for _, row := range rows {
		if row.Variant != store.UsageDocumentKind || row.Usage == nil || row.CaptureID != registered.AdmittedID {
			t.Fatalf("usage row: %+v", row)
		}
		types[row.Usage.Type]++
	}
	if len(rows) != 5 || types[store.UsageTypeRecent] != 2 || types[store.UsageTypeDocument] != 2 || types[store.UsageTypeCollaborator] != 1 {
		t.Fatalf("recent-only, variant or collaborator evidence missing: %v", types)
	}
	// The other selections never touch a usage source, and a usage selection never touches theirs.
	for name, options := range map[string]DocumentOptions{"--root": {DBPath: path, Root: root, Explicit: true, Discover: true},
		"--registry-file": {DBPath: path, RegistryFile: registry, Explicit: true, Discover: true}} {
		noDiscovery(name)
		report, err = SyncDocuments(ctx, options)
		if err != nil || report.Captured == 0 {
			t.Fatalf("%s after usage registration: %+v %v", name, report, err)
		}
		for _, source := range report.Sources {
			if source.Kind == store.UsageDocumentKind {
				t.Fatalf("%s refreshed a usage source", name)
			}
		}
	}
	noDiscovery("--onedrive-usage-file")
	report, err = SyncDocuments(ctx, DocumentOptions{DBPath: path, UsageFile: good, Explicit: true, Discover: true})
	if err != nil || report.Unchanged != 1 || len(report.Sources) != 1 || report.Sources[0].ID != registered.ID ||
		report.Sources[0].Status != "unchanged" || report.Sources[0].AdmittedID <= registered.AdmittedID {
		t.Fatalf("unchanged retry of the supplied file: %+v %v", report, err)
	}
	for _, row := range usageRowsOf(t, path, registered.ID) {
		if row.CaptureID != registered.AdmittedID || row.Retained {
			t.Fatalf("an unchanged retry rewrote the original capture of a row: %+v", row)
		}
	}
	// Default discovery: JSON refused, registry unavailable, usage finds a good and a broken file
	// and one account directory it does not read.
	unsupported := filepath.Join(filepath.Dir(good), "Business_other")
	stubDocumentDiscovery(t, func() ([]officecapture.DocumentInput, error) {
		return nil, &officecapture.ReadError{Code: "office_json_input_unqualified"}
	}, func() ([]officecapture.RegistryInput, error) {
		return nil, &officecapture.ReadError{Code: "office_registry_unavailable"}
	})
	stubUsageDiscovery(t, func() (officecapture.UsageDiscovery, error) {
		return officecapture.UsageDiscovery{Inputs: []officecapture.UsageInput{{Path: broken}, {Path: good}},
			Outcomes: []officecapture.UsageDiscoveryOutcome{{Locator: unsupported, Code: "onedrive_usage_discovery_unsupported"}}}, nil
	})
	report, err = SyncDocuments(ctx, DocumentOptions{DBPath: path, Explicit: true, Discover: true})
	if err != nil || report.Failed != 1 || report.Unchanged != 4 || report.Captured != 0 || len(report.Sources) != 5 || len(report.Discovery) != 3 ||
		report.Discovery[0] != (DocumentDiscovery{Kind: DocumentFamilyJSON, Code: "office_json_input_unqualified"}) ||
		report.Discovery[1] != (DocumentDiscovery{Kind: store.RegistryDocumentKind, Code: "office_registry_unavailable"}) ||
		report.Discovery[2] != (DocumentDiscovery{Kind: store.UsageDocumentKind, Code: "onedrive_usage_discovery_unsupported", Locator: unsupported}) {
		t.Fatalf("a refused family or a failed source suppressed its peers: %+v %v", report, err)
	}
	failed := sourceByLocator(t, report.Sources, broken)
	if failed.ID != 0 || failed.AdmittedID != 0 || failed.Status != "failed" || failed.Reason != "onedrive_usage_input_unqualified" ||
		failed.Kind != store.UsageDocumentKind {
		t.Fatalf("first failed usage capture invented an admitted source: %+v", failed)
	}
	// The usage family is unavailable: its registered sources and the other families still run.
	stubDocumentDiscovery(t, func() ([]officecapture.DocumentInput, error) { return nil, nil },
		func() ([]officecapture.RegistryInput, error) { return nil, nil })
	stubUsageDiscovery(t, func() (officecapture.UsageDiscovery, error) {
		return officecapture.UsageDiscovery{}, &officecapture.ReadError{Code: "onedrive_usage_unavailable"}
	})
	report, err = SyncDocuments(ctx, DocumentOptions{DBPath: path, Explicit: true, Discover: true})
	if err != nil || len(report.Discovery) != 1 || report.Discovery[0] != (DocumentDiscovery{Kind: store.UsageDocumentKind, Code: "onedrive_usage_unavailable"}) ||
		report.Unchanged != 4 || report.Failed != 1 {
		t.Fatalf("unavailable usage family suppressed registered sources: %+v %v", report, err)
	}
	// A named registered usage source refreshes alone, through the usage family.
	before := sourceByLocator(t, report.Sources, broken)
	noDiscovery("--source-id")
	report, err = SyncDocuments(ctx, DocumentOptions{DBPath: path, SourceID: registered.ID, Explicit: true})
	if err != nil || len(report.Sources) != 1 || report.Unchanged != 1 || report.Sources[0].ID != registered.ID || report.Sources[0].Kind != store.UsageDocumentKind {
		t.Fatalf("named usage source refresh: %+v %v", report, err)
	}
	// Ordinary reads refresh registered sources only and never discover.
	noDiscovery("an ordinary read")
	refresh, err := SyncDocuments(ctx, DocumentOptions{DBPath: path, MaxAge: time.Hour})
	if err != nil || refresh.Skipped != 5 || refresh.Failed != 0 || len(refresh.Sources) != 5 {
		t.Fatalf("ordinary read: %+v %v", refresh, err)
	}
	if sourceByLocator(t, refresh.Sources, broken).AttemptID != before.AttemptID {
		t.Fatal("a named refresh or an ordinary read touched an unselected failed peer")
	}
	// The same path under another family is another source: the usage registration is not rebound.
	noDiscovery("--registry-file")
	report, err = SyncDocuments(ctx, DocumentOptions{DBPath: path, RegistryFile: good, Explicit: true, Discover: true})
	if err != nil || report.Failed != 1 || len(report.Sources) != 1 || report.Sources[0].Kind != store.RegistryDocumentKind || report.Sources[0].ID != 0 ||
		report.Sources[0].Reason != "office_registry_index_unrecognized" {
		t.Fatalf("a usage file supplied as a registry file: %+v %v", report, err)
	}
	if rows := usageRowsOf(t, path, registered.ID); len(rows) != 5 || rows[0].Variant != store.UsageDocumentKind {
		t.Fatalf("usage registration was rebound to the registry: %d rows", len(rows))
	}
	// A changed file is a new capture: the changed variant is new, the dropped one is retained.
	usageExec(t, good, "DELETE FROM recent_files_formatted_spo WHERE Format='variant-b'",
		"INSERT INTO recent_files_spo(ID) VALUES('added-later')")
	noDiscovery("--onedrive-usage-file")
	report, err = SyncDocuments(ctx, DocumentOptions{DBPath: path, UsageFile: good, Explicit: true, Discover: true})
	if err != nil || report.Captured != 1 || report.Sources[0].Status != "complete" || report.Sources[0].ID != registered.ID {
		t.Fatalf("changed usage file: %+v %v", report, err)
	}
	changed := report.Sources[0]
	retained, current := 0, 0
	for _, row := range usageRowsOf(t, path, registered.ID) {
		switch {
		case row.Retained && row.Usage.Type == store.UsageTypeDocument && row.Usage.Document.Format == "variant-b" && row.CaptureID == registered.AdmittedID:
			retained++
		case !row.Retained:
			current++
		}
	}
	if retained != 1 || current != 5 {
		t.Fatalf("after a changed capture: %d retained, %d current", retained, current)
	}
	// A later failure keeps the last good capture and its rows.
	if err := os.WriteFile(good, []byte(strings.Repeat("not a database ", 16)), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err = SyncDocuments(ctx, DocumentOptions{DBPath: path, UsageFile: good, Explicit: true, Discover: true})
	if err != nil || report.Failed != 1 || report.Sources[0].Status != "failed" || report.Sources[0].ID != registered.ID ||
		report.Sources[0].AdmittedID != changed.AdmittedID || report.Sources[0].Reason != "onedrive_usage_input_unqualified" {
		t.Fatalf("failure after a good capture: %+v %v", report, err)
	}
	if rows := usageRowsOf(t, path, registered.ID); len(rows) != 6 {
		t.Fatalf("a failed capture removed rows: %d", len(rows))
	}
	cancelled, cancel := context.WithCancel(ctx)
	stubDocumentDiscovery(t, func() ([]officecapture.DocumentInput, error) { return nil, nil },
		func() ([]officecapture.RegistryInput, error) { return nil, nil })
	stubUsageDiscovery(t, func() (officecapture.UsageDiscovery, error) {
		cancel()
		return officecapture.UsageDiscovery{}, context.Canceled
	})
	if _, err := SyncDocuments(cancelled, DocumentOptions{DBPath: path, Explicit: true, Discover: true}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled discovery became a source outcome: %v", err)
	}
	stubUsageDiscovery(t, func() (officecapture.UsageDiscovery, error) { return officecapture.UsageDiscovery{}, os.ErrPermission })
	if _, err := SyncDocuments(ctx, DocumentOptions{DBPath: path, Explicit: true, Discover: true}); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("an unclassified discovery fault became a family outcome: %v", err)
	}
}

func TestUsageRunnerFailureBoundaries(t *testing.T) {
	for _, fault := range []string{"read", "empty-capture", "panic", "refused", "cancel-capture", "commit", "stale"} {
		t.Run(fault, func(t *testing.T) {
			file := usageFileFixture(t, "usage-fault.db")
			path := newDB(t)
			ctx := context.Background()
			options := DocumentOptions{DBPath: path, UsageFile: file, Explicit: true, Discover: true}
			now, capture := documentsNow, documentsUsageCapture
			t.Cleanup(func() { documentsNow, documentsUsageCapture = now, capture })
			documentsUsageCapture = func(ctx context.Context, input officecapture.UsageInput, consume func(*officecapture.UsageCapture) error) error {
				switch fault {
				case "read":
					return capture(ctx, input, func(c *officecapture.UsageCapture) error {
						_ = c.Close()
						return consume(c)
					})
				case "empty-capture":
					return nil
				case "panic":
					panic("private source payload")
				case "refused":
					return &officecapture.ReadError{Code: "onedrive_usage_source_busy"}
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
					_, err = st.BeginUsageDocumentAttempt(ctx, store.KnowledgeInput{Kind: store.UsageDocumentKind,
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
			want := map[string][2]string{"read": {"failed", "onedrive_usage_capture_failed"}, "empty-capture": {"failed", "onedrive_usage_capture_failed"},
				"panic": {"failed", "onedrive_usage_capture_failed"}, "refused": {"failed", "onedrive_usage_source_busy"},
				"cancel-capture": {"cancelled", "onedrive_usage_cancelled"}}[fault]
			if err != nil || report.Failed != 1 || len(report.Sources) != 1 || report.Sources[0].Status != want[0] ||
				report.Sources[0].Reason != want[1] || report.Sources[0].ID != 0 {
				t.Fatalf("usage source failure: %+v %v", report, err)
			}
			// Nothing staged inside the wrapper was published.
			if rows := usageRowsOf(t, path, 0); len(rows) != 0 {
				t.Fatalf("a failed capture published %d rows", len(rows))
			}
		})
	}
}

func TestUsageRunnerEmptyAndPartialIndex(t *testing.T) {
	empty := usageFileFixture(t, "usage-empty.db",
		"DELETE FROM recent_files_formatted_spo; DELETE FROM recent_files_spo; DELETE FROM top_collaborators;")
	path := newDB(t)
	ctx := context.Background()
	report, err := SyncDocuments(ctx, DocumentOptions{DBPath: path, UsageFile: empty, Explicit: true, Discover: true})
	if err != nil || report.Captured != 1 || len(report.Sources) != 1 || report.Sources[0].Status != "complete" ||
		report.Sources[0].Reason != UsageListedNothing || len(report.Sources[0].Losses) != 0 {
		t.Fatalf("usage file that lists nothing: %+v %v", report, err)
	}
	partial := usageFileFixture(t, "usage-partial.db", "INSERT INTO recent_files_formatted_spo VALUES('no-recent-row','variant-a','{}')",
		"INSERT INTO top_collaborators VALUES('unreadable','not json')")
	report, err = SyncDocuments(ctx, DocumentOptions{DBPath: path, UsageFile: partial, Explicit: true, Discover: true})
	if err != nil || report.Captured != 1 || report.Sources[0].Status != "partial" || report.Sources[0].Reason != "" ||
		report.Sources[0].Losses["onedrive_usage_document_orphan"] != 1 || report.Sources[0].Losses["onedrive_usage_collaborator_unmapped"] != 1 {
		t.Fatalf("usage file with unmapped rows: %+v %v", report, err)
	}
}

func TestUsageFileMustBeAnOrdinaryFileOtherThanTheArchive(t *testing.T) {
	path := newDB(t)
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	capture := documentsUsageCapture
	t.Cleanup(func() { documentsUsageCapture = capture })
	documentsUsageCapture = func(context.Context, officecapture.UsageInput, func(*officecapture.UsageCapture) error) error {
		t.Error("an unusable usage file reached capture")
		return nil
	}
	for name, file := range map[string]string{"missing": filepath.Join(dir, "missing.db"), "directory": dir, "archive": path,
		"archive-wal": path + "-wal", "archive-lock": path + ".lock"} {
		report, err := SyncDocuments(ctx, DocumentOptions{DBPath: path, UsageFile: file, Explicit: true, Discover: true})
		if !errors.Is(err, ErrUsageFileUnusable) || errors.Is(err, ErrRegistryFileUnusable) || len(report.Sources) != 0 {
			t.Fatalf("%s: %+v %v", name, report, err)
		}
	}
	if err := CheckUsageFile(path, filepath.Join(dir, "missing.db")); !errors.Is(err, ErrUsageFileUnusable) {
		t.Fatalf("CheckUsageFile: %v", err)
	}
	s, err := store.OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	sources, err := s.DocumentSources(ctx)
	if closeErr := s.Close(); err != nil || closeErr != nil || len(sources) != 0 {
		t.Fatalf("an unusable usage file registered a source: %+v %v %v", sources, err, closeErr)
	}
}

// Where the platform qualifies no OneDrive input, a supplied file is a fixed refusal recorded
// against the source, never a capture.
func TestUsageSuppliedFileOnUnqualifiedPlatform(t *testing.T) {
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		t.Skip("this platform qualifies ordinary local input")
	}
	file := filepath.Join(t.TempDir(), "usage.db")
	if err := os.WriteFile(file, []byte("synthetic"), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := SyncDocuments(context.Background(), DocumentOptions{DBPath: newDB(t), UsageFile: file, Explicit: true, Discover: true})
	if err != nil || report.Failed != 1 || len(report.Sources) != 1 || report.Sources[0].ID != 0 ||
		report.Sources[0].Reason != "onedrive_usage_input_unqualified" {
		t.Fatalf("unqualified platform: %+v %v", report, err)
	}
}
