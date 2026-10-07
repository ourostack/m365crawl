// Command provenance inspects a generated teams-fixture tree and prints the
// Blink and V8 wire versions and envelope kind counts of the decodable stores
// as JSON. It exits non-zero unless every envelope kind was written.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ourostack/m365crawl/internal/indexeddb"
)

type result struct {
	BlinkVersions  []int          `json:"blink_versions"`
	V8Versions     []int          `json:"v8_versions"`
	EnvelopeCounts map[string]int `json:"envelope_counts"`
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: provenance <IndexedDB dir>")
		os.Exit(2)
	}
	if err := run(os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, "FIXTURE GENERATION FAILED:", err)
		os.Exit(1)
	}
}

func run(dir string) error {
	const name = "https_teams.microsoft.com_0.indexeddb"
	o, err := indexeddb.Open(filepath.Join(dir, name+".leveldb"), filepath.Join(dir, name+".blob"))
	if err != nil {
		return err
	}
	defer func() { _ = o.Close() }()
	dbs, err := o.Databases()
	if err != nil {
		return err
	}
	blink, v8s := map[int]bool{}, map[int]bool{}
	counts := map[string]int{}
	for _, db := range dbs {
		// Never inspect the decoy auth database.
		if !strings.HasPrefix(db.Name, "Teams:replychain-manager:") && !strings.HasPrefix(db.Name, "Teams:conversation-manager:") {
			continue
		}
		for _, st := range db.Stores {
			err := o.Records(db.ID, st.ID, func(r indexeddb.Record) error {
				if r.Err != nil {
					return r.Err
				}
				k := indexeddb.EnvelopeKind(r.Raw)
				counts[k]++
				if k == "plain" || k == "v21" {
					blink[int(r.Raw[1])] = true
					off := 2
					if k == "v21" {
						off = 15
					}
					if len(r.Raw) > off+1 && r.Raw[off] == 0xff {
						v8s[int(r.Raw[off+1])] = true
					}
				}
				return nil
			})
			if err != nil {
				return err
			}
		}
	}
	for _, k := range []string{"snappy", "blob", "v21"} {
		if counts[k] == 0 {
			return fmt.Errorf("no %s envelope written (counts %v); adjust value sizes", k, counts)
		}
	}
	if counts["unknown"] > 0 {
		return fmt.Errorf("unknown envelopes written: %v", counts)
	}
	res := result{EnvelopeCounts: counts}
	for v := range blink {
		res.BlinkVersions = append(res.BlinkVersions, v)
	}
	for v := range v8s {
		res.V8Versions = append(res.V8Versions, v)
	}
	sort.Ints(res.BlinkVersions)
	sort.Ints(res.V8Versions)
	return json.NewEncoder(os.Stdout).Encode(res)
}
