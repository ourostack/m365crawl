package outlookdesktop

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
)

type fileState struct {
	data  string
	size  int64
	mode  os.FileMode
	mtime time.Time
}

func captureTree(t *testing.T, root string) map[string]fileState {
	t.Helper()
	out := map[string]fileState{}
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		st := fileState{size: info.Size(), mode: info.Mode(), mtime: info.ModTime()}
		if info.Mode().IsRegular() {
			b, err := os.ReadFile(p) //nolint:gosec // test works on its own temp fixture and package sources
			if err != nil {
				return err
			}
			st.data = string(b)
		}
		out[p] = st
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestReadOnlyFixtureIsUntouched makes a fixture profile unwritable (directories 0500, files
// 0400) and runs discovery, snapshot and fingerprint over it. If any of them tried to write,
// remove, rename or truncate inside it they would fail here; and every file's bytes, size, mode
// and modification time must be the same afterwards.
func TestReadOnlyFixtureIsUntouched(t *testing.T) {
	quiet(t)
	tempHome(t)
	root := t.TempDir()
	store := fakeProfile(t, root, "Main Profile", 4096)
	mustWrite(t, filepath.Join(root, "Old", "Data", "Outlook.sqlite"), []byte("sqlite"))

	var dirs []string
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			dirs = append(dirs, p)
		} else if err := os.Chmod(p, 0o400); err != nil { //nolint:gosec // test works on its own temp fixture and package sources
			t.Fatal(err)
		}
		return nil
	})
	sort.Sort(sort.Reverse(sort.StringSlice(dirs))) // children before parents
	for _, d := range dirs {
		if err := os.Chmod(d, 0o500); err != nil { //nolint:gosec // test works on its own temp fixture and package sources
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { // so TempDir can remove it
		sort.Strings(dirs)
		for _, d := range dirs {
			_ = os.Chmod(d, 0o700) //nolint:gosec // test works on its own temp fixture and package sources
		}
	})
	before := captureTree(t, root)

	profiles, classic, skipped, err := Discover(root)
	if err != nil || len(profiles) != 1 || len(classic) != 1 || len(skipped) != 0 {
		t.Fatalf("discover: %v %v %v %v", profiles, classic, skipped, err)
	}
	info, cleanup, err := Snapshot(context.Background(), store)
	if err != nil || info.Size != 4096 {
		t.Fatalf("snapshot: %+v %v", info, err)
	}
	cleanup()
	if _, err := FingerprintOf(store, baseVersions); err != nil {
		t.Fatal(err)
	}

	after := captureTree(t, root)
	if len(after) != len(before) {
		t.Fatalf("file count %d -> %d", len(before), len(after))
	}
	for p, b := range before {
		a, ok := after[p]
		if !ok || a != b {
			t.Errorf("%s changed: before %+v after %+v", p, summary(b), summary(a))
		}
	}
	if runtime.GOOS != "windows" {
		if st := before[store]; st.mode.Perm() != 0o400 {
			t.Fatalf("fixture store mode %v, the test did not make it read-only", st.mode.Perm())
		}
	}
}

func summary(s fileState) [3]any { return [3]any{s.size, s.mode, s.mtime} }

// Why this scan exists: the package's safety promise is that it never writes to Outlook's
// storage. Tests prove it on behavior, and this scan stops a later edit from adding a call
// that can write, remove, rename, truncate, chmod or lock without someone noticing: any such
// call outside the small allowlist below (the temp-directory helper in snapshot.go) fails.

// denied are selector names that can modify the filesystem or take a lock.
var denied = map[string]bool{
	"Remove": true, "RemoveAll": true, "Rename": true, "Truncate": true, "Ftruncate": true,
	"Chmod": true, "Fchmod": true, "Chown": true, "Lchown": true, "Fchown": true, "Chtimes": true,
	"WriteFile": true, "Create": true, "CreateTemp": true, "Mkdir": true, "MkdirAll": true, "MkdirTemp": true,
	"Symlink": true, "Link": true, "Unlink": true, "Mknod": true, "Mkfifo": true,
	"Flock": true, "Fcntl": true, "FcntlFlock": true, "LockFileEx": true, "Lockf": true,
	"Setxattr": true, "Removexattr": true, "Utimes": true, "UtimesNano": true, "UtimesNanoAt": true,
	"OpenFile": true, "WriteAt": true, "WriteString": true, "Sync": true,
	"O_WRONLY": true, "O_RDWR": true, "O_APPEND": true, "O_CREATE": true, "O_TRUNC": true, "O_EXCL": true,
}

// allowed lists "<file>:<top-level declaration>:<selector>" uses that are the temp-directory
// helper or the single read-only open of the original.
var allowed = map[string]bool{
	"snapshot.go:Snapshot:MkdirTemp":  true, // the private snapshot directory
	"snapshot.go:Snapshot:RemoveAll":  true, // removing that directory
	"snapshot.go:removeFile:Remove":   true, // removing the stale copy inside it
	"snapshot.go:openFile:OpenFile":   true, // the original, opened with sourceOpenFlags (read-only; checked by TestSnapshotOpensReadOnly...)
	"snapshot.go:createFile:OpenFile": true, // the copy inside the snapshot directory
	"snapshot.go:createFile:O_WRONLY": true,
	"snapshot.go:createFile:O_CREATE": true,
	"snapshot.go:createFile:O_EXCL":   true,
}

// scanSource returns the denied uses in one Go source file as "<file>:<decl>:<selector>".
func scanSource(t *testing.T, name, src string) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), name, src, 0)
	if err != nil {
		t.Fatal(err)
	}
	var hits []string
	inspect := func(declName string, n ast.Node) {
		ast.Inspect(n, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok && denied[sel.Sel.Name] {
				hits = append(hits, name+":"+declName+":"+sel.Sel.Name)
			}
			return true
		})
	}
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			inspect(d.Name.Name, d)
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				if vs, ok := spec.(*ast.ValueSpec); ok {
					inspect(vs.Names[0].Name, vs)
				} else {
					inspect("", spec)
				}
			}
		}
	}
	return hits
}

func TestNoWritingCallsOutsideTheTempHelper(t *testing.T) {
	// The scanner finds a violation when there is one.
	bad := scanSource(t, "x.go", "package p\nimport \"os\"\nfunc f(p string) { os.Chmod(p, 0); os.Remove(p) }\nvar v = os.O_RDWR\n")
	if len(bad) != 3 {
		t.Fatalf("scanner missed violations: %v", bad)
	}
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatal("no source files", err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f) //nolint:gosec // test works on its own temp fixture and package sources
		if err != nil {
			t.Fatal(err)
		}
		for _, hit := range scanSource(t, f, string(src)) {
			if !allowed[hit] {
				t.Errorf("%s: a call that can write, remove, rename, truncate, chmod or lock is not on the allowlist", hit)
			}
		}
	}
	// Every allowlist entry is still used, so the list cannot rot into a blanket permission.
	used := map[string]bool{}
	for _, f := range []string{"snapshot.go"} {
		src, _ := os.ReadFile(f) //nolint:gosec // package sources
		for _, hit := range scanSource(t, f, string(src)) {
			used[hit] = true
		}
	}
	for k := range allowed {
		if !used[k] {
			t.Errorf("stale allowlist entry %s", k)
		}
	}
}
