package store

import (
	"context"
	"encoding/hex"
	"fmt"
	"strings"
)

// Read memory. A sync can skip a Teams record whose stored bytes have not changed since the sync
// that last committed it, because that sync left the archive exactly where reading the record
// again would leave it. Two tables hold what that takes:
//
//   - records.raw_digest and records.value_redacted: for a generic record, the digest of the
//     value's bytes that produced the row, and how many redactions the value's scrub made. They
//     live on the row, so every statement that rewrites or clears the row (UpsertRecords,
//     PurgeDenied, PurgeUnscrubbedKeys) keeps them true, and a removed row never matches.
//   - typed_memo: for a record of a typed manager (reply chains, conversations, activity), its
//     digest and an opaque effects blob the syncer encodes (the rows the record produced and the
//     people it named).
//
// Both are written in the sync's own transaction, so a rollback or a crash forgets them with the
// rows they describe.
//
// What they cost is bounded by the cache, not by the archive's age. A removed generic row keeps no
// digest (MarkRecordsRemoved, MarkDatabasesRemoved clear it; removed_at itself is never touched,
// so a record that left stays removed and one that comes back is read in full). A sync deletes
// the typed_memo rows of records that are no longer in the cache (DeleteTypedMemoKeys,
// DeleteTypedMemoOutside). A sync loads the memory of one database at a time, and the content
// hashes of the archive rows that memory names (RowHashesOf), never a whole table.

// Kinds of row a typed record produces, for RowRef.
const (
	RowConversation byte = iota + 1
	RowMessage
	RowActivity
)

// DigestLen is the length of the record digests and row hashes the memory keeps.
const DigestLen = 16

// RowRef names a row a typed record produced and the content hash the row held after that
// record was applied (the first DigestLen bytes of the stored hash).
type RowRef struct {
	Kind  byte
	Rowid int64
	Hash  [DigestLen]byte
}

// RefOf is the RowRef of an applied row. It reports false when the hash is not a full hex hash.
func RefOf(kind byte, r RowState) (RowRef, bool) {
	ref := RowRef{Kind: kind, Rowid: r.Rowid}
	b, err := hex.DecodeString(r.Hash)
	if err != nil || len(b) < DigestLen {
		return RowRef{}, false
	}
	copy(ref.Hash[:], b)
	return ref, true
}

var rowHashTable = map[byte]string{
	RowConversation: "conversations",
	RowMessage:      "messages",
	RowActivity:     "activity",
}

// rowHashChunk is how many rowids one RowHashesOf query names.
const rowHashChunk = 500

// RowHashesOf returns the first DigestLen bytes of the content hash of the rows of the table kind
// names with the given rowids, by rowid, as the transaction sees them now. A rowid with no row, or
// a row with no usable hash, is absent from the answer: it holds nothing a record could have left
// there. Asking for exactly the rows the memory references keeps the load as large as the memory,
// which the cache bounds, and not as large as the archive.
func (x *Session) RowHashesOf(kind byte, rowids []int64) (map[int64][DigestLen]byte, error) {
	table, ok := rowHashTable[kind]
	if !ok {
		return nil, fmt.Errorf("unknown row kind %d", kind)
	}
	out := map[int64][DigestLen]byte{}
	for len(rowids) > 0 {
		n := min(rowHashChunk, len(rowids))
		if err := x.rowHashChunk(table, rowids[:n], out); err != nil {
			return nil, err
		}
		rowids = rowids[n:]
	}
	return out, nil
}

// rowHashChunk adds the hashes of the given rows of table to out.
func (x *Session) rowHashChunk(table string, rowids []int64, out map[int64][DigestLen]byte) error {
	args := make([]any, len(rowids))
	for i, id := range rowids {
		args[i] = id
	}
	rows, err := x.tx.QueryContext(context.Background(), `select rowid, content_hash from `+table+` where rowid in (?`+strings.Repeat(",?", len(rowids)-1)+`)`, args...) //nolint:gosec // G202: table is from a fixed map; the rest are placeholders
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var rowid int64
		var hash string
		if err := rows.Scan(&rowid, &hash); err != nil {
			return err
		}
		if b, err := hex.DecodeString(hash); err == nil && len(b) >= DigestLen {
			var h [DigestLen]byte
			copy(h[:], b)
			out[rowid] = h
		}
	}
	return rows.Err()
}

// TypedMemo is what the archive remembers of one typed record.
type TypedMemo struct{ Digest, Effects []byte }

// LoadTypedMemo returns the remembered typed records of one database of source, by key_json.
func (x *Session) LoadTypedMemo(source, database string) (map[string]TypedMemo, error) {
	rows, err := x.tx.QueryContext(context.Background(), `select key_json, digest, effects from typed_memo where source=? and database=?`, source, database)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]TypedMemo{}
	for rows.Next() {
		var key string
		var m TypedMemo
		if err := rows.Scan(&key, &m.Digest, &m.Effects); err != nil {
			return nil, err
		}
		out[key] = m
	}
	return out, rows.Err()
}

// PutTypedMemo remembers a typed record, replacing what was remembered for its key.
func (x *Session) PutTypedMemo(source, database, keyJSON string, m TypedMemo) error {
	_, err := x.tx.ExecContext(context.Background(), `insert into typed_memo(source, database, key_json, digest, effects) values(?,?,?,?,?)
on conflict(source, database, key_json) do update set digest=excluded.digest, effects=excluded.effects`,
		source, database, keyJSON, m.Digest, m.Effects)
	return err
}

// DeleteTypedMemoKeys forgets the remembered records of one database of source with these keys.
// The archive rows the records produced are not touched.
func (x *Session) DeleteTypedMemoKeys(source, database string, keys []string) error {
	for _, k := range keys {
		if _, err := x.tx.ExecContext(context.Background(), `delete from typed_memo where source=? and database=? and key_json=?`, source, database, k); err != nil {
			return err
		}
	}
	return nil
}

// DeleteTypedMemoOutside forgets every remembered record of source whose database is not in keep
// (the databases the cache still holds), so the memory of a database that left the cache goes
// with it. Call it only after reading every database of source.
func (x *Session) DeleteTypedMemoOutside(source string, keep []string) error {
	q := `delete from typed_memo where source=?`
	args := []any{source}
	if len(keep) > 0 {
		q += ` and database not in (?` + strings.Repeat(",?", len(keep)-1) + `)` //nolint:gosec // G202: only placeholders are added
		for _, d := range keep {
			args = append(args, d)
		}
	}
	_, err := x.tx.ExecContext(context.Background(), q, args...)
	return err
}

// RecordMemo is what a live generic row remembers of the bytes it was made from.
type RecordMemo struct {
	Digest   []byte
	Redacted int
}

// LoadRecordMemos returns the live rows of one database of source that carry a digest, by
// (store, key_json). A removed row carries none that counts: it must be read again.
func (x *Session) LoadRecordMemos(source, database string) (map[[2]string]RecordMemo, error) {
	rows, err := x.tx.QueryContext(context.Background(), `select store, key_json, raw_digest, value_redacted from records where source=? and database=? and removed_at is null and raw_digest is not null and value_json is not null`, source, database)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[[2]string]RecordMemo{}
	for rows.Next() {
		var st, key string
		var m RecordMemo
		if err := rows.Scan(&st, &key, &m.Digest, &m.Redacted); err != nil {
			return nil, err
		}
		out[[2]string{st, key}] = m
	}
	return out, rows.Err()
}
