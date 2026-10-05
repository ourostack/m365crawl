package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ourostack/teamscrawl/internal/hxstore"
	"github.com/ourostack/teamscrawl/internal/hxstore/hxbuild"
)

func writeStore(t *testing.T, b *hxbuild.Builder) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "HxStore.copy")
	if err := os.WriteFile(p, b.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRunPrintsOnlyCounts(t *testing.T) {
	b := hxbuild.New(hxbuild.Options{})
	secret := hxbuild.NewObject(0x6b, 40, 40)
	secret.AppendString("Subject Fixture Secret")
	b.Block(hxbuild.Payload(secret, hxbuild.NewObject(0x6b, 40, 40), hxbuild.NewObject(0x1, 12, 12)))
	b.Block(hxbuild.Payload(hxbuild.NewObject(0x71, 20, 20)))
	bad := hxbuild.EncodeBlock(8, []byte("xx"))
	bad[0] ^= 1
	b.Raw(bad)
	path := writeStore(t, b)

	var out, errOut bytes.Buffer
	if code := run([]string{path}, &out, &errOut); code != 0 || errOut.Len() != 0 {
		t.Fatalf("code %d %s", code, errOut.String())
	}
	if strings.Contains(out.String(), "Secret") || strings.Contains(out.String(), "HxStore.copy") {
		t.Fatalf("output leaks content or the path: %s", out.String())
	}
	var r report
	if err := json.Unmarshal(out.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	if r.VersionByte != 'i' || r.PageSize != 4096 || r.BlocksFound != 3 || r.BlocksValid != 2 || r.BlocksRejected != 1 ||
		r.Rejected["header_crc"] != 1 || r.Objects != 4 || r.UnwalkedBytes != 0 || r.PairsOverflow != 0 || r.FileSize != int64(b.Len()) {
		t.Fatalf("%+v", r)
	}
	want := []pairCount{{1, 12, 1}, {0x6b, 40, 2}, {0x71, 20, 1}}
	if len(r.Pairs) != 3 || r.Pairs[0] != want[0] || r.Pairs[1] != want[1] || r.Pairs[2] != want[2] {
		t.Fatalf("%+v", r.Pairs)
	}
}

func TestRunEmptyStoreHasEmptyTables(t *testing.T) {
	path := writeStore(t, hxbuild.New(hxbuild.Options{}))
	var out bytes.Buffer
	if code := run([]string{path}, &out, &bytes.Buffer{}); code != 0 {
		t.Fatal(code)
	}
	for _, want := range []string{`"rejected_by_reason": {}`, `"objects_by_class_tag": []`} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %s in %s", want, out.String())
		}
	}
}

func TestPairOrdering(t *testing.T) {
	b := hxbuild.New(hxbuild.Options{})
	b.Block(hxbuild.Payload(
		hxbuild.NewObject(9, 13, 13), hxbuild.NewObject(9, 12, 12), hxbuild.NewObject(2, 12, 12)))
	var out bytes.Buffer
	run([]string{writeStore(t, b)}, &out, &bytes.Buffer{})
	var r report
	if err := json.Unmarshal(out.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	want := []pairCount{{2, 12, 1}, {9, 12, 1}, {9, 13, 1}}
	for i := range want {
		if r.Pairs[i] != want[i] {
			t.Fatalf("%+v", r.Pairs)
		}
	}
}

func TestRunFailures(t *testing.T) {
	dir := t.TempDir()
	junk := filepath.Join(dir, "junk")
	if err := os.WriteFile(junk, []byte("not a store at all, not a store at all, not a store at all, no!"), 0o600); err != nil {
		t.Fatal(err)
	}
	locked := filepath.Join(dir, "locked")
	if err := os.WriteFile(locked, nil, 0o000); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, path, want string
		found            string
	}{
		{"missing", filepath.Join(dir, "missing"), "not_found", ""},
		{"directory", dir, "not_a_regular_file", ""},
		{"junk", junk, "not_hxstore", ""},
		{"version", writeStore(t, hxbuild.New(hxbuild.Options{Version: 'j'})), "store_version", `"found": 106`},
		{"page size", writeStore(t, hxbuild.New(hxbuild.Options{PageSize: 8192})), "page_size", `"found": 8192`},
	}
	if os.Geteuid() != 0 {
		cases = append(cases, struct{ name, path, want, found string }{"permission", locked, "permission_denied", ""})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			if code := run([]string{tc.path}, &out, &errOut); code != 1 {
				t.Fatalf("code %d", code)
			}
			if !strings.Contains(out.String(), `"error": "`+tc.want+`"`) || !strings.Contains(out.String(), tc.found) {
				t.Fatalf("%s", out.String())
			}
			if strings.Contains(out.String(), dir) {
				t.Fatalf("path leaked: %s", out.String())
			}
		})
	}
}

func TestRunReadFailedAndUsage(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := fail(&out, errors.New("open /some/private/path: boom")); code != 1 ||
		!strings.Contains(out.String(), "read_failed") || strings.Contains(out.String(), "private") {
		t.Fatalf("%d %s", code, out.String())
	}
	for _, args := range [][]string{nil, {"a", "b"}} {
		out.Reset()
		errOut.Reset()
		if code := run(args, &out, &errOut); code != 2 || out.Len() != 0 || !strings.Contains(errOut.String(), "usage") {
			t.Fatalf("%v: %d %q", args, code, errOut.String())
		}
	}
}

// failWriter makes the JSON encoder fail.
type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("closed") }

func TestEmitWriteFailure(t *testing.T) {
	path := writeStore(t, hxbuild.New(hxbuild.Options{}))
	if code := run([]string{path}, failWriter{}, &bytes.Buffer{}); code != 3 {
		t.Fatal(code)
	}
}

func TestRunWalkError(t *testing.T) {
	orig := walk
	defer func() { walk = orig }()
	walk = func(*hxstore.Store) (hxstore.Stats, error) { return hxstore.Stats{}, errors.New("read failed at 12") }
	var out bytes.Buffer
	path := writeStore(t, hxbuild.New(hxbuild.Options{}))
	if code := run([]string{path}, &out, &bytes.Buffer{}); code != 1 || !strings.Contains(out.String(), "read_failed") {
		t.Fatalf("%d %s", code, out.String())
	}
}

func TestMainRunsRun(t *testing.T) {
	path := writeStore(t, hxbuild.New(hxbuild.Options{}))
	oldArgs, oldExit := os.Args, exit
	defer func() { os.Args, exit = oldArgs, oldExit }()
	got := -1
	exit = func(c int) { got = c }
	os.Args = []string{"hxstat", path}
	main()
	if got != 0 {
		t.Fatal(got)
	}
}
