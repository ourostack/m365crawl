// Command schemaguard fails when this tree's archive schema numbers are wrong, alone or against a
// base revision (#306): `go run ./scripts/schemaguard [BASE_REV]`. CI runs it on pull requests and
// in the merge queue with the base branch's commit, so a feature schema number that the base
// branch has already used is caught before the merge.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ourostack/m365crawl/internal/schemaguard"
)

const storeDir = "internal/store"

func main() {
	head, err := workingTree()
	if err == nil && len(os.Args) > 2 {
		err = fmt.Errorf("usage: schemaguard [BASE_REV]")
	}
	var base map[string][]byte
	if err == nil && len(os.Args) == 2 {
		base, err = revision(os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "schemaguard:", err)
		os.Exit(2)
	}
	if problems := schemaguard.Check(base, head); len(problems) > 0 {
		for _, p := range problems {
			fmt.Fprintln(os.Stderr, "schemaguard:", p)
		}
		os.Exit(1)
	}
	fmt.Println("schemaguard: schema numbers OK")
}

func source(name string) bool {
	return strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go")
}

func workingTree() (map[string][]byte, error) {
	names, err := filepath.Glob(filepath.Join(storeDir, "*.go"))
	files := map[string][]byte{}
	for _, name := range names {
		if err == nil && source(name) {
			files[name], err = os.ReadFile(name) //nolint:gosec // G304: the repository's own source
		}
	}
	return files, err
}

func revision(rev string) (map[string][]byte, error) {
	out, err := exec.Command("git", "ls-tree", "--name-only", rev, storeDir+"/").Output() //nolint:gosec // G204: a revision the caller names
	if err != nil {
		return nil, fmt.Errorf("list %s at %s: %w", storeDir, rev, err)
	}
	files := map[string][]byte{}
	for _, name := range strings.Fields(string(out)) {
		if !source(name) {
			continue
		}
		if files[name], err = exec.Command("git", "show", rev+":"+name).Output(); err != nil { //nolint:gosec // G204: as above
			return nil, fmt.Errorf("read %s at %s: %w", name, rev, err)
		}
	}
	return files, nil
}
