package outlookowa

import (
	"context"
	"encoding/binary"
	"errors"
	"reflect"
	"testing"

	"github.com/ourostack/m365crawl/internal/hxstore"
	"github.com/ourostack/m365crawl/internal/indexeddb"
	gl "github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"
)

func TestSelectMailStoresZeroStoreMetadataPolicy(t *testing.T) {
	full := indexeddb.Database{ID: 1, Name: "owa-offline-data", Stores: []indexeddb.Store{{ID: 1, Name: "folders"}, {ID: 2, Name: "messages"}, {ID: 3, Name: "messageBodies"}, {ID: 4, Name: "credentials"}}}
	zero := indexeddb.Database{ID: 2, Name: "owa-offline-data-zero"}
	partial := indexeddb.Database{ID: 3, Name: "owa-offline-data-partial", Stores: []indexeddb.Store{{ID: 1, Name: "credentials"}}}
	other := indexeddb.Database{ID: 4, Name: "unrelated", Stores: full.Stores}
	for _, tc := range []struct {
		name   string
		dbs    []indexeddb.Database
		reason string
	}{
		{"single", []indexeddb.Database{full}, ""},
		{"full_zero", []indexeddb.Database{full, zero}, ""},
		{"zero_full", []indexeddb.Database{zero, full}, ""},
		{"unrelated_zero_full", []indexeddb.Database{other, zero, full}, ""},
		{"no_match", []indexeddb.Database{other}, "content_database_missing"},
		{"all_zero", []indexeddb.Database{zero, {ID: 5, Name: "owa-offline-data"}}, "required_store_missing"},
		{"single_zero", []indexeddb.Database{zero}, "required_store_missing"},
		{"full_partial", []indexeddb.Database{full, partial, zero}, "multiple_mailboxes"},
		{"two_full", []indexeddb.Database{full, {ID: 5, Name: "owa-offline-data-second", Stores: full.Stores}, zero}, "multiple_mailboxes"},
		{"partial_zero", []indexeddb.Database{partial, zero}, "required_store_missing"},
		{"missing_folders", []indexeddb.Database{{ID: 1, Name: full.Name, Stores: full.Stores[1:]}}, "required_store_missing"},
		{"missing_messages", []indexeddb.Database{{ID: 1, Name: full.Name, Stores: []indexeddb.Store{full.Stores[0], full.Stores[2]}}}, "required_store_missing"},
		{"missing_bodies", []indexeddb.Database{{ID: 1, Name: full.Name, Stores: full.Stores[:2]}}, "required_store_missing"},
		{"duplicate_required", []indexeddb.Database{{ID: 1, Name: full.Name, Stores: append(append([]indexeddb.Store{}, full.Stores...), indexeddb.Store{ID: 5, Name: "messages"})}, zero}, "required_store_ambiguous"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, stores, err := selectMailStores(tc.dbs)
			if tc.reason != "" {
				var guard *hxstore.GuardError
				if !errors.As(err, &guard) || guard.Code != "outlook_mail_layout_unsupported" || guard.Detail != tc.reason {
					t.Fatal("layout_fixed_refusal_missing")
				}
				return
			}
			if err != nil || !reflect.DeepEqual(db, full) || !reflect.DeepEqual(stores, map[string]int64{"folders": 1, "messages": 2, "messageBodies": 3}) {
				t.Fatal("populated_mail_selection_changed")
			}
		})
	}
}

type zeroStoreMetadataReader struct {
	*faultReader
	t     *testing.T
	scans int
}

func (r *zeroStoreMetadataReader) Records(db, store int64, fn func(indexeddb.Record) error) error {
	if db != 1 || store < 1 || store > 3 {
		r.t.Fatal("ancillary_or_zero_database_values_requested")
	}
	r.scans++
	return r.faultReader.Records(db, store, fn)
}

func TestCollectIgnoresOnlyZeroStoreMatchingMetadata(t *testing.T) {
	f := readerWith(obj("ItemId", obj("Id", "M"), "ParentFolderId", obj("Id", "F"), "HasAttachments", false))
	f.dbs = append([]indexeddb.Database{{ID: 99, Name: "owa-offline-data-zero"}}, f.dbs...)
	r := &zeroStoreMetadataReader{faultReader: f, t: t}
	result, err := collect(context.Background(), r, "outlook/Synthetic", true)
	if err != nil || len(result.Messages) != 1 || result.Doubtful || r.scans != 3 {
		t.Fatal("zero_store_metadata_collection_refused")
	}

}

