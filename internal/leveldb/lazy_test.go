package leveldb

import (
	"bytes"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"testing"

	gl "github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"
	"github.com/syndtr/goleveldb/leveldb/util"
)

// buildMixed writes a database whose live state spans several tables and a log, with small,
// large and multi-block values, overwrites and deletions, and returns goleveldb's own view.
func buildMixed(t *testing.T, compression opt.Compression) (string, map[string][]byte) {
	t.Helper()
	dir := t.TempDir()
	db, err := gl.OpenFile(dir, &opt.Options{Compression: compression, BlockSize: 1024, WriteBuffer: 64 << 10})
	if err != nil {
		t.Fatal(err)
	}
	rng := rand.New(rand.NewSource(7)) //nolint:gosec // deterministic test data
	val := func(i int) []byte {
		sizes := []int{0, 3, 100, 511, 512, 513, 900, 3000, 20000}
		v := make([]byte, sizes[i%len(sizes)])
		for j := range v {
			v[j] = "abcd"[rng.Intn(4)] // compressible
		}
		if i%5 == 0 {
			_, _ = rng.Read(v) // incompressible
		}
		return v
	}
	for round := 0; round < 3; round++ {
		for i := 0; i < 400; i++ {
			k := []byte(fmt.Sprintf("k%04d", (i*7+round*13)%500))
			if (i+round)%11 == 0 {
				if err := db.Delete(k, nil); err != nil {
					t.Fatal(err)
				}
				continue
			}
			if err := db.Put(k, val(i+round), nil); err != nil {
				t.Fatal(err)
			}
		}
		if round < 2 {
			if err := db.CompactRange(util.Range{Limit: []byte(fmt.Sprintf("k%04d", 250*(round+1)))}); err != nil {
				t.Fatal(err)
			}
		}
	}
	want := map[string][]byte{}
	it := db.NewIterator(nil, nil)
	for it.Next() {
		want[string(it.Key())] = append([]byte(nil), it.Value()...)
	}
	it.Release()
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return dir, want
}

func checkAgainst(t *testing.T, d *DB, want map[string][]byte) {
	t.Helper()
	got := map[string][]byte{}
	if err := d.Scan(nil, func(k, v []byte) error {
		got[string(k)] = append([]byte(nil), v...)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("scan has %d keys, goleveldb %d", len(got), len(want))
	}
	for k, v := range want {
		if !bytes.Equal(got[k], v) {
			t.Fatalf("%s: scan value differs (%d vs %d bytes)", k, len(got[k]), len(v))
		}
		if g, ok := d.Get([]byte(k)); !ok || !bytes.Equal(g, v) {
			t.Fatalf("%s: Get differs", k)
		}
	}
}

// The table reader agrees with goleveldb on every live key and value, compressed or not.
func TestTablesMatchGoleveldb(t *testing.T) {
	for _, c := range []struct {
		name string
		comp opt.Compression
	}{{"snappy", opt.SnappyCompression}, {"none", opt.NoCompression}} {
		t.Run(c.name, func(t *testing.T) {
			dir, want := buildMixed(t, c.comp)
			if len(listExt(t, dir, ".ldb")) < 2 {
				t.Fatal("want several tables")
			}
			d, err := Load(dir)
			if err != nil {
				t.Fatal(err)
			}
			checkAgainst(t, d, want)
			if d.Stats().Keys != len(want) {
				t.Fatalf("Keys = %d want %d", d.Stats().Keys, len(want))
			}
		})
	}
}

// Large table values are not held in memory: they are re-read from their block when asked for.
func TestLargeTableValuesAreLazy(t *testing.T) {
	dir, want := buildMixed(t, opt.SnappyCompression)
	d, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	lazy, held := 0, 0
	for _, e := range d.entries {
		if e.deleted {
			continue
		}
		if e.lazy {
			lazy++
			if e.value != nil || e.loc.n < lazyMin {
				t.Fatalf("lazy entry holds %d bytes, length %d", len(e.value), e.loc.n)
			}
			continue
		}
		held++
		if len(e.value) >= lazyMin && e.loc != (location{}) {
			t.Fatalf("table value of %d bytes held", len(e.value))
		}
	}
	if lazy == 0 || held == 0 {
		t.Fatalf("lazy %d, held %d: fixture does not exercise both", lazy, held)
	}
	checkAgainst(t, d, want)
	if d.cache.reads == 0 {
		t.Fatal("no block was re-read")
	}
	if len(d.cache.blocks) > blockCacheSize {
		t.Fatalf("cache holds %d blocks", len(d.cache.blocks))
	}
}

// With every table value lazy, the results are still goleveldb's.
func TestAllTableValuesLazy(t *testing.T) {
	old := lazyMin
	lazyMin = 0
	t.Cleanup(func() { lazyMin = old })
	dir, want := buildMixed(t, opt.SnappyCompression)
	d, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	checkAgainst(t, d, want)
}

// A lazy value whose table vanished is an error from Scan and absent from Get.
func TestLazyValueRereadFailure(t *testing.T) {
	dir, want := buildMixed(t, opt.SnappyCompression)
	d, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	var lazyKey string
	for k, e := range d.entries {
		if e.lazy && !e.deleted {
			lazyKey = k
			break
		}
	}
	if lazyKey == "" || want[lazyKey] == nil {
		t.Fatal("no lazy key")
	}
	for _, p := range listExt(t, dir, ".ldb") {
		if err := os.Remove(p); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok := d.Get([]byte(lazyKey)); ok {
		t.Fatal("Get of an unreadable lazy value reported ok")
	}
	err = d.Scan([]byte(lazyKey), func(k, v []byte) error { return nil })
	var mf *MissingFileError
	if !errors.As(err, &mf) {
		t.Fatalf("Scan err = %v, want MissingFileError", err)
	}
}

// A corrupted data block fails the load.
func TestTableChecksumMismatch(t *testing.T) {
	dir := compactedDir(t)
	tabs := listExt(t, dir, ".ldb")
	b, err := os.ReadFile(tabs[0])
	if err != nil {
		t.Fatal(err)
	}
	b[3] ^= 0xff
	if err := os.WriteFile(tabs[0], b, 0o600); err != nil { //nolint:gosec // test temp dir
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil {
		t.Fatal("corrupt block accepted")
	}
}

// A table too short for its footer, or with a bad magic number, fails the load.
func TestTableBadFooter(t *testing.T) {
	for name, mutate := range map[string]func([]byte) []byte{
		"short": func(b []byte) []byte { return b[:20] },
		"magic": func(b []byte) []byte { b[len(b)-1] ^= 0xff; return b },
	} {
		t.Run(name, func(t *testing.T) {
			dir := compactedDir(t)
			tab := listExt(t, dir, ".ldb")[0]
			b, err := os.ReadFile(tab) //nolint:gosec // test temp dir
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(tab, mutate(b), 0o600); err != nil { //nolint:gosec // test temp dir
				t.Fatal(err)
			}
			if _, err := Load(dir); err == nil {
				t.Fatal("bad table accepted")
			}
		})
	}
}

func TestBlockEntriesRejectsMalformed(t *testing.T) {
	for name, b := range map[string][]byte{
		"short":    {1, 2},
		"restarts": {0, 0, 0, 0xff, 0xff, 0xff, 0x7f},
		"overrun":  append([]byte{0, 5, 1, 'a'}, 0, 0, 0, 0, 1, 0, 0, 0),
		"shared":   append([]byte{3, 1, 0, 'a'}, 0, 0, 0, 0, 1, 0, 0, 0),
	} {
		if err := blockEntries(b, func([]byte, int, int) error { return nil }); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
