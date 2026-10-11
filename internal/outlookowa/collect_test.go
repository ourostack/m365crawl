package outlookowa

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/hxstore"
	"github.com/ourostack/m365crawl/internal/indexeddb"
	"github.com/ourostack/m365crawl/internal/leveldb"
	"github.com/ourostack/m365crawl/internal/outlookmail"
)

func openFixture(t *testing.T, variant string, blobs bool) *indexeddb.Origin {
	t.Helper()
	base := filepath.Join("..", "..", "testdata", "outlook-owa-fixture", variant, "IndexedDB")
	blob := ""
	if blobs {
		blob = filepath.Join(base, "https_outlook.office.com_0.indexeddb.blob")
	}
	o, err := indexeddb.Open(filepath.Join(base, "https_outlook.office.com_0.indexeddb.leveldb"), blob)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := o.Close(); err != nil {
			t.Error(err)
		}
	})
	return o
}

func TestCollectSyntheticNormalAndBlob(t *testing.T) {
	o := openFixture(t, "mail", true)
	r, err := Collect(context.Background(), o, "outlook/Synthetic", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Messages) != 5 || len(r.Folders) != 7 || r.RootID != "Root/Case==" || r.Doubtful || len(r.Losses) != 0 || r.Notes.HeadersSeen != 5 || r.Notes.Messages != 5 || r.Notes.BodiesInline != 3 || r.Notes.BodiesNone != 2 {
		t.Fatalf("synthetic collection: %+v", r)
	}
	byID := map[string]Message{}
	for _, m := range r.Messages {
		byID[m.ID] = m
	}
	normal := byID["Message/Normal=="].Value
	blob := byID["Message/Blob=="].Value
	if string(normal.Body.HTML) != "<p>Synthetic normal cached HTML</p>" || normal.Body.State != "inline" || normal.Flag != "none" || normal.ReadState != "read" || normal.HasAttachments == nil || *normal.HasAttachments || len(normal.Attachments) != 1 || normal.ToMe || normal.InReplyTo != "" || len(normal.Recipients) != 4 || normal.Recipients[3].Role != "bcc" {
		t.Fatalf("normal header/body mapping: %+v", normal)
	}
	if blob.Body.State != "inline" || len(blob.Body.HTML) < 500000 || !strings.HasPrefix(string(blob.Body.HTML), "<p>Synthetic spill ") || blob.Flag != "flagged" || blob.ReadState != "unread" {
		t.Fatal("blob body was not joined/decoded")
	}
	if byID["Message/Sparse=="].Value.Body.State != "none" || byID["Message/Null=="].Value.Body.State != "none" || byID["Message/Null=="].Value.ReadState != "unknown" || byID["Message/Undefined=="].Value.ReadState != "unknown" || byID["Message/Undefined=="].Value.Flag != "complete" {
		t.Fatal("sparse/null/undefined semantics")
	}
}

func TestCollectMissingBlobKeepsHeaders(t *testing.T) {
	r, err := Collect(context.Background(), openFixture(t, "mail", false), "A", true)
	if err != nil || len(r.Messages) != 5 || !r.Doubtful || !hasLoss(r, "outlook_owa_reader_blob_missing") || r.Notes.BodiesInline != 2 {
		t.Fatalf("missing blob = err %v, notes %+v, losses %+v", err, r.Notes, r.Losses)
	}
}

func TestCollectFixtureLayoutGuards(t *testing.T) {
	for _, tc := range []struct {
		variant, reason string
		expect          bool
	}{{"unknown", "content_database_missing", false}, {"multiple", "multiple_mailboxes", false}, {"missing-store", "required_store_missing", false}, {"empty", "no_mail_objects", true}} {
		t.Run(tc.variant, func(t *testing.T) {
			r, err := Collect(context.Background(), openFixture(t, tc.variant, true), "A", tc.expect)
			var g *hxstore.GuardError
			if !errors.As(err, &g) || g.Code != outlookmail.CodeMailLayoutUnsupported || g.Detail != tc.reason || len(r.Messages) != 0 {
				t.Fatalf("guard: %v", err)
			}
		})
	}
	r, err := Collect(context.Background(), openFixture(t, "empty", true), "A", false)
	if err != nil || len(r.Messages) != 0 || r.RootID != "Root/Case==" {
		t.Fatal("new empty mailbox refused")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Collect(ctx, openFixture(t, "mail", true), "A", false); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation ignored")
	}
	if _, err := Collect(context.Background(), nil, "A", false); err == nil {
		t.Fatal("nil origin accepted")
	}
}

func hasLoss(r Result, code string) bool {
	for _, l := range r.Losses {
		if l.Code == code && l.Count > 0 {
			return true
		}
	}
	return false
}

// Faults model the reader boundary only; successful reads use real Chromium fixture bytes above.
type faultReader struct {
	dbs                                      []indexeddb.Database
	values                                   map[int64][]any
	metaErr, scanErr, decodeErr, errorRecord error
	cancel                                   func()
	stats                                    leveldb.Stats
}

