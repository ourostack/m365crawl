package outlookowa

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/teamsdesktop"
)

const fixtureOrigin = "https_outlook.office.com_0"

func sourceTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func fixtureProfile(t *testing.T, root, name, variant string, blobs bool) Profile {
	t.Helper()
	var err error
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, name)
	src := filepath.Join("..", "..", "testdata", "outlook-owa-fixture", variant, "IndexedDB")
	dst := filepath.Join(dir, "IndexedDB")
	err = filepath.WalkDir(src, func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if !blobs && (rel == fixtureOrigin+".indexeddb.blob" || strings.HasPrefix(rel, fixtureOrigin+".indexeddb.blob"+string(filepath.Separator))) {
			if e.IsDir() {
				return filepath.SkipDir
			}

			return nil
		}
		target := filepath.Join(dst, rel)
		if e.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		b, err := os.ReadFile(path) //nolint:gosec // G304: walk reads only the committed synthetic OWA fixture above.
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o600) //nolint:gosec // G703: rel comes from the fixture walk; dst is the caller's test-owned profile.
	})
	if err != nil {
		t.Fatal(err)
	}
	blob := filepath.Join(dst, fixtureOrigin+".indexeddb.blob")
	if !blobs {
		blob = ""
	}
	return Profile{Name: name, Dir: dir, Source: teamsdesktop.Source{Profile: name, Origin: fixtureOrigin, LevelDBDir: filepath.Join(dst, fixtureOrigin+".indexeddb.leveldb"), BlobDir: blob}}
}

func TestSourceErrorsAreFixedAndIdentityDetectsReplacement(t *testing.T) {
	p := fixtureProfile(t, t.TempDir(), "Default", "mail", true)
	fp, fresh, err := Fingerprint(p)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(p.Source.LevelDBDir, "CURRENT")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path) //nolint:gosec // G304: CURRENT belongs to the synthetic profile copied into t.TempDir.
	if err != nil {
		t.Fatal(err)
	}
	// Keep the old file alive so its native file identity cannot be reused.
	oldPath := filepath.Join(t.TempDir(), "old-current")
	if err := os.Rename(path, oldPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil { //nolint:gosec // G703: replace only the test-owned synthetic CURRENT file.
		t.Fatal(err)
	}
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if next, nextFresh, err := Fingerprint(p); err != nil || next == fp || !nextFresh.Equal(fresh) {
		t.Fatalf("same-size/time file replacement escaped identity: %q %v %v", next, nextFresh, err)
	}
	if err := ValidateSource(teamsdesktop.Source{}); err == nil || err.Error() != "outlookowa: source_missing" {
		t.Fatalf("missing source error: %v", err)
	}
	if _, _, err := Fingerprint(Profile{Source: teamsdesktop.Source{LevelDBDir: filepath.Join(t.TempDir(), "absent")}}); err == nil {
		t.Fatal("missing source fingerprint succeeded")
	}
	oldRead := sourceReadDir
	sourceReadDir = func(string) ([]os.DirEntry, error) { return nil, errors.New("synthetic-private-path") }
	t.Cleanup(func() { sourceReadDir = oldRead })
	if _, _, err := Discover(filepath.Dir(p.Dir)); err == nil || err.Error() != "outlookowa: root_unreadable" {
		t.Fatalf("unsafe diagnostic: %v", err)
	}
}

func TestDiscoverProfilesAndOptionalBlob(t *testing.T) {
	root := sourceTempDir(t)
	fixtureProfile(t, root, "Default", "mail", true)
	fixtureProfile(t, root, "Profile 2", "empty", false)
	if err := os.MkdirAll(filepath.Join(root, "Unrelated", "IndexedDB", "https_teams.microsoft.com_0.indexeddb.leveldb"), 0o700); err != nil {
		t.Fatal(err)
	}
	p, skipped, err := Discover(root)
	if err != nil || len(skipped) != 0 || len(p) != 2 || p[0].Name != "Default" || p[1].Name != "Profile 2" || p[1].Source.BlobDir != "" || p[0].Source.Profile != "Default" || p[0].Source.Origin != fixtureOrigin {
		t.Fatalf("profiles: %+v skipped: %+v err: %v", p, skipped, err)
	}
}

