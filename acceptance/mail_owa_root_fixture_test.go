//go:build acceptance

package acceptance

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/indexeddb"
	"github.com/ourostack/m365crawl/internal/outlookowa"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/syncer"
	gl "github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"
)

func owaAnchoredCopy(t *testing.T) owaObservation {
	t.Helper()
	c := owaObserve(t, owaFixtureProfile("mail", "Synthetic"))
	root, err := c.census.root()
	if err != nil {
		t.Fatal("synthetic_old_root_failed")
	}
	owaSyntheticTable(t, c, nil, false)
	keys := map[string][]byte{}
	o, err := indexeddb.OpenWith(c.profile.Source.LevelDBDir, c.profile.Source.BlobDir, indexeddb.OpenOptions{KeepStore: func(db, st int64) bool { return db == 1 && st == 1 }})
	if err != nil {
		t.Fatal("synthetic_folder_keys_open_failed")
	}
	scanErr := o.Records(1, 1, func(rec indexeddb.Record) error {
		value, err := o.Decode(1, rec.Raw)
		if err != nil {
			return err
		}
		key, ok := rec.Key.(string)
		if !ok {
			t.Fatal("synthetic_folder_key_shape_failed")
		}
		f := owaFields{}
		keys[f.text(value, "id")] = owaSyntheticKey(1, key)
		return nil
	})
	if closeErr := o.Close(); scanErr != nil || closeErr != nil {
		t.Fatal("synthetic_folder_keys_failed")
	}
	db, err := gl.OpenFile(c.profile.Source.LevelDBDir, &opt.Options{})
	if err != nil {
		t.Fatal("synthetic_root_table_open_failed")
	}
	values := map[string]any{}
	for id, f := range c.census.folders {
		parent := f.parent
		if parent == root {
			parent = "SyntheticAnchor"
		}
		values[id] = owaObject("id", id, "parentFolderId", parent, "displayName", f.name, "distinguishedFolderType", f.kind)
	}
	values["SyntheticAnchor"] = owaObject("id", "SyntheticAnchor", "parentFolderId", root, "displayName", "not-a-root-selector", "distinguishedFolderType", "MSGFOLDERROOT")
	for id, parent := range map[string]string{"Unused1": "OpaqueAncillary", "Unused2": "Unused1", "Unused3": "Unused2"} {
		values[id] = owaObject("id", id, "parentFolderId", parent, "displayName", "msgfolderroot", "distinguishedFolderType", "other")
	}
	for id, value := range values {
		key := owaSyntheticKey(1, "anchor-"+id)
		// Use the existing key for an existing folder rather than create a conflicting raw duplicate.
		if _, exists := c.census.folders[id]; exists {
			key = keys[id]
		}
		if db.Put(key, append([]byte{1, 0xff, 0x10, 0xff, 15}, owaEncodeSynthetic(t, value)...), nil) != nil {
			if db.Close() != nil {
				t.Error("synthetic_root_table_close_failed")
			}
			t.Fatal("synthetic_root_table_write_failed")
		}
	}
	if db.Close() != nil {
		t.Fatal("synthetic_root_table_close_failed")
	}
	c.digest = owaPayloadDigestContext(t, c.ctx, c.profile.Source)
	return c
}

func TestOWAActualAnchoredRootRetainsAncillaryFolders(t *testing.T) {
	c := owaAnchoredCopy(t)
	o, err := outlookowa.Open(c.ctx, c.profile.Source.LevelDBDir, c.profile.Source.BlobDir)
	if err != nil {
		t.Fatal("synthetic_anchor_open_failed")
	}
	r, readErr := outlookowa.Collect(c.ctx, o, c.account, true)
	closeErr := o.Close()
	if readErr != nil || closeErr != nil || r.RootID != "Root/Case==" || r.Doubtful || len(r.Folders) != 11 || len(r.Messages) != 5 {
		t.Fatal("actual_anchor_collection_refused")
	}
	c.census, err = owaCensusContext(c.ctx, c.profile.Source.LevelDBDir, c.profile.Source.BlobDir)
	if err != nil || len(c.census.folders) != 11 || len(c.census.messages) != 5 {
		t.Fatal("independent_anchor_fixture_refused")
	}
	root, err := c.census.root()
	if err != nil || root != "Root/Case==" {
		t.Fatal("independent_opaque_anchor_root_changed")
	}
	path := filepath.Join(owaScratch(t), "anchor.db")
	owaExercise(t, path, c)
	st := owaArchive(t, path)
	defer owaCloseArchive(t, st)
	if fields := owaCompareAll(st, []owaObservation{c}); len(fields) != 0 {
		t.Fatal("anchor_raw_archive_mismatch")
	}

}

func TestOWAActualAnchoredRawCensus(t *testing.T) {
	c := owaAnchoredCopy(t)
	census, err := owaCensusContext(c.ctx, c.profile.Source.LevelDBDir, c.profile.Source.BlobDir)
	if err != nil || len(census.folders) != 11 || len(census.messages) != 5 {
		t.Fatal("raw_anchored_copy_refused")
	}
	root, err := census.root()
	if err != nil || root != "Root/Case==" {
		t.Fatal("raw_anchored_opaque_root_changed")
	}
}
func TestOWAMapper3PersistedRootReread(t *testing.T) {
	c := owaAnchoredCopy(t)
	var err error
	c.census, err = owaCensusContext(c.ctx, c.profile.Source.LevelDBDir, c.profile.Source.BlobDir)
	if err != nil {
		t.Fatal("synthetic_anchor_census_failed")
	}
	path := filepath.Join(owaScratch(t), "mapper3.db")
	opts := syncer.Options{Root: filepath.Join(c.root, "no-teams"), DBPath: path, OutlookRoot: c.root, OutlookEnabled: true, OutlookMinReadInterval: -1}
	rep, _, err := syncer.Run(c.ctx, opts)
	source, found := mailSource(rep)
	if err != nil || !found || source.Status != syncer.StatusOK {
		t.Fatal("initial_anchor_import_failed")
	}
	// Controlled persisted previous loss receipt plus RootKey zero models mapper-v2's uncertain-root derivation; no source lifecycle or production transaction is changed.
	owaCorrupt(t, path, `update meta set value='0' where key like 'outlook_mail_root:%'`)
	w, err := store.Open(c.ctx, path)
	if err != nil {
		t.Fatal("old_root_archive_open_failed")
	}
	now := time.Now().UTC()
	recordErr := w.RecordRun(c.ctx, store.Run{Source: source.Source, StartedAt: now, FinishedAt: now, Fingerprint: "acceptance-controlled-mapper-v2-root-uncertain", Status: syncer.StatusOmissions, Omissions: map[string]int{"outlook_owa_root_uncertain": 1}})
	closeErr := w.Close()
	if recordErr != nil || closeErr != nil {
		t.Fatal("old_root_receipt_failed")
	}
	rep, _, err = syncer.Run(c.ctx, opts)
	source, found = mailSource(rep)
	if err != nil || !found || source.Status != syncer.StatusOK || len(source.Omissions) != 0 {
		t.Fatal("mapper3_eligible_reread_missing")
	}
	st := owaArchive(t, path)
	if fields := owaCompareAll(st, []owaObservation{c}); len(fields) != 0 {
		t.Fatal("mapper3_persisted_root_not_restored")
	}
	owaCloseArchive(t, st)
	rep, _, err = syncer.Run(c.ctx, opts)
	source, found = mailSource(rep)
	if err != nil || !found || source.Status != syncer.StatusUnchanged || source.Counts != nil {
		t.Fatal("mapper3_stable_skip_missing")
	}
}
