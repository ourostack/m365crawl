package leveldb

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	gl "github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"
	"github.com/syndtr/goleveldb/leveldb/util"
)

func openGL(t *testing.T, dir string) *gl.DB {
	t.Helper()
	db, err := gl.OpenFile(dir, &opt.Options{Compression: opt.NoCompression})
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func listExt(t *testing.T, dir, ext string) []string {
	t.Helper()
	m, err := filepath.Glob(filepath.Join(dir, "*"+ext))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestManifestLiveSet(t *testing.T) {
	dir := t.TempDir()
	db := openGL(t, dir)
	want := map[string]string{}
	for i := 0; i < 300; i++ {
		k, v := fmt.Sprintf("key-%04d", i), fmt.Sprintf("value-%d", i)
		if err := db.Put([]byte(k), []byte(v), nil); err != nil {
			t.Fatal(err)
		}
		want[k] = v
	}
	if err := db.CompactRange(util.Range{}); err != nil {
		t.Fatal(err)
	}
	for i := 250; i < 350; i++ {
		k, v := fmt.Sprintf("key-%04d", i), fmt.Sprintf("newer-%d", i)
		if err := db.Put([]byte(k), []byte(v), nil); err != nil {
			t.Fatal(err)
		}
		want[k] = v
	}
	// Compare against goleveldb itself before closing.
	for k, v := range want {
		got, err := db.Get([]byte(k), nil)
		if err != nil || string(got) != v {
			t.Fatalf("goleveldb disagrees on %s", k)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	// A stale, obsolete table that the manifest no longer references must be ignored.
	if err := os.WriteFile(filepath.Join(dir, "999999.ldb"), []byte("not a table"), 0o600); err != nil {
		t.Fatal(err)
	}

	d, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range want {
		got, ok := d.Get([]byte(k))
		if !ok || string(got) != v {
			t.Fatalf("Get(%s) = %q,%v want %q", k, got, ok, v)
		}
	}
	st := d.Stats()
	if st.Keys != len(want) {
		t.Fatalf("Keys = %d want %d", st.Keys, len(want))
	}
	if st.Tables < 1 || st.Logs < 1 {
		t.Fatalf("expected tables and logs, got %+v", st)
	}
	if st.Comparator != "leveldb.BytewiseComparator" {
		t.Fatalf("comparator = %q", st.Comparator)
	}
	if st.TruncatedLogTails != 0 {
		t.Fatalf("unexpected truncated tails: %+v", st)
	}
}

func TestScanPrefixOrdered(t *testing.T) {
	dir := t.TempDir()
	db := openGL(t, dir)
	for _, k := range []string{"b2", "a1", "b1", "c1", "b3"} {
		if err := db.Put([]byte(k), []byte("v"+k), nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.CompactRange(util.Range{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Put([]byte("b0"), []byte("vb0"), nil); err != nil {
		t.Fatal(err)
	}
	if err := db.Delete([]byte("b2"), nil); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	d, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	if err := d.Scan([]byte("b"), func(k, v []byte) error {
		got = append(got, string(k)+"="+string(v))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	want := []string{"b0=vb0", "b1=vb1", "b3=vb3"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	stop := errors.New("stop")
	if err := d.Scan(nil, func(k, v []byte) error { return stop }); !errors.Is(err, stop) {
		t.Fatalf("Scan did not propagate error: %v", err)
	}
	if _, ok := d.Get([]byte("nope")); ok {
		t.Fatal("Get of absent key returned ok")
	}
}

func TestDeletionNewestWins(t *testing.T) {
	dir := t.TempDir()
	db := openGL(t, dir)
	if err := db.Put([]byte("k"), []byte("v"), nil); err != nil {
		t.Fatal(err)
	}
	if err := db.CompactRange(util.Range{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Delete([]byte("k"), nil); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	d, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := d.Get([]byte("k")); ok {
		t.Fatalf("deleted key visible: %q", v)
	}
	if d.Stats().Keys != 0 {
		t.Fatalf("Keys = %d", d.Stats().Keys)
	}
}

func TestHighestSequenceWins(t *testing.T) {
	dir := t.TempDir()
	db := openGL(t, dir)
	if err := db.Put([]byte("k"), []byte("old"), nil); err != nil {
		t.Fatal(err)
	}
	if err := db.CompactRange(util.Range{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Put([]byte("k"), []byte("new"), nil); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	d, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := d.Get([]byte("k")); !ok || string(v) != "new" {
		t.Fatalf("got %q,%v", v, ok)
	}
}

func TestTruncatedLogTail(t *testing.T) {
	dir := t.TempDir()
	db := openGL(t, dir)
	for i := 0; i < 5; i++ {
		if err := db.Put([]byte{'k', byte('0' + i)}, []byte{'v', byte('0' + i)}, nil); err != nil {
			t.Fatal(err)
		}
	}
	_ = db.Close()
	logs := listExt(t, dir, ".log")
	if len(logs) == 0 {
		t.Fatal("no log")
	}
	f, err := os.OpenFile(logs[len(logs)-1], os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(bytes.Repeat([]byte{0xAB}, 7)); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	d, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if d.Stats().TruncatedLogTails != 1 {
		t.Fatalf("TruncatedLogTails = %d", d.Stats().TruncatedLogTails)
	}
	for i := 0; i < 5; i++ {
		if v, ok := d.Get([]byte{'k', byte('0' + i)}); !ok || !bytes.Equal(v, []byte{'v', byte('0' + i)}) {
			t.Fatalf("record %d lost", i)
		}
	}
}

func TestMissingManifestFile(t *testing.T) {
	dir := t.TempDir()
	db := openGL(t, dir)
	for i := 0; i < 50; i++ {
		_ = db.Put([]byte(fmt.Sprintf("k%d", i)), []byte("v"), nil)
	}
	if err := db.CompactRange(util.Range{}); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	tabs := listExt(t, dir, ".ldb")
	if len(tabs) == 0 {
		t.Fatal("no table")
	}
	if err := os.Remove(tabs[0]); err != nil {
		t.Fatal(err)
	}
	_, err := Load(dir)
	var mf *MissingFileError
	if !errors.As(err, &mf) || mf.Name != filepath.Base(tabs[0]) {
		t.Fatalf("err = %v", err)
	}
}

func TestMissingCurrent(t *testing.T) {
	_, err := Load(t.TempDir())
	var mf *MissingFileError
	if !errors.As(err, &mf) || mf.Name != "CURRENT" {
		t.Fatalf("err = %v", err)
	}
}
