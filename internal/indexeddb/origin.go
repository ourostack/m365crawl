// Package indexeddb reads Chromium's IndexedDB backing store (LevelDB key
// coding, Blink value envelopes and external blob files) from a copied
// profile directory and hands V8 payloads to internal/v8.
//
// Values that cannot be decoded are omissions, not failures. They are
// *OmissionError values whose Code is one of this complete list:
//
//	empty_value       the record has no value
//	unknown_envelope  the Blink envelope is not recognized (detail ends with the first 16 bytes in hex)
//	blob_missing      the external blob file or its blob entry is absent
//	bad_key           the record key could not be decoded (Record.Err; detail has the key in hex)
//	v8_unknown_tag, v8_host_object, v8_shared   passed through from internal/v8
//	v8_version        the V8 wire format version is unsupported
//	v8_malformed      any other V8 decoding error
package indexeddb

import (
	"github.com/ourostack/teamscrawl/internal/leveldb"
)

// kv is the read-only view of the LevelDB the package needs.
type kv interface {
	Get(key []byte) ([]byte, bool)
	Scan(prefix []byte, fn func(key, value []byte) error) error
}

// Origin is one origin's IndexedDB: a LevelDB directory plus its blob directory.
type Origin struct {
	kv      kv
	blobDir string
	stats   leveldb.Stats
}

// Open loads a copied <origin>.indexeddb.leveldb directory. blobDir is the
// sibling .blob directory; it may be empty or absent, in which case blob-backed
// values decode as blob_missing.
func Open(leveldbDir, blobDir string) (*Origin, error) {
	db, err := leveldb.Load(leveldbDir)
	if err != nil {
		return nil, err
	}
	return &Origin{kv: db, blobDir: blobDir, stats: db.Stats()}, nil
}

// Stats reports what the LevelDB reader loaded.
func (o *Origin) Stats() leveldb.Stats { return o.stats }
