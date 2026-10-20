package indexeddb

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"github.com/ourostack/m365crawl/internal/leveldb"
	gl "github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"
	"github.com/syndtr/goleveldb/leveldb/util"
)

func TestCensusTornJournalRetainsOnlyObservedPrefix(t *testing.T) {
	for _, journalDatabase := range []bool{false, true} {
		t.Run(fmt.Sprintf("journal-database=%t", journalDatabase), func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "origin.leveldb")
			db, err := gl.OpenFile(dir, &opt.Options{Compression: opt.NoCompression})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			put := func(k, v []byte) {
				t.Helper()
				if err := db.Put(k, v, &opt.WriteOptions{Sync: true}); err != nil {
					t.Fatal(err)
				}
			}
			put(dbNameKey("https://x", "prefix"), []byte{1})
			put(storeNameKey(1, 1), u16("rows"))
			put(append(idPrefix(1, 1, indexData, 1), idbString("prefix-key")...), []byte("stable-prefix"))
			put(storeNameKey(2, 1), u16("rows"))
			if !journalDatabase {
				put(dbNameKey("https://x", "journal"), []byte{2})
			}
			if err := db.CompactRange(util.Range{}); err != nil {
				t.Fatal(err)
			}
			if journalDatabase {
				put(dbNameKey("https://x", "journal"), []byte{2})
			}
			put(append(idPrefix(2, 1, indexData, 1), idbString("journal-key")...), []byte("journal-value"))
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			logs, err := filepath.Glob(filepath.Join(dir, "*.log"))
			if err != nil {
				t.Fatal(err)
			}
			saved := map[string][]byte{}
			for _, path := range logs {
				b, err := os.ReadFile(path) //nolint:gosec // G304: journal generated above in this test's private t.TempDir, selected by its local *.log glob
				if err != nil {
					t.Fatal(err)
				}
				if len(b) > 7 {
					saved[path] = b
				}
			}
			if len(saved) == 0 {
				t.Fatal("fixture setup produced no journal payload")
			}
			var healthyHeld int64
			for cycle := 0; cycle < 2; cycle++ {
				for _, damaged := range []bool{false, true, false} {
					for path, b := range saved {
						if damaged {
							b = b[:7] // A complete physical header with an absent payload.
						}
						if err := os.WriteFile(path, b, 0o600); err != nil {
							t.Fatal(err)
						}
					}
					o, err := Open(dir, "")
					if err != nil {
						t.Fatal(err)
					}
					stats := o.Stats()
					if err := o.Close(); err != nil {
						t.Fatal(err)
					}
					held, dbs, err := Census(dir)
					if err != nil {
						t.Fatal(err)
					}
					withHeld, withDBs, withStats, err := CensusWithStats(dir)
					if err != nil || !reflect.DeepEqual(withHeld, held) || !reflect.DeepEqual(withDBs, dbs) ||
						withStats.TruncatedLogTails != stats.TruncatedLogTails {
						t.Fatalf("stats census changed compatibility output or lost journal evidence: held=%v dbs=%+v stats=%+v err=%v",
							withHeld, withDBs, withStats, err)
					}
					wantDBs := 2
					if damaged && journalDatabase {
						wantDBs = 1
					}
					if healthyHeld == 0 && !damaged {
						healthyHeld = held[2]
					}
					if len(dbs) != wantDBs || held[1] == 0 || healthyHeld == 0 ||
						(damaged && held[2] >= healthyHeld) || (!damaged && held[2] != healthyHeld) {
						t.Errorf("cycle=%d damaged=%t: census held=%v dbs=%+v", cycle, damaged, held, dbs)
					}
					if (stats.TruncatedLogTails > 0) != damaged {
						t.Errorf("fixture did not discriminate torn journal: damaged=%t stats=%+v", damaged, stats)
					}
				}
			}
		})
	}
	t.Run("census-errors", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "origin.leveldb")
		if _, _, _, err := CensusWithStats(dir); err == nil {
			t.Fatal("missing origin was mistaken for a complete empty census")
		}
		db, err := gl.OpenFile(dir, &opt.Options{Compression: opt.NoCompression})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		if err := db.Put([]byte{0, 0, 0, 0, 201, 0x80}, []byte{1}, &opt.WriteOptions{Sync: true}); err != nil {
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		if _, _, stats, err := CensusWithStats(dir); err == nil || stats.Keys == 0 {
			t.Fatalf("metadata error lost its loaded observation stats: stats=%+v err=%v", stats, err)
		}
		if _, _, err := Census(dir); err == nil {
			t.Fatal("compatibility wrapper swallowed metadata error")
		}
	})
}

func heapNow() uint64 {
	var m runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m)
	return m.HeapAlloc
}

// Every database in the fixture appears in the census with a positive estimate, and dbs is
// what Origin.Databases returns.
func TestCensusFixture(t *testing.T) {
	ldb, _ := fixtureDirs()
	held, dbs, err := Census(ldb)
	if err != nil {
		t.Fatal(err)
	}
	want, err := openFixture(t).Databases()
	if err != nil {
		t.Fatal(err)
	}
	if len(dbs) != len(want) || len(dbs) == 0 {
		t.Fatalf("census lists %d databases, Databases %d", len(dbs), len(want))
	}
	for i, d := range dbs {
		if d.ID != want[i].ID || d.Name != want[i].Name || len(d.Stores) != len(want[i].Stores) {
			t.Errorf("database %d: census %+v, Databases %+v", i, d, want[i])
		}
		if held[d.ID] <= 0 {
			t.Errorf("database %d (%s): estimate %d", d.ID, d.Name, held[d.ID])
		}
	}
}

