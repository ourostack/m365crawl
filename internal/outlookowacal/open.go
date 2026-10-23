package outlookowacal

import (
	"context"
	"errors"

	"github.com/ourostack/m365crawl/internal/indexeddb"
)

// ErrReaderClose means an aborted open could not prove that its reader was released.
var ErrReaderClose = errors.New("outlook_owa_calendar_close_failed")

var (
	calendarCensus   = indexeddb.CensusWithStats
	calendarOpenWith = indexeddb.OpenWith
	calendarClose    = (*indexeddb.Origin).Close
)

// Open rejects ambiguous metadata before loading event values and excludes every other store.
func Open(ctx context.Context, leveldbDir, blobDir string) (*indexeddb.Origin, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	_, dbs, stats, err := calendarCensus(leveldbDir)
	if canceled := ctx.Err(); canceled != nil {
		return nil, canceled
	}
	if err != nil {
		return nil, errors.New("outlook_owa_calendar_metadata_unreadable")
	}
	if stats.TruncatedLogTails > 0 {
		return nil, layout("owa_metadata_truncated")
	}
	dbID, storeID, err := selectEvents(dbs)
	if err != nil {
		return nil, err
	}
	o, err := calendarOpenWith(leveldbDir, blobDir, indexeddb.OpenOptions{
		KeepStore: func(db, store int64) bool { return db == dbID && store == storeID },
	})
	if err != nil {
		return nil, errors.New("outlook_owa_calendar_records_unreadable")
	}
	if err := ctx.Err(); err != nil {
		if closeErr := closeCanceledReader(o); closeErr != nil {
			return nil, errors.Join(err, ErrReaderClose)
		}
		return nil, err
	}
	return o, nil
}

func closeCanceledReader(o *indexeddb.Origin) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrReaderClose
		}
	}()
	if calendarClose(o) != nil {
		return ErrReaderClose
	}
	return nil
}
