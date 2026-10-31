package outlookowa

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"testing"

	"github.com/ourostack/m365crawl/internal/hxstore"
	"github.com/ourostack/m365crawl/internal/indexeddb"
	"github.com/ourostack/m365crawl/internal/leveldb"
	"github.com/ourostack/m365crawl/internal/outlookmail"
)

func notesWithHeaders(n int) outlookmail.Notes { return outlookmail.Notes{HeadersSeen: n} }

type mailboxReader struct {
	dbs              []indexeddb.Database
	values           map[[2]int64][]any
	metaErr, scanErr error
}

func (m *mailboxReader) Stats() leveldb.Stats                     { return leveldb.Stats{} }
func (m *mailboxReader) Databases() ([]indexeddb.Database, error) { return m.dbs, m.metaErr }
func (m *mailboxReader) Records(db, store int64, fn func(indexeddb.Record) error) error {
	for i := range m.values[[2]int64{db, store}] {
		if err := fn(indexeddb.Record{Key: "synthetic-key", Raw: []byte(fmt.Sprintf("%d/%d/%d", db, store, i))}); err != nil {
			return err
		}
	}
	return m.scanErr
}
func (m *mailboxReader) Decode(_ int64, raw []byte) (any, error) {
	var db, store int64
	var i int
	if _, err := fmt.Sscanf(string(raw), "%d/%d/%d", &db, &store, &i); err != nil {
		return nil, err
	}
	return m.values[[2]int64{db, store}][i], nil
}

func info(smtp, user any) *objectBuilder {
	return &objectBuilder{kv: []any{"mailboxSmtpAddress", smtp, "userIdentity", user, "sourceId", "synthetic-source", "type", "UserMailbox"}}
}

type objectBuilder struct{ kv []any }

func (b *objectBuilder) build() any { return obj(b.kv...) }

func mailboxDB(id int64) indexeddb.Database {
	return indexeddb.Database{ID: id, Name: "owa-offline-data-synthetic-" + strconv.FormatInt(id, 10), Stores: []indexeddb.Store{{ID: 1, Name: "folders"}, {ID: 2, Name: "messages"}, {ID: 3, Name: "messageBodies"}, {ID: 4, Name: "events"}}}
}

// mailboxRecords builds one populated content database whose folder and message records carry the given mailboxInfo.
func mailboxRecords(r *mailboxReader, db int64, prefix string, folderInfo, messageInfo any) {
	r.dbs = append(r.dbs, mailboxDB(db))
	r.values[[2]int64{db, 1}] = []any{obj("id", prefix+"/Inbox", "parentFolderId", prefix+"/Root", "displayName", "Synthetic inbox", "distinguishedFolderType", "inbox", "mailboxInfo", folderInfo)}
	r.values[[2]int64{db, 2}] = []any{obj("ItemId", obj("Id", prefix+"/Message", "mailboxInfo", messageInfo), "ParentFolderId", ident(prefix+"/Inbox"), "Subject", "Synthetic")}
}

func newMailboxReader() *mailboxReader { return &mailboxReader{values: map[[2]int64][]any{}} }

func TestCollectMailboxesResolvesIdentityPerDatabase(t *testing.T) {
	r := newMailboxReader()
	mailboxRecords(r, 1, "A", info("Owner@Example.invalid", "owner@example.invalid").build(), info("owner@example.invalid", " OWNER@example.invalid ").build())
	mailboxRecords(r, 2, "B", info("shared@example.invalid", "owner@example.invalid").build(), info("shared@example.invalid", "owner@example.invalid").build())
	got, err := collectMailboxes(context.Background(), r)
	if err != nil || len(got) != 2 {
		t.Fatalf("collect = %d, %v", len(got), err)
	}
	if got[0].Identity == "" || got[1].Identity == "" || got[0].Identity == got[1].Identity {
		t.Fatal("each populated database needs its own resolved identity")
	}
	if len(got[0].Identity) != 64 || got[0].Identity != MailboxIdentity("owner@example.invalid", "OWNER@example.invalid") {
		t.Fatal("identity is not the normalized address pair hash")
	}
	if len(got[0].Result.Messages) != 1 || got[0].Result.Messages[0].ID != "A/Message" || got[1].Result.Messages[0].ID != "B/Message" {
		t.Fatal("mailbox records crossed databases")
	}
	for _, mb := range got {
		if mb.Result.Messages[0].Value.Account != "" {
			t.Fatal("collector assigned an account before attribution")
		}
		mb.Result.SetAccount("outlook/Synthetic")
		if mb.Result.Messages[0].Value.Account != "outlook/Synthetic" {
			t.Fatal("SetAccount did not attribute messages")
		}
	}
	if r2, _ := collectMailboxes(context.Background(), r); r2[0].Identity != got[0].Identity || r2[1].Identity != got[1].Identity {
		t.Fatal("identity is not deterministic across reads")
	}
}

