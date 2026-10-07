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
	"strconv"
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
//
// This is a tripwire, not proof. It matches names in the syntax tree, so it cannot see a
// forbidden call made through a variable, a function value, an interface, reflection, a helper
// in another package or a method the deny-list does not name, and a new spelling of a write
// can slip past it. It only makes the obvious additions loud. The behavioral tests above (the
// read-only fixture, the open-flag check) are what show the package leaves Outlook alone.

// denied are selector names that can modify the filesystem or take a lock. Open and Write are
// here although they are common names: a read-only open and a write to our own copy must each be
// named in the allowlist, which makes a new one a decision rather than an accident.
var denied = map[string]bool{
	"Remove": true, "RemoveAll": true, "Rename": true, "Truncate": true, "Ftruncate": true,
	"Rmdir": true, "Unlinkat": true, "Renameat": true,
	"Chmod": true, "Fchmod": true, "Chown": true, "Lchown": true, "Fchown": true, "Chtimes": true,
	"WriteFile": true, "Create": true, "CreateTemp": true, "Mkdir": true, "MkdirAll": true, "MkdirTemp": true,
	"Symlink": true, "Link": true, "Unlink": true, "Mknod": true, "Mkfifo": true,
	"Flock": true, "Fcntl": true, "FcntlFlock": true, "LockFileEx": true, "Lockf": true,
	"Setxattr": true, "Removexattr": true, "Utimes": true, "UtimesNano": true, "UtimesNanoAt": true,
	"OpenFile": true, "Open": true, "Openat": true,
	"Write": true, "WriteAt": true, "WriteString": true, "Pwrite": true, "Sync": true,
	"O_WRONLY": true, "O_RDWR": true, "O_APPEND": true, "O_CREATE": true, "O_TRUNC": true, "O_EXCL": true,
}

// deniedImports are packages the package must not import at all: running another program is a way
// to write or lock that no selector name would show.
var deniedImports = map[string]bool{"os/exec": true}

// seams are the package variables that wrap a remove or an open. A call through one is as
// dangerous as the call it wraps, and the name hides it from the selector list, so each use is
// named in the allowlist too.
var seams = map[string]bool{"removeFile": true, "openFile": true}

// allowed lists "<file>:<top-level declaration>:<name>" uses that are the temp-directory
// helper, the single read-only open of the original, or a write to our own copy.
var allowed = map[string]bool{
	"snapshot.go:Snapshot:MkdirTemp":    true, // the private snapshot directory
	"snapshot.go:Snapshot:RemoveAll":    true, // removing that directory
	"snapshot.go:removeFile:Remove":     true, // removing the stale copy inside it
	"snapshot.go:copyOnce:removeFile":   true, // the seam, called on dst: the path inside our snapshot directory
	"snapshot.go:openFile:OpenFile":     true, // the original, opened with sourceOpenFlags (read-only; checked by TestSnapshotOpensReadOnly...)
	"snapshot.go:copyOnce:openFile":     true, // the seam, called with sourceOpenFlags on the original
	"readfile.go:OpenReadOnly:openFile": true, // the seam, called with sourceOpenFlags on a file in the profile
	"snapshot.go:createFile:OpenFile":   true, // the copy inside the snapshot directory
	"snapshot.go:createFile:O_WRONLY":   true,
	"snapshot.go:createFile:O_CREATE":   true,
	"snapshot.go:createFile:O_EXCL":     true,
	"snapshot.go:copyStream:Write":      true, // writes to the io.Writer for our copy, never to the original
}

// scanSource returns the denied uses in one Go source file as "<file>:<decl>:<name>". Imports
// are reported as "<file>:import:<path>", and a dot import as "<file>:import:.".
func scanSource(t *testing.T, name, src string) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), name, src, 0)
	if err != nil {
		t.Fatal(err)
	}
	var hits []string
	for _, imp := range f.Imports {
		if path, _ := strconv.Unquote(imp.Path.Value); deniedImports[path] {
			hits = append(hits, name+":import:"+path)
		}
		if imp.Name != nil && imp.Name.Name == "." {
			hits = append(hits, name+":import:.") // a dot import hides which package a call comes from
		}
	}
	inspect := func(declName string, n ast.Node) {
		ast.Inspect(n, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.SelectorExpr:
				if denied[x.Sel.Name] {
					hits = append(hits, name+":"+declName+":"+x.Sel.Name)
				}
			case *ast.Ident:
				if seams[x.Name] {
					hits = append(hits, name+":"+declName+":"+x.Name)
				}
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
					// The declared names are not uses; only the type and the values are.
					if vs.Type != nil {
						inspect(vs.Names[0].Name, vs.Type)
					}
					for _, v := range vs.Values {
						inspect(vs.Names[0].Name, v)
					}
				} else {
					inspect("", spec)
				}
			}
		}
	}
	return hits
}

