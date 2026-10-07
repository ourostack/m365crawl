package outlookdesktop

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/errs"
)

var baseVersions = Versions{Store: "i", Reader: 1, Mapper: 2, Rules: 3}

func TestFingerprintChangesWithEachInput(t *testing.T) {
	root := t.TempDir()
	store := fakeProfile(t, root, "Main Profile", 100)
	base, err := FingerprintOf(store, baseVersions)
	if err != nil || len(base) != 64 {
		t.Fatalf("base = %q, %v", base, err)
	}
	again, _ := FingerprintOf(store, baseVersions)
	if again != base {
		t.Fatal("fingerprint is not deterministic")
	}

	seen := map[string]string{base: "base"}
	check := func(name string, fp string) {
		t.Helper()
		if prev, dup := seen[fp]; dup {
			t.Errorf("%s gives the same fingerprint as %s", name, prev)
		}
		seen[fp] = name
	}
	for name, v := range map[string]Versions{
		"store version":  {Store: "j", Reader: 1, Mapper: 2, Rules: 3},
		"reader version": {Store: "i", Reader: 9, Mapper: 2, Rules: 3},
		"mapper version": {Store: "i", Reader: 1, Mapper: 9, Rules: 3},
		"rules version":  {Store: "i", Reader: 1, Mapper: 2, Rules: 9},
		"mail version":   {Store: "i", Reader: 1, Mapper: 2, Rules: 3, Mail: 9},
	} {
		fp, err := FingerprintOf(store, v)
		if err != nil {
			t.Fatal(err)
		}
		check(name, fp)
	}

	setMtime(t, store, time.Microsecond) // above the coarsest timestamp tick (100 ns on NTFS)
	fp, _ := FingerprintOf(store, baseVersions)
	check("mtime in nanoseconds", fp)

	mustWrite(t, store, syntheticStore(101))
	fp, _ = FingerprintOf(store, baseVersions)
	check("size", fp)
}

func TestFingerprintIgnoresSiblingFiles(t *testing.T) {
	root := t.TempDir()
	store := fakeProfile(t, root, "Main Profile", 100)
	base, _ := FingerprintOf(store, baseVersions)
	dir := filepath.Dir(store)
	mustWrite(t, filepath.Join(dir, "hxcore.hfl"), []byte("rewritten and longer"))
	setMtime(t, filepath.Join(dir, "HxStore.lock"), time.Hour)
	if got, _ := FingerprintOf(store, baseVersions); got != base {
		t.Fatal("a sibling file changed the fingerprint")
	}
}

func TestFingerprintForMatchesFingerprintOf(t *testing.T) {
	store := fakeProfile(t, t.TempDir(), "Main Profile", 100)
	info, err := os.Stat(store)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := FingerprintOf(store, baseVersions)
	if got := FingerprintFor(info.Size(), info.ModTime(), baseVersions); got != want {
		t.Fatalf("%s != %s", got, want)
	}
}

func TestFingerprintErrors(t *testing.T) {
	root := t.TempDir()
	if _, err := FingerprintOf(filepath.Join(root, "missing"), baseVersions); !errors.Is(err, ErrStoreNotFound) {
		t.Fatalf("missing: %v", err)
	}
	if _, err := FingerprintOf(root, baseVersions); codeOf(t, err) != errs.CodeInternal {
		t.Fatalf("directory: %v", err)
	}
	orig := statSource
	t.Cleanup(func() { statSource = orig })
	statSource = func(string) (fs.FileInfo, error) { return nil, fs.ErrPermission }
	if _, err := FingerprintOf(filepath.Join(root, "x"), baseVersions); codeOf(t, err) != errs.CodeNoFullDiskAccess {
		t.Fatalf("permission: %v", err)
	}
}