func TestZeroStoreRefusalsDoNotRequestValues(t *testing.T) {
	full := readerWith().dbs[0]
	for _, tc := range []struct {
		name   string
		dbs    []indexeddb.Database
		reason string
	}{
		{"all_zero", []indexeddb.Database{{ID: 1, Name: "owa-offline-data"}, {ID: 2, Name: "owa-offline-data-zero"}}, "required_store_missing"},
		{"full_partial", []indexeddb.Database{full, {ID: 2, Name: "owa-offline-data-partial", Stores: []indexeddb.Store{{ID: 9, Name: "credentials"}}}}, "multiple_mailboxes"},
		{"duplicate", []indexeddb.Database{{ID: full.ID, Name: full.Name, Stores: append(append([]indexeddb.Store{}, full.Stores...), full.Stores[0])}, {ID: 2, Name: "owa-offline-data-zero"}}, "required_store_ambiguous"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			oldCensus, oldOpen := mailCensus, mailOpenWith
			t.Cleanup(func() { mailCensus, mailOpenWith = oldCensus, oldOpen })
			mailCensus = func(string) (map[int64]int64, []indexeddb.Database, error) {
				return nil, tc.dbs, nil
			}
			mailOpenWith = func(string, string, indexeddb.OpenOptions) (*indexeddb.Origin, error) {
				t.Fatal("unsupported_metadata_requested_values")
				return nil, nil
			}
			o, err := Open(context.Background(), "synthetic-metadata-only", "")
			var guard *hxstore.GuardError
			if o != nil || !errors.As(err, &guard) || guard.Detail != tc.reason {
				t.Fatal("metadata_open_refusal_missing")
			}
			reader := &zeroStoreMetadataReader{faultReader: &faultReader{dbs: tc.dbs}, t: t}
			_, err = collect(context.Background(), reader, "outlook/Synthetic", true)
			if !errors.As(err, &guard) || guard.Detail != tc.reason || reader.scans != 0 {
				t.Fatal("metadata_collect_refusal_requested_values")
			}
		})
	}
}

func TestOpenCollectZeroStoreMetadataUsesOnlyPopulatedMail(t *testing.T) {
	p := tableProfile(t)
	db, err := gl.OpenFile(p.Source.LevelDBDir, &opt.Options{})
	if err != nil {
		t.Fatal("synthetic_metadata_open_failed")
	}
	idbString := func(s string) []byte {
		out := binary.AppendUvarint(nil, uint64(len(s)))
		for _, c := range []byte(s) {
			out = append(out, 0, c)
		}
		return out
	}
	key := append([]byte{0, 0, 0, 0, 201}, idbString("https_outlook.office.com_0")...)
	key = append(key, idbString("owa-offline-data-zero")...)
	putErr := db.Put(key, []byte{99}, nil)
	closeErr := db.Close()
	if putErr != nil || closeErr != nil {
		t.Fatal("synthetic_metadata_write_failed")
	}
	_, dbs, err := indexeddb.Census(p.Source.LevelDBDir)
	if err != nil {
		t.Fatal("synthetic_metadata_read_failed")
	}
	zeros := 0
	for _, d := range dbs {
		if d.ID == 99 && len(d.Stores) == 0 {
			zeros++
		}
	}
	if zeros != 1 {
		t.Fatal("synthetic_zero_metadata_not_present")
	}
	o, err := Open(context.Background(), p.Source.LevelDBDir, p.Source.BlobDir)
	if err != nil {
		t.Fatal("zero_store_open_refused")
	}
	defer func() {
		if o.Close() != nil {
			t.Error("synthetic_origin_close_failed")
		}
	}()
	result, err := Collect(context.Background(), o, "outlook/Default", true)
	if err != nil || len(result.Messages) != 5 || result.Doubtful || len(result.Losses) != 0 {
		t.Fatal("zero_store_collection_changed")
	}
	for _, d := range dbs {
		for _, s := range d.Stores {
			if d.ID == 1 && (s.Name == "folders" || s.Name == "messages" || s.Name == "messageBodies") {
				continue
			}
			n := 0
			if o.Records(d.ID, s.ID, func(indexeddb.Record) error { n++; return nil }) != nil || n != 0 {
				t.Fatal("ancillary_values_loaded")
			}
		}
	}
}