// A synthetic origin big enough for heap numbers to dominate noise: the estimate for each
// database is within 2x of the measured heap of keep-loading it alone.
func TestCensusWithin2xOfKeepLoad(t *testing.T) {
	dir := t.TempDir() + "/o.leveldb"
	db, err := gl.OpenFile(dir, &opt.Options{Compression: opt.NoCompression})
	if err != nil {
		t.Fatal(err)
	}
	put := func(k, v []byte) {
		t.Helper()
		if err := db.Put(k, v, nil); err != nil {
			t.Fatal(err)
		}
	}
	names := []string{"small", "big"}
	counts := []int{50000, 50000}
	sizes := []int{37, 701} // "big" values exceed the lazy threshold
	for i, name := range names {
		id := uint64(i + 1)
		key := append([]byte{0, 0, 0, 0, metaDatabaseName}, append(idbString("https://x"), idbString(name)...)...)
		put(key, []byte{byte(id)})
		storeKey := idPrefix(id, 0, 0, metaObjectStore, 1, storeMetaName)
		put(storeKey, u16(fmt.Sprintf("store%d", i)))
		for n := 0; n < counts[i]; n++ {
			k := idPrefix(id, 1, indexData, 1)
			k = append(k, fmt.Sprintf("%08d", n)...)
			put(k, make([]byte, sizes[i]))
		}
	}
	if err := db.CompactRange(util.Range{}); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if _, err := os.Stat(dir); err != nil {
		t.Fatal(err)
	}

	held, dbs, err := Census(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(dbs) != 2 {
		t.Fatalf("dbs %+v", dbs)
	}
	base := heapNow()
	o0, err := OpenWith(dir, "", OpenOptions{KeepDatabase: func(string) bool { return false }})
	if err != nil {
		t.Fatal(err)
	}
	baseLoaded := int64(heapNow()) - int64(base) //nolint:gosec // heap sizes are small
	runtime.KeepAlive(o0)
	for _, d := range dbs {
		before := heapNow()
		o, err := OpenWith(dir, "", OpenOptions{KeepDatabase: func(n string) bool { return n == d.Name }})
		if err != nil {
			t.Fatal(err)
		}
		measured := int64(heapNow()) - int64(before) - baseLoaded //nolint:gosec // heap sizes are small
		runtime.KeepAlive(o)
		est := held[d.ID]
		t.Logf("%s: estimate %d, measured %d", d.Name, est, measured)
		if float64(est) < 0.9*float64(measured) || est > 2*measured {
			t.Errorf("%s: estimate %d outside 0.9x to 2x of measured %d", d.Name, est, measured)
		}
	}
}

func TestCensusErrors(t *testing.T) {
	if _, _, err := Census(t.TempDir() + "/missing"); err == nil {
		t.Error("missing directory: want an error")
	}
	dir := t.TempDir()
	db, err := gl.OpenFile(dir, &opt.Options{Compression: opt.NoCompression})
	if err != nil {
		t.Fatal(err)
	}
	// A database-name key too short to hold its origin string.
	if err := db.Put([]byte{0, 0, 0, 0, metaDatabaseName, 0x80}, []byte{1}, nil); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if _, _, err := Census(dir); err == nil {
		t.Error("malformed database name: want an error")
	}
}

// Exact sums: a table value one byte under the lazy threshold is held and counted, one at the
// threshold is not; a log value, a superseded version and a deletion each count as an entry.
func TestCensusExactSums(t *testing.T) {
	dir := t.TempDir()
	db, err := gl.OpenFile(dir, &opt.Options{Compression: opt.NoCompression})
	if err != nil {
		t.Fatal(err)
	}
	put := func(k, v []byte) {
		t.Helper()
		if err := db.Put(k, v, nil); err != nil {
			t.Fatal(err)
		}
	}
	dk := func(name string) []byte { return append(idPrefix(1, 1, indexData, 1), name...) }
	lazy := leveldb.LazyMin()
	overhead := leveldb.EntryOverhead
	put(append([]byte{0, 0, 0, 0, metaDatabaseName}, append(idbString("https://x"), idbString("d")...)...), []byte{1})
	storeKey := idPrefix(1, 0, 0, metaObjectStore, 1, storeMetaName)
	storeVal := u16("s")
	put(storeKey, storeVal)
	// Table generation.
	put(dk("A"), make([]byte, lazy-1))
	put(dk("B"), make([]byte, lazy))
	put(dk("C"), make([]byte, 5))
	put(dk("D"), make([]byte, 7))
	if err := db.CompactRange(util.Range{}); err != nil {
		t.Fatal(err)
	}
	// Log generation: a log value, a superseded version, a deletion.
	put(dk("L"), make([]byte, 11))
	put(dk("C"), make([]byte, 9))
	if err := db.Delete(dk("D"), nil); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	want := int64(len(storeKey) + overhead + len(storeVal)) // per-database metadata
	want += int64(len(dk("A")) + overhead + lazy - 1)       // table, under threshold: held
	want += int64(len(dk("B")) + overhead)                  // table, at threshold: lazy
	want += int64(len(dk("C")) + overhead + 5)              // table version of C
	want += int64(len(dk("D")) + overhead + 7)              // table version of D
	want += int64(len(dk("L")) + overhead + 11)             // log value
	want += int64(len(dk("C")) + overhead + 9)              // superseded by a log version
	want += int64(len(dk("D")) + overhead)                  // deletion
	held, _, err := Census(dir)
	if err != nil {
		t.Fatal(err)
	}
	if held[1] != want {
		t.Errorf("held[1] = %d, want %d", held[1], want)
	}
}
