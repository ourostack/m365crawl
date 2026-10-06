package hxfixture

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/ourostack/teamscrawl/internal/hxstore"
)

// committedDir is the committed fixture, from this package's directory.
var committedDir = filepath.Join("..", "..", "..", filepath.FromSlash(Dir))

func byName(t *testing.T) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	for _, f := range Build() {
		out[f.Name] = f.Data
	}
	return out
}

// TestFixtureIsReproducible rebuilds the fixture and compares it, byte for
// byte, with what is committed, and checks that nothing else is committed.
func TestFixtureIsReproducible(t *testing.T) {
	first, second := Build(), Build()
	if len(first) != len(second) {
		t.Fatal("two builds differ in length")
	}
	onDisk := map[string]bool{}
	err := filepath.WalkDir(committedDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(committedDir, path)
		onDisk[filepath.ToSlash(rel)] = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for i, f := range first {
		if f.Name != second[i].Name || !bytes.Equal(f.Data, second[i].Data) {
			t.Fatalf("%s differs between two builds", f.Name)
		}
		have, err := os.ReadFile(filepath.Join(committedDir, filepath.FromSlash(f.Name))) //nolint:gosec // a fixed fixture path
		if err != nil || !bytes.Equal(have, f.Data) {
			t.Fatalf("%s: committed copy differs from the generator (run go run ./scripts/hxfixture): %v", f.Name, err)
		}
		delete(onDisk, f.Name)
	}
	if len(onDisk) != 0 {
		t.Fatalf("files in %s the generator does not make: %v", Dir, onDisk)
	}
}

func TestBuildIsSortedAndProvenanceMatches(t *testing.T) {
	files := Build()
	var names []string
	for _, f := range files[:len(files)-1] {
		names = append(names, f.Name)
	}
	if !sort.StringsAreSorted(names) || files[len(files)-1].Name != "PROVENANCE.json" {
		t.Fatal(names)
	}
	var doc provenanceDoc
	if err := json.Unmarshal(files[len(files)-1].Data, &doc); err != nil {
		t.Fatal(err)
	}
	all := sha256.New()
	for _, f := range files[:len(files)-1] {
		sum := sha256.Sum256(f.Data)
		if doc.Files[f.Name] != hex.EncodeToString(sum[:]) {
			t.Fatalf("hash of %s", f.Name)
		}
		_, _ = fmt.Fprintf(all, "%s\n%x\n", f.Name, sum)
	}
	if doc.Hash != hex.EncodeToString(all.Sum(nil)) || doc.GeneratorVersion != GeneratorVersion || len(doc.Files) != len(files)-1 {
		t.Fatalf("%+v", doc)
	}
}

func open(t *testing.T, data []byte) (*hxstore.Store, error) {
	t.Helper()
	return hxstore.Open(bytes.NewReader(data), int64(len(data)))
}

func walk(t *testing.T, data []byte, fn func(hxstore.Object)) hxstore.Stats {
	t.Helper()
	s, err := open(t, data)
	if err != nil {
		t.Fatal(err)
	}
	st, err := s.Walk(context.Background(), hxstore.WalkOptions{}, func(o hxstore.Object) error {
		if fn != nil {
			fn(o)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// TestMainStoreCensus checks the reader's census of the main store against what
// the generator wrote, with no object reached after unknown bytes.
func TestMainStoreCensus(t *testing.T) {
	st := walk(t, byName(t)["HxStore.hxd"], nil)
	want := map[Pair]int{{0x6b, 0x455}: 14, {0x6c, 0x348}: 8, {0x71, 20}: 3, {0x55, 32}: 2, {0xd7, 40}: 1}
	if len(MainCounts()) != len(want) {
		t.Fatalf("%v", MainCounts())
	}
	for p, n := range want {
		if MainCounts()[p] != n || st.Pairs[hxstore.Pair{Class: p.Class, Tag: p.Tag}] != n {
			t.Fatalf("%v: generator %d, reader %d, want %d", p, MainCounts()[p], st.Pairs[hxstore.Pair{Class: p.Class, Tag: p.Tag}], n)
		}
	}
	if st.BlocksValid != 4 || st.BlocksRejected() != 0 || st.Objects != 28 || st.ObjectsResynced != 0 || st.UnwalkedBytes != 0 {
		t.Fatalf("%+v", st)
	}
}

// TestEventsReadBack reads the fixture's events through the reader, field by
// field, so a wrong offset in the builder disagrees with the reader's own
// accessors on the envelope and the scalar words.
func TestEventsReadBack(t *testing.T) {
	var events, stubs, details int
	ids := map[string]int{}
	walk(t, byName(t)["HxStore.hxd"], func(o hxstore.Object) {
		switch o.Class {
		case 0x6c:
			details++
		case 0x6b:
			area, _ := o.U32(104)
			if o.Len() == 1554 {
				stubs++
				if area != 445 {
					t.Fatalf("stub area %d", area)
				}
				return
			}
			events++
			if area != 813 && area != 812 {
				t.Fatalf("area one %d", area)
			}
			idOff, _ := o.U32(820)
			idLen, _ := o.U32(824)
			start := 1109 + int(idOff)
			// The id is the upper-case hex of a 56-byte global object id, stored as
			// UTF-16LE text with a terminator; the length word counts those bytes.
			if idLen != 226 {
				t.Fatalf("id is %d bytes", idLen)
			}
			var text []byte // ASCII hex, so the low byte of each UTF-16 unit
			for i := start; i+1 < start+int(idLen) && o.Raw[i] != 0; i += 2 {
				text = append(text, o.Raw[i])
			}
			if _, err := hex.DecodeString(string(text)); err != nil || len(text) != 112 {
				t.Fatalf("id text %q", text)
			}
			ids[string(text)]++
		}
	})
	if events != 12 || stubs != 2 || details != 8 {
		t.Fatalf("events %d stubs %d details %d", events, stubs, details)
	}
	if len(ids) != 10 {
		t.Fatalf("%d distinct ids", len(ids))
	}
	versioned := 0
	for id, n := range ids {
		if strings.HasSuffix(id, "464958545552452D4556542D30303036") && n == 3 { // FIXTURE-EVT-0006
			versioned++
		}
	}
	if versioned != 1 {
		t.Fatal("the versioned event must appear three times")
	}
}

func TestGuardStores(t *testing.T) {
	files := byName(t)
	var ev hxstore.ErrStoreVersion
	var ep hxstore.ErrPageSize
	if _, err := open(t, files["store-version-j.hxd"]); !errors.As(err, &ev) || ev.Found != 'j' {
		t.Fatal(err)
	}
	if _, err := open(t, files["store-page-8192.hxd"]); !errors.As(err, &ep) || ep.Found != 8192 {
		t.Fatal(err)
	}
	for _, n := range []string{"store-not-hxstore.hxd", "store-short.hxd"} {
		if _, err := open(t, files[n]); !errors.Is(err, hxstore.ErrNotHxStore) {
			t.Fatalf("%s: %v", n, err)
		}
	}
	if st := walk(t, files["store-empty.hxd"], nil); st.BlocksFound != 0 || st.Objects != 0 {
		t.Fatalf("%+v", st)
	}
	// A new tag mixed with the known one: two events at 0x455, one at 0x456.
	st := walk(t, files["store-new-tag-mixed.hxd"], nil)
	if st.Pairs[hxstore.Pair{Class: 0x6b, Tag: 0x455}] != 2 || st.Pairs[hxstore.Pair{Class: 0x6b, Tag: 0x456}] != 1 || st.ObjectsResynced != 0 {
		t.Fatalf("%+v", st.Pairs)
	}
	st = walk(t, files["store-unknown-classes-only.hxd"], nil)
	if st.Objects != 6 || st.Pairs[hxstore.Pair{Class: 0x6b, Tag: 0x455}] != 0 {
		t.Fatalf("%+v", st.Pairs)
	}
	// Low coverage: objects cover well under 80% of the payload.
	st = walk(t, files["store-low-coverage.hxd"], nil)
	if st.UnwalkedBytes*100 < st.PayloadBytes*80 {
		t.Fatalf("unwalked %d of %d", st.UnwalkedBytes, st.PayloadBytes)
	}
	// Damaged: one of each rejection, a valid block hidden inside a torn one,
	// and one payload with no object.
	st = walk(t, files["store-damaged-blocks.hxd"], nil)
	wantRejected := map[string]int{
		hxstore.RejectHeaderCRC: 1, hxstore.RejectPayloadCRC: 1, hxstore.RejectOversize: 1, hxstore.RejectTypeOther: 1,
		hxstore.RejectHeaderUnknown: 1, hxstore.RejectInflate: 1, hxstore.RejectTruncated: 1,
	}
	if st.BlocksFound != 10 || st.BlocksValid != 3 || st.PayloadsNoObject != 1 || st.Objects != 2 || len(st.Rejected) != len(wantRejected) {
		t.Fatalf("%+v", st)
	}
	for k, n := range wantRejected {
		if st.Rejected[k] != n {
			t.Fatalf("%s: %d", k, st.Rejected[k])
		}
	}
	// The second profile's store is a valid store of its own.
	if st := walk(t, files["profile-two/HxStore.hxd"], nil); st.Objects != 4 || st.BlocksValid != 1 {
		t.Fatalf("%+v", st)
	}
}

var (
	asciiRun = regexp.MustCompile(`[ -~]{8,}`)
	wideRun  = regexp.MustCompile(`(?:[ -~]\x00){4,}`)
	hexText  = regexp.MustCompile(`^(?:[0-9A-F]{2})+$`)
	atSign   = regexp.MustCompile(`@([^ <>"]*)`)
	urlText  = regexp.MustCompile(`https?://([^/ "<]*)`)
)

// textRuns returns the printable runs in b, read as ASCII and as UTF-16LE at
// either alignment.
func textRuns(b []byte) []string {
	var out []string
	for _, r := range asciiRun.FindAll(b, -1) {
		out = append(out, string(r))
	}
	for _, r := range wideRun.FindAll(b, -1) {
		out = append(out, strings.ReplaceAll(string(r), "\x00", ""))
	}
	return out
}

// realLooking explains why text could be real data, or returns "".
func realLooking(s string) string {
	if hexText.MatchString(s) {
		raw, _ := hex.DecodeString(s)
		if bytes.Contains(raw, []byte("FIXTURE")) {
			return ""
		}
		return "hex text without FIXTURE in it"
	}
	for _, m := range atSign.FindAllStringSubmatch(s, -1) {
		if m[1] != "example.invalid" {
			return "an address outside example.invalid"
		}
	}
	for _, m := range urlText.FindAllStringSubmatch(s, -1) {
		if m[1] != "example.invalid" {
			return "a link outside example.invalid"
		}
	}
	if !strings.Contains(strings.ToLower(s), "fixture") {
		return "text that does not say it is a fixture"
	}
	return ""
}

// TestFixtureHasNoRealLookingText reads every printable run in every fixture
// file, as written and as read back through the reader, and fails on any that
// could be real: an address or link outside example.invalid, an id without the
// word FIXTURE in it, or text that does not announce itself as a fixture.
func TestFixtureHasNoRealLookingText(t *testing.T) {
	for _, f := range Build() {
		if strings.HasSuffix(f.Name, ".json") || strings.HasSuffix(f.Name, ".txt") {
			continue // checked by TestTextFilesCarryNoMachineData
		}
		// Literal text outside any object (a payload with no object) is only
		// visible in the raw bytes, and only where the block is not compressed.
		// Elsewhere raw bytes are checksums, ticks and compressed data, so
		// everything else is read back through the reader.
		var runs []string
		if f.Name == "store-damaged-blocks.hxd" {
			runs = textRuns(f.Data[hxstore.FileHeaderSize:])
		}
		if s, err := open(t, headerFixed(f.Data)); err == nil {
			if _, err := s.Walk(context.Background(), hxstore.WalkOptions{}, func(o hxstore.Object) error {
				runs = append(runs, textRuns(o.Raw)...)
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		}
		for _, r := range runs {
			if why := realLooking(r); why != "" {
				t.Errorf("%s: %s (%d characters) %q", f.Name, why, len(r), r)
			}
		}
	}
}

// headerFixed returns data with the file header set to the known version and
// page size, so the guard stores whose header is wrong on purpose can still be
// read through the reader. Data that is not a store is returned as it is.
func headerFixed(data []byte) []byte {
	if len(data) < hxstore.FileHeaderSize || string(data[:8]) != "Nostromo" {
		return data
	}
	out := append([]byte(nil), data...)
	out[8] = 'i'
	binary.LittleEndian.PutUint64(out[0x38:], hxstore.KnownPageSize)
	return out
}

func TestRealLookingDetector(t *testing.T) {
	for in, want := range map[string]bool{
		"Fixture plain event":                    false,
		"fixture.a@example.invalid":              false,
		"https://example.invalid/fixture/join/1": false,
		"a.person@contoso.com Fixture":           true,
		"https://teams.example.com/meet Fixture": true,
		"Weekly planning":                        true,
		"464958545552452D31":                     false,
		"4142434445":                             true,
		"<p>Fixture body</p> mail me at x@y.org": true,
	} {
		if got := realLooking(in) != ""; got != want {
			t.Errorf("%q: %v", in, got)
		}
	}
	if len(textRuns([]byte("ab\x00\x01W\x00o\x00r\x00d\x00s\x00 plain run"))) != 2 {
		t.Fatal("run extraction")
	}
}

func TestTextFilesCarryNoMachineData(t *testing.T) {
	files := byName(t)
	for _, n := range []string{"PROVENANCE.json", "twin-candidates.txt"} {
		s := string(files[n])
		for _, bad := range []string{"@", "/Users", "/home", "C:\\", "http", ".local", "localhost"} {
			if strings.Contains(s, bad) {
				t.Errorf("%s contains %q", n, bad)
			}
		}
	}
}

func TestEveryTwinIsAnEventID(t *testing.T) {
	lines := strings.Split(strings.TrimSpace(string(byName(t)["twin-candidates.txt"])), "\n")
	var ids []string
	for _, l := range lines {
		if !strings.HasPrefix(l, "#") {
			ids = append(ids, l)
		}
	}
	if len(ids) != 5 {
		t.Fatal(ids)
	}
	for _, id := range ids {
		if len(id) != 112 || !strings.HasPrefix(id, "040000008200E00074C5B7101A82E008") {
			t.Fatal(id)
		}
	}
}

// TestTwinsAreInTeamsFixture checks that every twin id appears, as the hex text
// Teams stores, in the committed Teams fixture: the two fixtures describe the
// same meetings.
func TestTwinsAreInTeamsFixture(t *testing.T) {
	teamsDir := filepath.Join("..", "..", "..", "testdata", "teams-fixture")
	var all [][]byte
	err := filepath.WalkDir(teamsDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(path) //nolint:gosec // walking the committed Teams fixture
		all = append(all, b)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(byName(t)["twin-candidates.txt"])), "\n") {
		if strings.HasPrefix(line, "#") {
			continue
		}
		// The store holds the id in upper case and Teams in lower case, so the Teams fixture must
		// hold the lower-case form and not the store's.
		found := false
		for _, b := range all {
			if bytes.Contains(b, []byte(line)) {
				t.Errorf("twin %s is in the Teams fixture in the store's case", line)
			}
			if bytes.Contains(b, []byte(strings.ToLower(line))) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("twin %s is not in the Teams fixture", line)
		}
	}
}

func TestMainEventsMatchTheStore(t *testing.T) {
	cases := MainEvents()
	if len(cases) != MainCounts()[Pair{0x6b, 0x455}] {
		t.Fatalf("%d cases, %d objects", len(cases), MainCounts()[Pair{0x6b, 0x455}])
	}
	stubs := 0
	for _, c := range cases {
		if c.Stub {
			stubs++
		}
	}
	if stubs != 2 {
		t.Fatal(stubs)
	}
}
