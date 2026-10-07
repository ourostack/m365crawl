package outlookmail

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// Why this scan exists: this package may read one kind of file, a message body under the Outlook
// profile, and only through outlookdesktop.OpenReadOnly. Tests prove the behavior; this scan
// stops a later edit from adding a call that opens, writes, removes or renames a file, or an
// import that gives direct file access, without someone noticing. It is a tripwire on names in
// the syntax tree, not proof: a call through a variable or another package is invisible to it.

// deniedImports give file access or run programs; the package imports none of them.
var deniedImports = map[string]bool{"os": true, "os/exec": true, "io/ioutil": true, "syscall": true, "golang.org/x/sys/unix": true}

// denied are selector names that can open, write or change a file.
var denied = map[string]bool{
	"Open": true, "OpenFile": true, "Create": true, "CreateTemp": true, "ReadFile": true, "WriteFile": true,
	"Remove": true, "RemoveAll": true, "Rename": true, "Truncate": true, "Chmod": true, "Chtimes": true,
	"Mkdir": true, "MkdirAll": true, "MkdirTemp": true, "Symlink": true, "Link": true,
}

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
	}
	ast.Inspect(f, func(n ast.Node) bool {
		if x, ok := n.(*ast.SelectorExpr); ok && denied[x.Sel.Name] {
			hits = append(hits, name+":"+x.Sel.Name)
		}
		return true
	})
	return hits
}

func TestScannerFiresOnEachRule(t *testing.T) {
	for name, tc := range map[string]struct {
		src  string
		want string
	}{
		"os import":      {"package p\nimport \"os\"\nvar _ = os.Getpid", "x.go:import:os"},
		"exec import":    {"package p\nimport \"os/exec\"\nvar _ = exec.Command", "x.go:import:os/exec"},
		"ioutil import":  {"package p\nimport \"io/ioutil\"\nvar _ = ioutil.ReadAll", "x.go:import:io/ioutil"},
		"syscall import": {"package p\nimport \"syscall\"\nvar _ = syscall.Getpid", "x.go:import:syscall"},
		"unix import":    {"package p\nimport \"golang.org/x/sys/unix\"\nvar _ = unix.Getpid", "x.go:import:golang.org/x/sys/unix"},
		"open call":      {"package p\nfunc f(o interface{ Open(string) }) { o.Open(\"a\") }", "x.go:Open"},
		"write call":     {"package p\nfunc f(o interface{ WriteFile(string) }) { o.WriteFile(\"a\") }", "x.go:WriteFile"},
	} {
		got := scanSource(t, "x.go", tc.src)
		if len(got) != 1 || got[0] != tc.want {
			t.Errorf("%s: %v, want %s", name, got, tc.want)
		}
	}
	if got := scanSource(t, "x.go", "package p\nimport \"path/filepath\"\nvar _ = filepath.EvalSymlinks"); len(got) != 0 {
		t.Errorf("false positive %v", got)
	}
}

func TestPackageOnlyReadsThroughTheReadOnlyOpen(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatal("no source files", err)
	}
	var hits []string
	var opens []string
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f) //nolint:gosec // test reads the package's own sources
		if err != nil {
			t.Fatal(err)
		}
		hits = append(hits, scanSource(t, f, string(src))...)
		if strings.Contains(string(src), "outlookdesktop.OpenReadOnly") {
			opens = append(opens, f)
		}
	}
	sort.Strings(hits)
	if len(hits) != 0 {
		t.Errorf("file access outside outlookdesktop.OpenReadOnly: %v", hits)
	}
	if len(opens) != 1 || opens[0] != "body.go" {
		t.Errorf("the read-only open is used in %v, want only body.go", opens)
	}
}
