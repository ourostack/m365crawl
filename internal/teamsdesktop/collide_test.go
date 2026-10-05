package teamsdesktop_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/teamscrawl/internal/syncer"
	"github.com/ourostack/teamscrawl/internal/teamsdesktop"
)

const fixtureRoot = "../../testdata/teams-fixture/EBWebView"

const genericDB = "Teams:scripted-manager:react-web-client:00000000-0000-4000-8000-0000000000a1:00000000-0000-4000-8000-0000000000b1:en-us"

// copyFixture copies the committed fixture so a test can change its modification times.
func copyFixture(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "EBWebView")
	err := filepath.WalkDir(fixtureRoot, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(fixtureRoot, p)
		target := filepath.Join(root, rel)
		if e.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		b, err := os.ReadFile(p) //nolint:gosec // test fixture
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o600) //nolint:gosec // test copy into a temp dir
	})
	if err != nil {
		t.Fatal(err)
	}
	return root
}

var touched int

// touchLog makes the next sync see a changed source.
func touchLog(t *testing.T, root string) {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(root, "*", "IndexedDB", "*.leveldb", "*.log"))
	if len(m) != 1 {
		t.Fatalf("log files: %v", m)
	}
	touched++
	later := time.Now().Add(time.Duration(touched) * time.Hour)
	if err := os.Chtimes(m[0], later, later); err != nil {
		t.Fatal(err)
	}
}

// recordsDump is every generic record row of the archive, one line each.
func recordsDump(t *testing.T, path string) string {
	t.Helper()
	d, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	rows, err := d.Query(`select database, store, key_json, value_json, content_hash, removed_at is not null from records order by 1, 2, 3`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var b strings.Builder
	for rows.Next() {
		var db, st, key, hash string
		var val sql.NullString
		var removed bool
		if err := rows.Scan(&db, &st, &key, &val, &hash, &removed); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&b, "%s|%s|%s|%v|%s|%t\n", db, st, key, val, hash, removed)
	}
	return b.String()
}

func reportKey(t *testing.T, r syncer.Report, ch []syncer.Change) string {
	t.Helper()
	r.StartedAt, r.FinishedAt = time.Time{}, time.Time{}
	b, err := json.Marshal(struct {
		Report  syncer.Report
		Changes []syncer.Change
	}{r, ch})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// syncBothWays runs one sync that skips what it may and one that reads everything, over the same
// cache, into two archives, and fails when the reports (counts included) or the generic rows differ.
func syncBothWays(t *testing.T, root, skipDB, fullDB, label string) {
	t.Helper()
	touchLog(t, root)
	sr, sc, err1 := syncer.Run(context.Background(), syncer.Options{Root: root, DBPath: skipDB})
	fr, fc, err2 := syncer.Run(context.Background(), syncer.Options{Root: root, DBPath: fullDB, FullRead: true})
	if err1 != nil || err2 != nil {
		t.Fatalf("%s: errors %v / %v", label, err1, err2)
	}
	if a, b := reportKey(t, sr, sc), reportKey(t, fr, fc); a != b {
		t.Fatalf("%s: reports differ\nskip: %s\nfull: %s", label, a, b)
	}
	if a, b := recordsDump(t, skipDB), recordsDump(t, fullDB); a != b {
		t.Fatalf("%s: records differ\nskip:\n%s\nfull:\n%s", label, a, b)
	}
}

func newPaths(t *testing.T) (skipDB, fullDB string) {
	t.Helper()
	d := t.TempDir()
	return filepath.Join(d, "skip", "teamscrawl.db"), filepath.Join(d, "full", "teamscrawl.db")
}

// Two generic records whose keys scrub to the same key share one row. A skipping sync and a full
// read must leave that row, and count it, the same way on every sync, however the two records and
// their order change.
func TestCollidingGenericKeysMatchAFullReadOnEverySync(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	root := copyFixture(t)
	a, b := "https://x.test/a?sig=AAA", "https://x.test/a?sig=BBB"
	states := [][]teamsdesktop.FakeRecord{
		{{Key: a, Value: "first"}, {Key: b, Value: "second"}},
		{{Key: a, Value: "first"}, {Key: b, Value: "second"}},
		{{Key: a, Value: "first"}, {Key: b, Value: "second"}},
		{{Key: a, Value: "first"}, {Key: b, Value: "second"}},
		{{Key: b, Value: "second"}, {Key: a, Value: "first"}},
		{{Key: b, Value: "second"}, {Key: a, Value: "first"}},
		{{Key: a, Value: "first"}, {Key: b, Value: "second"}},
		{{Key: a, Value: "first"}, {Key: b, Value: "changed"}},
		{{Key: a, Value: "first"}, {Key: b, Value: "changed"}},
		{{Key: a, Value: "changed too"}, {Key: b, Value: "changed"}},
		{{Key: a, Value: "changed too"}},
		{{Key: a, Value: "changed too"}},
		{{Key: a, Value: "same"}, {Key: b, Value: "same"}},
		{{Key: a, Value: "same"}, {Key: b, Value: "same"}},
		{{Key: "https://x.test/other", Value: "plain"}, {Key: a, Value: "same"}, {Key: b, Value: "same"}},
		{{Key: "https://x.test/other", Value: "plain"}, {Key: a, Value: "same"}, {Key: b, Value: "same"}},
	}
	cur := 0
	teamsdesktop.InstallFakeGeneric(t, func() map[string][]teamsdesktop.FakeRecord {
		return map[string][]teamsdesktop.FakeRecord{genericDB: states[cur]}
	})
	skipDB, fullDB := newPaths(t)
	for i := range states {
		cur = i
		syncBothWays(t, root, skipDB, fullDB, fmt.Sprintf("sync %d", i+1))
	}
}
