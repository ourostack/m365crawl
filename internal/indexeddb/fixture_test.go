package indexeddb

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/ourostack/teamscrawl/internal/v8"
)

var updateGolden = flag.Bool("update", false, "rewrite testdata/teams-fixture/expected/*.json")

const (
	fixtureRoot   = "../../testdata/teams-fixture"
	fixtureOrigin = "https_teams.microsoft.com_0.indexeddb"
	goldenInline  = 8192 // records whose canonical form is larger are pinned by hash only
)

func openFixture(t *testing.T) *Origin {
	t.Helper()
	dir := filepath.Join(fixtureRoot, "EBWebView", "WV2Profile_fixture", "IndexedDB")
	o, err := Open(filepath.Join(dir, fixtureOrigin+".leveldb"), filepath.Join(dir, fixtureOrigin+".blob"))
	if err != nil {
		t.Fatalf("Open fixture: %v", err)
	}
	return o
}

// wantedStores lists the decodable (database, store) pairs. The decoy auth
// database is never touched.
func wantedStores(t *testing.T, o *Origin, manager, store string) []struct {
	DB    Database
	Store Store
} {
	t.Helper()
	dbs, err := o.Databases()
	if err != nil {
		t.Fatalf("Databases: %v", err)
	}
	var out []struct {
		DB    Database
		Store Store
	}
	for _, db := range dbs {
		if !strings.HasPrefix(db.Name, "Teams:"+manager+":react-web-client:") {
			continue
		}
		for _, s := range db.Stores {
			if s.Name == store {
				out = append(out, struct {
					DB    Database
					Store Store
				}{db, s})
			}
		}
	}
	return out
}

func TestFixtureEnvelopeCoverage(t *testing.T) {
	o := openFixture(t)
	kinds := map[string]int{}
	total := 0
	for _, ms := range [][2]string{{"replychain-manager", "replychains-2"}, {"conversation-manager", "conversations"}} {
		found := wantedStores(t, o, ms[0], ms[1])
		if len(found) != 2 {
			t.Fatalf("%s/%s: want 2 accounts, got %d", ms[0], ms[1], len(found))
		}
		for _, f := range found {
			err := o.Records(f.DB.ID, f.Store.ID, func(r Record) error {
				if r.Err != nil {
					t.Errorf("bad key in %s: %v", f.DB.Name, r.Err)
					return nil
				}
				total++
				kinds[EnvelopeKind(r.Raw)]++
				if _, err := o.Decode(f.DB.ID, r.Raw); err != nil {
					t.Errorf("omission in %s: %v", f.DB.Name, err)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, k := range []string{"snappy", "blob", "v21"} {
		if kinds[k] == 0 {
			t.Errorf("no %s envelope in fixture: %v", k, kinds)
		}
	}
	if kinds["unknown"] != 0 {
		t.Errorf("unknown envelopes: %v", kinds)
	}
	if total < 80 {
		t.Errorf("fixture too small: %d records", total)
	}

	var prov struct {
		Browser       string `json:"browser"`
		BlinkVersions []int  `json:"blink_versions"`
		V8Versions    []int  `json:"v8_versions"`
	}
	b, err := os.ReadFile(filepath.Join(fixtureRoot, "PROVENANCE.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &prov); err != nil {
		t.Fatal(err)
	}
	if prov.Browser != "msedge" || len(prov.BlinkVersions) == 0 || len(prov.V8Versions) == 0 {
		t.Fatalf("PROVENANCE incomplete: %+v", prov)
	}
	// Every plain or v21 record carries a Blink version listed in PROVENANCE.
	listed := map[int]bool{}
	for _, v := range prov.BlinkVersions {
		listed[v] = true
	}
	for _, f := range wantedStores(t, o, "replychain-manager", "replychains-2") {
		_ = o.Records(f.DB.ID, f.Store.ID, func(r Record) error {
			if k := EnvelopeKind(r.Raw); (k == "v21" || k == "plain") && !listed[int(r.Raw[1])] {
				t.Errorf("Blink version %d not in PROVENANCE %v", r.Raw[1], prov.BlinkVersions)
			}
			return nil
		})
	}
}

func goldenFor(t *testing.T, o *Origin, manager, store string) []byte {
	t.Helper()
	type rec struct {
		Key       string          `json:"key"`
		Envelope  string          `json:"envelope"`
		Bytes     int             `json:"canonical_bytes"`
		SHA256    string          `json:"canonical_sha256"`
		Canonical json.RawMessage `json:"canonical,omitempty"`
	}
	out := map[string][]rec{}
	for _, f := range wantedStores(t, o, manager, store) {
		var recs []rec
		err := o.Records(f.DB.ID, f.Store.ID, func(r Record) error {
			if r.Err != nil {
				return r.Err
			}
			v, err := o.Decode(f.DB.ID, r.Raw)
			if err != nil {
				return err
			}
			c, err := v8.Canonical(v)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(c)
			e := rec{Key: fmt.Sprint(r.Key), Envelope: EnvelopeKind(r.Raw), Bytes: len(c), SHA256: hex.EncodeToString(sum[:])}
			if len(c) <= goldenInline {
				e.Canonical = c
			}
			recs = append(recs, e)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		sort.Slice(recs, func(i, j int) bool { return recs[i].Key < recs[j].Key })
		out[f.DB.Name] = recs
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(out); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestFixtureGolden(t *testing.T) {
	o := openFixture(t)
	for name, ms := range map[string][2]string{
		"replychains":   {"replychain-manager", "replychains-2"},
		"conversations": {"conversation-manager", "conversations"},
	} {
		got := goldenFor(t, o, ms[0], ms[1])
		path := filepath.Join(fixtureRoot, "expected", name+".json")
		if *updateGolden {
			if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, got, 0o600); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(path) //nolint:gosec // fixed fixture path
		if err != nil {
			t.Fatalf("%s: %v (run go test ./internal/indexeddb -run TestFixtureGolden -update)", name, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s golden mismatch (rerun with -update to review the diff)", name)
		}
	}
}
