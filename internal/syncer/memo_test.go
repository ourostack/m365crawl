package syncer

import (
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
	for _, huge := range [][]byte{{effectsVersion, 0xff, 0xff, 0xff, 0xff, 0x0f}, {effectsVersion, 0, 0xff, 0xff, 0xff, 0xff, 0x0f}} {
		if _, err := decodeEffects(huge); err == nil {
			t.Fatalf("%x accepted", huge)
		}
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
					w.memo.rowHashes = func(byte) (map[int64][store.DigestLen]byte, error) { return nil, boom }
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
	w := &writer{memo: &memo{source: "s", putTyped: func(_, _, key string, _ store.TypedMemo) error { put = append(put, key); return nil }}}
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
