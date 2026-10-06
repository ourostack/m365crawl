package syncer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/teamscrawl/internal/errs"
	"github.com/ourostack/teamscrawl/internal/store"
	"github.com/ourostack/teamscrawl/internal/teamsdesktop"
)

// dumpArchive renders every content table of the archive, one line per row, in primary key order.
// It leaves out the read memory itself and the columns that hold when a sync ran, which differ
// between two archives built at different moments; whether such a column is set is kept.
func dumpArchive(t *testing.T, db string) string {
	t.Helper()
	d := openRaw(t, db)
	queries := []struct{ name, q string }{
		{"accounts", `select tenant_id, user_id, profile, locale, first_seen_at is not null, last_synced_at is not null from accounts order by 1, 2`},
		{"conversations", `select rowid, tenant_id, user_id, id, kind, title, topic, display_name, team_id, parent_id, members_json, last_message_at, read_horizon_at, read_horizon_client_message_id, favorite, raw_json, content_hash from conversations order by 1`},
		{"messages", `select rowid, tenant_id, user_id, conversation_id, id, reply_chain_id, parent_message_id, client_message_id, sender_id, sender_name, sent_at, edited_at, deleted_at, message_type, content_type, content_html, content_text, version, mentions_json, mentions_me, reactions_json, files_json, links_json, subject, importance, pinned, link, raw_json, content_hash from messages order by 1`},
		{"people", `select tenant_id, id, display_name, first_seen_at, last_seen_at from people order by 1, 2`},
		{"activity", `select rowid, tenant_id, user_id, id, type, subtype, is_read, at, conversation_id, message_id, reply_chain_id, app_id, raw_json, content_hash from activity order by 1`},
		{"records", `select source, tenant_id, user_id, database, store, key_json, value_json, content_hash, first_seen_at is not null, removed_at is not null from records order by 1, 4, 5, 6`},
		{"message_fts", `select rowid, message_key, content from message_fts order by 1`},
		{"conversation_fts", `select rowid, conversation_id, title from conversation_fts order by 1`},
	}
	var b strings.Builder
	for _, q := range queries {
		rows, err := d.Query(q.q)
		if err != nil {
			t.Fatalf("%s: %v", q.name, err)
		}
		cols, _ := rows.Columns()
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			fmt.Fprintf(&b, "%s %q\n", q.name, vals)
		}
		_ = rows.Close()
	}
	return b.String()
}

