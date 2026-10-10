package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("synthetic read failure") }

func TestCoverageProfileModesAndReadFailures(t *testing.T) {
	for _, mode := range []string{"set", "count", "atomic"} {
		body := "mode: " + mode + "\nsynthetic.test/coverage/a.go:1.1,1.2 1 2\nsynthetic.test/coverage/a.go:1.1,1.2 1 3\n"
		hits, err := parseProfile(strings.NewReader(body))
		if err != nil || hits["synthetic.test/coverage/a.go"][block{1, 1, 1, 2, 1}] != 5 {
			t.Fatalf("mode %s: %v %v", mode, hits, err)
		}
	}
	if _, err := parseProfile(io.MultiReader(strings.NewReader("mode: count\n"), failingReader{})); err == nil {
		t.Fatal("profile read failure accepted")
	}
	if _, err := parseProfile(strings.NewReader("mode: count\n")); err == nil {
		t.Fatal("profile with no blocks accepted")
	}
}

func TestCoverageSourceInventoryIncludesMethodsAndInitializers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.go")
	body := "package sample\ntype T struct{}\nfunc (t *T) Method() int { return 1 }\nvar initializer = func() int { return 2 }\nvar declaration int\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	fns, err := sourceFunctions(path)
	if err != nil || len(fns) != 2 || fns[0].name != "T.Method" || fns[1].name != "initializer:initializer" {
		t.Fatalf("method/initializer inventory = %v %v", fns, err)
	}
	if _, err := sourceBlocks(filepath.Join(t.TempDir(), "missing.go")); err == nil {
		t.Fatal("instrumentation source failure accepted")
	}
	t.Setenv("TMP", path)
	t.Setenv("TEMP", path)
	t.Setenv("TMPDIR", path)
	if _, err := sourceBlocks(path); err == nil {
		t.Fatal("scratch creation failure accepted")
	}
}

func TestNativeScopeRequiresProvidersAndCommonMailFunctions(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	scope, err := nativeScope(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{
		"internal/cli/mail_source.go", "internal/cli/mail.go", "internal/cli/doctor.go",
		"internal/store/mail_source_ids.go", "internal/store/mail_schema.go", "internal/store/mail_owa.go",
		"internal/outlookowa/collect.go", "internal/outlookmail/message.go", "internal/syncer/outlook_owa.go",
	} {
		if _, ok := scope[file]; !ok {
			t.Errorf("required feature scope missing %s", file)
		}
	}
	var out, stderr bytes.Buffer
	if code := run([]string{"-unknown"}, &out, &stderr); code != 2 {
		t.Fatalf("invalid flag exit = %d", code)
	}
	out.Reset()
	stderr.Reset()
	if code := run([]string{"-root=" + t.TempDir(), "-profile=missing.cover"}, &out, &stderr); code != 1 || stderr.Len() == 0 {
		t.Fatalf("missing package scope exit = %d: %s", code, stderr.String())
	}
	out.Reset()
	stderr.Reset()
	if code := run([]string{"-root=" + root, "-profile=" + filepath.Join(t.TempDir(), "missing.cover")}, &out, &stderr); code != 1 || !strings.Contains(out.String(), "missing profile") {
		t.Fatalf("missing profile exit = %d: %s %s", code, out.String(), stderr.String())
	}
}
