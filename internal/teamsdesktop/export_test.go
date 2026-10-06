package teamsdesktop

import (
	"sort"
	"testing"

	"github.com/ourostack/teamscrawl/internal/indexeddb"
	"github.com/ourostack/teamscrawl/internal/leveldb"
)

// FakeRecord is one record of a scripted generic object store: the key and the value bytes.
type FakeRecord struct{ Key, Value string }

// InstallFakeGeneric makes the generic read see the databases dbs returns each time it takes its
// census, instead of the snapshot: database name -> the records of its one object store, "store",
// in order. Typed reads are not affected. It is for tests of other packages that drive the whole
// sync (an external test package sees this file because it is compiled with the package's tests).
func InstallFakeGeneric(t *testing.T, dbs func() map[string][]FakeRecord) {
	t.Helper()
	oc, oo := censusFn, openBatch
	t.Cleanup(func() { censusFn, openBatch = oc, oo })
	current := map[int64][]indexeddb.Record{}
	censusFn = func(string) (map[int64]int64, []indexeddb.Database, error) {
		scripted := dbs()
		names := make([]string, 0, len(scripted))
		for n := range scripted {
			names = append(names, n)
		}
		sort.Strings(names)
		held := map[int64]int64{}
		var out []indexeddb.Database
		current = map[int64][]indexeddb.Record{}
		for i, n := range names {
			id := int64(i + 1)
			held[id] = 1
			out = append(out, indexeddb.Database{ID: id, Name: n, Stores: []indexeddb.Store{{ID: 1, Name: "store"}}})
			for _, r := range scripted[n] {
				current[id] = append(current[id], indexeddb.Record{Key: r.Key, Raw: []byte(r.Value)})
			}
		}
		return held, out, nil
	}
	openBatch = func(string, func(int64, int64) bool) (genericOrigin, error) { return scriptedOrigin(current), nil }
}

type scriptedOrigin map[int64][]indexeddb.Record

func (o scriptedOrigin) Records(dbID, _ int64, fn func(indexeddb.Record) error) error {
	for _, r := range o[dbID] {
		if err := fn(r); err != nil {
			return err
		}
	}
	return nil
}
func (scriptedOrigin) Decode(_ int64, raw []byte) (any, error)     { return string(raw), nil }
func (scriptedOrigin) DecodePayload(payload []byte) (any, error)   { return string(payload), nil }
func (scriptedOrigin) Payload(_ int64, raw []byte) ([]byte, error) { return raw, nil }
func (scriptedOrigin) Stats() leveldb.Stats                        { return leveldb.Stats{} }
func (scriptedOrigin) Close() error                                { return nil }
