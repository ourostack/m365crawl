package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGateEntrypointRequiresACompleteRealProfile(t *testing.T) {
	root, profile := fixtureProfile(t, true, "github.com/ourostack/m365crawl")
	if err := os.WriteFile(filepath.Join(root, "declarations.go"), []byte("package sample\nconst DeclarationOnly = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := coverageScope
	t.Cleanup(func() { coverageScope = old })
	coverageScope = func(string) (map[string][]string, error) {
		return map[string][]string{"sample.go": {"Called", "Uncalled"}, "declarations.go": nil}, nil
	}
	var out, stderr bytes.Buffer
	if code := run([]string{"-root=" + root, "-profile=" + profile}, &out, &stderr); code != 0 || stderr.Len() > 0 || !strings.Contains(out.String(), "100.0%") || !strings.Contains(out.String(), "no executable statements") {
		t.Fatalf("complete real profile = %d: %s %s", code, out.String(), stderr.String())
	}
}
