package teamsdesktop

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestRealCache runs discovery, snapshot and read against the live Teams cache. It needs Full
// Disk Access and TEAMSCRAWL_REAL_CACHE=1, and logs counts and timings only, never names, ids
// or content, because the cache is private data.
func TestRealCache(t *testing.T) {
	if os.Getenv("TEAMSCRAWL_REAL_CACHE") != "1" {
		t.Skip("set TEAMSCRAWL_REAL_CACHE=1 to run against the real Teams cache")
	}
	srcs, other, err := Discover(DefaultRoot())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("sources=%d other_origins=%d", len(srcs), len(other))
	for i, s := range srcs {
		start := time.Now()
		fp, err := FingerprintOf(s)
		if err != nil {
			t.Fatal(err)
		}
		fpTime := time.Since(start)

		start = time.Now()
		snap, cleanup, err := Snapshot(context.Background(), s)
		if err != nil {
			t.Fatal(err)
		}
		snapTime := time.Since(start)
		var size int64
		_ = walkSize(snap, &size)

		counts := map[string]int{}
		start = time.Now()
		om, err := Read(context.Background(), snap, nil, func(a Account, kind string, v any) error {
			counts[kind]++
			return nil
		})
		readTime := time.Since(start)
		cleanup()
		if _, statErr := os.Stat(snap); statErr == nil {
			t.Errorf("snapshot %d not cleaned up", i)
		}
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("source %d: fingerprint=%d chars in %v; snapshot %d bytes in %v; read in %v; kinds=%v omissions=%v",
			i, len(fp), fpTime, size, snapTime, readTime, counts, om)
	}
}
