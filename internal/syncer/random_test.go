package syncer

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/syndtr/goleveldb/leveldb/journal"

	"github.com/ourostack/teamscrawl/internal/teamsdesktop"
)

// A LevelDB write-ahead log as the tests edit it: write batches of put and delete entries.
type logEntry struct {
	typ      byte // 1 put, 0 delete
	key, val []byte
}

type logBatch struct {
	seq     uint64
	entries []logEntry
}

func nextSeq(bs []logBatch) (next uint64) {
	for _, b := range bs {
		next = max(next, b.seq+uint64(len(b.entries)))
	}
	return next
}

func readLogBatches(t *testing.T, path string) []logBatch {
	t.Helper()
	f, err := os.Open(path) //nolint:gosec // test fixture copy
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var out []logBatch
	jr := journal.NewReader(f, nil, false, true)
	for {
		rr, err := jr.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		rec, err := io.ReadAll(rr)
		if err != nil {
			t.Fatal(err)
		}
		b := logBatch{seq: binary.LittleEndian.Uint64(rec)}
		count, rest := binary.LittleEndian.Uint32(rec[8:]), rec[12:]
		for i := uint32(0); i < count; i++ {
			e := logEntry{typ: rest[0]}
			kl, n := binary.Uvarint(rest[1:])
			e.key = rest[1+n : 1+n+int(kl)]
			rest = rest[1+n+int(kl):]
			if e.typ == 1 {
				vl, n := binary.Uvarint(rest)
				e.val = rest[n : n+int(vl)]
				rest = rest[n+int(vl):]
			}
			b.entries = append(b.entries, e)
		}
		out = append(out, b)
	}
}

func writeLogBatches(t *testing.T, path string, bs []logBatch) {
	t.Helper()
	out, err := os.Create(path) //nolint:gosec // test fixture copy
	if err != nil {
		t.Fatal(err)
	}
	jw := journal.NewWriter(out)
	for _, b := range bs {
		rec := binary.LittleEndian.AppendUint64(nil, b.seq)
		rec = binary.LittleEndian.AppendUint32(rec, uint32(len(b.entries))) //nolint:gosec // a handful of entries
		for _, e := range b.entries {
			rec = append(rec, e.typ)
			rec = binary.AppendUvarint(rec, uint64(len(e.key)))
			rec = append(rec, e.key...)
			if e.typ == 1 {
				rec = binary.AppendUvarint(rec, uint64(len(e.val)))
				rec = append(rec, e.val...)
			}
		}
		w, err := jw.Next()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(rec); err != nil {
			t.Fatal(err)
		}
	}
	if err := jw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
}

// textPuts lists the entries of the batches that hold a message with HTML text: the records a
// mutation can change without breaking them.
func textPuts(bs []logBatch) (out []logEntry) {
	for _, b := range bs {
		for _, e := range b.entries {
			if e.typ == 1 && len(e.key) > 4 && e.key[0] == 0 && e.key[2] == 1 && e.key[3] == 1 && bytes.Contains(e.val, []byte("<p>")) {
				out = append(out, e)
			}
		}
	}
	return out
}

// changedText is val with one letter after "<p>" replaced by another (same length, still valid).
func changedText(rng *rand.Rand, val []byte) []byte {
	out := append([]byte(nil), val...)
	i := bytes.Index(out, []byte("<p>")) + 3
	for {
		c := byte('a' + rng.Intn(26))
		if c != out[i] {
			out[i] = c
			return out
		}
	}
}

// secondSource copies the fixture's profile as another profile of the same root: another source
// with the same accounts, so the two sources produce the same rows.
func secondSource(t *testing.T, root string) {
	t.Helper()
	copyTree(t, filepath.Join(root, "WV2Profile_fixture"), filepath.Join(root, "WV2Profile_second"))
}

func logFiles(t *testing.T, root string) []string {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(root, "*", "IndexedDB", "*.leveldb", "*.log"))
	return m
}

