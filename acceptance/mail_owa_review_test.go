//go:build acceptance

package acceptance

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/indexeddb"
	"github.com/ourostack/m365crawl/internal/leveldb"
	"github.com/ourostack/m365crawl/internal/outlookowa"
	"github.com/ourostack/m365crawl/internal/teamsdesktop"
	"github.com/ourostack/m365crawl/internal/v8"
	gl "github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"
	"github.com/syndtr/goleveldb/leveldb/util"
)

func owaObject(kv ...any) *v8.Object {
	if len(kv)%2 != 0 {
		panic("synthetic_object_arity_refused")
	}
	o := &v8.Object{}
	for i := 0; i+1 < len(kv); i += 2 {
		o.Keys = append(o.Keys, kv[i].(string))
		o.Values = append(o.Values, kv[i+1])
	}
	return o
}

func owaIdentity(id string) *v8.Object { return owaObject("Id", id) }

func owaDetailOnly() *v8.Object {
	recipient := owaObject("Name", "Synthetic", "EmailAddress", "synthetic@example.invalid")
	return owaObject("ItemId", owaIdentity("Message/Sparse=="),
		"NormalizedBody", owaObject("Value", "<p>synthetic detail</p>"),
		"ToRecipients", []any{recipient}, "CcRecipients", []any{recipient}, "BccRecipients", []any{recipient},
		"Attachments", []any{owaObject("AttachmentId", owaIdentity("SyntheticDetailAttachment"), "Name", "synthetic", "IsInline", false)},
		"UID", "SYNTHETIC-DETAIL-UID", "InternetMessageId", "<synthetic-detail@example.invalid>")
}

func TestOWAMailReviewA1DetailOnlySelection(t *testing.T) {
	header := owaObject("ParentFolderId", owaIdentity("F"), "HasAttachments", false)
	for _, field := range []string{"ToRecipients", "CcRecipients", "BccRecipients", "Attachments", "UID", "InternetMessageId"} {
		t.Run(field, func(t *testing.T) {
			f := owaFields{}
			body := owaObject(field, f.get(owaDetailOnly(), field))
			m, err := owaRawHeader(header, body)
			if err != nil || len(m.detail.present) != 1 || m.detail.present[0] != field {
				t.Fatal("approved_detail_selection_refused")
			}
		})
	}
}

func TestOWAMailReviewA1ActualDetailMapping(t *testing.T) {
	s := owaCapture(t, owaFixtureProfile("mail", "Synthetic"))
	owaSyntheticTable(t, s, owaDetailOnly(), false)
	o, err := outlookowa.Open(s.ctx, s.profile.Source.LevelDBDir, s.profile.Source.BlobDir)
	if err != nil {
		t.Fatal("synthetic_origin_open_refused")
	}
	r, readErr := outlookowa.Collect(s.ctx, o, s.account, false)
	closeErr := o.Close()
	if readErr != nil || closeErr != nil || len(r.Messages) != 5 || len(r.Losses) != 0 || r.Doubtful {
		t.Fatal("synthetic_product_observation_refused")
	}
	found := false
	for _, m := range r.Messages {
		if m.ID == "Message/Sparse==" {
			found = true
			if len(m.Value.Recipients) != 3 || len(m.Value.Attachments) != 1 || m.Value.ICalUID != "synthetic-detail-uid" || m.Value.MessageID != "<synthetic-detail@example.invalid>" || m.Value.Body.State != "inline" || m.Value.HasAttachments == nil || *m.Value.HasAttachments {
				t.Fatal("approved_product_detail_mapping_mismatch")
			}
		}
	}
	if !found {
		t.Fatal("synthetic_product_message_missing")
	}
	s.census, err = owaCensusContext(s.ctx, s.profile.Source.LevelDBDir, s.profile.Source.BlobDir)
	if err != nil {
		t.Fatal("independent_detail_census_refused")
	}
	s.digest = owaPayloadDigestContext(t, s.ctx, s.profile.Source)
	path := filepath.Join(owaScratch(t), "detail-loss.db")
	owaExercise(t, path, s)
	st := owaArchive(t, path)
	if fields := owaCompareAll(st, []owaObservation{s}); len(fields) != 0 {
		t.Fatalf("actual_detail_archive_mismatch fields=%v", fields)
	}
	if owaScalar(t, st, `select count(*) from mail_recipients r join mail_messages m on m.rowid=r.message_rowid join mail_source_ids i on i.account=m.account and i.kind='message' and i.key=m.detail_key where i.source_id='Message/Sparse=='`) != 3 {
		t.Fatal("detail_recipient_literal_mismatch")
	}
	owaCloseArchive(t, st)
	owaCorrupt(t, path, `update mail_messages set ical_uid='',internet_message_id='' where ical_uid='synthetic-detail-uid'`)
	bad := owaArchive(t, path)
	defer owaCloseArchive(t, bad)
	if fields := owaCompareAll(bad, []owaObservation{s}); !reflect.DeepEqual(fields, []string{"ical_uid", "internet_message_id"}) {
		t.Fatalf("deliberate_detail_corruption_undetected fields=%v", fields)
	}
}

