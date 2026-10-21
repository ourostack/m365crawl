package outlookowa

import (
	"context"
	"errors"

	"github.com/ourostack/m365crawl/internal/indexeddb"
)

// ErrReaderClose means an aborted open could not prove that its reader was released.
var ErrReaderClose = errors.New("outlookowa: reader_close_failed")

var (
	mailCensus   = indexeddb.Census
	mailOpenWith = indexeddb.OpenWith
	mailClose    = (*indexeddb.Origin).Close
)

func Open(ctx context.Context, leveldbDir, blobDir string) (*indexeddb.Origin, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	_, dbs, err := mailCensus(leveldbDir)
	if err != nil {
		return nil, sourceFailure("metadata_unreadable", err)
	}
	db, stores, err := selectMailStores(dbs)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	o, err := mailOpenWith(leveldbDir, blobDir, indexeddb.OpenOptions{KeepStore: func(dbID, storeID int64) bool {
		return dbID == db.ID && (storeID == stores["folders"] || storeID == stores["messages"] || storeID == stores["messageBodies"])
	}})
	if err != nil {
		return nil, sourceFailure("records_unreadable", err)
	}
	if err := ctx.Err(); err != nil {
		if closeErr := mailClose(o); closeErr != nil {
			return nil, errors.Join(err, ErrReaderClose)
		}
		return nil, err
	}
	return o, nil
}
