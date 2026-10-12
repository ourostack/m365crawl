package store

import (
	"context"
	"database/sql"
	"strconv"
	"time"
)

// appBackfillBatch is how many retained messages the application backfill reads at a time.
const appBackfillBatch = 500

// ApplicationBackfillReport is what one application backfill did: the mapper version it projected
// under and how many retained messages it processed, with the failed and absent among them. It
// counts application evidence only, never legacy updates, omissions or covering freshness.
type ApplicationBackfillReport struct {
	MapperVersion int `json:"mapper_version"`
	Processed     int `json:"processed"`
	Failed        int `json:"failed"`
	Absent        int `json:"absent"`
}

// BackfillMessageApplications projects every retained message's raw JSON under the active mapper
// version when the archive's last complete backfill used another version (or none): it fills
// missing raw digests and the current and last good pointers, reusing immutable representations,
// and records the version in meta only with the complete transaction. It never changes message
// content, hashes, text, the text index, update clocks or legacy counts. A settled archive does
// nothing; failed and absent representations count as processed, so unchanged unsupported data is
// not projected again on every sync. A database error or cancellation rolls everything back, and
// the next writable sync retries.
func (s *Store) BackfillMessageApplications(ctx context.Context) (ApplicationBackfillReport, error) {
	var rep ApplicationBackfillReport
	err := s.inTx(ctx, func(tx *sql.Tx) (e error) { rep, e = backfillMessageApplications(ctx, tx, time.Now().UTC()); return })
	if err != nil {
		return ApplicationBackfillReport{MapperVersion: activeApplicationVersion}, err
	}
	return rep, nil
}

type appBackfillRow struct {
	rowid                     int64
	id                        messageIdentity
	raw, digest               string
	rawPresent, digestPresent bool
}

func backfillMessageApplications(ctx context.Context, tx *sql.Tx, at time.Time) (ApplicationBackfillReport, error) {
	rep := ApplicationBackfillReport{MapperVersion: activeApplicationVersion}
	version := strconv.Itoa(activeApplicationVersion)
	var marker string
	if err := tx.QueryRowContext(ctx, `select coalesce((select value from meta where key=?),'')`, messageAppKey).Scan(&marker); err != nil {
		return rep, err
	}
	if marker == version {
		return rep, nil
	}
	for last := int64(0); ; {
		batch, err := readAppBackfillBatch(ctx, tx, last)
		if err != nil {
			return rep, err
		}
		if len(batch) == 0 {
			break
		}
		last = batch[len(batch)-1].rowid
		for _, m := range batch {
			raw := []byte(m.raw)
			want := rawDigest(raw, m.rawPresent)
			if m.digestPresent != m.rawPresent || m.digest != want {
				var digest any
				if m.rawPresent {
					digest = want
				}
				if _, err := tx.ExecContext(ctx, `update messages set raw_digest=? where rowid=?`, digest, m.rowid); err != nil {
					return rep, err
				}
			}
			state, err := publishMessageApplication(ctx, tx, m.id, raw, m.rawPresent, at)
			if err != nil {
				return rep, err
			}
			rep.Processed++
			switch state {
			case AppStateFailed:
				rep.Failed++
			case AppStateAbsent:
				rep.Absent++
			}
		}
	}
	_, err := tx.ExecContext(ctx, `insert into meta(key, value) values(?, ?) on conflict(key) do update set value=excluded.value`, messageAppKey, version)
	return rep, err
}

// readAppBackfillBatch reads the next batch of retained messages after rowid last, closing its rows
// before the caller writes.
func readAppBackfillBatch(ctx context.Context, tx *sql.Tx, last int64) ([]appBackfillRow, error) {
	rows, err := tx.QueryContext(ctx, `select rowid, tenant_id, user_id, conversation_id, id, sender_id, coalesce(raw_json,''), raw_json is not null, coalesce(raw_digest,''), raw_digest is not null
 from messages where rowid>? order by rowid limit ?`, last, appBackfillBatch)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []appBackfillRow
	for rows.Next() {
		var m appBackfillRow
		if err := rows.Scan(&m.rowid, &m.id.TenantID, &m.id.UserID, &m.id.ConversationID, &m.id.MessageID, &m.id.SenderID, &m.raw, &m.rawPresent, &m.digest, &m.digestPresent); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