func (f *faultReader) Stats() leveldb.Stats                     { return f.stats }
func (f *faultReader) Databases() ([]indexeddb.Database, error) { return f.dbs, f.metaErr }
func (f *faultReader) Records(db, store int64, fn func(indexeddb.Record) error) error {
	if store < 0 || store > 255 || len(f.values[store]) > 256 {
		return errors.New("synthetic record key exceeds byte range")
	}
	if f.cancel != nil {
		f.cancel()
	}
	for i := range f.values[store] {
		if err := fn(indexeddb.Record{Key: "synthetic-key", Raw: []byte{byte(store), byte(i)}, Err: f.errorRecord}); err != nil {
			return err
		}
	}
	return f.scanErr
}
func (f *faultReader) Decode(db int64, raw []byte) (any, error) {
	if f.decodeErr != nil {
		return nil, f.decodeErr
	}
	if len(raw) != 2 || int(raw[1]) >= len(f.values[int64(raw[0])]) {
		return nil, errors.New("invalid synthetic record key")
	}
	return f.values[int64(raw[0])][int(raw[1])], nil
}

func readerWith(headers ...any) *faultReader {
	return &faultReader{dbs: []indexeddb.Database{{ID: 1, Name: "owa-offline-data-synthetic", Stores: []indexeddb.Store{{ID: 1, Name: "folders"}, {ID: 2, Name: "messages"}, {ID: 3, Name: "messageBodies"}, {ID: 4, Name: "credentials"}, {ID: 5, Name: "events"}, {ID: 6, Name: "attachmentPayloads"}}}}, values: map[int64][]any{1: {obj("id", "F", "parentFolderId", "R", "displayName", "Synthetic", "distinguishedFolderType", "inbox")}, 2: headers}}
}

