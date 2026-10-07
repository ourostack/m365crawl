package teamsdesktop

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"
)

// TestRealCache runs discovery, snapshot and read against the live Teams cache. It needs Full
// Disk Access and M365CRAWL_REAL_CACHE=1, and logs counts and timings only, never names, ids
// or content, because the cache is private data.
func TestRealCache(t *testing.T) {
	if os.Getenv("M365CRAWL_REAL_CACHE") != "1" {
		t.Skip("set M365CRAWL_REAL_CACHE=1 to run against the real Teams cache")
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

// TestRealCacheMapping maps every record of the live cache and logs only counts and percentages,
// never names, ids or content.
func TestRealCacheMapping(t *testing.T) {
	if os.Getenv("M365CRAWL_REAL_CACHE") != "1" {
		t.Skip("set M365CRAWL_REAL_CACHE=1 to run against the real Teams cache")
	}
	srcs, _, err := Discover(DefaultRoot())
	if err != nil {
		t.Fatal(err)
	}
	var msgs, withText, mentionsMe, convs, withName, acts, reactions, withFiles, withLinks, pinned, deleted, edited int
	people := map[string]bool{}
	unmapped := map[string]int{}
	for _, s := range srcs {
		snap, cleanup, err := Snapshot(context.Background(), s)
		if err != nil {
			t.Fatal(err)
		}
		_, err = Read(context.Background(), snap, nil, func(a Account, kind string, v any) error {
			var ps []Person
			var err error
			switch kind {
			case KindReplyChain:
				var ms []Message
				ms, ps, err = MapReplyChain(a, v)
				for _, m := range ms {
					msgs++
					withText += b2i(m.ContentText != "")
					mentionsMe += b2i(m.MentionsMe)
					reactions += b2i(len(m.Reactions) > 0)
					withFiles += b2i(len(m.Files) > 0)
					withLinks += b2i(len(m.Links) > 0)
					pinned += b2i(m.Pinned)
					deleted += b2i(!m.DeletedAt.IsZero())
					edited += b2i(!m.EditedAt.IsZero())
				}
			case KindConversation:
				var c Conversation
				c, ps, err = MapConversation(a, v)
				if err == nil {
					convs++
					withName += b2i(c.DisplayName != "")
				}
			case KindActivity:
				_, err = MapActivity(a, v)
				acts += b2i(err == nil)
			}
			var um *UnmappedError
			if errors.As(err, &um) {
				unmapped[kind+": "+um.Reason]++
				return nil
			}
			for _, p := range ps {
				people[p.TenantID+"|"+p.ID] = true
			}
			return err
		})
		cleanup()
		if err != nil {
			t.Fatal(err)
		}
	}
	pct := func(n, d int) string {
		if d == 0 {
			return "n/a"
		}
		return fmt.Sprintf("%.1f%%", 100*float64(n)/float64(d))
	}
	t.Logf("messages=%d conversations=%d people=%d activity=%d unmapped=%v", msgs, convs, len(people), acts, unmapped)
	t.Logf("messages with text=%s mentions_me=%d with_reactions=%d with_files=%d with_links=%d pinned=%d deleted=%d edited=%d conversations with display_name=%s",
		pct(withText, msgs), mentionsMe, reactions, withFiles, withLinks, pinned, deleted, edited, pct(withName, convs))
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