// A seeded random series of cache changes, over two sources and two accounts, read by skipping
// syncs and by full reads into two archives: after every sync the reports (with every count), the
// change lists and the archives are identical. The changes: a log cut back or forward, a record
// whose text changes under the same key, a second record that carries the same message (so two
// records make one row, identical or not), edits that revert, a source that stays still, and a
// sync of one account only. A failure names the seed; TEAMSCRAWL_TEST_SEED=<n> runs that one.
func TestRandomCacheMutationsSkipEqualsFull(t *testing.T) {
	seeds := []int64{1, 2, 3}
	if s := os.Getenv("TEAMSCRAWL_TEST_SEED"); s != "" {
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seeds = []int64{n}
	}
	for _, seed := range seeds {
		t.Run(fmt.Sprintf("seed %d", seed), func(t *testing.T) {
			isolateTmp(t)
			rng := rand.New(rand.NewSource(seed)) //nolint:gosec // a test series, not security
			root := fixtureCopy(t)
			secondSource(t, root)
			logs := logFiles(t, root)
			if len(logs) != 2 {
				t.Fatalf("logs: %v", logs)
			}
			base := make([][]logBatch, len(logs))
			for i, l := range logs {
				base[i] = readLogBatches(t, l)
			}
			accounts := []*teamsdesktop.Account{
				nil, nil, nil,
				{TenantID: "00000000-0000-4000-8000-000000000001", UserID: "00000000-0000-4000-8000-0000000000a1"},
				{TenantID: "00000000-0000-4000-8000-000000000002", UserID: "00000000-0000-4000-8000-0000000000a2"},
			}
			skipDB, fullDB := newDB(t), newDB(t)
			for step := 1; step <= 5; step++ {
				for i, l := range logs {
					if rng.Intn(4) == 0 {
						continue // this source does not change this step
					}
					keep := base[i][:len(base[i])*(1+rng.Intn(10))/10]
					bs := append([]logBatch(nil), keep...)
					var extra []logEntry
					cands := textPuts(keep)
					for range rng.Intn(4) {
						if len(cands) == 0 {
							break
						}
						e := cands[rng.Intn(len(cands))]
						c := logEntry{1, append([]byte(nil), e.key...), e.val}
						switch rng.Intn(3) {
						case 0: // the same key, other text
							c.val = changedText(rng, e.val)
						case 1: // another record with the same message and other text
							c.key[len(c.key)-1] = byte('1' + rng.Intn(9))
							c.val = changedText(rng, e.val)
						default: // another record with the very same message
							c.key[len(c.key)-1] = byte('1' + rng.Intn(9))
						}
						extra = append(extra, c)
					}
					if len(extra) > 0 {
						bs = append(bs, logBatch{seq: nextSeq(bs), entries: extra})
					}
					writeLogBatches(t, l, bs)
				}
				acct := accounts[rng.Intn(len(accounts))]
				label := fmt.Sprintf("seed %d step %d (account filter %v)", seed, step, acct)
				for pass := 0; pass < 1+rng.Intn(2); pass++ {
					// Every pass sees changed files (the fingerprint), whether or not the records changed.
					for _, l := range logs {
						touchCount++
						later := time.Now().Add(time.Duration(touchCount) * time.Hour)
						if err := os.Chtimes(l, later, later); err != nil {
							t.Fatal(err)
						}
					}
					sr, sc, err1 := Run(context.Background(), Options{Root: root, DBPath: skipDB, Account: acct})
					fr, fc, err2 := Run(context.Background(), Options{Root: root, DBPath: fullDB, Account: acct, FullRead: true})
					if err1 != nil || err2 != nil {
						t.Fatalf("%s: errors %v / %v", label, err1, err2)
					}
					if a, b := reportKey(t, sr, sc), reportKey(t, fr, fc); a != b {
						t.Fatalf("%s pass %d: reports differ\nskip: %s\nfull: %s", label, pass, a, b)
					}
					if a, b := dumpArchive(t, skipDB), dumpArchive(t, fullDB); a != b {
						t.Fatalf("%s pass %d: archives differ", label, pass)
					}
				}
			}
		})
	}
}
