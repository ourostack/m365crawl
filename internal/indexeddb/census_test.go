package indexeddb

import (
	"fmt"
	"os"
	"runtime"
	"testing"

	gl "github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"
	"github.com/syndtr/goleveldb/leveldb/util"
)

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
	sizes := []int{20, 700} // "big" values exceed the lazy threshold
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
		if est > 2*measured || measured > 2*est {
			t.Errorf("%s: estimate %d not within 2x of measured %d", d.Name, est, measured)
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
