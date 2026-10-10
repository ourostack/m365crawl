//go:build acceptance

package acceptance

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/hxstore"
	"github.com/ourostack/m365crawl/internal/outlookowa"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/syncer"
	"github.com/ourostack/m365crawl/internal/teamsdesktop"
)

type owaObservation struct {
	root, account string
	profile       outlookowa.Profile
	census        owaRawCensus
	stable        bool
	digest        [32]byte
}

func owaStable(before, after string) bool { return before != "" && before == after }

func owaCapture(t *testing.T, p outlookowa.Profile) owaObservation {
	t.Helper()
	before, fresh, err := outlookowa.Fingerprint(p)
	if err != nil {
		t.Fatal("source_fingerprint_failed")
	}
	// Background owns the snapshot lifetime: no cancellation removes bytes under a reader.
	dir, cleanup, err := teamsdesktop.Snapshot(context.Background(), p.Source)
	if err != nil {
		cleanup()
		t.Fatal("snapshot_refused")
	}
	release := func() {
		cleanup()
		if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
			t.Error("snapshot_cleanup_failed")
		}
	}
	calMu.Lock()
	calCleanups = append(calCleanups, cleanup)
	calMu.Unlock()
	t.Cleanup(release)
	after, afterFresh, err := outlookowa.Fingerprint(p)
	if err != nil {
		t.Fatal("source_fingerprint_failed")
	}
	root := filepath.Join(dir, "EBWebView")
	profileDir := filepath.Join(root, p.Name)
	idb := filepath.Join(profileDir, "IndexedDB")
	if err := os.MkdirAll(idb, 0o700); err != nil {
		t.Fatal("snapshot_shape_failed")
	}
	level := filepath.Join(idb, p.Source.Origin+".indexeddb.leveldb")
	blob := filepath.Join(idb, p.Source.Origin+".indexeddb.blob")
	for i, pair := range [][2]string{{filepath.Join(dir, "leveldb"), level}, {filepath.Join(dir, "blob"), blob}} {
		if err := os.Rename(pair[0], pair[1]); err != nil {
			var code syscall.Errno
			if errors.As(err, &code) {
				t.Fatalf("snapshot_shape_failed operation=rename index=%d errno=%d", i, code)
			}
			t.Fatal("snapshot_shape_failed operation=rename")
		}
	}
	p.Dir = profileDir
	p.Source.LevelDBDir, p.Source.BlobDir = level, blob
	return owaObservation{root: root, account: "outlook/" + p.Name, profile: p, stable: owaStable(before, after) && fresh.Equal(afterFresh), digest: owaPayloadDigest(t, p.Source)}
}

func owaObserve(t *testing.T, p outlookowa.Profile) owaObservation {
	t.Helper()
	s := owaCapture(t, p)
	var err error
	s.census, err = owaCensus(s.profile.Source.LevelDBDir, s.profile.Source.BlobDir)
	if err != nil {
		t.Fatal("raw_census_refused")
	}
	if s.digest != owaPayloadDigest(t, s.profile.Source) {
		t.Fatal("private_source_mutated")
	}
	if !s.stable {
		// A bounded captured-copy check can still run, but this is not live absence evidence.
		t.Log("unstable_source_observation")
	}
	return s
}

func owaArchive(t *testing.T, path string) *store.Store {
	t.Helper()
	st, err := store.OpenReadOnly(context.Background(), path)
	if err != nil {
		t.Fatal("archive_open_failed")
	}
	t.Cleanup(func() { owaCloseArchive(t, st) })
	return st
}

// Register exact flat archive cleanup with the existing TestMain owner, and verify it at the test boundary after all later-registered handles have closed.
func owaScratch(t *testing.T) string {
	t.Helper()
	dir := calendarScratch(t)
	t.Cleanup(func() {
		removeFlat(dir)
		if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
			t.Error("archive_cleanup_failed")
		}
	})
	return dir
}

func owaCloseArchive(t *testing.T, st *store.Store) {
	t.Helper()
	if err := st.Close(); err != nil {
		t.Error("archive_close_failed")
	}
}

func owaQuery(st *store.Store, q string) ([][]any, error) {
	_, rows, truncated, err := st.SQL(context.Background(), q, acceptanceRowLimit)
	if err != nil || truncated {
		return nil, errors.New("archive_query_refused")
	}
	return rows, nil
}

func owaScalar(t *testing.T, st *store.Store, q string) int {
	t.Helper()
	rows, err := owaQuery(st, q)
	if err != nil || len(rows) != 1 || len(rows[0]) != 1 {
		t.Fatal("archive_count_refused")
	}
	n, ok := rows[0][0].(int64)
	if !ok || n < 0 || n > 1<<30 {
		t.Fatal("archive_count_invalid")
	}
	return int(n)
}

