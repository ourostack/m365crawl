package syncer

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"time"

	"github.com/ourostack/teamscrawl/internal/store"
	"github.com/ourostack/teamscrawl/internal/teamsdesktop"
)

// Read memory: skipping records whose stored bytes have not changed.
//
// A typed record (a reply chain, a conversation, an activity item) is remembered as the digest of
// its bytes plus its effects: the rows it produced, each with the content hash the row held after
// the record was applied, and the people it named. On the next sync a record is skipped, and its
// effects replayed, only when
//
//  1. its digest matches (the digest covers MemoSignature, so a new decoder, mapper version,
//     scrub rule or deny list never matches), and
//  2. every row it produced still exists with that same hash.
//
// Given 1 and 2, reading the record in full would map it to the same rows it mapped to last time
// and find every one of them already stored: each row counts as seen and unchanged, nothing is
// written, and the people it names are merged in the same order. Replaying the effects therefore
// gives the same counts, the same change list (none), the same people merge and the same archive.
// The one way 2 can go stale inside a run is another record of the same run rewriting a row a
// skipped record produced; the writer detects that at the end of the read and the source is read
// again in full (errMemoConflict), so the result is still exact.
//
// Only a record that decoded and mapped cleanly is remembered, so a record that is an omission
// is decoded again, and counted again, on every sync.

// errMemoConflict says a record of this read rewrote a row that a skipped record had vouched for.
var errMemoConflict = errors.New("syncer: read memory conflict")

// FullReadEnv, set to 1, makes every sync read every record in full (the memory is still
// refreshed). Options.FullRead does the same for one run.
const FullReadEnv = "TEAMSCRAWL_FULL_READ"

type typedEffects struct {
	rows   []store.RowRef
	people []teamsdesktop.Person
}

const effectsVersion = 1

func (e typedEffects) encode() []byte {
	b := []byte{effectsVersion}
	b = binary.AppendUvarint(b, uint64(len(e.rows)))
	for _, r := range e.rows {
		b = append(b, r.Kind)
		b = binary.AppendVarint(b, r.Rowid)
		b = append(b, r.Hash[:]...)
	}
	b = binary.AppendUvarint(b, uint64(len(e.people)))
	for _, p := range e.people {
		for _, s := range []string{p.TenantID, p.ID, p.DisplayName} {
			b = binary.AppendUvarint(b, uint64(len(s)))
			b = append(b, s...)
		}
		ts, _ := p.SeenAt.MarshalBinary() // only fails for an out-of-range zone offset, which a parsed time never has
		b = append(b, byte(len(ts)))      //nolint:gosec // a marshaled time is 15 or 16 bytes
		b = append(b, ts...)
	}
	return b
}

var errBadEffects = errors.New("bad effects")

// decodeEffects parses encode's output. Anything it does not understand is an error, and the
// record is then read in full.
func decodeEffects(b []byte) (typedEffects, error) {
	var e typedEffects
	if len(b) == 0 || b[0] != effectsVersion {
		return e, errBadEffects
	}
	r := bytes.NewReader(b[1:])
	n, err := binary.ReadUvarint(r)
	if err != nil || n > uint64(len(b)) {
		return e, errBadEffects
	}
	for i := uint64(0); i < n; i++ {
		var ref store.RowRef
		if ref.Kind, err = r.ReadByte(); err != nil {
			return e, errBadEffects
		}
		if ref.Rowid, err = binary.ReadVarint(r); err != nil {
			return e, errBadEffects
		}
		if _, err = io.ReadFull(r, ref.Hash[:]); err != nil {
			return e, errBadEffects
		}
		e.rows = append(e.rows, ref)
	}
	if n, err = binary.ReadUvarint(r); err != nil || n > uint64(len(b)) {
		return e, errBadEffects
	}
	for i := uint64(0); i < n; i++ {
		var p teamsdesktop.Person
		for _, dst := range []*string{&p.TenantID, &p.ID, &p.DisplayName} {
			l, err := binary.ReadUvarint(r)
			if err != nil || l > uint64(r.Len()) { //nolint:gosec // Len is never negative
				return e, errBadEffects
			}
			s := make([]byte, l)
			_, _ = io.ReadFull(r, s) // cannot fail: l was checked against what is left
			*dst = string(s)
		}
		tl, err := r.ReadByte()
		if err != nil {
			return e, errBadEffects
		}
		ts := make([]byte, tl)
		if _, err := io.ReadFull(r, ts); err != nil {
			return e, errBadEffects
		}
		if p.SeenAt, err = decodeTime(ts); err != nil {
			return e, errBadEffects
		}
		e.people = append(e.people, p)
	}
	if r.Len() != 0 {
		return e, errBadEffects
	}
	return e, nil
}

func decodeTime(b []byte) (t time.Time, err error) {
	err = t.UnmarshalBinary(b)
	return t, err
}

// memoEntry is a typed record read in full this run, to be remembered when the source commits.
type memoEntry struct {
	database, keyJSON string
	digest            []byte
	eff               typedEffects
	registered        bool // the record mapped cleanly: it can be remembered
	unusable          bool // a row of it has no hash to remember
}

