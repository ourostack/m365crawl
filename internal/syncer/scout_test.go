package syncer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/scoutcapture"
	"github.com/ourostack/m365crawl/internal/scoutsessions"
	"github.com/ourostack/m365crawl/internal/store"
)

const scoutDev = "0123456789abcdef0123456789abcdef"

// Every session body here is synthetic.
const (
	scoutSession  = `{"id":"s-1","name":"Synthetic chat","messages":[{"id":"m1","role":"user","content":"hello synthetic world","timestamp":"2026-04-01T10:00:00Z"}]}`
	scoutScrubbed = `{"id":"s-2","name":"Token chat","messages":[{"id":"m1","role":"assistant","content":"Bearer abcdefghijklmnopqrstuvwxyz0123456789","timestamp":"2026-04-01T11:00:00Z"}]}`
)

func scoutSwap[T any](t *testing.T, target *T, value T) {
	t.Helper()
	old := *target
	*target = value
	t.Cleanup(func() { *target = old })
}

func scoutFolder(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "sessions", "devices", scoutDev), 0o700); err != nil {
		t.Fatal(err)
	}
	return root
}

// fakeScan hands consume the files given, then returns err with one listed device.
func fakeScan(t *testing.T, files []scoutcapture.File, err error, seen *scoutcapture.Options) {
	t.Helper()
	scoutSwap(t, &scoutSupported, func() bool { return true })
	// A portable stand-in for the guarded layout check, which scoutcapture tests itself.
	scoutSwap(t, &scoutCheck, func(_ context.Context, root string) (string, error) {
		if !filepath.IsAbs(root) {
			return "", &scoutcapture.Error{Code: "scout_root_invalid"}
		}
		if info, err := os.Stat(filepath.Join(root, "sessions", "devices")); err != nil || !info.IsDir() {
			return "", &scoutcapture.Error{Code: "scout_root_invalid"}
		}
		return root, nil
	})
	scoutSwap(t, &scoutScan, func(ctx context.Context, opts scoutcapture.Options, consume func(scoutcapture.File) error) (scoutcapture.Result, error) {
		if seen != nil {
			*seen = opts
		}
		res := scoutcapture.Result{Root: opts.Root, Devices: []scoutcapture.Device{{ID: scoutDev, Listed: true}, {ID: "fedcba9876543210fedcba9876543210", Reason: "scout_device_unreadable"}}}
		for _, f := range files {
			if cerr := consume(f); cerr != nil {
				return res, cerr
			}
		}
		return res, err
	})
}

func read(name, body string) scoutcapture.File {
	return scoutcapture.File{Device: scoutDev, Name: name, State: scoutcapture.StateRead, Size: int64(len(body)), Modified: time.Unix(100, 5), Data: []byte(body)}
}

