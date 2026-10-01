package leveldb

import (
	"fmt"
	"sync"
)

// blockCacheSize is how many uncompressed data blocks a DB keeps for re-reading lazy values.
// Scan visits keys in bytewise order, which is not the tables' stored order, so neighbours
// often share a block but not always; a few blocks cover the common case.
const blockCacheSize = 16

type blockKey struct {
	table int
	off   uint64
}

// blockCache is a small least-recently-used cache of uncompressed blocks. Values handed out
// alias a cached block; eviction only drops the cache's reference, so they stay valid.
type blockCache struct {
	mu     sync.Mutex
	blocks map[blockKey][]byte
	order  []blockKey // least recently used first
	reads  int        // blocks read from disk, for tests
}

func (c *blockCache) get(tables []tableRef, table int, h blockHandle) ([]byte, error) {
	k := blockKey{table: table, off: h.off}
	c.mu.Lock()
	defer c.mu.Unlock()
	if b, ok := c.blocks[k]; ok {
		c.touch(k)
		return b, nil
	}
	if table < 0 || table >= len(tables) {
		return nil, fmt.Errorf("leveldb: table index %d out of range", table)
	}
	t := tables[table]
	f, size, err := openTable(t.path, t.name)
	if err != nil {
		return nil, err
	}
	b, err := readBlock(f, size, h)
	_ = f.Close()
	if err != nil {
		return nil, fmt.Errorf("leveldb: table %s: %w", t.name, err)
	}
	c.reads++
	if c.blocks == nil {
		c.blocks = map[blockKey][]byte{}
	}
	if len(c.order) >= blockCacheSize {
		delete(c.blocks, c.order[0])
		c.order = c.order[1:]
	}
	c.blocks[k] = b
	c.order = append(c.order, k)
	return b, nil
}

func (c *blockCache) touch(k blockKey) {
	for i, o := range c.order {
		if o == k {
			copy(c.order[i:], c.order[i+1:])
			c.order[len(c.order)-1] = k
			return
		}
	}
}
