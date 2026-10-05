package teamsdesktop_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io/fs"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
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

// Values that Scrub redacts, a value that changes under the same key, and records that vanish and
// come back: a skipping sync and a full read agree on the rows, the redaction counts and the
// record counts, on every sync.
func TestRedactedAndChangingGenericValuesMatchAFullRead(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	root := copyFixture(t)
	bearer, signed := "Bearer secret-token-value-1", "https://x.test/f?a=1&sig=deadbeef&b=2"
	states := [][]teamsdesktop.FakeRecord{
		{{Key: "k1", Value: bearer}, {Key: "k2", Value: signed}, {Key: "k3", Value: "plain"}},
		{{Key: "k1", Value: bearer}, {Key: "k2", Value: signed}, {Key: "k3", Value: "plain"}},
		{{Key: "k1", Value: "Bearer another-secret-2"}, {Key: "k2", Value: signed}, {Key: "k3", Value: "plain"}},
		{{Key: "k1", Value: "Bearer another-secret-2"}, {Key: "k2", Value: signed}, {Key: "k3", Value: "plain"}},
		{{Key: "k1", Value: "not secret any more"}, {Key: "k2", Value: signed}, {Key: "k3", Value: "plain"}},
		{{Key: "k1", Value: "not secret any more"}, {Key: "k3", Value: "plain"}},
		{{Key: "k1", Value: "not secret any more"}, {Key: "k3", Value: "plain"}},
		{{Key: "k1", Value: bearer}, {Key: "k2", Value: signed}, {Key: "k3", Value: "plain"}},
		{{Key: "k1", Value: bearer}, {Key: "k2", Value: signed}, {Key: "k3", Value: "plain"}},
		{{Key: "k3", Value: "plain"}},
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

// A seeded random series of generic states drawn from a pool that holds colliding scrubbed keys,
// redacted values and changing values, in random order, over two databases (two accounts): every
// sync equals a full read. A failure names the seed; TEAMSCRAWL_TEST_SEED=<n> runs that one.
func TestRandomGenericStatesSkipEqualsFull(t *testing.T) {
	seeds := []int64{1, 2, 3}
	if s := os.Getenv("TEAMSCRAWL_TEST_SEED"); s != "" {
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seeds = []int64{n}
	}
	other := "Teams:scripted-manager:react-web-client:00000000-0000-4000-8000-000000000002:00000000-0000-4000-8000-0000000000a2:en-us"
	pool := []teamsdesktop.FakeRecord{
		{Key: "https://x.test/a?sig=AAA", Value: "one"}, {Key: "https://x.test/a?sig=BBB", Value: "two"},
		{Key: "https://x.test/a?sig=CCC", Value: "one"}, {Key: "https://x.test/a?sig=BBB", Value: "three"},
		{Key: "k1", Value: "Bearer secret-1"}, {Key: "k1", Value: "Bearer secret-2"}, {Key: "k1", Value: "plain"},
		{Key: "k2", Value: "https://x.test/f?sig=deadbeef"}, {Key: "k2", Value: "https://x.test/f?sig=feedface"},
		{Key: "k3", Value: "same"}, {Key: "k4", Value: "x"}, {Key: "k4", Value: "y"},
	}
	for _, seed := range seeds {
		t.Run(fmt.Sprintf("seed %d", seed), func(t *testing.T) {
			t.Setenv("TMPDIR", t.TempDir())
			rng := rand.New(rand.NewSource(seed)) //nolint:gosec // a test series, not security
			root := copyFixture(t)
			var current map[string][]teamsdesktop.FakeRecord
			teamsdesktop.InstallFakeGeneric(t, func() map[string][]teamsdesktop.FakeRecord { return current })
			skipDB, fullDB := newPaths(t)
			for step := 1; step <= 8; step++ {
				current = map[string][]teamsdesktop.FakeRecord{genericDB: nil, other: nil}
				for db := range current {
					for _, i := range rng.Perm(len(pool))[:rng.Intn(len(pool))] {
						current[db] = append(current[db], pool[i])
					}
				}
				syncBothWays(t, root, skipDB, fullDB, fmt.Sprintf("seed %d step %d", seed, step))
				if rng.Intn(2) == 0 { // the same state again
					syncBothWays(t, root, skipDB, fullDB, fmt.Sprintf("seed %d step %d again", seed, step))
				}
			}
		})
	}
}