func TestSyncScoutCountsEveryFileState(t *testing.T) {
	root := scoutFolder(t)
	db := filepath.Join(t.TempDir(), "data", "m365crawl.db")
	files := []scoutcapture.File{read("a.json", scoutSession), read("b.json", scoutScrubbed), read("c.json", "not json"),
		{Device: scoutDev, Name: "d.json", State: scoutcapture.StateCloudOnly, Reason: "scout_file_cloud_only", Size: 10},
		{Device: scoutDev, Name: "e.json", State: scoutcapture.StateUnsafe, Reason: "scout_file_unsafe"},
		{Device: scoutDev, Name: "f.json", State: scoutcapture.StateTooLarge, Reason: "scout_file_too_large"}}
	fakeScan(t, files, nil, nil)
	report, err := SyncScout(context.Background(), ScoutOptions{DBPath: db, Roots: []string{root}})
	if err != nil || report.Discovered || len(report.Folders) != 1 {
		t.Fatal(report, err)
	}
	f := report.Folders[0]
	if f.SourceID != 1 || f.Root != root || f.Outcome != "partial" || f.Listed != 6 || f.Admitted != 2 || f.Failed != 1 || f.CloudOnly != 1 ||
		f.Unsafe != 1 || f.TooLarge != 1 || f.Redacted != 1 || f.Devices != 2 || f.UnreadableDevices != 1 || f.Gone != 0 {
		t.Fatalf("%+v", f)
	}
	// The next run passes the archive's last reads to the scan.
	var seen scoutcapture.Options
	fakeScan(t, []scoutcapture.File{{Device: scoutDev, Name: "a.json", State: scoutcapture.StateUnchanged}}, nil, &seen)
	report, err = SyncScout(context.Background(), ScoutOptions{DBPath: db, Roots: []string{root}})
	if err != nil || report.Folders[0].Unchanged != 1 || report.Folders[0].Gone != 5 ||
		seen.Known[scoutcapture.Key{Device: scoutDev, Name: "a.json"}] != (scoutcapture.Known{Size: int64(len(scoutSession)), ModNanos: time.Unix(100, 5).UnixNano()}) {
		t.Fatalf("%+v %+v %v", report, seen, err)
	}
	st, err := store.OpenReadOnly(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	hits, _, err := st.ScoutSearch(context.Background(), "abcdefghijklmnopqrstuvwxyz0123456789", store.ScoutFilter{})
	if err != nil || len(hits) != 0 {
		t.Fatalf("a credential was archived: %v %v", hits, err)
	}
}

func TestSyncScoutOutcomes(t *testing.T) {
	db := filepath.Join(t.TempDir(), "data", "m365crawl.db")
	root := scoutFolder(t)
	check := func(name string, scanErr error, files []scoutcapture.File, outcome, reason string, wantErr bool) {
		t.Helper()
		fakeScan(t, files, scanErr, nil)
		scoutSwap(t, &scoutScan, func(ctx context.Context, opts scoutcapture.Options, consume func(scoutcapture.File) error) (scoutcapture.Result, error) {
			for _, f := range files {
				if err := consume(f); err != nil {
					return scoutcapture.Result{}, err
				}
			}
			return scoutcapture.Result{Devices: []scoutcapture.Device{{ID: scoutDev, Listed: true}}}, scanErr
		})
		report, err := SyncScout(context.Background(), ScoutOptions{DBPath: db, Roots: []string{root}})
		if (err != nil) != wantErr || report.Folders[0].Outcome != outcome || report.Folders[0].Reason != reason {
			t.Fatalf("%s: %+v %v", name, report, err)
		}
	}
	check("complete", nil, []scoutcapture.File{read("a.json", scoutSession)}, "complete", "", false)
	check("coded", &scoutcapture.Error{Code: "scout_devices_unreadable"}, nil, "failed", "scout_devices_unreadable", false)
	check("other", errors.New("synthetic"), nil, "failed", "", true)
}

func TestSyncScoutCancellationIsRecorded(t *testing.T) {
	db := filepath.Join(t.TempDir(), "data", "m365crawl.db")
	root := scoutFolder(t)
	ctx, cancel := context.WithCancel(context.Background())
	fakeScan(t, nil, nil, nil)
	scoutSwap(t, &scoutScan, func(ctx context.Context, opts scoutcapture.Options, consume func(scoutcapture.File) error) (scoutcapture.Result, error) {
		cancel()
		return scoutcapture.Result{}, consume(read("a.json", scoutSession))
	})
	report, err := SyncScout(ctx, ScoutOptions{DBPath: db, Roots: []string{root}})
	if !errors.Is(err, context.Canceled) || report.Folders[0].Outcome != "cancelled" {
		t.Fatalf("%+v %v", report, err)
	}
	st, err := store.OpenReadOnly(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	sources, _, err := st.ScoutSources(context.Background())
	if err != nil || sources[0].LastRun.Outcome != "cancelled" {
		t.Fatalf("%+v %v", sources, err)
	}
	// A run that cannot even begin returns the archive's error.
	ctx, cancel = context.WithCancel(context.Background())
	scoutSwap(t, &scoutCheck, func(_ context.Context, root string) (string, error) {
		cancel()
		return root, nil
	})
	if _, err := SyncScout(ctx, ScoutOptions{DBPath: db, Roots: []string{root}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled begin: %v", err)
	}
}

func TestSyncScoutRefusesFoldersWithoutTheLayout(t *testing.T) {
	db := filepath.Join(t.TempDir(), "data", "m365crawl.db")
	fakeScan(t, nil, nil, nil)
	empty := t.TempDir()
	file := filepath.Join(empty, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := SyncScout(context.Background(), ScoutOptions{DBPath: db, Roots: []string{"relative", filepath.Join(empty, "missing"), empty, file}})
	if err != nil || len(report.Folders) != 4 {
		t.Fatal(report, err)
	}
	for _, f := range report.Folders {
		if f.Outcome != "failed" || f.Reason != "scout_root_invalid" || f.SourceID != 0 {
			t.Fatalf("%+v", f)
		}
	}
}

func TestSyncScoutDiscoveryAndRefusals(t *testing.T) {
	db := filepath.Join(t.TempDir(), "data", "m365crawl.db")
	root := scoutFolder(t)
	fakeScan(t, nil, nil, nil)
	scoutSwap(t, &scoutDiscover, func(context.Context, string) ([]string, error) { return []string{root}, nil })
	if report, err := SyncScout(context.Background(), ScoutOptions{DBPath: db, Home: "/synthetic"}); err != nil || !report.Discovered || len(report.Folders) != 1 {
		t.Fatal(report, err)
	}
	scoutSwap(t, &scoutDiscover, func(context.Context, string) ([]string, error) { return nil, nil })
	if _, err := SyncScout(context.Background(), ScoutOptions{DBPath: db}); !errors.Is(err, ErrScoutFolderNotFound) {
		t.Fatal(err)
	}
	synthetic := errors.New("synthetic")
	scoutSwap(t, &scoutDiscover, func(context.Context, string) ([]string, error) { return nil, synthetic })
	if _, err := SyncScout(context.Background(), ScoutOptions{DBPath: db}); !errors.Is(err, synthetic) {
		t.Fatal(err)
	}
	scoutSwap(t, &scoutAcquire, func(string) (func(), error) { return nil, synthetic })
	if _, err := SyncScout(context.Background(), ScoutOptions{DBPath: db, Roots: []string{root}}); !errors.Is(err, synthetic) {
		t.Fatal(err)
	}
	scoutSwap(t, &scoutAcquire, store.AcquireLock)
	scoutSwap(t, &scoutOpen, func(context.Context, string) (*store.Store, error) { return nil, synthetic })
	if _, err := SyncScout(context.Background(), ScoutOptions{DBPath: db, Roots: []string{root}}); !errors.Is(err, synthetic) {
		t.Fatal(err)
	}
	scoutSwap(t, &scoutOpen, store.Open)
	scoutSwap(t, &scoutSupported, func() bool { return false })
	if _, err := SyncScout(context.Background(), ScoutOptions{DBPath: db, Roots: []string{root}}); !errors.Is(err, scoutcapture.ErrPlatformUnqualified) {
		t.Fatal(err)
	}
}

func TestSyncScoutReadsARealFolder(t *testing.T) {
	if !scoutcapture.Supported() {
		t.Skip("acquisition is not qualified on this platform")
	}
	root := scoutFolder(t)
	if err := os.WriteFile(filepath.Join(root, "sessions", "devices", scoutDev, "s-1.json"), []byte(scoutSession), 0o600); err != nil {
		t.Fatal(err)
	}
	db := filepath.Join(t.TempDir(), "data", "m365crawl.db")
	report, err := SyncScout(context.Background(), ScoutOptions{DBPath: db, Roots: []string{root}})
	if err != nil || report.Folders[0].Outcome != "complete" || report.Folders[0].Admitted != 1 {
		t.Fatalf("%+v %v", report, err)
	}
	report, err = SyncScout(context.Background(), ScoutOptions{DBPath: db, Roots: []string{root}})
	if err != nil || report.Folders[0].Unchanged != 1 {
		t.Fatalf("%+v %v", report, err)
	}
}

func TestSyncScoutRescrubsArchivedAndGoneSessionsWhenRulesChange(t *testing.T) {
	db := filepath.Join(t.TempDir(), "data", "m365crawl.db")
	root := scoutFolder(t)
	fakeScan(t, []scoutcapture.File{read("a.json", scoutSession)}, nil, nil)
	if _, err := SyncScout(context.Background(), ScoutOptions{DBPath: db, Roots: []string{root}}); err != nil {
		t.Fatal(err)
	}
	// The session leaves OneDrive, so the archive alone holds it.
	fakeScan(t, nil, nil, nil)
	if report, err := SyncScout(context.Background(), ScoutOptions{DBPath: db, Roots: []string{root}}); err != nil || report.Folders[0].Gone != 1 || report.Rescrubbed != 0 {
		t.Fatalf("%+v %v", report, err)
	}
	// A rule added later: "world" becomes a secret.
	scoutSwap(t, &scoutStamp, func() string { return "later-rules" })
	scoutSwap(t, &scoutScrub, func(raw []byte) ([]byte, int) {
		out, n := scoutsessions.Scrub(raw)
		return []byte(strings.ReplaceAll(string(out), "world", "[redacted]")), n + strings.Count(string(out), "world")
	})
	report, err := SyncScout(context.Background(), ScoutOptions{DBPath: db, Roots: []string{root}})
	if err != nil || report.Rescrubbed != 1 {
		t.Fatalf("%+v %v", report, err)
	}
	st, err := store.OpenReadOnly(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if hits, _, err := st.ScoutSearch(context.Background(), "world", store.ScoutFilter{}); err != nil || len(hits) != 0 {
		t.Fatalf("the gone session kept the secret: %v %v", hits, err)
	}
	if hits, _, _ := st.ScoutSearch(context.Background(), "synthetic", store.ScoutFilter{}); len(hits) != 1 || hits[0].Message.Text != "hello synthetic [redacted]" {
		t.Fatalf("%+v", hits)
	}
}

func TestSyncScoutRestoresTheSameBytesUnderANewStamp(t *testing.T) {
	db := filepath.Join(t.TempDir(), "data", "m365crawl.db")
	root := scoutFolder(t)
	fakeScan(t, []scoutcapture.File{read("a.json", scoutSession)}, nil, nil)
	if _, err := SyncScout(context.Background(), ScoutOptions{DBPath: db, Roots: []string{root}}); err != nil {
		t.Fatal(err)
	}
	// The stamp changes between the archive pass and the folder read, so the read meets bytes the
	// archive holds under another stamp and stores them again in place from the source.
	stamps := []string{"old", "new"}
	scoutSwap(t, &scoutStamp, func() string {
		stamp := stamps[0]
		if len(stamps) > 1 {
			stamps = stamps[1:]
		}
		return stamp
	})
	report, err := SyncScout(context.Background(), ScoutOptions{DBPath: db, Roots: []string{root}})
	if err != nil || report.Rescrubbed != 1 || report.Folders[0].Rescrubbed != 1 || report.Folders[0].Unchanged != 0 {
		t.Fatalf("%+v %v", report, err)
	}
}

func TestSyncScoutStopsOnCheckAndRescrubErrors(t *testing.T) {
	db := filepath.Join(t.TempDir(), "data", "m365crawl.db")
	root := scoutFolder(t)
	fakeScan(t, []scoutcapture.File{read("a.json", scoutSession)}, nil, nil)
	if _, err := SyncScout(context.Background(), ScoutOptions{DBPath: db, Roots: []string{root}}); err != nil {
		t.Fatal(err)
	}
	scoutSwap(t, &scoutCheck, func(context.Context, string) (string, error) { return "", context.Canceled })
	if _, err := SyncScout(context.Background(), ScoutOptions{DBPath: db, Roots: []string{root}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("check: %v", err)
	}
	scoutSwap(t, &scoutStamp, func() string { return "broken-rules" })
	scoutSwap(t, &scoutScrub, func([]byte) ([]byte, int) { return []byte("not json"), 0 })
	if _, err := SyncScout(context.Background(), ScoutOptions{DBPath: db, Roots: []string{root}}); !errors.Is(err, store.ErrScoutRescrub) {
		t.Fatalf("rescrub: %v", err)
	}
}

// laterRule makes "world" a secret under a new stamp.
func laterRule(t *testing.T, stamp string) {
	t.Helper()
	scoutSwap(t, &scoutStamp, func() string { return stamp })
	scoutSwap(t, &scoutScrub, func(raw []byte) ([]byte, int) {
		out, n := scoutsessions.Scrub(raw)
		return []byte(strings.ReplaceAll(string(out), "world", "[redacted]")), n + strings.Count(string(out), "world")
	})
}

func scoutWorldHits(t *testing.T, db string) int {
	t.Helper()
	st, err := store.OpenReadOnly(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	hits, _, err := st.ScoutSearch(context.Background(), "world", store.ScoutFilter{})
	if err != nil {
		t.Fatal(err)
	}
	return len(hits)
}

// With no Scout folder reachable (the platform refuses, or discovery finds none) the archive is
// still scrubbed again before the refusal.
func TestSyncScoutRescrubsBeforeRefusing(t *testing.T) {
	db := filepath.Join(t.TempDir(), "data", "m365crawl.db")
	root := scoutFolder(t)
	fakeScan(t, []scoutcapture.File{read("a.json", scoutSession)}, nil, nil)
	if _, err := SyncScout(context.Background(), ScoutOptions{DBPath: db, Roots: []string{root}}); err != nil || scoutWorldHits(t, db) != 1 {
		t.Fatal(err)
	}
	laterRule(t, "rules-2")
	scoutSwap(t, &scoutSupported, func() bool { return false })
	report, err := SyncScout(context.Background(), ScoutOptions{DBPath: db})
	if !errors.Is(err, scoutcapture.ErrPlatformUnqualified) || report.Rescrubbed != 1 || scoutWorldHits(t, db) != 0 {
		t.Fatalf("%+v %v", report, err)
	}
	scoutSwap(t, &scoutSupported, func() bool { return true })
	scoutSwap(t, &scoutStamp, func() string { return "rules-3" })
	scoutSwap(t, &scoutDiscover, func(context.Context, string) ([]string, error) { return nil, nil })
	if report, err := SyncScout(context.Background(), ScoutOptions{DBPath: db}); !errors.Is(err, ErrScoutFolderNotFound) || report.Rescrubbed != 1 {
		t.Fatalf("%+v %v", report, err)
	}
	// A failing re-scrub or checkpoint is the run's error.
	synthetic := errors.New("synthetic")
	scoutSwap(t, &scoutStamp, func() string { return "rules-4" })
	scoutSwap(t, &scoutCheckpoint, func(*store.Store, context.Context) error { return synthetic })
	if _, err := SyncScout(context.Background(), ScoutOptions{DBPath: db}); !errors.Is(err, synthetic) {
		t.Fatal(err)
	}
	scoutSwap(t, &scoutAcquire, func(string) (func(), error) { return nil, synthetic })
	if _, err := SyncScout(context.Background(), ScoutOptions{DBPath: db}); !errors.Is(err, synthetic) {
		t.Fatal(err)
	}
	// With no archive yet, the archive opens only after the folders are known.
	fresh := filepath.Join(t.TempDir(), "data", "m365crawl.db")
	if _, err := SyncScout(context.Background(), ScoutOptions{DBPath: fresh, Roots: []string{root}}); !errors.Is(err, synthetic) {
		t.Fatal(err)
	}
}

// An ordinary sync scrubs archived Scout sessions again from the archive alone.
func TestSyncRescrubsScoutSessions(t *testing.T) {
	db := filepath.Join(t.TempDir(), "data", "m365crawl.db")
	root := scoutFolder(t)
	fakeScan(t, []scoutcapture.File{read("a.json", scoutSession)}, nil, nil)
	if _, err := SyncScout(context.Background(), ScoutOptions{DBPath: db, Roots: []string{root}}); err != nil {
		t.Fatal(err)
	}
	laterRule(t, "rules-2")
	scoutSwap(t, &scoutScan, func(context.Context, scoutcapture.Options, func(scoutcapture.File) error) (scoutcapture.Result, error) {
		t.Fatal("sync read a Scout folder")
		return scoutcapture.Result{}, nil
	})
	rep, _, err := Run(context.Background(), Options{Root: fixtureRoot, DBPath: db})
	if err != nil || rep.ScoutRescrubbed != 1 || scoutWorldHits(t, db) != 0 {
		t.Fatalf("%d %v", rep.ScoutRescrubbed, err)
	}
	scoutSwap(t, &scoutArchiveRescrub, func(context.Context, *store.Store) (int, error) { return 0, errors.New("synthetic") })
	if _, _, err := Run(context.Background(), Options{Root: fixtureRoot, DBPath: db}); err == nil {
		t.Fatal("a failing re-scrub did not fail the sync")
	}
}
