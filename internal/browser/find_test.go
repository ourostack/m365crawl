package browser

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ourostack/m365crawl/internal/errs"
)

func TestFindPreference(t *testing.T) {
	dir := t.TempDir()
	edge := filepath.Join(dir, "edge")
	chrome := filepath.Join(dir, "chrome")
	custom := filepath.Join(dir, "mybrowser")
	for _, p := range []string{edge, chrome, custom} {
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	oldC := candidates
	t.Cleanup(func() { candidates = oldC })
	have := map[Kind][]string{KindEdge: {filepath.Join(dir, "missing"), edge}, KindChrome: {chrome}}
	candidates = func(k Kind) []string { return have[k] }

	cases := []struct {
		name, pref, path string
		kind             Kind
	}{
		{"empty prefers edge", "", edge, KindEdge},
		{"edge", "edge", edge, KindEdge},
		{"mixed case chrome", " Chrome ", chrome, KindChrome},
		{"path", custom, custom, KindCustom},
	}
	for _, c := range cases {
		path, kind, err := Find(c.pref)
		if err != nil || path != c.path || kind != c.kind {
			t.Errorf("%s: got %q %q %v, want %q %q", c.name, path, kind, err, c.path, c.kind)
		}
	}

	have[KindEdge] = nil
	if path, kind, err := Find(""); err != nil || path != chrome || kind != KindChrome {
		t.Errorf("empty falls back to chrome: %q %q %v", path, kind, err)
	}
	have[KindChrome] = nil
	for _, pref := range []string{"", "edge", "chrome", filepath.Join(dir, "nope"), dir} {
		_, _, err := Find(pref)
		var coded *errs.Coded
		if !errors.As(err, &coded) || coded.Code != "transcripts_no_browser" || coded.Exit != 3 {
			t.Errorf("Find(%q) = %v, want transcripts_no_browser", pref, err)
		}
	}
}

func TestIsFile(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "f")
	if err := os.WriteFile(f, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if !isFile(f) || isFile(dir) || isFile(filepath.Join(dir, "missing")) {
		t.Fatal("isFile must be true for a file only")
	}
}