// Each row is a small source file the scanner must flag, with the exact hits it must report.
// Without this table a rule could be deleted or misspelled and the real sources would still pass.
func TestScannerFiresOnEachRule(t *testing.T) {
	const head = "package p\nimport \"os\"\n"
	cases := []struct {
		name string
		src  string
		want []string
	}{
		{"Remove", head + "func f(p string) { os.Remove(p) }", []string{"x.go:f:Remove"}},
		{"RemoveAll", head + "func f(p string) { os.RemoveAll(p) }", []string{"x.go:f:RemoveAll"}},
		{"Rename", head + "func f(p string) { os.Rename(p, p) }", []string{"x.go:f:Rename"}},
		{"Rmdir", "package p\nimport \"syscall\"\nfunc f(p string) { syscall.Rmdir(p) }", []string{"x.go:f:Rmdir"}},
		{"Unlinkat", "package p\nimport \"syscall\"\nfunc f() { syscall.Unlinkat(1, \"a\") }", []string{"x.go:f:Unlinkat"}},
		{"Renameat", "package p\nimport \"syscall\"\nfunc f() { syscall.Renameat(1, \"a\", 2, \"b\") }", []string{"x.go:f:Renameat"}},
		{"Open", head + "func f(p string) { os.Open(p) }", []string{"x.go:f:Open"}},
		{"Openat", "package p\nimport \"syscall\"\nfunc f() { syscall.Openat(1, \"a\", 0, 0) }", []string{"x.go:f:Openat"}},
		{"OpenFile", head + "func f(p string) { os.OpenFile(p, 0, 0) }", []string{"x.go:f:OpenFile"}},
		{"Write", head + "func f(w *os.File) { w.Write(nil) }", []string{"x.go:f:Write"}},
		{"Pwrite", "package p\nimport \"syscall\"\nfunc f() { syscall.Pwrite(1, nil, 0) }", []string{"x.go:f:Pwrite"}},
		{"WriteAt", head + "func f(w *os.File) { w.WriteAt(nil, 0) }", []string{"x.go:f:WriteAt"}},
		{"Chmod", head + "func f(p string) { os.Chmod(p, 0) }", []string{"x.go:f:Chmod"}},
		{"flag in a package variable", head + "var v = os.O_RDWR", []string{"x.go:v:O_RDWR"}},
		{"os/exec import", "package p\nimport \"os/exec\"\nvar _ = exec.Command", []string{"x.go:import:os/exec"}},
		{"os/exec import under an alias", "package p\nimport run \"os/exec\"\nvar _ = run.Command", []string{"x.go:import:os/exec"}},
		{"dot import", "package p\nimport . \"os\"\nvar _ = Getpid", []string{"x.go:import:."}},
		{"dot import of os/exec", "package p\nimport . \"os/exec\"\nvar _ = Command", []string{"x.go:import:os/exec", "x.go:import:."}},
		{"call through removeFile", "package p\nfunc f(p string) { removeFile(p) }", []string{"x.go:f:removeFile"}},
		{"call through openFile", "package p\nfunc f(p string) { openFile(p, 0, 0) }", []string{"x.go:f:openFile"}},
		{"seam as a value", "package p\nvar g = removeFile", []string{"x.go:g:removeFile"}},
		{"seam in a package variable's function", "package p\nvar g = func() { openFile(\"a\", 0, 0) }", []string{"x.go:g:openFile"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := scanSource(t, "x.go", c.src)
			sort.Strings(got)
			want := append([]string(nil), c.want...)
			sort.Strings(want)
			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Fatalf("hits %v, want %v", got, want)
			}
		})
	}
	// Declaring a seam, or reading a file, is not a use: clean sources report nothing.
	for name, src := range map[string]string{
		"declaring the seams": "package p\nimport \"os\"\nvar removeFile = fn\nvar fn = func(string) error { return nil }\nvar _ = os.Getpid",
		"stat and read":       head + "func f(p string) { os.Stat(p); os.ReadFile(p); os.ReadDir(p) }",
		"plain import":        "package p\nimport \"os\"\nvar _ = os.Getpid",
	} {
		if got := scanSource(t, "x.go", src); len(got) != 0 {
			t.Errorf("%s: false positive %v", name, got)
		}
	}
}

func TestNoWritingCallsOutsideTheTempHelper(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatal("no source files", err)
	}
	used := map[string]bool{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f) //nolint:gosec // test works on its own temp fixture and package sources
		if err != nil {
			t.Fatal(err)
		}
		for _, hit := range scanSource(t, f, string(src)) {
			used[hit] = true
			if !allowed[hit] {
				t.Errorf("%s: a call that can write, remove, rename, truncate, chmod or lock, or an import that can run a program, is not on the allowlist", hit)
			}
		}
	}
	// Every allowlist entry is still used, so the list cannot rot into a blanket permission.
	for k := range allowed {
		if !used[k] {
			t.Errorf("stale allowlist entry %s", k)
		}
	}
}
