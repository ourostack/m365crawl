//go:build acceptance

package acceptance

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/indexeddb"
	"github.com/ourostack/m365crawl/internal/outlookowa"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/syncer"
	"github.com/ourostack/m365crawl/internal/teamsdesktop"
	"github.com/ourostack/m365crawl/internal/v8"
)

// The mailbox census reports counts, field names and value types only. Identity values are
// hashed in memory to count distinct values and cross-database overlap; no value, database
// name, suffix, path or record key leaves the process.
const owaMailboxCensusEnv = "OWA_MAILBOX_CENSUS"

var owaMailboxCensusStores = []string{"folders", "messages", "messageBodies", "events"}

var owaMailboxCensusPaths = map[string][][]string{
	"folders":  {{"mailboxInfo"}},
	"messages": {{"ItemId", "mailboxInfo"}, {"ParentFolderId", "mailboxInfo"}},
	"events":   {{"ItemId", "mailboxInfo"}, {"ParentFolderId", "mailboxInfo"}},
}

var owaMailboxCensusFields = []string{"mailboxSmtpAddress", "userIdentity", "sourceId", "mailboxRank", "type", "auxiliaryMailboxSmtpAddress", "mailboxProvider"}

type owaFieldCensus struct {
	Present  int            `json:"present"`
	Absent   int            `json:"absent"`
	Types    map[string]int `json:"types"`
	Empty    int            `json:"empty_string"`
	Distinct int            `json:"distinct"`
	values   map[[32]byte]bool
}

type owaDBCensus struct {
	Index        int                        `json:"index"`
	BareName     bool                       `json:"bare_name"`
	StoreCount   int                        `json:"store_count"`
	Records      map[string]int             `json:"records"`
	Undecoded    map[string]int             `json:"undecoded"`
	InfoKeys     map[string]map[string]int  `json:"mailbox_info_keys"`
	Fields       map[string]*owaFieldCensus `json:"fields"`
	Consistency  map[string]int             `json:"consistency"`
	tuples       map[[32]byte]bool
	folderTuples map[string][32]byte
}

type owaProfileCensus struct {
	Index            int            `json:"index"`
	OWADatabases     int            `json:"owa_databases"`
	ContentDatabases int            `json:"content_databases"`
	Databases        []*owaDBCensus `json:"databases"`
	CrossDatabase    map[string]int `json:"cross_database_shared_values"`
	Error            string         `json:"error,omitempty"`
}

func TestOWAMailboxIdentityCensus(t *testing.T) {
	if os.Getenv(owaMailboxCensusEnv) != "1" {
		t.Skip("set " + owaMailboxCensusEnv + "=1 to census mailbox identity fields of the real new Outlook cache")
	}
	root := os.Getenv(outlookRootEnv)
	if root == "" {
		var err error
		if root, err = outlookowa.DefaultRoot(); err != nil {
			t.Fatalf("default root refused")
		}
	}
	profiles, skipped, err := outlookowa.Discover(root)
	if err != nil {
		t.Fatalf("discover refused")
	}
	sort.Slice(profiles, func(i, j int) bool { return profiles[i].Name < profiles[j].Name })
	report := struct {
		GeneratedAt string              `json:"generated_at"`
		Profiles    int                 `json:"profiles"`
		Skipped     int                 `json:"skipped_profiles"`
		Detail      []*owaProfileCensus `json:"detail"`
		Sync        *owaSyncCensus      `json:"sync,omitempty"`
	}{GeneratedAt: time.Now().UTC().Format(time.RFC3339), Profiles: len(profiles), Skipped: len(skipped)}
	for i, p := range profiles {
		report.Detail = append(report.Detail, owaCensusProfile(i, p.Source))
	}
	if os.Getenv(owaMailboxCensusEnv+"_SYNC") == "1" {
		report.Sync = owaCensusSync(t, root)
	}
	data, _ := json.MarshalIndent(report, "", " ")
	if path := os.Getenv(owaMailboxCensusEnv + "_PATH"); path != "" {
		if err := os.WriteFile(path, data, 0o600); err != nil { //nolint:gosec // the operator chooses the private report path
			t.Fatalf("write report refused")
		}
	}
	t.Logf("%s", data)
}

