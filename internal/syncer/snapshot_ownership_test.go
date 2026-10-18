package syncer

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/indexeddb"
	"github.com/ourostack/m365crawl/internal/leveldb"
	gl "github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"
	"github.com/syndtr/goleveldb/leveldb/util"
)

func ownershipLazySource(t *testing.T) (string, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "EBWebView")
	dir := filepath.Join(root, "WV2Profile_owned", "IndexedDB", "https_teams.microsoft.com_0.indexeddb.leveldb")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := gl.OpenFile(dir, &opt.Options{Compression: opt.NoCompression})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	put := func(key, value []byte) {
		t.Helper()
		if err := db.Put(key, value, &opt.WriteOptions{Sync: true}); err != nil {
			t.Fatal(err)
		}
	}
	name := append([]byte{0, 0, 0, 0, 201}, observationUTF16("https://teams.microsoft.com", true)...)
	name = append(name, observationUTF16("owned-lazy-records", true)...)
	put(name, []byte{1})
	put([]byte{0, 1, 0, 0, 50, 1, 0}, observationUTF16("rows", false))
	text := strings.Repeat("owned lazy value ", leveldb.LazyMin()/16+1024)
	value := binary.AppendUvarint([]byte{1, 0xff, 15, 'S'}, uint64(len(text)))
	value = append(value, text...)
	key := append([]byte{0, 1, 1, 1, 1}, observationUTF16("lazy-key", true)...)
	put(key, value)
	if err := db.CompactRange(util.Range{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	tables, err := filepath.Glob(filepath.Join(dir, "*.ldb"))
	if err != nil || len(tables) == 0 || len(value) < leveldb.LazyMin() {
		t.Fatalf("setup must put a lazy-sized value in a real table: tables=%v size=%d err=%v", tables, len(value), err)
	}
	return root, text
}

func TestAgedActiveSnapshotSurvivesCrossArchiveSweep(t *testing.T) {
	tmp := isolateTmp(t)
	root, want := ownershipLazySource(t)
	dbA, dbB := newDB(t), newDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	ready, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var first atomic.Bool
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	old := afterSnapshot
	afterSnapshot = func() {
		if first.CompareAndSwap(false, true) {
			close(ready)
			select {
			case <-release:
			case <-ctx.Done():
			}
		}
	}
	t.Cleanup(func() { afterSnapshot = old })
	type result struct {
		report Report
		err    error
	}
	results := make(chan result, 1)
	go func() {
		defer close(done)
		report, _, err := Run(ctx, Options{Root: root, DBPath: dbA})
		results <- result{report, err}
	}()
	t.Cleanup(func() { cancel(); unblock(); <-done })
	select {
	case <-ready:
	case r := <-results:
		t.Fatalf("first run failed before holding its snapshot: %+v err=%v", r.report, r.err)
	case <-ctx.Done():
		t.Fatal("first run did not reach its snapshot boundary")
	}
	paths := snapshotDirs(t, tmp)
	if len(paths) != 1 {
		t.Fatalf("setup must identify exactly its own active snapshot: %v", paths)
	}
	active := paths[0]
	origin, err := indexeddb.Open(filepath.Join(active, "leveldb"), filepath.Join(active, "blob"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = origin.Close() })
	if origin.Stats().Tables == 0 {
		t.Fatal("active reader has no actual table-backed input")
	}
	legacy := filepath.Join(tmp, "m365crawl-snapshot-legacy-owned")
	if err := os.Mkdir(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	aged := time.Now().Add(-2 * staleSnapshotAfter)
	for _, dir := range []string{active, legacy} {
		if err := os.Chtimes(dir, aged, aged); err != nil {
			t.Fatal(err)
		}
	}
	second, _, secondErr := Run(ctx, Options{Root: root, DBPath: dbB})
	if secondErr != nil || second.Status != StatusOK || second.Records.Inserted != 1 {
		t.Fatalf("second archive did not independently read the owned source: %+v err=%v", second, secondErr)
	}
	if err := ctx.Err(); err != nil {
		t.Fatalf("setup deadline expired rather than demonstrating a sweep: %v", err)
	}
	for _, dir := range []string{active, legacy} {
		if _, err := os.Lstat(dir); err != nil {
			t.Errorf("second archive swept live/unknown owned root %s: %v", dir, err)
		}
	}
	rows := 0
	err = origin.Records(1, 1, func(r indexeddb.Record) error {
		rows++
		if r.Err != nil {
			return r.Err
		}
		value, err := origin.Decode(1, r.Raw)
		if err != nil {
			return err
		}
		if text, ok := value.(string); !ok || text != want {
			t.Error("the live lazy reader no longer resolves the original owned value")
		}
		return nil
	})
	if err != nil || rows != 1 {
		t.Errorf("aged active table reader lost its value: rows=%d err=%v", rows, err)
	}
	if err := origin.Close(); err != nil {
		t.Fatal(err)
	}
	unblock()
	<-done
	a := <-results
	if a.err != nil || a.report.Status != StatusOK || a.report.Records.Inserted != 1 {
		t.Errorf("first archive did not survive the cross-archive sweep: %+v err=%v", a.report, a.err)
	}
	if _, err := os.Stat(active); !os.IsNotExist(err) {
		t.Errorf("completed first run failed to clean its own snapshot: %v", err)
	}
	if left := snapshotDirs(t, tmp); len(left) != 1 || left[0] != legacy {
		t.Errorf("only deliberately retained unknown legacy root should remain: %v", left)
	}
}
