package leveldb

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/syndtr/goleveldb/leveldb/comparer"
	lerrors "github.com/syndtr/goleveldb/leveldb/errors"
	"github.com/syndtr/goleveldb/leveldb/opt"
	"github.com/syndtr/goleveldb/leveldb/storage"
	"github.com/syndtr/goleveldb/leveldb/table"
	"github.com/syndtr/goleveldb/leveldb/util"
)

const (
	typeDeletion = 0
	typeValue    = 1
)

// ErrUnsupportedCompression is returned when a table block uses a compression type other than none or snappy.
var ErrUnsupportedCompression = errors.New("leveldb: unsupported block compression")

// MissingFileError reports a file named by the manifest (or CURRENT) that is absent from the directory.
type MissingFileError struct{ Name string }

func (e *MissingFileError) Error() string { return "leveldb: missing file " + e.Name }

// entry is the newest known record for a user key.
type entry struct {
	seq     uint64
	deleted bool
	value   []byte
}

type store map[string]entry

// put records a version of key if it is newer than what is held.
func (s store) put(key []byte, seq uint64, deleted bool, value []byte) {
	if cur, ok := s[string(key)]; ok && cur.seq >= seq {
		return
	}
	e := entry{seq: seq, deleted: deleted}
	if !deleted {
		e.value = append([]byte(nil), value...)
	}
	s[string(key)] = e
}

func splitInternalKey(ik []byte) (user []byte, seq uint64, typ byte, ok bool) {
	if len(ik) < 8 {
		return nil, 0, 0, false
	}
	t := binary.LittleEndian.Uint64(ik[len(ik)-8:])
	return ik[:len(ik)-8], t >> 8, byte(t & 0xff), true
}

func readTable(path, name string, bp *util.BufferPool, into store) error {
	f, err := os.Open(path) //nolint:gosec // path is built from a manifest-derived file name inside the caller-chosen directory
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &MissingFileError{Name: name}
		}
		return fmt.Errorf("leveldb: open %s: %w", name, err)
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return fmt.Errorf("leveldb: stat %s: %w", name, err)
	}
	r, err := table.NewReader(f, st.Size(), storage.FileDesc{}, nil, bp, &opt.Options{Comparer: comparer.DefaultComparer})
	if err != nil {
		return tableError(name, err)
	}
	defer r.Release()
	it := r.NewIterator(nil, nil)
	defer it.Release()
	for it.Next() {
		user, seq, typ, ok := splitInternalKey(it.Key())
		if !ok {
			return fmt.Errorf("leveldb: table %s: internal key too short", name)
		}
		into.put(user, seq, typ == typeDeletion, it.Value())
	}
	if err := it.Error(); err != nil {
		return tableError(name, err)
	}
	return nil
}

func tableError(name string, err error) error {
	var ce *lerrors.ErrCorrupted
	if errors.As(err, &ce) && strings.Contains(err.Error(), "unknown compression type") {
		return fmt.Errorf("leveldb: table %s: %w: %s", name, ErrUnsupportedCompression, err.Error())
	}
	return fmt.Errorf("leveldb: table %s: %w", name, err)
}
