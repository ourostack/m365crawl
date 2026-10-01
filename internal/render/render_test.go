package render

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mattn/go-runewidth"
)

var update = flag.Bool("update", false, "rewrite golden files")

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.WriteFile(path, //nolint:gosec // golden files are not secret
			[]byte(got), 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path) //nolint:gosec // path is a fixed testdata name
	if err != nil {
		t.Fatalf("read golden (run with -update): %v", err)
	}
	if string(want) != got {
		t.Errorf("%s mismatch\n--- want ---\n%s\n--- got ---\n%s", name, want, got)
	}
}

func both(t *testing.T, name string, fn func(w *bytes.Buffer, color bool)) {
	t.Helper()
	t.Setenv("COLORTERM", "truecolor")
	for _, color := range []bool{false, true} {
		var b bytes.Buffer
		fn(&b, color)
		suffix := "plain"
		if color {
			suffix = "color"
		} else if strings.Contains(b.String(), "\x1b") {
			t.Errorf("%s: color-off output contains escape", name)
		}
		golden(t, name+"."+suffix, b.String())
	}
}

func TestBanner(t *testing.T) {
	both(t, "banner", func(w *bytes.Buffer, c bool) { Banner(w, "Doctor", c) })
	both(t, "banner_nosub", func(w *bytes.Buffer, c bool) { Banner(w, "", c) })
}

func TestBannerWidthAndFallback(t *testing.T) {
	var b bytes.Buffer
	Banner(&b, "", false)
	for _, l := range strings.Split(b.String(), "\n") {
		if runewidth.StringWidth(l) > 60 {
			t.Errorf("line too wide: %q", l)
		}
	}
	for _, ct := range []string{"truecolor", "24bit"} {
		t.Setenv("COLORTERM", ct)
		b.Reset()
		Banner(&b, "x", true)
		if !strings.Contains(b.String(), "\x1b[38;2;98;100;167m") {
			t.Errorf("COLORTERM=%s: want truecolor purple", ct)
		}
	}
	t.Setenv("COLORTERM", "")
	b.Reset()
	Banner(&b, "x", true)
	if !strings.Contains(b.String(), "\x1b[38;5;61m") || strings.Contains(b.String(), "38;2;") {
		t.Errorf("want 256-color fallback")
	}
}

func fakeChecks() []Check {
	return []Check{
		{Name: "config", Detail: "/home/u/.config/teamscrawl/config.toml", Status: OK},
		{Name: "cache", Detail: "Edge profile Default, 2 accounts", Status: OK},
		{Name: "full disk access", Detail: "cache not readable", Fix: "grant Full Disk Access to your terminal", Status: Fail},
		{Name: "archive age", Detail: "last sync 3d ago", Fix: "run: teamscrawl sync", Status: Warn},
		{Name: "fts", Detail: "", Status: OK},
	}
}

func fakeSnap() *Snapshot {
	return &Snapshot{
		Pairs: [][2]string{{"accounts", "2"}, {"conversations", "41"}, {"messages", "18230"}, {"people", "97"}, {"activity", "312"}},
		Lines: [][2]string{{"last sync", "2026-09-27T10:00:00Z"}, {"archive age", "3d"}},
	}
}

func TestDoctor(t *testing.T) {
	both(t, "doctor", func(w *bytes.Buffer, c bool) { Doctor(w, "Doctor", fakeChecks(), fakeSnap(), c) })
	both(t, "doctor_nosnap", func(w *bytes.Buffer, c bool) { Doctor(w, "", fakeChecks()[:1], nil, c) })
	both(t, "doctor_snap_pairs_only", func(w *bytes.Buffer, c bool) {
		Doctor(w, "Doctor", nil, &Snapshot{Pairs: [][2]string{{"a", "1"}}}, c)
	})
}

func TestTable(t *testing.T) {
	cols := []string{"id", "name", "messages"}
	rows := [][]string{{"19:abc", "Project Alpha", "120"}, {"19:def", "", "7"}, {"19:x"}}
	both(t, "table", func(w *bytes.Buffer, c bool) { Table(w, cols, rows, c) })
	both(t, "table_empty", func(w *bytes.Buffer, c bool) { Table(w, cols, nil, c) })
}

func TestTableAlignsWideNames(t *testing.T) {
	var b bytes.Buffer
	Table(&b, []string{"name", "n"}, [][]string{{"plain", "1"}, {"日本語チャット", "2"}, {"party 🎉🎉", "3"}, {"é ñ", "4"}}, false)
	lines := strings.Split(strings.TrimRight(b.String(), "\n"), "\n")
	col := -1
	for i, l := range lines {
		if i == 1 {
			continue
		}
		// the last column starts at the same display column on every row
		idx := strings.LastIndex(l, "  ")
		pos := runewidth.StringWidth(l[:idx+2])
		if col == -1 {
			col = pos
		} else if pos != col {
			t.Errorf("row %d misaligned: got col %d want %d\n%s", i, pos, col, b.String())
		}
	}
}

