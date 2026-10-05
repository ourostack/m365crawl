package store

import (
	"context"
	"encoding/hex"
	"fmt"
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

var rowHashSQL = map[byte]string{
	RowConversation: `select rowid, content_hash from conversations`,
	RowMessage:      `select rowid, content_hash from messages`,
	RowActivity:     `select rowid, content_hash from activity`,
}

// RowHashes returns, for every row of the table kind names, the first DigestLen bytes of its
// content hash, by rowid, as the transaction sees it now. One scan answers every later question
// of whether a row still holds what a record left in it.
func (x *Session) RowHashes(kind byte) (map[int64][DigestLen]byte, error) {
	q, ok := rowHashSQL[kind]
	if !ok {
		return nil, fmt.Errorf("unknown row kind %d", kind)
	}
	rows, err := x.tx.QueryContext(context.Background(), q)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[int64][DigestLen]byte{}
	for rows.Next() {
		var rowid int64
		var hash string
		if err := rows.Scan(&rowid, &hash); err != nil {
			return nil, err
		}
		var h [DigestLen]byte
		if b, err := hex.DecodeString(hash); err == nil && len(b) >= DigestLen {
			copy(h[:], b)
			out[rowid] = h
		} // a row with no usable hash holds nothing a record could have left: it matches no ref
	}
	return out, rows.Err()
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