func TestCollectReaderFailuresAreFixedAndUntrusted(t *testing.T) {
	sensitive := errors.New("synthetic-private-database-suffix-do-not-export")
	for _, tc := range []struct {
		name    string
		set     func(*faultReader)
		wantErr bool
		code    string
	}{
		{"metadata", func(f *faultReader) { f.metaErr = sensitive }, true, ""},
		{"scan", func(f *faultReader) { f.scanErr = sensitive }, true, ""},
		{"bad-key", func(f *faultReader) {
			f.errorRecord = &indexeddb.OmissionError{Omission: indexeddb.Omission{Code: indexeddb.CodeBadKey, Detail: sensitive.Error()}}
		}, false, "outlook_owa_reader_bad_key"},
		{"decode", func(f *faultReader) {
			f.decodeErr = &indexeddb.OmissionError{Omission: indexeddb.Omission{Code: indexeddb.CodeV8Malformed, Detail: sensitive.Error()}}
		}, false, "outlook_owa_reader_v8_malformed"},
		{"unknown-reader-error", func(f *faultReader) { f.decodeErr = sensitive }, false, "outlook_owa_reader_omission"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := readerWith(header("M", "F"))
			tc.set(f)
			r, err := collect(context.Background(), f, "A", false)
			if tc.wantErr {
				if err == nil || strings.Contains(err.Error(), "private") {
					t.Fatalf("unsafe error %v", err)
				}
			} else if err != nil || !r.Doubtful || !hasLoss(r, tc.code) || r.RootID != "" {
				t.Fatalf("omission: %+v %v", r, err)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	f := readerWith(header("M", "F"))
	f.cancel = cancel
	if _, err := collect(ctx, f, "A", false); !errors.Is(err, context.Canceled) {
		t.Fatal("mid-scan cancellation")
	}
}

func TestCollectSeenIDsAndConflictingDuplicates(t *testing.T) {
	first := header("Conflict", "F")
	conflict := header("Conflict", "F")
	conflict.Keys = append(conflict.Keys, "Subject")
	conflict.Values = append(conflict.Values, "different synthetic value")
	f := readerWith(first, first, conflict, header("Missing", "absent"), obj("ItemId", ident("SeenInvalid")), obj("ItemId", ident("")), header("Kept", "F"))
	f.values[3] = []any{obj("ItemId", ident("Kept"), "NormalizedBody", obj("Value", "<p>first</p>")), obj("ItemId", ident("Kept"), "NormalizedBody", obj("Value", "<p>conflict</p>"))}
	r, err := collect(context.Background(), f, "A", false)
	if err != nil || len(r.Messages) != 1 || r.Messages[0].ID != "Kept" || r.Messages[0].Value.Body.State != "none" || !reflect.DeepEqual(r.SeenIDs, []string{"Conflict", "Kept", "Missing", "SeenInvalid"}) || !r.Doubtful || !hasLoss(r, "outlook_owa_duplicate_conflict") || !hasLoss(r, outlookmail.CodeMailFolderMissing) {
		t.Fatalf("conflicts/seen IDs: %+v %v", r, err)
	}
	if r.Notes.MissingFolder != 1 || r.Notes.Unmapped == 0 {
		t.Fatal("missing-folder/unmapped notes")
	}
	f = readerWith(header("M", "F"))
	f.values[1] = append(f.values[1], obj("id", "F", "parentFolderId", "OtherRoot"))
	r, err = collect(context.Background(), f, "A", false)
	if err != nil || len(r.Messages) != 0 || r.RootID != "" || !r.Doubtful {
		t.Fatal("conflicting folder guessed")
	}
}

func TestCollectRootGraph(t *testing.T) {
	for _, tc := range []struct {
		name    string
		folders []any
		root    string
	}{
		{"cycle", []any{obj("id", "F", "parentFolderId", "G"), obj("id", "G", "parentFolderId", "F")}, ""},
		{"self-cycle", []any{obj("id", "F", "parentFolderId", "F")}, ""},
		{"multiple", []any{obj("id", "F", "parentFolderId", "R"), obj("id", "G", "parentFolderId", "S")}, ""},
		{"invalid", []any{obj("id", "F", "parentFolderId", "")}, ""},
		{"empty", nil, ""},
		{"nested", []any{obj("id", "F", "parentFolderId", "G"), obj("id", "G", "parentFolderId", "R")}, "R"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := readerWith()
			f.values[1] = tc.folders
			r, err := collect(context.Background(), f, "A", false)
			if err != nil || r.RootID != tc.root || (tc.root == "" && (!r.Doubtful || !hasLoss(r, "outlook_owa_root_uncertain"))) {
				t.Fatalf("root graph: %+v %v", r, err)
			}
		})
	}
}

func TestCollectMalformedAndKeyedBodyIdentities(t *testing.T) {
	f := readerWith(header("synthetic-key", "F"))
	f.values[1] = append(f.values[1], obj("id", float64(12)), obj("id", ""), nil)
	f.values[3] = []any{obj("NormalizedBody", obj("Value", "<p>key-joined</p>")), obj("ItemId", ident(""), "NormalizedBody", obj("Value", "not joined"))}
	r, err := collect(context.Background(), f, "A", false)
	if err != nil || len(r.Messages) != 1 || string(r.Messages[0].Value.Body.HTML) != "<p>key-joined</p>" || r.RootID != "" || !r.Doubtful || !hasLoss(r, outlookmail.CodeMailUnmapped) {
		t.Fatal("malformed/keyed source identity handling")
	}
	f = readerWith(header("M", "F"))
	f.values[2] = append(f.values[2], header("M", "F"), obj("ItemId", ident("M"), "ParentFolderId", ident("F"), "HasAttachments", true), header("M", "F"))
	f.values[1] = append(f.values[1], obj("id", "G", "parentFolderId", "R"))
	r, err = collect(context.Background(), f, "A", false)
	if err != nil || len(r.Messages) != 0 || !reflect.DeepEqual(r.SeenIDs, []string{"M"}) {
		t.Fatal("poisoned duplicate was resurrected")
	}
	f = readerWith()
	f.dbs[0].Stores = append(f.dbs[0].Stores, indexeddb.Store{ID: 7, Name: "folders"})
	_, err = collect(context.Background(), f, "A", false)
	var guard *hxstore.GuardError
	if !errors.As(err, &guard) || guard.Detail != "required_store_ambiguous" {
		t.Fatal("ambiguous store accepted")
	}
	f = readerWith(header("synthetic-key", "F"))
	f.values[3] = []any{nil}
	r, err = collect(context.Background(), f, "A", false)
	if err != nil || len(r.Messages) != 1 || r.Messages[0].Value.Body.State != "none" || len(r.Losses) != 0 {
		t.Fatal("null keyed body was treated as a missing header")
	}
}

func TestCollectDecoyIsUndecodableButExcluded(t *testing.T) {
	o := openFixture(t, "mail", true)
	dbs, err := o.Databases()
	if err != nil {
		t.Fatal(err)
	}
	decoys := 0
	for _, s := range dbs[0].Stores {
		if s.Name != "credentials" {
			continue
		}
		err = o.Records(dbs[0].ID, s.ID, func(rec indexeddb.Record) error {
			_, err := o.Decode(dbs[0].ID, rec.Raw)
			var omission *indexeddb.OmissionError
			if !errors.As(err, &omission) || omission.Code != "v8_host_object" {
				t.Fatal("credential trap was not an unsupported host object")
			}
			decoys++
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if decoys != 1 {
		t.Fatal("synthetic credential decoy absent")
	}
	r, err := Collect(context.Background(), o, "A", false)
	if err != nil || len(r.Losses) != 0 {
		t.Fatal("excluded credential decoy decoded by collector")
	}
}

func TestCollectTruncatedSnapshotIsUntrusted(t *testing.T) {
	f := readerWith(header("M", "F"))
	f.stats.TruncatedLogTails = 2
	r, err := collect(context.Background(), f, "A", false)
	if err != nil || len(r.Messages) != 1 || !r.Doubtful || r.RootID != "" || !hasLoss(r, "outlook_owa_reader_truncated_log_tail") || !hasLoss(r, "outlook_owa_root_uncertain") {
		t.Fatal("truncated snapshot trusted absence")
	}
	found := false
	for _, loss := range r.Losses {
		if loss.Code == "outlook_owa_reader_truncated_log_tail" {
			found = loss.Count == 2
		}
	}
	if !found {
		t.Fatal("snapshot omission count")
	}
}
