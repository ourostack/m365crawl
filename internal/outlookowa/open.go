package outlookowa

import (
	"context"

	"github.com/ourostack/m365crawl/internal/indexeddb"
)

func Open(ctx context.Context, leveldbDir, blobDir string) (*indexeddb.Origin, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	_, dbs, err := indexeddb.Census(leveldbDir)
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
	o, err := indexeddb.OpenWith(leveldbDir, blobDir, indexeddb.OpenOptions{KeepStore: func(dbID, storeID int64) bool {
		return dbID == db.ID && (storeID == stores["folders"] || storeID == stores["messages"] || storeID == stores["messageBodies"])
	}})
	if err != nil {
		return nil, sourceFailure("records_unreadable", err)
	}
	if err := ctx.Err(); err != nil {
		_ = o.Close()
		return nil, err
	}
	return o, nil
}