func TestCollectMailboxesLeavesUnprovedIdentityUnresolved(t *testing.T) {
	good := info("owner@example.invalid", "owner@example.invalid").build()
	for name, tc := range map[string]struct{ folder, message any }{
		"folder_missing":   {nil, good},
		"message_missing":  {good, nil},
		"conflicting":      {good, info("other@example.invalid", "owner@example.invalid").build()},
		"empty_address":    {info("", "owner@example.invalid").build(), info("", "owner@example.invalid").build()},
		"empty_user":       {info("owner@example.invalid", "").build(), info("owner@example.invalid", "").build()},
		"non_string":       {info(7.0, "owner@example.invalid").build(), info(7.0, "owner@example.invalid").build()},
		"info_not_object":  {"owner@example.invalid", "owner@example.invalid"},
		"undefined_fields": {obj("type", "UserMailbox"), obj("type", "UserMailbox")},
	} {
		t.Run(name, func(t *testing.T) {
			r := newMailboxReader()
			mailboxRecords(r, 1, "A", tc.folder, tc.message)
			got, err := collectMailboxes(context.Background(), r)
			if err != nil || len(got) != 1 || got[0].Identity != "" || len(got[0].Result.Messages) != 1 {
				t.Fatalf("unproved identity = %+v, %v", got, err)
			}
		})
	}
}

func TestCollectMailboxesParentFolderInfoMustAgreeWhenPresent(t *testing.T) {
	r := newMailboxReader()
	owner := info("owner@example.invalid", "owner@example.invalid").build()
	mailboxRecords(r, 1, "A", owner, owner)
	r.values[[2]int64{1, 2}] = []any{obj("ItemId", obj("Id", "A/Message", "mailboxInfo", owner), "ParentFolderId", obj("Id", "A/Inbox", "mailboxInfo", info("other@example.invalid", "owner@example.invalid").build()))}
	got, err := collectMailboxes(context.Background(), r)
	if err != nil || got[0].Identity != "" {
		t.Fatal("a disagreeing parent folder identity was ignored")
	}
	r.values[[2]int64{1, 2}] = []any{obj("ItemId", obj("Id", "A/Message", "mailboxInfo", owner), "ParentFolderId", obj("Id", "A/Inbox", "mailboxInfo", owner))}
	if got, err = collectMailboxes(context.Background(), r); err != nil || got[0].Identity == "" {
		t.Fatal("an agreeing parent folder identity was refused")
	}
}

func TestSelectMailboxesKeepsLayoutGuards(t *testing.T) {
	full := mailboxDB(1)
	second := mailboxDB(2)
	zero := indexeddb.Database{ID: 3, Name: "owa-offline-data-zero"}
	partial := indexeddb.Database{ID: 4, Name: "owa-offline-data-partial", Stores: []indexeddb.Store{{ID: 1, Name: "credentials"}}}
	got, err := selectMailboxes([]indexeddb.Database{zero, full, second})
	if err != nil || len(got) != 2 || got[0].db.ID != 1 || got[1].db.ID != 2 || got[1].stores["messageBodies"] != 3 {
		t.Fatalf("two populated databases = %+v, %v", got, err)
	}
	for name, tc := range map[string]struct {
		dbs    []indexeddb.Database
		reason string
	}{
		"none":      {[]indexeddb.Database{{ID: 9, Name: "unrelated", Stores: full.Stores}}, "content_database_missing"},
		"zero_only": {[]indexeddb.Database{zero}, "required_store_missing"},
		"partial":   {[]indexeddb.Database{full, partial}, "required_store_missing"},
		"duplicate": {[]indexeddb.Database{full, {ID: 2, Name: "owa-offline-data", Stores: append(append([]indexeddb.Store{}, full.Stores...), indexeddb.Store{ID: 9, Name: "folders"})}}, "required_store_ambiguous"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := selectMailboxes(tc.dbs)
			var guard *hxstore.GuardError
			if !errors.As(err, &guard) || guard.Detail != tc.reason {
				t.Fatalf("guard = %v", err)
			}
		})
	}
	if _, _, err := selectMailStores([]indexeddb.Database{full, second}); !errors.As(err, new(*hxstore.GuardError)) {
		t.Fatal("the single-mailbox selector must keep refusing several mailboxes")
	}
}

func TestExpectMailRefusesEmptyHeldMailbox(t *testing.T) {
	if err := CheckExpected(Result{}, true); err == nil {
		t.Fatal("an archived mailbox with no cached headers must refuse")
	}
	if CheckExpected(Result{}, false) != nil || CheckExpected(Result{Notes: notesWithHeaders(1)}, true) != nil {
		t.Fatal("expected mail check refused a valid read")
	}
}

func TestCollectMailboxesFailuresAreFixed(t *testing.T) {
	good := func() *mailboxReader {
		r := newMailboxReader()
		owner := info("owner@example.invalid", "owner@example.invalid").build()
		mailboxRecords(r, 1, "A", owner, owner)
		return r
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := collectMailboxes(canceled, good()); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled = %v", err)
	}
	meta := good()
	meta.metaErr = errors.New("synthetic-private-detail")
	if _, err := collectMailboxes(context.Background(), meta); err == nil || err.Error() != "outlookowa: metadata_unreadable" {
		t.Fatalf("metadata = %v", err)
	}
	var guard *hxstore.GuardError
	if _, err := collectMailboxes(context.Background(), newMailboxReader()); !errors.As(err, &guard) || guard.Detail != "content_database_missing" {
		t.Fatalf("layout = %v", err)
	}
	scan := good()
	scan.scanErr = errors.New("synthetic-private-detail")
	if _, err := collectMailboxes(context.Background(), scan); err == nil || err.Error() != "outlookowa: records_unreadable" {
		t.Fatalf("records = %v", err)
	}
}