func owaCensusProfile(index int, source teamsdesktop.Source) (pc *owaProfileCensus) {
	pc = &owaProfileCensus{Index: index, CrossDatabase: map[string]int{}}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	snap, cleanup, err := teamsdesktop.Snapshot(ctx, source)
	defer cleanup()
	if err != nil {
		pc.Error = "snapshot_refused"
		return pc
	}
	level, blob := filepath.Join(snap, "leveldb"), filepath.Join(snap, "blob")
	_, dbs, err := indexeddb.Census(level)
	if err != nil {
		pc.Error = "census_metadata_refused"
		return pc
	}
	var content []indexeddb.Database
	for _, db := range dbs {
		if db.Name == "owa-offline-data" || strings.HasPrefix(db.Name, "owa-offline-data-") {
			pc.OWADatabases++
			if len(db.Stores) > 0 {
				content = append(content, db)
			}
		}
	}
	pc.ContentDatabases = len(content)
	keep := map[[2]int64]bool{}
	storeIDs := map[int64]map[string]int64{}
	for _, db := range content {
		storeIDs[db.ID] = map[string]int64{}
		for _, s := range db.Stores {
			for _, want := range owaMailboxCensusStores {
				if s.Name == want {
					storeIDs[db.ID][want] = s.ID
					keep[[2]int64{db.ID, s.ID}] = true
				}
			}
		}
	}
	o, err := indexeddb.OpenWith(level, blob, indexeddb.OpenOptions{KeepStore: func(d, s int64) bool { return keep[[2]int64{d, s}] }})
	if err != nil || o == nil {
		pc.Error = "open_refused"
		return pc
	}
	defer func() {
		if o.Close() != nil {
			pc.Error = "close_refused"
		}
	}()
	for i, db := range content {
		dc := &owaDBCensus{Index: i, BareName: db.Name == "owa-offline-data", StoreCount: len(db.Stores), Records: map[string]int{}, Undecoded: map[string]int{},
			InfoKeys: map[string]map[string]int{}, Fields: map[string]*owaFieldCensus{}, Consistency: map[string]int{}, tuples: map[[32]byte]bool{}, folderTuples: map[string][32]byte{}}
		for _, name := range owaMailboxCensusStores {
			storeID, ok := storeIDs[db.ID][name]
			if !ok {
				dc.Records[name] = -1
				continue
			}
			err := o.Records(db.ID, storeID, func(rec indexeddb.Record) error {
				dc.Records[name]++
				if rec.Err != nil {
					dc.Undecoded[name]++
					return nil
				}
				value, err := o.Decode(db.ID, rec.Raw)
				if err != nil {
					dc.Undecoded[name]++
					return nil
				}
				dc.observe(name, value)
				return nil
			})
			if err != nil {
				pc.Error = "records_refused"
				return pc
			}
		}
		for _, f := range dc.Fields {
			f.Distinct = len(f.values)
		}
		dc.Consistency["distinct_identity_tuples"] = len(dc.tuples)
		pc.Databases = append(pc.Databases, dc)
	}
	for i := range pc.Databases {
		for j := i + 1; j < len(pc.Databases); j++ {
			for name, a := range pc.Databases[i].Fields {
				b, ok := pc.Databases[j].Fields[name]
				if !ok {
					continue
				}
				for v := range a.values {
					if b.values[v] {
						pc.CrossDatabase[name]++
					}
				}
			}
			for v := range pc.Databases[i].tuples {
				if pc.Databases[j].tuples[v] {
					pc.CrossDatabase["identity_tuple"]++
				}
			}
		}
	}
	return pc
}

func owaCensusGet(value any, path ...string) (any, bool) {
	for _, key := range path {
		obj, ok := value.(*v8.Object)
		if !ok || obj == nil {
			return nil, false
		}
		found := false
		for i, k := range obj.Keys {
			if k == key && i < len(obj.Values) {
				value, found = obj.Values[i], true
				break
			}
		}
		if !found {
			return nil, false
		}
	}
	return value, true
}

