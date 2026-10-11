//go:build acceptance

package acceptance

import (
	"errors"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/indexeddb"
)

func owaRawMailMetadata(dbs []indexeddb.Database) (indexeddb.Database, map[string]int64, error) {
	var selected []indexeddb.Database
	for _, db := range dbs {
		if (db.Name == "owa-offline-data" || strings.HasPrefix(db.Name, "owa-offline-data-")) && len(db.Stores) > 0 {
			selected = append(selected, db)
		}
	}
	if len(selected) != 1 {
		return indexeddb.Database{}, nil, errors.New("census_layout_refused")
	}
	db := selected[0]
	stores := map[string]int64{}
	for _, s := range db.Stores {
		if s.Name == "folders" || s.Name == "messages" || s.Name == "messageBodies" {
			if _, exists := stores[s.Name]; exists {
				return indexeddb.Database{}, nil, errors.New("census_layout_refused")
			}
			stores[s.Name] = s.ID
		}
	}
	if len(stores) != 3 {
		return indexeddb.Database{}, nil, errors.New("census_layout_refused")
	}
	return db, stores, nil
}

func TestOWARawLayoutZeroStoreMetadata(t *testing.T) {
	full := indexeddb.Database{ID: 1, Name: "owa-offline-data-synthetic", Stores: []indexeddb.Store{{ID: 1, Name: "folders"}, {ID: 2, Name: "messages"}, {ID: 3, Name: "messageBodies"}}}
	zero := indexeddb.Database{ID: 2, Name: "owa-offline-data-zero"}
	for _, tc := range []struct {
		name string
		dbs  []indexeddb.Database
		ok   bool
	}{
		{"full_zero", []indexeddb.Database{full, zero}, true},
		{"zero_full", []indexeddb.Database{zero, full}, true},
		{"single", []indexeddb.Database{full}, true},
		{"all_zero", []indexeddb.Database{zero, {ID: 3, Name: "owa-offline-data"}}, false},
		{"none", nil, false},
		{"full_partial", []indexeddb.Database{full, {ID: 3, Name: "owa-offline-data-partial", Stores: full.Stores[:1]}, zero}, false},
		{"two_full", []indexeddb.Database{full, {ID: 3, Name: "owa-offline-data-second", Stores: full.Stores}, zero}, false},
		{"partial_zero", []indexeddb.Database{{ID: 3, Name: "owa-offline-data-partial", Stores: full.Stores[:1]}, zero}, false},
		{"duplicate", []indexeddb.Database{{ID: 1, Name: full.Name, Stores: append(append([]indexeddb.Store{}, full.Stores...), full.Stores[0])}, zero}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, stores, err := owaRawMailMetadata(tc.dbs)
			if tc.ok {
				if err != nil || db.ID != 1 || len(stores) != 3 {
					t.Fatal("raw_zero_metadata_selection_refused")
				}
			} else if err == nil || err.Error() != "census_layout_refused" {
				t.Fatal("raw_layout_refusal_missing")
			}
		})
	}
}