func TestDiscoverMissingEmptyAndAmbiguous(t *testing.T) {
	root := sourceTempDir(t)
	if _, _, err := Discover(filepath.Join(root, "absent")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing root: %v", err)
	}
	if p, s, err := Discover(root); err != nil || len(p)+len(s) != 0 {
		t.Fatalf("empty root: %+v %+v %v", p, s, err)
	}
	p := fixtureProfile(t, root, "Default", "mail", true)
	if err := os.MkdirAll(filepath.Join(p.Dir, "IndexedDB", "https_outlook.office.com_443.indexeddb.leveldb"), 0o700); err != nil {
		t.Fatal(err)
	}
	found, skipped, err := Discover(root)
	if err != nil || len(found) != 0 || len(skipped) != 1 || skipped[0].Reason != "origin_ambiguous" {
		t.Fatalf("ambiguous origin: %+v %+v %v", found, skipped, err)
	}
}

func TestFingerprintMetadataFreshnessAndVersions(t *testing.T) {
	p := fixtureProfile(t, t.TempDir(), "Default", "mail", true)
	at := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	err := filepath.WalkDir(p.Dir, func(path string, e fs.DirEntry, err error) error {
		if err == nil && !e.IsDir() {
			err = os.Chtimes(path, at, at) //nolint:gosec // G122: fixture tree is test-owned and no concurrent writer exists in this metadata test.
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	fp, fresh, err := Fingerprint(p)
	if err != nil || len(fp) != 64 || !fresh.Equal(at) {
		t.Fatalf("fingerprint/freshness: %q %v %v", fp, fresh, err)
	}
	for _, name := range []string{"LOCK", "LOG", "LOG.old"} {
		if err := os.WriteFile(filepath.Join(p.Source.LevelDBDir, name), []byte("synthetic noise"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if next, fresh, err := Fingerprint(p); err != nil || next != fp || !fresh.Equal(at) {
		t.Fatalf("noise changed fingerprint: %s %v %v", next, fresh, err)
	}
	old := mapperVersion
	mapperVersion++
	t.Cleanup(func() { mapperVersion = old })
	if next, _, err := Fingerprint(p); err != nil || next == fp {
		t.Fatal("mapper invalidation missing")
	}
	mapperVersion = old
	for _, delta := range []time.Duration{time.Hour, -time.Hour} {
		changed := at.Add(delta)
		path := filepath.Join(p.Source.LevelDBDir, "CURRENT")
		if err := os.Chtimes(path, changed, changed); err != nil {
			t.Fatal(err)
		}
		if next, fresh, err := Fingerprint(p); err != nil || next == fp || (delta > 0 && !fresh.Equal(changed)) || (delta < 0 && !fresh.Equal(at)) {
			t.Fatalf("source metadata change: %q %v %v", next, fresh, err)
		}
	}
}

func TestSourceRejectsNonregularAndUnreadableEntries(t *testing.T) {
	for _, bad := range []string{"leveldb", "blob", "CURRENT"} {
		t.Run(bad, func(t *testing.T) {
			p := fixtureProfile(t, t.TempDir(), "Default", "empty", false)
			path := p.Source.LevelDBDir
			switch bad {
			case "CURRENT":
				path = filepath.Join(path, "CURRENT")
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			case "blob":
				path = filepath.Join(p.Dir, "IndexedDB", fixtureOrigin+".indexeddb.blob")
				if err := os.WriteFile(path, nil, 0o600); err != nil {
					t.Fatal(err)
				}
			default:
				// A nested directory would be skipped by the LevelDB copier.
				if err := os.Mkdir(filepath.Join(path, "unexpected"), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			found, skipped, err := Discover(filepath.Dir(p.Dir))
			if err != nil || len(found) != 0 || len(skipped) != 1 || skipped[0].Reason != "unsafe_source" {
				t.Fatalf("unsafe source accepted: %+v %+v %v", found, skipped, err)
			}
		})
	}
	p := fixtureProfile(t, t.TempDir(), "Default", "mail", true)
	old := sourceReadDir
	sourceReadDir = func(path string) ([]os.DirEntry, error) {
		if path == filepath.Join(p.Dir, "IndexedDB") {
			return nil, fs.ErrPermission
		}
		return old(path)
	}
	t.Cleanup(func() { sourceReadDir = old })
	found, skipped, err := Discover(filepath.Dir(p.Dir))
	if err != nil || len(found) != 0 || len(skipped) != 1 || skipped[0].Reason != "unreadable" || !errors.Is(skipped[0].Err, fs.ErrPermission) {
		t.Fatalf("permission failure: %+v %+v %v", found, skipped, err)
	}
}