func owaCensusType(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case v8.Undefined:
		return "undefined"
	case string:
		return "string"
	case bool:
		return "bool"
	case float64, int32, uint32, int64:
		return "number"
	case *v8.Object:
		return "object"
	case []any:
		return "array"
	default:
		return fmt.Sprintf("%T", x)
	}
}

func owaCensusHash(v any) [32]byte {
	return sha256.Sum256([]byte(fmt.Sprintf("%T\x00%v", v, v)))
}

// owaCensusTuple hashes the three candidate identity fields of one mailboxInfo object.
func owaCensusTuple(info any) ([32]byte, bool) {
	var parts []string
	ok := false
	for _, f := range []string{"mailboxSmtpAddress", "userIdentity", "sourceId"} {
		v, present := owaCensusGet(info, f)
		if present {
			ok = true
		}
		parts = append(parts, fmt.Sprintf("%v:%T:%v", present, v, v))
	}
	return sha256.Sum256([]byte(strings.Join(parts, "\x00"))), ok
}

func owaCensusFold(v any) string {
	s, _ := v.(string)
	return strings.ToLower(strings.TrimSpace(s))
}

func (dc *owaDBCensus) observe(store string, value any) {
	var tuples [][32]byte
	var infos []any
	for _, path := range owaMailboxCensusPaths[store] {
		label := store + "." + strings.Join(path, ".")
		info, present := owaCensusGet(value, path...)
		if dc.InfoKeys[label] == nil {
			dc.InfoKeys[label] = map[string]int{}
		}
		if !present {
			dc.InfoKeys[label]["<absent>"]++
			infos = append(infos, nil)
			continue
		}
		obj, isObj := info.(*v8.Object)
		if !isObj || obj == nil {
			dc.InfoKeys[label]["<"+owaCensusType(info)+">"]++
			infos = append(infos, nil)
			continue
		}
		infos = append(infos, info)
		for _, k := range obj.Keys {
			dc.InfoKeys[label][k]++
		}
		for _, field := range owaMailboxCensusFields {
			key := label + "." + field
			fc := dc.Fields[key]
			if fc == nil {
				fc = &owaFieldCensus{Types: map[string]int{}, values: map[[32]byte]bool{}}
				dc.Fields[key] = fc
			}
			v, ok := owaCensusGet(info, field)
			if !ok {
				fc.Absent++
				continue
			}
			fc.Present++
			fc.Types[owaCensusType(v)]++
			if s, isString := v.(string); isString && s == "" {
				fc.Empty++
			}
			fc.values[owaCensusHash(v)] = true
		}
		smtp, _ := owaCensusGet(info, "mailboxSmtpAddress")
		user, _ := owaCensusGet(info, "userIdentity")
		if owaCensusFold(smtp) != "" && owaCensusFold(user) != "" {
			if owaCensusFold(smtp) == owaCensusFold(user) {
				dc.Consistency[label+".smtp_equals_user_identity"]++
			} else {
				dc.Consistency[label+".smtp_differs_from_user_identity"]++
			}
		}
		if tuple, ok := owaCensusTuple(info); ok {
			dc.tuples[tuple] = true
			tuples = append(tuples, tuple)
		}
	}
	switch store {
	case "folders":
		id, _ := owaCensusGet(value, "id")
		if s, ok := id.(string); ok && len(tuples) == 1 {
			dc.folderTuples[s] = tuples[0]
		}
	case "messages", "events":
		if len(infos) == 2 && infos[0] != nil && infos[1] != nil {
			a, _ := owaCensusTuple(infos[0])
			b, _ := owaCensusTuple(infos[1])
			if a == b {
				dc.Consistency[store+".item_equals_parent_folder_info"]++
			} else {
				dc.Consistency[store+".item_differs_from_parent_folder_info"]++
			}
		}
		if store == "messages" && infos[0] != nil {
			parent, _ := owaCensusGet(value, "ParentFolderId", "Id")
			if s, ok := parent.(string); ok {
				if ft, known := dc.folderTuples[s]; known {
					a, _ := owaCensusTuple(infos[0])
					if a == ft {
						dc.Consistency["messages.item_equals_folder_record_info"]++
					} else {
						dc.Consistency["messages.item_differs_from_folder_record_info"]++
					}
				} else {
					dc.Consistency["messages.folder_record_unknown"]++
				}
			}
		}
	}
}