func TestOWAMailReviewA2QueryRefusals(t *testing.T) {
	c := owaObserve(t, owaFixtureProfile("mail", "Synthetic"))
	path := filepath.Join(owaScratch(t), "limits.db")
	owaExercise(t, path, c)
	st := owaArchive(t, path)
	defer owaCloseArchive(t, st)
	t.Run("cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := owaQueryContext(ctx, st, `select account from mail_messages`); err == nil {
			t.Fatal("query_cancellation_ignored")
		}
	})
	t.Run("rows", func(t *testing.T) {
		if _, err := owaQuery(st, `with recursive n(x) as (select 1 union all select x+1 from n where x<16385) select x from n`); err == nil {
			t.Fatal("query_row_budget_ignored")
		}
	})
	t.Run("payload", func(t *testing.T) {
		if _, err := owaQueryBudget(context.Background(), st, `select zeroblob(33554433) as payload`, 32<<20); err == nil {
			t.Fatal("query_payload_budget_ignored")
		}
	})
}

func TestOWAMailReviewA2CancellationKeepsCopyUntilClose(t *testing.T) {
	s := owaCapture(t, owaFixtureProfile("mail", "Synthetic"))
	owaSyntheticTable(t, s, nil, true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir, release, err := owaOwnedSnapshot(ctx, s.profile.Source, teamsdesktop.Snapshot)
	if err != nil {
		t.Fatal("synthetic_snapshot_refused")
	}
	defer release()
	t.Cleanup(func() {
		// The RED intentionally exercises premature automatic removal. Reconcile only this returned test-owned root, after every actual reader below is closed.
		if err := os.RemoveAll(dir); err != nil {
			t.Error("synthetic_failure_cleanup_failed")
		}
	})
	o, err := outlookowa.Open(ctx, filepath.Join(dir, "leveldb"), filepath.Join(dir, "blob"))
	if err != nil {
		t.Fatal("synthetic_origin_open_refused")
	}
	defer func() {
		if err := o.Close(); err != nil {
			t.Error("synthetic_origin_close_failed")
		}
	}()
	dbs, err := o.Databases()
	if err != nil {
		_ = o.Close()
		t.Fatal("synthetic_metadata_refused")
	}
	var dbID, storeID int64
	for _, db := range dbs {
		if strings.HasPrefix(db.Name, "owa-offline-data-") {
			dbID = db.ID
			for _, st := range db.Stores {
				if st.Name == "messages" {
					storeID = st.ID
				}
			}
		}
	}
	large := false
	err = o.Records(dbID, storeID, func(r indexeddb.Record) error {
		large = large || len(r.Raw) >= leveldb.LazyMin()
		return nil
	})
	if err != nil || !large || o.Stats().Tables == 0 {
		_ = o.Close()
		t.Fatal("lazy_origin_not_exercised")
	}
	cancel()
	// Observe the real automatic-removal callback while the actual lazy handle is open.
	until := time.Now().Add(150 * time.Millisecond)
	current := filepath.Join(dir, "leveldb", "CURRENT")
	for time.Now().Before(until) {
		if _, err := os.Stat(current); errors.Is(err, os.ErrNotExist) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	_, activeErr := os.Stat(current)
	closeErr := o.Close()
	closedErr := o.Records(dbID, storeID, func(indexeddb.Record) error { return nil })
	release()
	_, removedErr := os.Stat(dir)
	if activeErr != nil {
		t.Error("work_cancellation_deleted_active_copy")
	}
	if closeErr != nil || !errors.Is(closedErr, leveldb.ErrClosed) || !errors.Is(removedErr, os.ErrNotExist) {
		t.Error("close_before_cleanup_contract_failed")
	}
}

func TestOWAMailReviewA3UnexpectedCoherentAccount(t *testing.T) {
	path := filepath.Join(owaScratch(t), "accounts.db")
	a := owaObserve(t, owaFixtureProfile("mail", "Expected"))
	b := owaObserve(t, owaFixtureProfile("mail", "Unexpected"))
	owaExercise(t, path, a)
	owaExercise(t, path, b)
	st := owaArchive(t, path)
	defer owaCloseArchive(t, st)
	if len(owaCompareAll(st, []owaObservation{a, b})) != 0 {
		t.Fatal("declared_account_set_refused")
	}
	if len(owaCompareAll(st, []owaObservation{a})) == 0 {
		t.Fatal("unexpected_coherent_account_accepted")
	}
}

func TestOWAMailReviewA4CoupledTextCorruption(t *testing.T) {
	c := owaObserve(t, owaFixtureProfile("mail", "Synthetic"))
	path := filepath.Join(owaScratch(t), "coupled.db")
	owaExercise(t, path, c)
	owaCorrupt(t, path, `update mail_messages set body_text='' where body_state='inline'`)
	owaCorrupt(t, path, `update mail_fts set body_text=''`)
	st := owaArchive(t, path)
	defer owaCloseArchive(t, st)
	if len(owaCompare(st, c.account, c.census)) == 0 {
		t.Fatal("coupled_body_fts_corruption_accepted")
	}
}

func TestOWAMailReviewA4IndependentTextCorpus(t *testing.T) {
	for _, tc := range []struct{ html, text string }{
		{"<p>one</p>", "one"}, {"<p>a<b>b</b>c</p><p>d</p>", "abc d"},
		{"<div>a<br/>b</div>", "a b"}, {"<p>a&nbsp;b</p>", "a b"},
		{"<p>&lt;tag&gt;&amp;</p>", "<tag>&"}, {"<p>a<!--excluded-->b</p>", "ab"},
		{`<p style="display:none">hidden</p>`, "hidden"}, {"<script>ignored</script><p>shown", "shown"},
		{"<p>unclosed", "unclosed"}, {`<img src="https://example.invalid/never-fetched">`, ""},
		{"<div>A<style>.x { hidden }</style>B</div>", "A B"},
		{"<h1>heading</h1><ul><li>one<li>two</ul>", "heading one two"},
		{"2 < 3 &amp;&amp; 4 > 1", "2 < 3 && 4 > 1"},
	} {
		got, err := owaIndependentText(context.Background(), tc.html)
		if err != nil || got != tc.text {
			t.Error("independent_text_literal_mismatch")
		}
	}
	for _, html := range []string{"<p>\x00</p>", "<p>\xff</p>", "<p broken", "<svg><text>foreign</text></svg>", "<textarea>a<b>b</b></textarea>", "<!--unfinished"} {
		if _, err := owaIndependentText(context.Background(), html); err == nil {
			t.Error("unsupported_derivation_blessed")
		}
	}
}

func TestOWAMailReviewA1PrecedenceIsNotInvented(t *testing.T) {
	header := owaObject("ParentFolderId", owaIdentity("F"), "HasAttachments", false, "UID", "HEADER")
	if _, err := owaRawHeader(header, owaObject("UID", "DETAIL")); err == nil {
		t.Fatal("conflicting_detail_precedence_invented")
	}
	m, err := owaRawHeader(header, owaObject("UID", "HEADER", "HasAttachments", true, "ReplyTo", []any{owaObject("Name", "Synthetic")}))
	if err != nil || m.fields[12] != "header" || m.fields[16] != int64(0) || len(m.recipients) != 0 || len(m.detail.present) != 3 || len(m.detail.unproved) != 0 {
		t.Fatal("approved_precedence_or_exclusion_changed")
	}
}

func TestOWAMailReviewA2ActiveSQLDeadlineAndCopyDeadline(t *testing.T) {
	c := owaObserve(t, owaFixtureProfile("mail", "Synthetic"))
	path := filepath.Join(owaScratch(t), "deadline.db")
	owaExercise(t, path, c)
	st := owaArchive(t, path)
	defer owaCloseArchive(t, st)
	ctx, cancel := context.WithTimeout(owaWorkContext(t), 20*time.Millisecond)
	defer cancel()
	_, err := owaQueryContext(ctx, st, `with recursive n(x) as (select 1 union all select x+1 from n where x<1000000000) select sum(x) from n`)
	if err == nil || !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatal("active_sql_deadline_not_observed")
	}
	copyCtx, cancelCopy := context.WithTimeout(owaWorkContext(t), 20*time.Millisecond)
	defer cancelCopy()
	dir, release, err := owaOwnedSnapshot(copyCtx, c.profile.Source, func(work context.Context, source teamsdesktop.Source) (string, func(), error) {
		<-work.Done()
		return teamsdesktop.Snapshot(work, source)
	})
	release()
	if dir != "" || err == nil || !errors.Is(copyCtx.Err(), context.DeadlineExceeded) {
		t.Fatal("copy_deadline_not_observed")
	}
}

func TestOWAMailReviewA2CancelledRawWork(t *testing.T) {
	ctx, cancel := context.WithCancel(owaWorkContext(t))
	cancel()
	p := owaFixtureProfile("mail", "Synthetic")
	if err := owaCheckSource(ctx, p.Source); err == nil {
		t.Fatal("source_context_ignored")
	}
	if _, err := owaCensusContext(ctx, p.Source.LevelDBDir, p.Source.BlobDir); err == nil {
		t.Fatal("census_context_ignored")
	}
	if _, err := owaIndependentText(ctx, "<p>synthetic</p>"); err == nil {
		t.Fatal("derivation_context_ignored")
	}
}

func TestOWAMailReviewA3UnexpectedCoverageAndAbsence(t *testing.T) {
	c := owaObserve(t, owaFixtureProfile("mail", "Expected"))
	for _, q := range []string{
		`insert into mail_coverage(account,folder_key,count,read_at) values('outlook/Unexpected',1,0,'2030-01-01T00:00:00Z')`,
		`insert into mail_absent(account,detail_key,first_missed_at) values('outlook/Unexpected',1,'2030-01-01T00:00:00Z')`,
	} {
		path := filepath.Join(owaScratch(t), "extra.db")
		owaExercise(t, path, c)
		owaCorrupt(t, path, q)
		st := owaArchive(t, path)
		fields := owaCompareAll(st, []owaObservation{c})
		owaCloseArchive(t, st)
		if len(fields) == 0 {
			t.Fatal("unexpected_account_metadata_accepted")
		}
	}
}

func TestOWAMailReviewRealPermitRefusal(t *testing.T) {
	if err := owaRealAcceptancePermit(); err == nil || err.Error() != "qualification_protocol_refused" {
		t.Fatal("unreviewed_real_acceptance_permitted")
	}
}

func TestOWAMailReviewA4UnsupportedSourceIsUnproved(t *testing.T) {
	header := owaObject("ParentFolderId", owaIdentity("F"), "HasAttachments", false)
	_, err := owaRawHeader(header, owaObject("NormalizedBody", owaObject("Value", "<p>\xff</p>")))
	if err == nil {
		t.Fatal("unsupported_source_derivation_accepted")
	}
}

func TestOWAMailReviewA4NonHTMLSourceIsUnproved(t *testing.T) {
	header := owaObject("ParentFolderId", owaIdentity("F"), "HasAttachments", false)
	_, err := owaRawHeader(header, owaObject("NormalizedBody", owaObject("Value", "<p>synthetic</p>", "BodyType", "Text")))
	if err == nil {
		t.Fatal("non_html_source_rendered_as_html")
	}
}

// Only this synthetic input helper writes IndexedDB. It rebuilds the exact owned copied directory with the existing LevelDB toolkit, preserving raw keys/values except a literal test record.
func owaSyntheticTable(t *testing.T, observation owaObservation, detail *v8.Object, lazy bool) {
	t.Helper()
	source := observation.profile.Source
	rel, err := filepath.Rel(observation.ownedSnapshot, source.LevelDBDir)
	if observation.ownedSnapshot == "" || err != nil || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		t.Fatal("synthetic_source_ownership_refused")
	}
	src, err := leveldb.Load(source.LevelDBDir)
	if err != nil {
		t.Fatal("synthetic_table_read_refused")
	}
	values := map[string][]byte{}
	if err := src.Scan(nil, func(k, v []byte) error {
		values[string(k)] = append([]byte(nil), v...)
		return nil
	}); err != nil {
		_ = src.Close()
		t.Fatal("synthetic_table_scan_refused")
	}
	if err := src.Close(); err != nil {
		t.Fatal("synthetic_table_close_failed")
	}
	if detail != nil {
		key := owaSyntheticKey(3, "review-detail")
		f := owaFields{}
		if f.text(detail, "ItemId", "Id") == "Message/Normal==" {
			key = owaSyntheticKey(3, "body-0")
		}
		values[string(key)] = append([]byte{1, 0xff, 0x10, 0xff, 15}, owaEncodeSynthetic(t, detail)...)
	}
	if lazy {
		header := owaObject("ItemId", owaIdentity("SyntheticLarge"), "ParentFolderId", owaIdentity("Folder/0=="), "HasAttachments", false, "Subject", strings.Repeat("X", 2<<20))
		values[string(owaSyntheticKey(2, "review-large"))] = append([]byte{1, 0xff, 0x10, 0xff, 15}, owaEncodeSynthetic(t, header)...)
	}
	entries, err := os.ReadDir(source.LevelDBDir)
	if err != nil {
		t.Fatal("synthetic_table_list_refused")
	}
	for _, e := range entries {
		if e.IsDir() {
			t.Fatal("synthetic_table_layout_refused")
		}
		if err := os.Remove(filepath.Join(source.LevelDBDir, e.Name())); err != nil {
			t.Fatal("synthetic_table_replace_failed")
		}
	}
	w, err := gl.OpenFile(source.LevelDBDir, &opt.Options{Compression: opt.SnappyCompression, BlockSize: 2048})
	if err != nil {
		t.Fatal("synthetic_table_write_refused")
	}
	for k, v := range values {
		if err := w.Put([]byte(k), v, nil); err != nil {
			_ = w.Close()
			t.Fatal("synthetic_table_write_failed")
		}
	}
	if err := w.CompactRange(util.Range{}); err != nil {
		_ = w.Close()
		t.Fatal("synthetic_table_compact_failed")
	}
	if err := w.Close(); err != nil {
		t.Fatal("synthetic_table_close_failed")
	}
}