func owaExercise(t *testing.T, db string, c owaObservation) {
	t.Helper()
	before := c.digest
	opts := syncer.Options{Root: filepath.Join(c.root, "no-teams"), DBPath: db, OutlookRoot: c.root, OutlookEnabled: true, OutlookMinReadInterval: -1}
	var baseline map[string][][]any
	for step := 0; step < 3; step++ {
		opts.FullRead = step == 2
		start := time.Now()
		rep, _, err := syncer.Run(context.Background(), opts)
		if err != nil {
			t.Fatal("sync_refused")
		}
		src, found := mailSource(rep)
		if !found || len(rep.Sources) != 1 || len(src.Omissions) != 0 {
			t.Fatal("sync_source_refused")
		}
		wantStatus := syncer.StatusOK
		if step == 1 {
			wantStatus = syncer.StatusUnchanged
		}
		if src.Status != wantStatus {
			t.Fatal("sync_status_mismatch")
		}
		m := mailCountsOf(src)
		if m.Gone != 0 || m.Evicted != 0 || m.Withheld != 0 || m.Replaced != 0 || step > 0 && (m.Added != 0 || m.Updated != 0) || step == 0 && m.Added != len(c.census.messages) {
			t.Fatal("sync_counts_mismatch")
		}
		st := owaArchive(t, db)
		findings := owaCompare(st, c.account, c.census)
		state := owaArchiveState(t, st)
		owaCloseArchive(t, st)
		if len(findings) != 0 {
			t.Fatalf("archive_mismatch fields=%v", findings)
		}
		if step == 0 {
			baseline = state
		} else if !reflect.DeepEqual(state, baseline) {
			t.Fatal("repeat_archive_unstable")
		}
		t.Logf("step=%d messages=%d folders=%d wall_ms=%d", step, len(c.census.messages), len(c.census.folders), time.Since(start).Milliseconds())
	}
	if before != owaPayloadDigest(t, c.profile.Source) {
		t.Fatal("private_source_mutated")
	}
}

func owaArchiveState(t *testing.T, st *store.Store) map[string][][]any {
	t.Helper()
	state := map[string][][]any{}
	for _, table := range []string{"mail_messages", "mail_folders", "mail_recipients", "mail_attachments", "mail_source_ids", "mail_absent", "mail_fts"} {
		// Only this fixed table allowlist enters SQL, never a source name or value.
		rows, err := owaQuery(st, "select rowid,* from "+table+" order by rowid")
		if err != nil {
			t.Fatal("archive_state_refused")
		}
		state[table] = rows
	}
	return state
}

func owaPayloadDigest(t *testing.T, s teamsdesktop.Source) [32]byte {
	t.Helper()
	h := sha256.New()
	for _, dir := range []string{s.LevelDBDir, s.BlobDir} {
		err := filepath.WalkDir(dir, func(path string, e fs.DirEntry, err error) error {
			if err != nil {
				return errors.New("payload_walk_failed")
			}
			if e.IsDir() || e.Name() == "LOCK" || strings.HasPrefix(e.Name(), "LOG") {
				return nil
			}
			f, err := os.Open(path) //nolint:gosec // exact private Snapshot-owned source tree
			if err != nil {
				return errors.New("payload_open_failed")
			}
			_, readErr := io.Copy(h, f)
			closeErr := f.Close()
			if readErr != nil || closeErr != nil {
				return errors.New("payload_read_failed")
			}
			return nil
		})
		if err != nil {
			t.Fatal("payload_digest_failed")
		}
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

func owaExpectRefusal(t *testing.T, s owaObservation) {
	t.Helper()
	o, err := outlookowa.Open(context.Background(), s.profile.Source.LevelDBDir, s.profile.Source.BlobDir)
	if o != nil {
		if closeErr := o.Close(); closeErr != nil {
			t.Error("origin_close_failed")
		}
	}
	var guard *hxstore.GuardError
	if !errors.As(err, &guard) || guard.Code != "outlook_mail_layout_unsupported" {
		t.Fatal("fixed_layout_refusal_missing")
	}
}

func owaExpectLoss(t *testing.T, s owaObservation) {
	t.Helper()
	o, err := outlookowa.Open(context.Background(), s.profile.Source.LevelDBDir, s.profile.Source.BlobDir)
	if err != nil {
		t.Fatal("origin_open_failed")
	}
	res, readErr := outlookowa.Collect(context.Background(), o, s.account, true)
	closeErr := o.Close()
	if readErr != nil || closeErr != nil {
		t.Fatal("origin_read_or_close_failed")
	}
	if len(res.Messages) != 5 || !res.Doubtful || len(res.Losses) != 1 || res.Losses[0].Code != "outlook_owa_reader_blob_missing" {
		t.Fatal("fixed_decoder_loss_missing")
	}
	db := filepath.Join(owaScratch(t), "loss.db")
	rep, _, err := syncer.Run(context.Background(), syncer.Options{Root: filepath.Join(s.root, "no-teams"), DBPath: db, OutlookEnabled: true, OutlookRoot: s.root, OutlookMinReadInterval: -1})
	if err != nil {
		t.Fatal("loss_sync_failed")
	}
	src, found := mailSource(rep)
	if !found || src.Status != syncer.StatusOmissions || len(src.Omissions) != 1 || src.Omissions["outlook_owa_reader_blob_missing"] != 1 {
		t.Fatal("loss_sync_status_mismatch")
	}
	st := owaArchive(t, db)
	defer owaCloseArchive(t, st)
	if owaScalar(t, st, `select count(*) from mail_messages`) != 5 || owaScalar(t, st, `select count(*) from mail_absent`) != 0 {
		t.Fatal("loss_sync_unsafe_absence")
	}
}

func owaSortedFields(fields map[string]bool) []string {
	result := make([]string, 0, len(fields))
	for name := range fields {
		result = append(result, name)
	}
	sort.Strings(result)
	return result
}