type owaSyncRun struct {
	RunError    string         `json:"run_error,omitempty"`
	MailSources int            `json:"mail_sources"`
	Statuses    map[string]int `json:"statuses"`
	Errors      map[string]int `json:"errors"`
	Added       int            `json:"added"`
	Updated     int            `json:"updated"`
}

type owaSyncCensus struct {
	Runs               []owaSyncRun `json:"runs"`
	ArchiveError       string       `json:"archive_error,omitempty"`
	Accounts           int          `json:"mail_accounts"`
	MessagesPerAccount []int        `json:"messages_per_account"`
	Bindings           int          `json:"mailbox_bindings"`
	CrossAccountIDs    int          `json:"native_ids_in_several_accounts"`
	Absent             int          `json:"absent"`
	ReadMarkers        int          `json:"read_markers"`
}

// owaCensusSync runs the product's sync twice into a private scratch archive and reports counts only.
func owaCensusSync(t *testing.T, root string) *owaSyncCensus {
	t.Helper()
	// Windows archives need a directory only the current user and SYSTEM can read.
	dir := owaScratch(t)
	db := filepath.Join(dir, "census.db")
	out := &owaSyncCensus{}
	for i := 0; i < 2; i++ {
		rep, _, runErr := syncer.Run(context.Background(), syncer.Options{Root: filepath.Join(dir, "no-teams"), DBPath: db, OutlookEnabled: true, OutlookRoot: root, OutlookMinReadInterval: -1, FullRead: i == 1})
		run := owaSyncRun{Statuses: map[string]int{}, Errors: map[string]int{}}
		if runErr != nil {
			// Only the fixed code crosses the report boundary, never the message.
			run.RunError = "uncoded"
			var coded *errs.Coded
			if errors.As(runErr, &coded) {
				run.RunError = coded.Code
			}
		}
		for _, s := range rep.Sources {
			if !strings.HasPrefix(s.Source, "outlook|") || !strings.HasSuffix(s.Source, "|mail") {
				continue
			}
			run.MailSources++
			run.Statuses[s.Status]++
			if s.Error != nil {
				detail := ""
				if s.Error.Code == "outlook_mail_layout_unsupported" {
					if _, d, ok := strings.Cut(s.Error.Message, "supported: "); ok {
						detail = ":" + strings.Fields(d + " ")[0]
					}
				}
				run.Errors[s.Error.Code+detail]++
			}
			if s.Counts != nil && s.Counts.Mail != nil {
				run.Added += s.Counts.Mail.Added
				run.Updated += s.Counts.Mail.Updated
			}
		}
		out.Runs = append(out.Runs, run)
	}
	st, err := store.OpenReadOnly(context.Background(), db)
	if err != nil {
		out.ArchiveError = "open_refused"
		if errors.Is(err, store.ErrNoArchive) {
			out.ArchiveError = "not_created"
		}
		return out
	}
	defer func() { _ = st.Close() }()
	scalar := func(q string) int {
		rows, err := owaQuery(st, q)
		if err != nil || len(rows) != 1 || len(rows[0]) != 1 {
			t.Fatalf("archive count refused")
		}
		n, _ := rows[0][0].(int64)
		return int(n)
	}
	rows, err := owaQuery(st, `select count(*) from mail_messages group by account order by 1`)
	if err != nil {
		t.Fatalf("archive count refused")
	}
	for _, r := range rows {
		n, _ := r[0].(int64)
		out.MessagesPerAccount = append(out.MessagesPerAccount, int(n))
	}
	out.Accounts = len(out.MessagesPerAccount)
	out.Bindings = scalar(`select count(*) from meta where key like 'outlook_mailbox_identity:%'`)
	out.CrossAccountIDs = scalar(`select count(*) from (select kind, source_id from mail_source_ids group by kind, source_id having count(distinct account) > 1)`)
	out.Absent = scalar(`select count(*) from mail_absent`)
	out.ReadMarkers = scalar(`select count(*) from meta where key like 'outlook_mail_read:%'`)
	return out
}
