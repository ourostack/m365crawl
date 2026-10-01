package leveldb

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/syndtr/goleveldb/leveldb/journal"
)

type dropCounter struct{ n int }

func (d *dropCounter) Drop(error) { d.n++ }

// readLog replays one write-ahead log into the store. It reports whether the log ended in a
// truncated or corrupt tail, which is expected when Teams was writing during the copy.
func readLog(path, name string, into *store) (truncated bool, err error) {
	f, err := os.Open(path) //nolint:gosec // path is built from a manifest-derived file name inside the caller-chosen directory
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, &MissingFileError{Name: name}
		}
		return false, fmt.Errorf("leveldb: open %s: %w", name, err)
	}
	defer func() { _ = f.Close() }()

	dc := &dropCounter{}
	jr := journal.NewReader(f, dc, false, true)
	for {
		r, nerr := jr.Next()
		if errors.Is(nerr, io.EOF) {
			break
		}
		if nerr != nil {
			truncated = true
			break
		}
		rec, rerr := io.ReadAll(r)
		if rerr != nil {
			truncated = true
			break
		}
		if !applyBatch(rec, into) {
			truncated = true
			break
		}
	}
	if dc.n > 0 {
		truncated = true
	}
	return truncated, nil
}

// applyBatch applies a write batch: 8-byte sequence, 4-byte count, then records. It returns false
// when the batch is malformed; records decoded before the defect are kept.
func applyBatch(b []byte, into *store) bool {
	if len(b) < 12 {
		return false
	}
	seq := binary.LittleEndian.Uint64(b)
	count := binary.LittleEndian.Uint32(b[8:])
	d := &decoder{b: b[12:]}
	for i := uint32(0); i < count; i++ {
		if len(d.b) == 0 {
			return false
		}
		typ := d.b[0]
		d.b = d.b[1:]
		key := d.bytes()
		if d.err != nil {
			return false
		}
		switch typ {
		case typeValue:
			val := d.bytes()
			if d.err != nil {
				return false
			}
			into.put(key, seq+uint64(i), false, val, nil)
		case typeDeletion:
			into.put(key, seq+uint64(i), true, nil, nil)
		default:
			return false
		}
	}
	return true
}