func owaSyntheticKey(store byte, key string) []byte {
	out := []byte{0, 1, store, 1, 1}
	out = binary.AppendUvarint(out, uint64(len(key)))
	for _, c := range []byte(key) {
		out = append(out, 0, c)
	}
	return out
}

// A bounded synthetic-only V8 value writer, not a source parser or fixture-generator change.
func owaEncodeSynthetic(t *testing.T, value any) []byte {
	t.Helper()
	switch v := value.(type) {
	case nil:
		return []byte{'0'}
	case bool:
		if v {
			return []byte{'T'}
		}
		return []byte{'F'}
	case string:
		if len(v) > 2<<20 {
			t.Fatal("synthetic_value_bound_refused")
		}
		return append(binary.AppendUvarint([]byte{'S'}, uint64(len(v))), v...)
	case *v8.Object:
		out := []byte{'o'}
		for i, k := range v.Keys {
			out = append(out, owaEncodeSynthetic(t, k)...)
			out = append(out, owaEncodeSynthetic(t, v.Values[i])...)
		}
		return binary.AppendUvarint(append(out, '{'), uint64(len(v.Keys)))
	case []any:
		out := binary.AppendUvarint([]byte{'A'}, uint64(len(v)))
		for _, entry := range v {
			out = append(out, owaEncodeSynthetic(t, entry)...)
		}
		out = append(out, '$', 0)
		return binary.AppendUvarint(out, uint64(len(v)))
	}
	t.Fatal("synthetic_type_refused")
	return nil
}
