// Package schemaguard checks the archive's schema version numbers in internal/store's Go source.
// A schema number is picked when a change is written, and parallel changes overtake each other.
// SchemaVersion conflicts in Git when two changes raise it, but a feature constant
// (<Feature>SchemaVersion) is its own line and can silently stay at a number the base branch has
// since used (#306). Check catches that before a merge.
package schemaguard

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"slices"
	"strconv"
	"strings"
)

// Numbers are a store package's schema version constants.
type Numbers struct {
	// Schema is SchemaVersion, the archive's current schema.
	Schema int
	// Features are the <Feature>SchemaVersion constants: the schema that added a feature.
	Features map[string]int
}

// Versions reads SchemaVersion and every top-level <Feature>SchemaVersion constant from a
// package's Go files (name to source). Each must be a decimal integer literal, so a reader of the
// source and this check see the same number; anything else is a problem.
func Versions(files map[string][]byte) (Numbers, []string) {
	n := Numbers{Schema: -1, Features: map[string]int{}}
	var problems []string
	fset := token.NewFileSet()
	for _, name := range slices.Sorted(maps.Keys(files)) {
		f, err := parser.ParseFile(fset, name, files[name], parser.SkipObjectResolution)
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		for _, decl := range f.Decls {
			if g, ok := decl.(*ast.GenDecl); ok && g.Tok == token.CONST {
				problems = append(problems, constants(g, &n)...)
			}
		}
	}
	if n.Schema < 0 {
		problems = append(problems, "no SchemaVersion constant")
	}
	return n, problems
}

func constants(g *ast.GenDecl, n *Numbers) []string {
	var problems []string
	for _, spec := range g.Specs {
		vs := spec.(*ast.ValueSpec)
		for i, id := range vs.Names {
			if !strings.HasSuffix(id.Name, "SchemaVersion") {
				continue
			}
			v, err := literal(vs, i)
			if err != nil {
				problems = append(problems, id.Name+": "+err.Error())
				continue
			}
			if id.Name == "SchemaVersion" {
				n.Schema = v
			} else {
				n.Features[id.Name] = v
			}
		}
	}
	return problems
}

func literal(vs *ast.ValueSpec, i int) (int, error) {
	if i >= len(vs.Values) {
		return 0, fmt.Errorf("needs its own integer value")
	}
	lit, ok := vs.Values[i].(*ast.BasicLit)
	if !ok || lit.Kind != token.INT {
		return 0, fmt.Errorf("must be an integer literal")
	}
	return strconv.Atoi(lit.Value)
}

// Check returns what is wrong with head's schema numbers, alone and against base (the base
// branch's store package; nil checks head alone). Within head, two features may not share a
// number and none may be above SchemaVersion. Against base, SchemaVersion may not go down, a
// feature may not disappear, and a feature that is new or renumbered must be above the base
// branch's SchemaVersion: a lower number is already taken there.
func Check(base, head map[string][]byte) []string {
	h, problems := Versions(head)
	if len(problems) > 0 {
		return problems
	}
	names := slices.Sorted(maps.Keys(h.Features))
	byVersion := map[int]string{}
	for _, name := range names {
		v := h.Features[name]
		if other, ok := byVersion[v]; ok {
			problems = append(problems, fmt.Sprintf("%s and %s are both %d", other, name, v))
		}
		byVersion[v] = name
		if v > h.Schema {
			problems = append(problems, fmt.Sprintf("%s is %d, above SchemaVersion %d", name, v, h.Schema))
		}
	}
	if base == nil {
		return problems
	}
	b, baseProblems := Versions(base)
	if len(baseProblems) > 0 {
		return []string{"base branch: " + strings.Join(baseProblems, "; ")}
	}
	if h.Schema < b.Schema {
		problems = append(problems, fmt.Sprintf("SchemaVersion is %d, below the base branch's %d", h.Schema, b.Schema))
	}
	for _, name := range names {
		if v, was := h.Features[name], b.Features[name]; v != was && v <= b.Schema {
			problems = append(problems, fmt.Sprintf("%s is %d, not above the base branch's SchemaVersion %d: that number is taken there; use %d or above and raise SchemaVersion with it",
				name, v, b.Schema, b.Schema+1))
		}
	}
	for _, name := range slices.Sorted(maps.Keys(b.Features)) {
		if _, ok := h.Features[name]; !ok {
			problems = append(problems, fmt.Sprintf("%s (%d on the base branch) was removed", name, b.Features[name]))
		}
	}
	return problems
}
