//go:build acceptance

package acceptance

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/outlookowa"
	"github.com/ourostack/m365crawl/internal/store"
	gl "github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"
	"github.com/syndtr/goleveldb/leveldb/util"
)

func TestOWAAnchoredRootTrustedAbsenceAndReappearance(t *testing.T) {
	c := owaAnchoredCopy(t)
	inbox := ""
	for id, f := range c.census.folders {
		if f.kind == "inbox" {
			inbox = id
		}
	}
	if inbox == "" {
		t.Fatal("synthetic_inbox_missing")
	}
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(owaScratch(t), "anchor-absence.db")
	st, err := store.Open(c.ctx, path)
	if err != nil {
		t.Fatal("synthetic_store_open_failed")
	}
	defer owaCloseArchive(t, st)
	commit := func(hour int, includeMiddle, badCore bool) store.MailResult {
		t.Helper()
		db, err := gl.OpenFile(c.profile.Source.LevelDBDir, &opt.Options{})
		if err != nil {
			t.Fatal("synthetic_absence_source_open_failed")
		}
		iter := db.NewIterator(util.BytesPrefix([]byte{0, 1, 2, 1}), nil)
		var keys [][]byte
		for iter.Next() {
			keys = append(keys, append([]byte{}, iter.Key()...))
		}
		iterErr := iter.Error()
		iter.Release()
		if iterErr != nil {
			t.Fatal("synthetic_message_keys_failed")
		}
		for _, key := range keys {
			if db.Delete(key, nil) != nil {
				t.Fatal("synthetic_message_remove_failed")
			}
		}
		for i, id := range []string{"Old", "Middle", "New"} {
			if id == "Middle" && !includeMiddle {
				continue
			}
			v := owaObject("ItemId", owaIdentity(id), "ParentFolderId", owaIdentity(inbox), "HasAttachments", false, "DateTimeReceived", at.Add(time.Duration(i-3)*time.Hour).Format(time.RFC3339))
			if db.Put(owaSyntheticKey(2, id), append([]byte{1, 0xff, 0x10, 0xff, 15}, owaEncodeSynthetic(t, v)...), nil) != nil {
				t.Fatal("synthetic_absence_header_write_failed")
			}
		}
		badKey := owaSyntheticKey(1, "foreign-core")
		if badCore {
			v := owaObject("id", "ForeignCore", "parentFolderId", "OpaqueAncillary", "distinguishedFolderType", "sentitems")
			if db.Put(badKey, append([]byte{1, 0xff, 0x10, 0xff, 15}, owaEncodeSynthetic(t, v)...), nil) != nil {
				t.Fatal("synthetic_foreign_core_write_failed")
			}
		} else if db.Delete(badKey, nil) != nil {
			t.Fatal("synthetic_foreign_core_remove_failed")
		}
		if db.Close() != nil {
			t.Fatal("synthetic_absence_source_close_failed")
		}
		o, err := outlookowa.Open(c.ctx, c.profile.Source.LevelDBDir, c.profile.Source.BlobDir)
		if err != nil {
			t.Fatal("synthetic_absence_origin_open_failed")
		}
		r, err := outlookowa.Collect(c.ctx, o, c.account, true)
		closeErr := o.Close()
		if err != nil || closeErr != nil || (!badCore && (r.Doubtful || r.RootID != "Root/Case==")) || (badCore && (!r.Doubtful || r.RootID != "")) {
			t.Fatal("actual_absence_anchor_attribution_mismatch")
		}
		res, err := st.CommitOWAMail(c.ctx, store.OWAMailBatch{Account: c.account, ReadAt: at.Add(time.Duration(hour) * time.Hour), FreshAt: at.Add(time.Duration(hour) * time.Hour), Trusted: true, Result: r})
		if err != nil {
			t.Fatal("synthetic_anchored_commit_failed")
		}
		return res
	}
	if commit(0, true, false).Added != 3 {
		t.Fatal("initial_anchored_messages_missing")
	}
	if commit(1, false, true).Gone != 0 || commit(2, false, false).Gone != 0 {
		t.Fatal("uncertain_or_first_trusted_read_marked_gone")
	}
	// The existing store root marker changes through the uncertain read; restoration itself is not a trusted absence. Two later distinct trusted copies are still required.
	if commit(3, false, false).Gone != 0 {
		t.Fatal("first_stable_anchored_absence_marked_gone")
	}
	if commit(4, false, false).Gone != 1 {
		t.Fatal("second_trusted_absence_missing")
	}
	if commit(5, true, false).Gone != 0 {
		t.Fatal("reappearance_marked_gone")
	}
	read := owaArchive(t, path)
	defer owaCloseArchive(t, read)
	_, rows, truncated, err := read.SQL(c.ctx, `select gone_at from mail_messages where detail_key=(select key from mail_source_ids where kind='message' and source_id='Middle' and account='outlook/Synthetic')`, 1)
	if err != nil || truncated || len(rows) != 1 || rows[0][0] != nil {
		t.Fatal("reappeared_anchored_message_still_gone")
	}
}
