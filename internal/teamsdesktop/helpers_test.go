package teamsdesktop

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	gl "github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"
	"github.com/syndtr/goleveldb/leveldb/util"
)

const (
	fixtureRootDir = "../../testdata/teams-fixture/EBWebView"
	fixtureProfile = "WV2Profile_fixture"
	fixtureOrigin  = "https_teams.microsoft.com_0"
)

// fixtureSource discovers the committed fixture through Discover.
func fixtureSource(t *testing.T) Source {
	t.Helper()
	srcs, _, err := Discover(fixtureRootDir)
	if err != nil || len(srcs) != 1 {
		t.Fatalf("Discover fixture: %v %v", srcs, err)
	}
	return srcs[0]
}

// fakeLevelDB writes a small compacted LevelDB into dir and returns the table paths.
func fakeLevelDB(t *testing.T, dir string) []string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := gl.OpenFile(dir, &opt.Options{Compression: opt.NoCompression})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50; i++ {
		if err := db.Put([]byte(fmt.Sprintf("k%02d", i)), []byte("value"), nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.CompactRange(util.Range{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	tabs, err := filepath.Glob(filepath.Join(dir, "*.ldb"))
	if err != nil || len(tabs) == 0 {
		t.Fatalf("no tables written: %v", err)
	}
	return tabs
}

// fakeTree builds <root>/<profile>/IndexedDB/<name>.indexeddb.leveldb for each origin name.
func fakeTree(t *testing.T, profile string, origins ...string) string {
	t.Helper()
	root := t.TempDir()
	for _, o := range origins {
		if err := os.MkdirAll(filepath.Join(root, profile, "IndexedDB", o+".indexeddb.leveldb"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(root, profile, "IndexedDB", o+".indexeddb.blob"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func walkSize(dir string, total *int64) error {
	return filepath.WalkDir(dir, func(_ string, e os.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return err
		}
		if info, err := e.Info(); err == nil {
			*total += info.Size()
		}
		return nil
	})
}
