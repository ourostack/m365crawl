// Package leveldb reads a copied Chromium LevelDB directory without depending on file order.
// The manifest decides which tables and logs are live; the record with the highest sequence
// number wins for each key. Keys are ordered bytewise regardless of the database's comparator.
package leveldb

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/syndtr/goleveldb/leveldb/util"
)

// Stats summarizes what Load read. Counts only; never contains keys or values.
type Stats struct {
	Tables, Logs, Keys, TruncatedLogTails int
	Comparator                            string
}

// DB is an in-memory, read-only view of a LevelDB directory.
type DB struct {
	entries store
	keys    []string // live keys, bytewise ascending
	stats   Stats
}

// Load reads dir (a copy; it is never written) and returns its live contents.
func Load(dir string) (*DB, error) {
	cur, err := readCurrent(dir)
	if err != nil {
		return nil, err
	}
	m, err := readManifest(dir, cur)
	if err != nil {
		return nil, err
	}

	all := store{}
	bp := util.NewBufferPool(1 << 16)
	tableNums := sortedNums(m.tables)
	for _, n := range tableNums {
		name, err := tableName(dir, n)
		if err != nil {
			return nil, err
		}
		if err := readTable(filepath.Join(dir, name), name, bp, all); err != nil {
			return nil, err
		}
	}

	logs, err := liveLogs(dir, m)
	if err != nil {
		return nil, err
	}
	stats := Stats{Tables: len(tableNums), Logs: len(logs), Comparator: m.comparator}
	for _, n := range logs {
		name := fmt.Sprintf("%06d.log", n)
		trunc, err := readLog(filepath.Join(dir, name), name, all)
		if err != nil {
			return nil, err
		}
		if trunc {
			stats.TruncatedLogTails++
		}
	}

	d := &DB{entries: all}
	for k, e := range all {
		if !e.deleted {
			d.keys = append(d.keys, k)
		}
	}
	sort.Strings(d.keys)
	stats.Keys = len(d.keys)
	d.stats = stats
	return d, nil
}

func sortedNums(set map[uint64]struct{}) []uint64 {
	out := make([]uint64, 0, len(set))
	for n := range set {
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// tableName finds the on-disk name for table n (.ldb, or the legacy .sst).
func tableName(dir string, n uint64) (string, error) {
	ldb := fmt.Sprintf("%06d.ldb", n)
	sst := fmt.Sprintf("%06d.sst", n)
	for _, name := range []string{ldb, sst} {
		_, err := os.Stat(filepath.Join(dir, name))
		if err == nil {
			return name, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("leveldb: stat %s: %w", name, err)
		}
	}
	return "", &MissingFileError{Name: ldb}
}

// liveLogs returns the log numbers to replay, oldest first: every log numbered at or above the
// manifest's log number, plus the previous log when the manifest names one and it exists.
func liveLogs(dir string, m *manifest) ([]uint64, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("leveldb: read dir: %w", err)
	}
	var out []uint64
	for _, e := range ents {
		base, ok := strings.CutSuffix(e.Name(), ".log")
		if !ok {
			continue
		}
		n, err := strconv.ParseUint(base, 10, 64)
		if err != nil {
			continue
		}
		if n >= m.logNumber || (m.prevLog != 0 && n == m.prevLog) {
			out = append(out, n)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

// Get returns the live value for key.
func (d *DB) Get(key []byte) ([]byte, bool) {
	e, ok := d.entries[string(key)]
	if !ok || e.deleted {
		return nil, false
	}
	return e.value, true
}

// Scan calls fn for every live key with the given prefix, in bytewise ascending order.
// It stops at, and returns, the first error from fn.
func (d *DB) Scan(prefix []byte, fn func(key, value []byte) error) error {
	p := string(prefix)
	start := sort.SearchStrings(d.keys, p)
	for _, k := range d.keys[start:] {
		if !strings.HasPrefix(k, p) {
			break
		}
		if err := fn([]byte(k), d.entries[k].value); err != nil {
			return err
		}
	}
	return nil
}

// Stats reports counts describing the load.
func (d *DB) Stats() Stats { return d.stats }
