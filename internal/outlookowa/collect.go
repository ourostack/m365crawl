package outlookowa

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"strings"

	"github.com/ourostack/m365crawl/internal/hxstore"
	"github.com/ourostack/m365crawl/internal/indexeddb"
	"github.com/ourostack/m365crawl/internal/leveldb"
	"github.com/ourostack/m365crawl/internal/outlookmail"
)

type reader interface {
	Databases() ([]indexeddb.Database, error)
	Records(dbID, storeID int64, fn func(indexeddb.Record) error) error
	Decode(dbID int64, raw []byte) (any, error)
	Stats() leveldb.Stats
}

// Collect reads only the mail stores; the caller keeps its validated private snapshot alive until return.
func Collect(ctx context.Context, origin *indexeddb.Origin, account string, expectMail bool) (Result, error) {
	if origin == nil {
		return Result{}, errors.New("outlookowa: origin_missing")
	}
	return collect(ctx, origin, account, expectMail)
}

func layout(reason string) error {
	return &hxstore.GuardError{Code: outlookmail.CodeMailLayoutUnsupported, Detail: reason}
}

func (r *Result) omission(err error) {
	code := "outlook_owa_reader_omission"
	var oe *indexeddb.OmissionError
	if errors.As(err, &oe) {
		switch oe.Code {
		case "empty_value", "unknown_envelope", "snappy_too_large", "blob_missing", "bad_key", "v8_unknown_tag", "v8_host_object", "v8_shared", "v8_version", "v8_malformed":
			code = "outlook_owa_reader_" + oe.Code
		}
	}
	r.loss(code)
	r.Notes.Unmapped++
	r.Doubtful = true
}

type records struct {
	values    map[string]any
	conflicts map[string]bool
}

func newRecords() records { return records{values: map[string]any{}, conflicts: map[string]bool{}} }

func (s records) put(id string, v any, r *Result) {
	if s.conflicts[id] {
		return
	}
	if old, exists := s.values[id]; exists {
		if !reflect.DeepEqual(old, v) {
			delete(s.values, id)
			s.conflicts[id] = true
			r.loss("outlook_owa_duplicate_conflict")
			r.Doubtful = true
		}
		return
	}
	s.values[id] = v
}