// reportKey is a report without the fields that hold when the sync ran.
func reportKey(t *testing.T, r Report, ch []Change) string {
	t.Helper()
	r.StartedAt, r.FinishedAt = time.Time{}, time.Time{}
	b, err := json.Marshal(struct {
		Report  Report
		Changes []Change
	}{r, ch})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// touch changes a file's modification time so the next sync sees a changed source.
var touchCount int

func touchLog(t *testing.T, root string) {
	t.Helper()
	touchCount++
	later := time.Now().Add(time.Duration(touchCount) * time.Hour)
	if err := os.Chtimes(logFile(t, root), later, later); err != nil {
		t.Fatal(err)
	}
}

func memoRows(t *testing.T, db string) (typed, withDigest int) {
	t.Helper()
	d := openRaw(t, db)
	if err := d.QueryRow(`select (select count(*) from typed_memo), (select count(*) from records where raw_digest is not null)`).Scan(&typed, &withDigest); err != nil {
		t.Fatal(err)
	}
	return typed, withDigest
}

// cacheStates is the series of cache states the equivalence tests walk through: prefixes of the
// fixture's log (records appear, and vanish again when the log is cut back) and fixture blob
// files that come and go (so records become omissions and are restored).
type cacheState struct {
	name      string
	logCut    float64  // fraction of the full log kept
	dropBlobs []string // blob files (relative to the blob directory) removed
}

func applyState(t *testing.T, root string, full []byte, blobs map[string][]byte, s cacheState) {
	t.Helper()
	cut := int(float64(len(full)) * s.logCut)
	if err := os.WriteFile(logFile(t, root), full[:cut], 0o600); err != nil { //nolint:gosec // test fixture copy
		t.Fatal(err)
	}
	blobDir, _ := filepath.Glob(filepath.Join(root, "*", "IndexedDB", "*.blob"))
	for rel, b := range blobs {
		p := filepath.Join(blobDir[0], rel)
		_ = os.Remove(p)
		dropped := false
		for _, d := range s.dropBlobs {
			dropped = dropped || d == rel
		}
		if !dropped {
			if err := os.WriteFile(p, b, 0o600); err != nil { //nolint:gosec // test fixture copy
				t.Fatal(err)
			}
		}
	}
}

func readBlobs(t *testing.T, root string) map[string][]byte {
	t.Helper()
	blobDir, _ := filepath.Glob(filepath.Join(root, "*", "IndexedDB", "*.blob"))
	out := map[string][]byte{}
	err := filepath.WalkDir(blobDir[0], func(p string, e os.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(blobDir[0], p)
		b, err := os.ReadFile(p) //nolint:gosec // test fixture copy
		out[rel] = b
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// Skipping unchanged records gives exactly the archive, the report and the change list that
// reading every record in full gives, at every step of a series of cache states, whether the
// state is new or the same one is read again.
func TestSkippingEquivalentToFullReads(t *testing.T) {
	isolateTmp(t)
	root := fixtureCopy(t)
	full, err := os.ReadFile(logFile(t, root)) //nolint:gosec // test fixture copy
	if err != nil {
		t.Fatal(err)
	}
	blobs := readBlobs(t, root)
	omit := omittingBlob(t)
	states := []cacheState{
		{"empty-ish", 0.15, nil},
		{"third", 0.35, nil},
		{"two-thirds", 0.66, nil},
		{"all", 1, nil},
		{"all, blob lost", 1, []string{omit}},
		{"all, blob back", 1, nil},
		{"cut back", 0.5, nil},
		{"all again", 1, nil},
		{"almost", 0.97, []string{omit}},
		{"all, blob back again", 1, nil},
	}
	skipDB, fullDB := newDB(t), newDB(t)
	var typedSkips, genericSkips int
	old := afterApply
	afterApply = func(w *writer) { typedSkips += w.memo.typedSkipped; genericSkips += w.memo.genericSkipped }
	t.Cleanup(func() { afterApply = old })
	for _, s := range states {
		applyState(t, root, full, blobs, s)
		for pass := 0; pass < 2; pass++ { // the second pass reads the same bytes again
			touchLog(t, root)
			sr, sc, err1 := Run(context.Background(), Options{Root: root, DBPath: skipDB})
			fr, fc, err2 := Run(context.Background(), Options{Root: root, DBPath: fullDB, FullRead: true})
			if (err1 == nil) != (err2 == nil) {
				t.Fatalf("%s pass %d: errors %v vs %v", s.name, pass, err1, err2)
			}
			if a, b := reportKey(t, sr, sc), reportKey(t, fr, fc); a != b {
				t.Fatalf("%s pass %d: reports differ\nskip: %s\nfull: %s", s.name, pass, a, b)
			}
			if a, b := dumpArchive(t, skipDB), dumpArchive(t, fullDB); a != b {
				t.Fatalf("%s pass %d: archives differ", s.name, pass)
			}
		}
	}
	if typedSkips == 0 || genericSkips == 0 {
		t.Fatalf("nothing was skipped (typed %d, generic %d): the test proves nothing", typedSkips, genericSkips)
	}
	t.Logf("skipped %d typed and %d generic records", typedSkips, genericSkips)
	// A forced full read after any number of skipped syncs changes nothing.
	before := dumpArchive(t, skipDB)
	touchLog(t, root)
	r, ch, err := Run(context.Background(), Options{Root: root, DBPath: skipDB, FullRead: true})
	if err != nil {
		t.Fatal(err)
	}
	if after := dumpArchive(t, skipDB); after != before {
		t.Fatal("a forced full read changed the archive")
	}
	if r.Messages.Inserted+r.Messages.Updated+r.Activity.Inserted+r.Activity.Updated+r.Conversations.Updated+r.Records.Inserted+r.Records.Updated != 0 || len(ch) != 0 {
		t.Fatalf("a forced full read wrote: %+v", r)
	}
	_ = store.SchemaVersion
}

func TestEffectsRoundTrip(t *testing.T) {
	var h [store.DigestLen]byte
	for i := range h {
		h[i] = byte(i)
	}
	zone := time.FixedZone("x", 5*3600+1800)
	in := typedEffects{
		rows: []store.RowRef{{Kind: store.RowMessage, Rowid: 1, Hash: h}, {Kind: store.RowConversation, Rowid: 1 << 40, Hash: h}, {Kind: store.RowActivity, Rowid: 3, Hash: h}},
		people: []teamsdesktop.Person{
			{TenantID: "t", ID: "8:orgid:a", DisplayName: "Ada", SeenAt: time.Date(2026, 10, 1, 12, 30, 15, 123456789, time.UTC)},
			{TenantID: "t", ID: "8:orgid:b", DisplayName: "", SeenAt: time.Time{}},
			{TenantID: "t", ID: "8:orgid:c", DisplayName: "Zoë Ünï", SeenAt: time.Date(2026, 10, 1, 12, 30, 15, 5, zone)},
		},
	}
	b := in.encode()
	out, err := decodeEffects(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.rows) != 3 || out.rows[1] != in.rows[1] || len(out.people) != 3 {
		t.Fatalf("decoded %+v", out)
	}
	for i, p := range in.people {
		q := out.people[i]
		if q.TenantID != p.TenantID || q.ID != p.ID || q.DisplayName != p.DisplayName || !q.SeenAt.Equal(p.SeenAt) || q.SeenAt.IsZero() != p.SeenAt.IsZero() {
			t.Fatalf("person %d: %+v became %+v", i, p, q)
		}
	}
	if e, err := decodeEffects(typedEffects{}.encode()); err != nil || len(e.rows) != 0 || len(e.people) != 0 {
		t.Fatalf("empty effects: %+v %v", e, err)
	}
	// Anything short of the whole, or longer, or of another version, is refused, never half-read.
	for n := 0; n < len(b); n++ {
		if _, err := decodeEffects(b[:n]); err == nil {
			t.Fatalf("a %d-byte prefix of %d decoded", n, len(b))
		}
	}
	if _, err := decodeEffects(append(append([]byte(nil), b...), 0)); err == nil {
		t.Fatal("trailing bytes accepted")
	}
	wrong := append([]byte(nil), b...)
	wrong[0] = effectsVersion + 1
	if _, err := decodeEffects(wrong); err == nil {
		t.Fatal("another version accepted")
	}
	// Counts larger than the data are refused before anything is allocated for them.
	for _, huge := range [][]byte{{effectsVersion, 0, 0xff, 0xff, 0xff, 0xff, 0x0f}, {effectsVersion, 0, 0, 0xff, 0xff, 0xff, 0xff, 0x0f}} {
		if _, err := decodeEffects(huge); err == nil {
			t.Fatalf("%x accepted", huge)
		}
	}
	// The contended flag round-trips, and any other flag byte is refused.
	flagged := typedEffects{rows: in.rows, contended: true}.encode()
	if e, err := decodeEffects(flagged); err != nil || !e.contended || len(e.rows) != 3 {
		t.Fatalf("contended effects: %+v %v", e, err)
	}
	flagged[1] = 2
	if _, err := decodeEffects(flagged); err == nil {
		t.Fatal("an unknown flag byte accepted")
	}
	// A time that does not parse is refused.
	bad := typedEffects{people: []teamsdesktop.Person{{TenantID: "t", ID: "i", SeenAt: time.Unix(1, 0)}}}.encode()
	bad[len(bad)-15] = 9 // the time's version byte
	if _, err := decodeEffects(bad); err == nil {
		t.Fatal("a bad time accepted")
	}
}

func TestMemoConflictNeedsAChangedVouchedRow(t *testing.T) {
	m := &memo{changed: map[byte]map[int64]struct{}{}}
	if m.conflict() {
		t.Fatal("conflict with nothing")
	}
	m.hits = []store.RowRef{{Kind: store.RowMessage, Rowid: 5}, {Kind: store.RowActivity, Rowid: 7}}
	m.changed[store.RowMessage] = map[int64]struct{}{6: {}}
	m.changed[store.RowConversation] = map[int64]struct{}{5: {}, 7: {}} // same rowids, other tables
	if m.conflict() {
		t.Fatal("a row of another table is not a conflict")
	}
	m.changed[store.RowActivity] = map[int64]struct{}{7: {}}
	if !m.conflict() {
		t.Fatal("a vouched row that changed is a conflict")
	}
}

type memoRun struct {
	typedSkips, genericSkips int
	full                     bool
}

// watchApplies records, for each applied source read, how much was skipped and whether it was full.
func watchApplies(t *testing.T) *[]memoRun {
	t.Helper()
	var runs []memoRun
	old := afterApply
	afterApply = func(w *writer) {
		runs = append(runs, memoRun{w.memo.typedSkipped, w.memo.genericSkipped, w.memo.full})
	}
	t.Cleanup(func() { afterApply = old })
	return &runs
}

// A re-sync of an unchanged-bytes cache skips every typed and generic record it can, yet reports
// the same counts a full read reports, and writes nothing.
func TestResyncSkipsAndReportsTheSameCounts(t *testing.T) {
	isolateTmp(t)
	root := fixtureCopy(t)
	runs := watchApplies(t)
	db := newDB(t)
	first, _ := run(t, Options{Root: root, DBPath: db})
	if (*runs)[0].typedSkips != 0 || (*runs)[0].genericSkips != 0 {
		t.Fatalf("a first sync skipped: %+v", (*runs)[0])
	}
	if typed, digests := memoRows(t, db); typed == 0 || digests != fixtureRecords {
		t.Fatalf("memory after the first sync: %d typed, %d record digests", typed, digests)
	}
	touchLog(t, root)
	again, ch := run(t, Options{Root: root, DBPath: db})
	r := (*runs)[1]
	if r.typedSkips == 0 || r.genericSkips != fixtureRecords {
		t.Fatalf("second sync skipped %+v", r)
	}
	if len(ch) != 0 || again.Messages.Seen != fixtureMessages || again.Messages.Unchanged != fixtureMessages ||
		again.Conversations.Unchanged != fixtureConversations || again.Activity.Unchanged != fixtureActivity ||
		again.Records.Seen != fixtureRecords || again.Records.Unchanged != fixtureRecords {
		t.Fatalf("counts: %+v", again)
	}
	if again.People.Seen != first.People.Seen || again.People.Unchanged != first.People.Seen || again.Redacted != first.Redacted {
		t.Fatalf("people %+v vs first %+v; redacted %d vs %d", again.People, first.People, again.Redacted, first.Redacted)
	}
	if first.Redacted == 0 {
		t.Log("the fixture redacts nothing; redaction replay is covered by the generic record tests")
	}
}

// Anything that changes how bytes become rows (here: the signature, as a DecoderVersion bump
// changes it) makes every remembered digest stop matching, and the next sync remembers afresh.
func TestSignatureChangeRereadsEverything(t *testing.T) {
	isolateTmp(t)
	root := fixtureCopy(t)
	runs := watchApplies(t)
	skipDB, fullDB := newDB(t), newDB(t)
	run(t, Options{Root: root, DBPath: skipDB})
	run(t, Options{Root: root, DBPath: fullDB, FullRead: true})

	old := memoSignature
	t.Cleanup(func() { memoSignature = old })
	memoSignature = func(v int) []byte { return append(old(v), "bumped"...) }
	touchLog(t, root)
	sr, sc := run(t, Options{Root: root, DBPath: skipDB})
	fr, fc := run(t, Options{Root: root, DBPath: fullDB, FullRead: true})
	if r := (*runs)[2]; r.typedSkips != 0 || r.genericSkips != 0 {
		t.Fatalf("a new signature still skipped: %+v", r)
	}
	if reportKey(t, sr, sc) != reportKey(t, fr, fc) || dumpArchive(t, skipDB) != dumpArchive(t, fullDB) {
		t.Fatal("a re-read under a new signature differs from a full read")
	}
	touchLog(t, root)
	run(t, Options{Root: root, DBPath: skipDB})
	if r := (*runs)[4]; r.typedSkips == 0 || r.genericSkips == 0 {
		t.Fatalf("the memory was not rebuilt under the new signature: %+v", r)
	}
}

// A run for one account must not make another account's remembered records look current: the
// digests are per record, so after a signature change a filtered run refreshes only its own.
func TestFilteredRunDoesNotPoisonOtherAccounts(t *testing.T) {
	isolateTmp(t)
	root := fixtureCopy(t)
	runs := watchApplies(t)
	skipDB, fullDB := newDB(t), newDB(t)
	run(t, Options{Root: root, DBPath: skipDB})
	run(t, Options{Root: root, DBPath: fullDB, FullRead: true})

	old := memoSignature
	t.Cleanup(func() { memoSignature = old })
	memoSignature = func(v int) []byte { return append(old(v), "bumped"...) }
	// Only account A is read under the new signature.
	touchLog(t, root)
	run(t, Options{Root: root, DBPath: skipDB, Account: &acctA})
	run(t, Options{Root: root, DBPath: fullDB, Account: &acctA, FullRead: true})
	// Then everyone: A's records may be skipped, B's must be read in full.
	touchLog(t, root)
	sr, sc := run(t, Options{Root: root, DBPath: skipDB})
	fr, fc := run(t, Options{Root: root, DBPath: fullDB, FullRead: true})
	r := (*runs)[4]
	if r.typedSkips == 0 {
		t.Fatalf("account A's refreshed records were not skipped: %+v", r)
	}
	all := (*runs)[2].typedSkips + 0
	_ = all
	if reportKey(t, sr, sc) != reportKey(t, fr, fc) || dumpArchive(t, skipDB) != dumpArchive(t, fullDB) {
		t.Fatal("an unfiltered run after a filtered one differs from a full read")
	}
	// B's records were read in full, not skipped: fewer typed skips than a run where all match.
	touchLog(t, root)
	run(t, Options{Root: root, DBPath: skipDB})
	if full, partial := (*runs)[6].typedSkips, r.typedSkips; full <= partial {
		t.Fatalf("a run where every record matches skipped %d, the run after the filtered one %d", full, partial)
	}
}

// A record that failed to decode is never remembered, so it is decoded again, and counted
// again, on every sync.
func TestOmissionsAreRecountedOnEverySync(t *testing.T) {
	isolateTmp(t)
	rel := omittingBlob(t)
	root := fixtureCopy(t)
	blobDir, _ := filepath.Glob(filepath.Join(root, "*", "IndexedDB", "*.blob"))
	if err := os.Remove(filepath.Join(blobDir[0], rel)); err != nil {
		t.Fatal(err)
	}
	runs := watchApplies(t)
	db := newDB(t)
	first, _ := run(t, Options{Root: root, DBPath: db})
	for i := 0; i < 2; i++ {
		touchLog(t, root)
		r, _ := run(t, Options{Root: root, DBPath: db})
		if r.Status != "ok_with_omissions" || r.Omissions["blob_missing"] != 1 || r.Sources[0].Omissions["blob_missing"] != 1 {
			t.Fatalf("sync %d lost the omission: %v", i+2, r.Omissions)
		}
		if (*runs)[i+1].typedSkips == 0 {
			t.Fatalf("sync %d skipped nothing", i+2)
		}
	}
	if first.Omissions["blob_missing"] != 1 {
		t.Fatalf("first: %v", first.Omissions)
	}
}

// A blob-backed value is digested by what the blob holds: a blob file whose bytes change under an
// unchanged pointer makes the record be read again.
func TestBlobContentIsPartOfTheDigest(t *testing.T) {
	isolateTmp(t)
	rel := omittingBlob(t)
	root := fixtureCopy(t)
	blobDir, _ := filepath.Glob(filepath.Join(root, "*", "IndexedDB", "*.blob"))
	other := ""
	_ = filepath.WalkDir(blobDir[0], func(p string, e os.DirEntry, err error) error {
		if err == nil && !e.IsDir() {
			if r, _ := filepath.Rel(blobDir[0], p); r != rel && other == "" {
				other = r
			}
		}
		return nil
	})
	if other == "" {
		t.Fatal("the fixture has one blob file only")
	}
	skipDB, fullDB := newDB(t), newDB(t)
	run(t, Options{Root: root, DBPath: skipDB})
	run(t, Options{Root: root, DBPath: fullDB, FullRead: true})
	// Same pointer, other bytes: the blob file now holds another record's value.
	b, err := os.ReadFile(filepath.Join(blobDir[0], other)) //nolint:gosec // test fixture copy
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(blobDir[0], rel), b, 0o600); err != nil { //nolint:gosec // test fixture copy
		t.Fatal(err)
	}
	touchLog(t, root)
	sr, sc, e1 := Run(context.Background(), Options{Root: root, DBPath: skipDB})
	fr, fc, e2 := Run(context.Background(), Options{Root: root, DBPath: fullDB, FullRead: true})
	if (e1 == nil) != (e2 == nil) {
		t.Fatalf("errors %v vs %v", e1, e2)
	}
	if reportKey(t, sr, sc) != reportKey(t, fr, fc) || dumpArchive(t, skipDB) != dumpArchive(t, fullDB) {
		t.Fatal("a changed blob was not read again")
	}
	if dumpArchive(t, skipDB) == "" {
		t.Fatal("empty dump")
	}
}

// If a row a skipped record vouched for no longer holds what was remembered, the record is read
// in full (and the row is put right), exactly as a full read does.
func TestRowsThatMovedForceAFullReadOfTheRecord(t *testing.T) {
	isolateTmp(t)
	root := fixtureCopy(t)
	skipDB, fullDB := newDB(t), newDB(t)
	run(t, Options{Root: root, DBPath: skipDB})
	run(t, Options{Root: root, DBPath: fullDB, FullRead: true})
	for _, db := range []string{skipDB, fullDB} {
		d := openRaw(t, db)
		for _, q := range []string{
			`update messages set content_hash='moved' where rowid in (select rowid from messages order by rowid limit 3)`,
			`update conversations set content_hash='moved' where rowid = (select min(rowid) from conversations)`,
			`update activity set content_hash='moved' where rowid = (select min(rowid) from activity)`,
		} {
			exec(t, d, q)
		}
	}
	touchLog(t, root)
	runs := watchApplies(t)
	sr, sc := run(t, Options{Root: root, DBPath: skipDB})
	fr, fc := run(t, Options{Root: root, DBPath: fullDB, FullRead: true})
	if sr.Messages.Updated == 0 || sr.Conversations.Updated == 0 || sr.Activity.Updated == 0 {
		t.Fatalf("the moved rows were not put right: %+v", sr)
	}
	if reportKey(t, sr, sc) != reportKey(t, fr, fc) || dumpArchive(t, skipDB) != dumpArchive(t, fullDB) {
		t.Fatal("moved rows: skipping differs from a full read")
	}
	if (*runs)[0].typedSkips == 0 {
		t.Fatal("the unmoved records were not skipped")
	}
}

// Effects that cannot be read are no reason to fail: the record is read in full.
func TestUnreadableEffectsMeanAFullRead(t *testing.T) {
	isolateTmp(t)
	root := fixtureCopy(t)
	skipDB, fullDB := newDB(t), newDB(t)
	run(t, Options{Root: root, DBPath: skipDB})
	run(t, Options{Root: root, DBPath: fullDB, FullRead: true})
	exec(t, openRaw(t, skipDB), `update typed_memo set effects = x'00'`)
	touchLog(t, root)
	runs := watchApplies(t)
	sr, sc := run(t, Options{Root: root, DBPath: skipDB})
	fr, fc := run(t, Options{Root: root, DBPath: fullDB, FullRead: true})
	if (*runs)[0].typedSkips != 0 {
		t.Fatalf("records with unreadable effects were skipped: %+v", (*runs)[0])
	}
	if reportKey(t, sr, sc) != reportKey(t, fr, fc) || dumpArchive(t, skipDB) != dumpArchive(t, fullDB) {
		t.Fatal("unreadable effects: differs from a full read")
	}
}

// TEAMSCRAWL_FULL_READ=1 and Options.FullRead both read every record in full.
func TestFullReadSwitches(t *testing.T) {
	isolateTmp(t)
	root := fixtureCopy(t)
	db := newDB(t)
	run(t, Options{Root: root, DBPath: db})
	runs := watchApplies(t)
	touchLog(t, root)
	run(t, Options{Root: root, DBPath: db, FullRead: true})
	t.Setenv(FullReadEnv, "1")
	touchLog(t, root)
	run(t, Options{Root: root, DBPath: db})
	t.Setenv(FullReadEnv, "")
	touchLog(t, root)
	run(t, Options{Root: root, DBPath: db})
	if a, b, c := (*runs)[0], (*runs)[1], (*runs)[2]; a.typedSkips != 0 || !a.full || b.typedSkips != 0 || !b.full || c.typedSkips == 0 || c.full {
		t.Fatalf("runs = %+v", *runs)
	}
}

// When a record rewrites a row a skipped record vouched for, the source is read again in full
// from the same snapshot, and the outcome is a full read's.
func TestMemoConflictRereadsInFull(t *testing.T) {
	isolateTmp(t)
	root := fixtureCopy(t)
	skipDB, fullDB := newDB(t), newDB(t)
	run(t, Options{Root: root, DBPath: skipDB})
	run(t, Options{Root: root, DBPath: fullDB, FullRead: true})
	touchLog(t, root)
	runs := watchApplies(t)
	old := conflictFn
	t.Cleanup(func() { conflictFn = old })
	calls := 0
	conflictFn = func(m *memo) bool { calls++; return calls == 1 }
	sr, sc := run(t, Options{Root: root, DBPath: skipDB})
	fr, fc := run(t, Options{Root: root, DBPath: fullDB, FullRead: true})
	// The first attempt never reached afterApply (it was rolled back); the retry was full.
	if len(*runs) != 2 || (*runs)[0].typedSkips != 0 || !(*runs)[0].full {
		t.Fatalf("the retry skipped: %+v", (*runs)[0])
	}
	if reportKey(t, sr, sc) != reportKey(t, fr, fc) || dumpArchive(t, skipDB) != dumpArchive(t, fullDB) {
		t.Fatal("the retried source differs from a full read")
	}
}

// Failures of the archive operations the memory needs fail the source with a coded error and keep
// nothing.
func TestMemoFailuresFailTheSource(t *testing.T) {
	for _, name := range []string{"load typed", "rows match", "put typed", "load records"} {
		t.Run(name, func(t *testing.T) {
			isolateTmp(t)
			root := fixtureCopy(t)
			db := newDB(t)
			if name != "put typed" { // there is something to remember only when records are read in full
				run(t, Options{Root: root, DBPath: db})
				touchLog(t, root)
			}
			boom := errors.New("injected memory failure")
			old := beforeRead
			t.Cleanup(func() { beforeRead = old })
			beforeRead = func(w *writer) {
				switch name {
				case "load typed":
					w.memo.loadTyped = func(string, string) (map[string]store.TypedMemo, error) { return nil, boom }
				case "rows match":
					w.memo.rowHashes = func(byte, []int64) (map[int64][store.DigestLen]byte, error) { return nil, boom }
				case "put typed":
					w.memo.putTyped = func(string, string, string, store.TypedMemo) error { return boom }
				case "load records":
					w.memo.loadRecords = func(string, string) (map[[2]string]store.RecordMemo, error) { return nil, boom }
				}
			}
			before := ""
			if name != "put typed" {
				before = dumpArchive(t, db)
			}
			_, _, err := Run(context.Background(), Options{Root: root, DBPath: db})
			codedErr(t, err, errs.CodeDBError)
			if !strings.Contains(err.Error(), "injected memory failure") {
				t.Fatalf("cause lost: %v", err)
			}
			if name == "put typed" {
				requireNothingKept(t, db)
			} else if after := dumpArchive(t, db); after != before {
				t.Fatal("a failed source changed the archive")
			}
		})
	}
}

// The memory commits with the rows, and is rolled back with them.
func TestMemoRollsBackWithTheSource(t *testing.T) {
	isolateTmp(t)
	hookFlush(t, 10, func(kind string, _ int) error {
		if kind == "activity" {
			return errors.New("injected activity failure")
		}
		return nil
	})
	db := newDB(t)
	if _, _, err := Run(context.Background(), Options{Root: fixtureRoot, DBPath: db}); err == nil {
		t.Fatal("the injected failure did not fail the run")
	}
	if typed, digests := memoRows(t, db); typed != 0 || digests != 0 {
		t.Fatalf("a rolled-back source left memory: %d typed, %d record digests", typed, digests)
	}
	// And a store failure while remembering rolls everything back too.
	beforeFlush = func(string, int) error { return nil }
	db2 := archiveWith(t, `create trigger boom before insert on typed_memo begin select raise(abort, 'injected'); end;`)
	_, _, err := Run(context.Background(), Options{Root: fixtureRoot, DBPath: db2})
	codedErr(t, err, errs.CodeDBError)
	requireNothingKept(t, db2)
}

// A row with no usable hash can not be vouched for, so the record that produced it is not
// remembered; rows nobody owns are only noted as changed.
func TestUnusableRowsAreNotRemembered(t *testing.T) {
	m := &memo{changed: map[byte]map[int64]struct{}{}}
	good := store.RowState{Rowid: 1, Hash: strings.Repeat("ab", 32), Changed: true}
	bad := store.RowState{Rowid: 2, Hash: "not-hex", Changed: true}
	owner, other := &memoEntry{}, &memoEntry{}
	m.applied(store.RowMessage, []store.RowState{good, bad, good}, []*memoEntry{owner, owner, nil})
	m.applied(store.RowMessage, []store.RowState{good}, []*memoEntry{other})
	if !owner.unusable || len(owner.eff.rows) != 1 || other.unusable || len(other.eff.rows) != 1 {
		t.Fatalf("owner %+v other %+v", owner, other)
	}
	if _, ok := m.changed[store.RowMessage][2]; !ok || len(m.changed[store.RowMessage]) != 2 {
		t.Fatalf("changed = %v", m.changed)
	}
	// writeMemo skips the unusable entry.
	var put []string
	w := &writer{memo: &memo{source: "s", putTyped: func(_, _, key string, _ store.TypedMemo) error { put = append(put, key); return nil },
		deleteKeys: func(string, string, []string) error { return nil }, deleteDBs: func(string, []string) error { return nil }}}
	w.memo.entries = []*memoEntry{{keyJSON: "bad", unusable: true}, {keyJSON: "good"}}
	if err := w.writeMemo(); err != nil || len(put) != 1 || put[0] != "good" {
		t.Fatalf("put %v, %v", put, err)
	}
}

// Refreshing the account of a skipped record can fail like any write; the source then fails.
func TestAccountFailureWhileSkippingFailsTheSource(t *testing.T) {
	isolateTmp(t)
	root := fixtureCopy(t)
	db := newDB(t)
	run(t, Options{Root: root, DBPath: db})
	exec(t, openRaw(t, db), `create trigger boom before update on accounts begin select raise(abort, 'injected'); end;`)
	touchLog(t, root)
	_, _, err := Run(context.Background(), Options{Root: root, DBPath: db})
	codedErr(t, err, errs.CodeDBError)
}

// A forced full read does not stop at the fingerprint shortcut: when the cache has not changed
// since the last sync it still reads every record, in full, and says so in the report. Without
// the force, the same unchanged cache is "unchanged" and reads nothing.
func TestFullReadBypassesTheFingerprintShortcut(t *testing.T) {
	isolateTmp(t)
	root := fixtureCopy(t)
	db := newDB(t)
	runs := watchApplies(t)
	run(t, Options{Root: root, DBPath: db})
	quiet, _ := run(t, Options{Root: root, DBPath: db}) // nothing changed
	if quiet.Status != StatusUnchanged || len(*runs) != 1 {
		t.Fatalf("an unchanged cache must stay unchanged without the force: %q, %d reads", quiet.Status, len(*runs))
	}
	for name, o := range map[string]func(*testing.T) Options{
		"option": func(*testing.T) Options { return Options{Root: root, DBPath: db, FullRead: true} },
		"env": func(t *testing.T) Options {
			t.Setenv(FullReadEnv, "1")
			return Options{Root: root, DBPath: db}
		},
	} {
		t.Run(name, func(t *testing.T) {
			n := len(*runs)
			forced, _ := run(t, o(t)) // the same bytes, the same fingerprint
			if forced.Status == StatusUnchanged || len(*runs) != n+1 {
				t.Fatalf("the forced read did not run: status %q, %d reads", forced.Status, len(*runs)-n)
			}
			if r := (*runs)[n]; !r.full || r.typedSkips != 0 || r.genericSkips != 0 {
				t.Fatalf("the forced read skipped or was not full: %+v", r)
			}
			if forced.Messages.Seen != fixtureMessages || forced.Messages.Unchanged != fixtureMessages || forced.Records.Seen != fixtureRecords {
				t.Fatalf("counts: %+v", forced)
			}
		})
	}
}

// The read memory is bounded by the cache, not by the archive's age: when records leave the cache,
// the next sync deletes their typed memory and the digest of their generic row, while the archive
// rows themselves stay (removed_at stays set).
func TestMemoIsPrunedWhenRecordsLeaveTheCache(t *testing.T) {
	isolateTmp(t)
	root := fixtureCopy(t)
	full, err := os.ReadFile(logFile(t, root)) //nolint:gosec // test fixture copy
	if err != nil {
		t.Fatal(err)
	}
	blobs := readBlobs(t, root)
	db, fresh := newDB(t), newDB(t)
	applyState(t, root, full, blobs, cacheState{"all", 1, nil})
	run(t, Options{Root: root, DBPath: db})
	allTyped, allDigests := memoRows(t, db)
	applyState(t, root, full, blobs, cacheState{"cut", 0.4, nil})
	touchLog(t, root)
	run(t, Options{Root: root, DBPath: db})
	run(t, Options{Root: root, DBPath: fresh})
	typed, digests := memoRows(t, db)
	wantTyped, wantDigests := memoRows(t, fresh)
	if typed >= allTyped || digests >= allDigests {
		t.Fatalf("the cut removed nothing: %d/%d typed, %d/%d digests", typed, allTyped, digests, allDigests)
	}
	if typed != wantTyped || digests != wantDigests {
		t.Fatalf("memory after the cut: %d typed, %d digests; a fresh archive of the same cache has %d and %d", typed, digests, wantTyped, wantDigests)
	}
	d := openRaw(t, db)
	var removedWithDigest, removed int
	if err := d.QueryRow(`select count(*) filter (where raw_digest is not null), count(*) from records where removed_at is not null`).Scan(&removedWithDigest, &removed); err != nil {
		t.Fatal(err)
	}
	if removed == 0 || removedWithDigest != 0 {
		t.Fatalf("%d removed rows, %d of them still carry a digest", removed, removedWithDigest)
	}
	// The archive rows of the departed records are kept, as before.
	if a, b := dumpArchive(t, db), dumpArchive(t, fresh); a == b {
		t.Fatal("the departed records vanished from the archive")
	}
	// Reading the cache back in full restores them: a removed row never matches, so it is read in full.
	applyState(t, root, full, blobs, cacheState{"all", 1, nil})
	touchLog(t, root)
	run(t, Options{Root: root, DBPath: db})
	if typed, digests := memoRows(t, db); typed != allTyped || digests != allDigests {
		t.Fatalf("memory after the cache came back: %d typed, %d digests; want %d, %d", typed, digests, allTyped, allDigests)
	}
}

// A run for one account only prunes that account's memory: it never saw the others' databases, so
// it can not say their records left.
func TestFilteredRunDoesNotPruneOtherAccountsMemory(t *testing.T) {
	isolateTmp(t)
	root := fixtureCopy(t)
	db := newDB(t)
	run(t, Options{Root: root, DBPath: db})
	typed, digests := memoRows(t, db)
	acct := &teamsdesktop.Account{TenantID: "00000000-0000-4000-8000-000000000001", UserID: "00000000-0000-4000-8000-0000000000a1"}
	touchLog(t, root)
	run(t, Options{Root: root, DBPath: db, Account: acct})
	if t2, d2 := memoRows(t, db); t2 != typed || d2 != digests {
		t.Fatalf("a filtered run changed the memory: %d/%d typed, %d/%d digests", t2, typed, d2, digests)
	}
}

// Forgetting records that left the cache can fail like any write; the source then fails and keeps nothing.
func TestPruneFailuresFailTheSource(t *testing.T) {
	for _, name := range []string{"keys", "databases"} {
		t.Run(name, func(t *testing.T) {
			isolateTmp(t)
			root := fixtureCopy(t)
			full, err := os.ReadFile(logFile(t, root)) //nolint:gosec // test fixture copy
			if err != nil {
				t.Fatal(err)
			}
			blobs := readBlobs(t, root)
			db := newDB(t)
			run(t, Options{Root: root, DBPath: db})
			applyState(t, root, full, blobs, cacheState{"cut", 0.8, nil}) // 0.8, not 0.4: with the calendar databases added, a cut at 0.4 no longer leaves a fully read database missing keys, so nothing is pruned
			touchLog(t, root)
			boom := errors.New("injected prune failure")
			old := beforeRead
			t.Cleanup(func() { beforeRead = old })
			beforeRead = func(w *writer) {
				if name == "keys" {
					w.memo.deleteKeys = func(string, string, []string) error { return boom }
				} else {
					w.memo.deleteDBs = func(string, []string) error { return boom }
				}
			}
			before := dumpArchive(t, db)
			typed, digests := memoRows(t, db)
			_, _, err = Run(context.Background(), Options{Root: root, DBPath: db})
			codedErr(t, err, errs.CodeDBError)
			if !strings.Contains(err.Error(), "injected prune failure") {
				t.Fatalf("cause lost: %v", err)
			}
			if after := dumpArchive(t, db); after != before {
				t.Fatal("a failed source changed the archive")
			}
			if t2, d2 := memoRows(t, db); t2 != typed || d2 != digests {
				t.Fatal("a failed source changed the memory")
			}
		})
	}
}

// The hashes of archive rows are loaded for the rows the memory names, not for whole tables.
func TestRowHashesAreLoadedOnlyForRowsTheMemoryNames(t *testing.T) {
	isolateTmp(t)
	root := fixtureCopy(t)
	db := newDB(t)
	run(t, Options{Root: root, DBPath: db})
	asked := map[byte]int{}
	old := beforeRead
	t.Cleanup(func() { beforeRead = old })
	beforeRead = func(w *writer) {
		real := w.memo.rowHashes
		w.memo.rowHashes = func(kind byte, ids []int64) (map[int64][store.DigestLen]byte, error) {
			asked[kind] += len(ids)
			return real(kind, ids)
		}
	}
	touchLog(t, root)
	run(t, Options{Root: root, DBPath: db})
	if asked[store.RowMessage] != fixtureMessages || asked[store.RowConversation] != fixtureConversations || asked[store.RowActivity] != fixtureActivity {
		t.Fatalf("hashes asked for: %v; the memory names %d messages, %d conversations, %d activity items", asked, fixtureMessages, fixtureConversations, fixtureActivity)
	}
}

// addDuplicateRecord appends to the cache's log a second reply chain record, under another key,
// that carries the same message (same id, same version) with other text: two records of one
// source that produce one typed row with different content. No hook is involved; it is bytes in a
// cache. edit is applied to the copy of the record's value (same length).
func addDuplicateRecord(t *testing.T, root string, edit func(val []byte) []byte) {
	t.Helper()
	path := logFile(t, root)
	batches := readLogBatches(t, path)
	var dup *logEntry
	for _, b := range batches {
		for _, e := range b.entries {
			if dup == nil && e.typ == 1 && bytes.HasPrefix(e.key, []byte{0, 1, 1, 1}) && bytes.Contains(e.val, []byte("<p>Hello from Alex Fixture</p>")) {
				c := logEntry{1, append([]byte(nil), e.key...), edit(append([]byte(nil), e.val...))}
				dup = &c
			}
		}
	}
	if dup == nil {
		t.Fatal("the fixture has no reply chain record to duplicate")
	}
	dup.key[len(dup.key)-1] = '9' // the same key text with another last character: a different record
	batches = append(batches, logBatch{seq: nextSeq(batches), entries: []logEntry{*dup}})
	writeLogBatches(t, path, batches)
}

// Two records of one source that produce one message row at the same version with different text
// leave a skipping sync equal to a full read on every sync, and a sync costs one read of the
// source, not two: the conflict is remembered, so the records that share the row are read in full
// from the start, without first being skipped and rolled back.
func TestTwoRecordsOneTypedRowCostOneReadAndMatchAFullRead(t *testing.T) {
	isolateTmp(t)
	root := fixtureCopy(t)
	addDuplicateRecord(t, root, func(val []byte) []byte {
		return bytes.Replace(val, []byte("<p>Hello from"), []byte("<p>Hxllo from"), 1)
	})
	reads := 0
	old := beforeRead
	beforeRead = func(*writer) { reads++ }
	t.Cleanup(func() { beforeRead = old })
	skipDB, fullDB := newDB(t), newDB(t)
	for i := 1; i <= 6; i++ {
		touchLog(t, root)
		reads = 0
		sr, sc, err1 := Run(context.Background(), Options{Root: root, DBPath: skipDB})
		skipReads := reads
		fr, fc, err2 := Run(context.Background(), Options{Root: root, DBPath: fullDB, FullRead: true})
		if err1 != nil || err2 != nil {
			t.Fatalf("sync %d: %v / %v", i, err1, err2)
		}
		if a, b := reportKey(t, sr, sc), reportKey(t, fr, fc); a != b {
			t.Fatalf("sync %d: reports differ\nskip: %s\nfull: %s", i, a, b)
		}
		if a, b := dumpArchive(t, skipDB), dumpArchive(t, fullDB); a != b {
			t.Fatalf("sync %d: archives differ", i)
		}
		if i >= 3 && skipReads != 1 {
			t.Fatalf("sync %d read the source %d times; the conflict must be remembered, so one read is enough", i, skipReads)
		}
	}
}

// A record is contended when another record of the read produced one of its rows, whether that
// record was read in full or skipped; a row a record names twice is not contention.
func TestMarkContended(t *testing.T) {
	ref := func(id int64) store.RowRef { return store.RowRef{Kind: store.RowMessage, Rowid: id} }
	shared, alone, twice, vouched := &memoEntry{}, &memoEntry{}, &memoEntry{}, &memoEntry{}
	m := &memo{entries: []*memoEntry{
		shared, {eff: typedEffects{rows: []store.RowRef{ref(1)}}}, alone, twice, vouched,
	}}
	shared.eff.rows = []store.RowRef{ref(1), ref(2)}
	alone.eff.rows = []store.RowRef{ref(3)}
	twice.eff.rows = []store.RowRef{ref(4), ref(4)}
	vouched.eff.rows = []store.RowRef{ref(5)}
	m.hits = []store.RowRef{ref(5), {Kind: store.RowConversation, Rowid: 3}}
	m.markContended()
	got := []bool{shared.eff.contended, m.entries[1].eff.contended, alone.eff.contended, twice.eff.contended, vouched.eff.contended}
	want := []bool{true, true, false, false, true}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("contended = %v, want %v", got, want)
		}
	}
}

// A version 3 archive as v0.2.0 writes it (the records table without the read-memory columns, no
// typed_memo table, schema version 3, every row of the fixture synced) upgrades in place on the
// next sync: nothing is lost, the first sync reads every record and counts them as a full read
// does, remembers them, and the sync after that skips.
func TestUpgradeFromAVersion3ArchiveAsV020WritesIt(t *testing.T) {
	isolateTmp(t)
	root := fixtureCopy(t)
	db, fresh := newDB(t), newDB(t)
	run(t, Options{Root: root, DBPath: db})
	d := openRaw(t, db)
	for _, q := range []string{
		`alter table records drop column raw_digest`, `alter table records drop column value_redacted`,
		`drop table typed_memo`, `update schema_migrations set version = 3`,
	} {
		if _, err := d.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	wantRows := dumpV3Content(t, db)
	runs := watchApplies(t)
	touchLog(t, root)
	first, _ := run(t, Options{Root: root, DBPath: db})
	if r := (*runs)[len(*runs)-1]; r.typedSkips != 0 || r.genericSkips != 0 {
		t.Fatalf("the first sync after the upgrade skipped records that had no memory: %+v", r)
	}
	if first.Messages.Unchanged != fixtureMessages || first.Records.Unchanged != fixtureRecords || first.Messages.Updated != 0 {
		t.Fatalf("the upgrade read as edits: %+v", first)
	}
	if got := dumpV3Content(t, db); got != wantRows {
		t.Fatal("the upgrade changed archived content")
	}
	if typed, digests := memoRows(t, db); typed == 0 || digests != fixtureRecords {
		t.Fatalf("memory after the upgrade: %d typed, %d digests", typed, digests)
	}
	touchLog(t, root)
	again, _ := run(t, Options{Root: root, DBPath: db})
	if r := (*runs)[len(*runs)-1]; r.typedSkips == 0 || r.genericSkips != fixtureRecords {
		t.Fatalf("the second sync did not skip: %+v", r)
	}
	if again.Messages != first.Messages || again.Records != first.Records {
		t.Fatalf("counts moved: %+v vs %+v", again, first)
	}
	run(t, Options{Root: root, DBPath: fresh})
	if a, b := dumpArchive(t, db), dumpArchive(t, fresh); a != b {
		t.Fatal("the upgraded archive differs from a fresh one")
	}
}

// dumpV3Content is the archived content that exists in a version 3 archive.
func dumpV3Content(t *testing.T, db string) string {
	t.Helper()
	d := openRaw(t, db)
	var b strings.Builder
	for _, q := range []string{
		`select tenant_id, user_id, id, content_hash from conversations order by 1,2,3`,
		`select tenant_id, user_id, conversation_id, id, content_hash from messages order by 1,2,3,4`,
		`select tenant_id, user_id, id, content_hash from activity order by 1,2,3`,
		`select source, database, store, key_json, value_json, content_hash, removed_at is null from records order by 1,2,3,4`,
	} {
		rows, err := d.Query(q)
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := rows.Columns()
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			fmt.Fprintf(&b, "%q\n", vals)
		}
		_ = rows.Close()
	}
	return b.String()
}
