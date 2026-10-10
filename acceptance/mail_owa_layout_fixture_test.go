//go:build acceptance

package acceptance

import (
	"encoding/binary"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ourostack/m365crawl/internal/indexeddb"
	gl "github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"
)

func TestOWAZeroStoreMetadataActualCensusImportAgreement(t *testing.T) {
	c := owaObserve(t, owaFixtureProfile("mail", "Synthetic"))
	want := c.census.counts()
	owaSyntheticTable(t, c, nil, false)
	db, err := gl.OpenFile(c.profile.Source.LevelDBDir, &opt.Options{})
	if err != nil {
		t.Fatal("synthetic_metadata_open_failed")
	}
	stringKey := func(s string) []byte {
		out := binary.AppendUvarint(nil, uint64(len(s)))
		for _, c := range []byte(s) {
			out = append(out, 0, c)
		}
		return out
	}
	key := append([]byte{0, 0, 0, 0, 201}, stringKey("https_outlook.office.com_0")...)
	key = append(key, stringKey("owa-offline-data-zero")...)
	putErr := db.Put(key, []byte{99}, nil)
	closeErr := db.Close()
	if putErr != nil || closeErr != nil {
		t.Fatal("synthetic_metadata_write_failed")
	}
	_, dbs, err := indexeddb.Census(c.profile.Source.LevelDBDir)
	if err != nil {
		t.Fatal("synthetic_metadata_read_failed")
	}
	zero := false
	for _, d := range dbs {
		if d.ID == 99 && len(d.Stores) == 0 {
			zero = true
		}
	}
	if !zero {
		t.Fatal("synthetic_zero_metadata_missing")
	}
	c.census, err = owaCensusContext(c.ctx, c.profile.Source.LevelDBDir, c.profile.Source.BlobDir)
	if err != nil || !reflect.DeepEqual(c.census.counts(), want) {
		t.Fatal("independent_zero_metadata_census_changed")
	}
	c.digest = owaPayloadDigestContext(t, c.ctx, c.profile.Source)
	path := filepath.Join(owaScratch(t), "zero-metadata.db")
	owaExercise(t, path, c)
	st := owaArchive(t, path)
	defer owaCloseArchive(t, st)
	if fields := owaCompareAll(st, []owaObservation{c}); len(fields) != 0 {
		t.Fatal("zero_metadata_raw_archive_disagreement")
	}
}