func collect(ctx context.Context, o reader, account string, expectMail bool) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	dbs, err := o.Databases()
	if err != nil {
		return Result{}, errors.New("outlookowa: metadata_unreadable")
	}
	db, stores, err := selectMailStores(dbs)
	if err != nil {
		return Result{}, err
	}
	r := Result{}
	folders, headers, bodies := newRecords(), newRecords(), newRecords()
	seen := map[string]bool{}
	folderUncertain := false
	if tails := o.Stats().TruncatedLogTails; tails > 0 {
		r.Losses = append(r.Losses, outlookmail.Loss{Code: "outlook_owa_reader_truncated_log_tail", Count: tails})
		r.Doubtful = true
		folderUncertain = true
	}
	for _, name := range []string{"folders", "messages", "messageBodies"} {
		err = o.Records(db.ID, stores[name], func(rec indexeddb.Record) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if name == "messages" {
				r.Notes.HeadersSeen++
			}
			value, decodeErr := any(nil), rec.Err
			if decodeErr == nil {
				value, decodeErr = o.Decode(db.ID, rec.Raw)
			}
			if decodeErr != nil {
				r.omission(decodeErr)
				if name == "folders" {
					folderUncertain = true
				}
				return nil
			}
			switch name {
			case "folders":
				id := r.text(value, "id")
				if !validID(id) {
					r.Notes.Unmapped++
					r.Doubtful = true
					r.loss(outlookmail.CodeMailUnmapped)
					folderUncertain = true
					return nil
				}
				folders.put(id, value, &r)
			case "messages":
				id := r.text(value, "ItemId", "Id")
				if !validID(id) {
					r.Notes.Unmapped++
					r.Doubtful = true
					r.loss(outlookmail.CodeMailUnmapped)
					return nil
				}
				seen[id] = true
				headers.put(id, value, &r)
			case "messageBodies":
				id := r.text(value, "ItemId", "Id")
				if field(value, "ItemId") == nil {
					id, _ = rec.Key.(string)
				}
				if !validID(id) {
					r.Notes.Unmapped++
					r.Doubtful = true
					r.loss(outlookmail.CodeMailUnmapped)
					return nil
				}
				bodies.put(id, value, &r)
			}
			return nil
		})
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		if err != nil {
			return Result{}, errors.New("outlookowa: records_unreadable")
		}
	}
	if expectMail && r.Notes.HeadersSeen == 0 {
		return Result{}, layout("no_mail_objects")
	}
	mappedFolders := map[string]Folder{}
	for id, value := range folders.values {
		f, ok := mapFolder(value, &r)
		if !ok {
			folderUncertain = true
			continue
		}
		mappedFolders[id] = f
		r.Folders = append(r.Folders, f)
	}
	if len(folders.conflicts) > 0 {
		folderUncertain = true
	}
	r.RootID = folderRoot(mappedFolders)
	if folderUncertain || r.RootID == "" {
		r.RootID = ""
		r.Doubtful = true
		r.loss("outlook_owa_root_uncertain")
	}
	for _, value := range headers.values {
		m, ok := mapMessage(value, account, &r)
		if !ok {
			continue
		}
		f, ok := mappedFolders[m.FolderID]
		if !ok {
			r.Notes.MissingFolder++
			r.Notes.UnknownFolderUnattributed++
			r.Doubtful = true
			r.loss(outlookmail.CodeMailFolderMissing)
			continue
		}
		m.Value.Folder = f.Value
		if body, exists := bodies.values[m.ID]; exists {
			m.Value.Body = mapBody(body, &r)
		} else {
			r.Notes.MissingDetail++
		}
		if m.Value.Body.State == "inline" {
			r.Notes.BodiesInline++
		} else {
			r.Notes.BodiesNone++
		}
		r.Messages = append(r.Messages, m)
	}
	for id := range seen {
		r.SeenIDs = append(r.SeenIDs, id)
	}
	r.Notes.Messages = len(r.Messages)
	sort.Slice(r.Folders, func(i, j int) bool { return r.Folders[i].ID < r.Folders[j].ID })
	sort.Slice(r.Messages, func(i, j int) bool { return r.Messages[i].ID < r.Messages[j].ID })
	sort.Strings(r.SeenIDs)
	sort.Slice(r.Losses, func(i, j int) bool { return r.Losses[i].Code < r.Losses[j].Code })
	return r, nil
}

// selectMailStores is the single case-exact layout guard shared by opening and collecting.
func selectMailStores(dbs []indexeddb.Database) (indexeddb.Database, map[string]int64, error) {
	var selected []indexeddb.Database
	for _, db := range dbs {
		if db.Name == "owa-offline-data" || strings.HasPrefix(db.Name, "owa-offline-data-") {
			selected = append(selected, db)
		}
	}
	if len(selected) == 0 {
		return indexeddb.Database{}, nil, layout("content_database_missing")
	}
	if len(selected) != 1 {
		return indexeddb.Database{}, nil, layout("multiple_mailboxes")
	}
	db := selected[0]
	stores := map[string]int64{}
	for _, s := range db.Stores {
		switch s.Name {
		case "folders", "messages", "messageBodies":
			if _, exists := stores[s.Name]; exists {
				return indexeddb.Database{}, nil, layout("required_store_ambiguous")
			}
			stores[s.Name] = s.ID
		}
	}
	for _, name := range []string{"folders", "messages", "messageBodies"} {
		if _, ok := stores[name]; !ok {
			return indexeddb.Database{}, nil, layout("required_store_missing")
		}
	}
	return db, stores, nil
}

func folderRoot(folders map[string]Folder) string {
	roots := map[string]bool{}
	for id := range folders {
		visited := map[string]bool{}
		for {
			if visited[id] {
				return ""
			}
			visited[id] = true
			f, exists := folders[id]
			if !exists {
				roots[id] = true
				break
			}
			id = f.ParentID
		}
	}
	if len(roots) != 1 {
		return ""
	}
	var root string
	for id := range roots {
		root = id
	}
	return root
}