func TestBlock(t *testing.T) {
	type inner struct {
		Count int `json:"count"`
	}
	ts := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	value := map[string]any{
		"status":     "ok",
		"db_path":    "/tmp/x.db",
		"enabled":    true,
		"disabled":   false,
		"empty":      "",
		"missing":    nil,
		"synced_at":  ts,
		"never":      time.Time{},
		"nested":     map[string]any{"a": 1, "b": 2.5},
		"tags":       []any{"x", "y"},
		"rows":       []any{map[string]any{"id": "1", "name": "日本"}, map[string]any{"id": "22", "name": "b", "extra": true}},
		"struct":     inner{Count: 3},
		"ptr":        &inner{Count: 4},
		"empty_map":  map[string]any{},
		"empty_list": []any{},
		"mixed":      []any{"a", map[string]any{"k": "v"}},
	}
	both(t, "block", func(w *bytes.Buffer, c bool) { Block(w, "Status", value, c) })
	both(t, "block_untitled_nil", func(w *bytes.Buffer, c bool) { Block(w, "", nil, c) })
	both(t, "block_scalar", func(w *bytes.Buffer, c bool) { Block(w, "Note", "hello", c) })
	both(t, "block_misc", func(w *bytes.Buffer, c bool) {
		var np *inner
		Block(w, "Misc", map[string]any{"np": np, "n": inner{}, "i": 7, "cx": 1 + 2i}, c)
		Block(w, "Top", inner{Count: 1}, c)
		Block(w, "List", []inner{{1}, {2}}, c)
		Block(w, "Empty", map[string]any{}, c)
	})
}

func TestColorEnabled(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close(); _ = w.Close() }()
	t.Setenv("NO_COLOR", "")
	if ColorEnabled(w, false) {
		t.Error("pipe must not enable color")
	}
	if ColorEnabled(nil, false) {
		t.Error("nil file must not enable color")
	}
	cases := []struct {
		tty     bool
		flag    bool
		env     string
		setEnv  bool
		want    bool
		comment string
	}{
		{true, false, "", false, true, "tty"},
		{true, false, "", true, true, "empty NO_COLOR ignored"},
		{true, false, "1", true, false, "NO_COLOR set"},
		{true, true, "", false, false, "flag"},
		{false, false, "", false, false, "non-tty"},
	}
	for _, c := range cases {
		lookup := func(string) (string, bool) { return c.env, c.setEnv && c.env != "" }
		if got := colorEnabled(c.tty, c.flag, lookup); got != c.want {
			t.Errorf("%s: got %v want %v", c.comment, got, c.want)
		}
	}
	t.Setenv("NO_COLOR", "1")
	if ColorEnabled(w, false) {
		t.Error("NO_COLOR must disable")
	}
}

func TestDoctorEmptySnapshotAndNormalizeDefault(t *testing.T) {
	var b bytes.Buffer
	Doctor(&b, "", nil, &Snapshot{}, false)
	if strings.Contains(b.String(), "Snapshot") {
		t.Errorf("empty snapshot should be omitted: %q", b.String())
	}
	if got := normalize(make(chan int)); got == nil {
		t.Error("unsupported kinds fall back to their printed form")
	}
	if got := normalize(struct {
		Hidden string `json:"-"`
		Name   string
	}{"x", "y"}); got.(map[string]any)["Name"] != "y" || len(got.(map[string]any)) != 1 {
		t.Errorf("json:- and untagged fields: %v", got)
	}
}

func TestTruncateAndDim(t *testing.T) {
	cases := []struct {
		in    string
		width int
		want  string
	}{
		{"hello world", 8, "hello..."},
		{"short", 8, "short"},
		{"anything", 0, "anything"},
		{"abcdef", 3, "abc"},
		{"日本語チャット", 7, "日本..."},
	}
	for _, c := range cases {
		if got := Truncate(c.in, c.width); got != c.want {
			t.Errorf("Truncate(%q,%d) = %q, want %q", c.in, c.width, got, c.want)
		}
	}
	if Dim("x", false) != "x" || Dim("x", true) != "\x1b[2mx\x1b[0m" {
		t.Error("Dim")
	}
}

func TestDoctorWidensNameColumnForLongCheckNames(t *testing.T) {
	long := "a-check-name-longer-than-sixteen"
	var b bytes.Buffer
	Doctor(&b, "", []Check{{Name: long, Status: OK, Detail: "fine"}, {Name: "short", Status: OK, Detail: "fine"}}, nil, false)
	var lines []string
	for _, l := range strings.Split(b.String(), "\n") {
		if strings.Contains(l, "fine") {
			lines = append(lines, l)
		}
	}
	if len(lines) != 2 {
		t.Fatalf("want 2 check lines, got %q", b.String())
	}
	if strings.Index(lines[0], "fine") != strings.Index(lines[1], "fine") {
		t.Errorf("details are not aligned:\n%s\n%s", lines[0], lines[1])
	}
	if strings.Index(lines[0], "fine") < len(long) {
		t.Errorf("name column was not widened to the long name:\n%s", lines[0])
	}
}

func TestNormalizeSkipsUnexportedAndDashFieldsAndFallsBackToFieldName(t *testing.T) {
	type row struct {
		Tagged   string `json:"tagged,omitempty"`
		Hidden   string `json:"-"`
		Untagged string
		private  string
	}
	got, ok := normalize(row{Tagged: "a", Hidden: "b", Untagged: "c", private: "d"}).(map[string]any)
	if !ok {
		t.Fatal("normalize(struct) is not a map")
	}
	want := map[string]any{"tagged": "a", "Untagged": "c"}
	if len(got) != len(want) || got["tagged"] != "a" || got["Untagged"] != "c" {
		t.Errorf("normalize = %v, want %v", got, want)
	}
}

func TestNormalizeNilPointerIsNilAndPointerToStructIsMap(t *testing.T) {
	type row struct {
		A string `json:"a"`
	}
	var nilRow *row
	if got := normalize(nilRow); got != nil {
		t.Errorf("normalize(nil pointer) = %v, want nil", got)
	}
	got, ok := normalize(&row{A: "x"}).(map[string]any)
	if !ok || got["a"] != "x" {
		t.Errorf("normalize(&row) = %v", got)
	}
}

func TestNormalizeDereferencesPointerToScalar(t *testing.T) {
	s := "hello"
	if got := normalize(&s); got != "hello" {
		t.Errorf("normalize(*string) = %v, want hello", got)
	}
}