// memo is the writer's read memory for one source.
type memo struct {
	source string
	full   bool  // read every record in full
	err    error // the first failure of a memory read; it fails the source

	// The archive operations, fields so a test can make them fail.
	loadTyped   func(source, database string) (map[string]store.TypedMemo, error)
	rowHashes   func(kind byte) (map[int64][store.DigestLen]byte, error)
	putTyped    func(source, database, keyJSON string, m store.TypedMemo) error
	loadRecords func(source, database string) (map[[2]string]store.RecordMemo, error)

	hashes  map[byte]map[int64][store.DigestLen]byte // row hashes as the archive stood when the run began, by kind
	typedDB string
	typed   map[string]store.TypedMemo
	cur     *memoEntry // the record being read in full, until add registers it
	entries []*memoEntry
	hits    []store.RowRef              // rows vouched for by skipped records
	changed map[byte]map[int64]struct{} // rows this run inserted or updated, by kind

	genDB string
	gen   map[[2]string]store.RecordMemo

	typedSkipped, genericSkipped int // records not read in full, for tests
}

// skip is teamsdesktop.ReadOptions.Skip: it decides whether a typed record can be skipped and,
// when it can, replays what reading it would have done.
func (w *writer) skip(acct teamsdesktop.Account, _, database, keyJSON string, digest []byte) bool {
	m := w.memo
	m.cur = nil
	if m.err != nil || digest == nil {
		return false
	}
	if m.typedDB != database || m.typed == nil {
		typed, err := m.loadTyped(m.source, database)
		if err != nil {
			m.err = err
			return false
		}
		m.typed, m.typedDB = typed, database
	}
	if old, ok := m.typed[keyJSON]; ok && !m.full && bytes.Equal(old.Digest, digest) {
		if eff, err := decodeEffects(old.Effects); err == nil {
			match, err := m.match(eff.rows)
			if err != nil {
				m.err = err
				return false
			}
			if match {
				w.replay(acct, eff)
				return m.err == nil
			}
		}
	}
	m.cur = &memoEntry{database: database, keyJSON: keyJSON, digest: digest}
	return false
}

// match reports whether every referenced row held, when this run began, the hash the record left
// in it. A row a record of this run rewrites afterwards is caught by conflict.
func (m *memo) match(refs []store.RowRef) (bool, error) {
	for _, ref := range refs {
		rows, ok := m.hashes[ref.Kind]
		if !ok {
			var err error
			if rows, err = m.rowHashes(ref.Kind); err != nil {
				return false, err
			}
			m.hashes[ref.Kind] = rows
		}
		if h, ok := rows[ref.Rowid]; !ok || h != ref.Hash {
			return false, nil
		}
	}
	return true, nil
}

// replay does for a skipped record what applying it would have counted: the account is
// refreshed, every row is seen and unchanged, and the people it named are merged.
func (w *writer) replay(acct teamsdesktop.Account, eff typedEffects) {
	if err := w.ensureAccount(acct); err != nil {
		w.memo.err = err
		return
	}
	for _, ref := range eff.rows {
		switch ref.Kind {
		case store.RowConversation:
			w.counts.Conversations.Seen++
			w.counts.Conversations.Unchanged++
		case store.RowMessage:
			w.counts.Messages.Seen++
			w.counts.Messages.Unchanged++
		case store.RowActivity:
			w.counts.Activity.Seen++
			w.counts.Activity.Unchanged++
		}
	}
	w.addPeople(eff.people)
	w.memo.hits = append(w.memo.hits, eff.rows...)
	w.memo.typedSkipped++
}

// register notes that the record being read in full mapped cleanly, so it is remembered when the
// source commits, and returns the entry that collects the rows it produces. It returns nil when
// the record has no usable digest.
func (w *writer) register(people []teamsdesktop.Person) *memoEntry {
	e := w.memo.cur
	if e == nil {
		return nil
	}
	if !e.registered {
		e.registered = true
		e.eff.people = append([]teamsdesktop.Person(nil), people...)
		w.memo.entries = append(w.memo.entries, e)
	}
	return e
}

// applied collects what one Apply call did to the rows of the entries that produced them.
func (m *memo) applied(kind byte, rows []store.RowState, owners []*memoEntry) {
	for i, rs := range rows {
		if rs.Changed {
			if m.changed[kind] == nil {
				m.changed[kind] = map[int64]struct{}{}
			}
			m.changed[kind][rs.Rowid] = struct{}{}
		}
		owner := owners[i]
		if owner == nil {
			continue
		}
		ref, ok := store.RefOf(kind, rs)
		if !ok {
			owner.unusable = true
			continue
		}
		owner.eff.rows = append(owner.eff.rows, ref)
	}
}

// conflict reports whether a row this run inserted or updated is one a skipped record vouched for.
func (m *memo) conflict() bool {
	for _, ref := range m.hits {
		if _, ok := m.changed[ref.Kind][ref.Rowid]; ok {
			return true
		}
	}
	return false
}

// writeMemo remembers the records that were read in full and mapped cleanly.
func (w *writer) writeMemo() error {
	for _, e := range w.memo.entries {
		if e.unusable {
			continue
		}
		if err := w.memo.putTyped(w.memo.source, e.database, e.keyJSON, store.TypedMemo{Digest: e.digest, Effects: e.eff.encode()}); err != nil {
			return err
		}
	}
	return nil
}

// known is teamsdesktop.GenericOptions.Known: the generic record is skipped when the archive's
// live row for it was made from these same bytes.
func (w *writer) known(database, storeName, keyJSON string, digest []byte) (int, bool) {
	m := w.memo
	if m.full || m.err != nil {
		return 0, false
	}
	if m.genDB != database || m.gen == nil {
		recs, err := m.loadRecords(m.source, database)
		if err != nil {
			m.err = err
			return 0, false
		}
		m.gen, m.genDB = recs, database
	}
	rec, ok := m.gen[[2]string{storeName, keyJSON}]
	if !ok || !bytes.Equal(rec.Digest, digest) {
		return 0, false
	}
	w.counts.Records.Seen++
	w.counts.Records.Unchanged++
	m.genericSkipped++
	return rec.Redacted, true
}
