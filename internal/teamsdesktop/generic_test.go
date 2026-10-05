package teamsdesktop

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sort"
	"strings"
	"testing"
	"weak"

	"github.com/ourostack/teamscrawl/internal/errs"
	"github.com/ourostack/teamscrawl/internal/indexeddb"
	"github.com/ourostack/teamscrawl/internal/leveldb"
	"github.com/ourostack/teamscrawl/internal/v8"
)

func TestDeniedNames(t *testing.T) {
	for _, c := range []struct {
		name string
		want bool
	}{
		{"Teams:auth:t:u", true},
		{"Teams:token-cache-manager:t:u", true},
		{"Teams:MSAL-x", true},
		{"Teams:replychain-manager:t:u", false},
		{"Teams:calendar-manager:t:u", false},
		{"keyval-store", true},
		{"my-KeyStore", true},
		{"Teams:oneauth-cache", true},
		{"Teams:cookie-jar", true},
		{"Teams:credential-manager", true},
		{"Teams:secret-store", true},
		{"Teams:session-manager", true},
		{"Teams:key-store-manager", true},
		{"Teams:key-value-store", true},
		{"keyring", true},
		{"Teams:crypto-manager", true},
		{"Teams:encrypted-blobs", true},
		{"pkce-verifiers", true},
		{"BearerCache", true},
		{"Teams:password-store", true},
		{"refresh-info", true},
		{"adal-cache", true},
		{"Teams:aad-cache:t:u", true},
		{"aad", true},
		{"Teams:keys", true},
		{"Teams:jwt", true},
		{"Teams:e2ee", true},
		{"e2ee_store", true},
		{"Teams:load-manager", false},
		{"Teams:monkeys-manager", false},
		{"Teams:aadvark", false},
		{"Teams:jwtx", false},
		{"Teams:e2eex", false},
		{"userKeys", true},
		{"foo/keys", true},
		{"authToken", true},
		{"Teams:userAAD", true},
		{"AADToken", true},
		{"myJWTStore", true},
		{"load", false},
		{"monkeys", false},
		{"Teams:loadManager", false},
		{"fooKeysBar", true},
		{"a/load", false},
	} {
		if got := Denied(c.name); got != c.want {
			t.Errorf("Denied(%q) = %v want %v", c.name, got, c.want)
		}
	}
}

func TestDeniedStoreNames(t *testing.T) {
	if !Denied("sessionTokens") || Denied("events") {
		t.Fatal("store name denial wrong")
	}
}

type genericRead struct {
	recs []GenericRecord
	res  GenericResult
	// seen is what OnDatabase was given, per database.
	seen map[string]map[string]map[string]struct{}
}

func (g *genericRead) opts() GenericOptions {
	g.seen = map[string]map[string]map[string]struct{}{}
	return GenericOptions{OnDatabase: func(db string, _ bool, seen map[string]map[string]struct{}) error {
		g.seen[db] = seen
		return nil
	}}
}

func readGenericAll(t *testing.T, snap string, account *Account, budget int64) genericRead {
	t.Helper()
	var g genericRead
	res, err := ReadGeneric(context.Background(), snap, account, budget, g.opts(), func(r GenericRecord) error {
		g.recs = append(g.recs, r)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	g.res = res
	return g
}

func TestReadGenericFixture(t *testing.T) {
	g := readGenericAll(t, fixtureSnapshot(t), nil, DefaultGenericBudget)
	perStore := map[string]int{}
	for _, r := range g.recs {
		m, _, ok := ParseDatabaseName(r.Database)
		if !ok {
			t.Errorf("unexpected database %q", r.Database)
			continue
		}
		if _, isTyped := typed[m]; isTyped || Denied(r.Database) || Denied(r.Store) {
			t.Errorf("record from %s/%s", r.Database, r.Store)
		}
		if r.Account == nil || r.ValueJSON == nil {
			t.Errorf("record without account or value: %+v", r)
		}
		perStore[m+"/"+r.Store]++
	}
	if perStore["calendar-manager/events"] != 4 || perStore["pinned-manager/pins"] != 8 || len(perStore) != 2 {
		t.Fatalf("records per store = %v", perStore)
	}
	if g.res.Omissions["denied_database"] != 3 || g.res.Omissions["denied_store"] != 1 {
		t.Fatalf("omissions = %v", g.res.Omissions)
	}
	if len(g.res.Present) != 4 {
		t.Fatalf("present = %v", g.res.Present)
	}
	for _, n := range g.res.Present {
		if !g.res.Complete[n] || len(g.seen[n]) == 0 {
			t.Errorf("%s: complete=%v stores seen=%d", n, g.res.Complete[n], len(g.seen[n]))
		}
	}
}

func TestReadGenericKeysCanonical(t *testing.T) {
	g := readGenericAll(t, fixtureSnapshot(t), &Account{TenantID: tenant1, UserID: user1}, DefaultGenericBudget)
	var keys []string
	for _, r := range g.recs {
		if r.Store == "pins" {
			keys = append(keys, string(r.KeyJSON))
		}
	}
	sort.Strings(keys)
	want := []string{
		`"fixture-pin-1"`,
		`["fixture",1,"array"]`,
		`{"$bytes":"AQIDAQ=="}`,
		`{"$date":"2023-11-14T22:13:21Z"}`,
	}
	if fmt.Sprint(keys) != fmt.Sprint(want) {
		t.Fatalf("keys = %q want %q", keys, want)
	}
	for _, k := range want {
		if _, ok := g.seen[g.recs[0].Database]; !ok {
			t.Fatal("seen missing")
		}
		found := false
		for _, s := range g.seen {
			if _, ok := s["pins"][k]; ok {
				found = true
			}
		}
		if !found {
			t.Errorf("key %s not in Seen", k)
		}
	}
}

// fakeGeneric scripts a snapshot for ReadGeneric: the census and the origin of each batch.
type fakeGeneric struct {
	dbs        []indexeddb.Database
	held       map[int64]int64
	records    map[int64][]indexeddb.Record
	decode     func(raw []byte) (any, error)
	recErr     error
	stats      leveldb.Stats
	openErr    error
	censusEr   error
	opens      [][]string
	prev       weak.Pointer[fakeGenericOrigin]
	released   []bool
	closes     int // origins closed
	payloadErr error
}

type fakeGenericOrigin struct {
	f   *fakeGeneric
	buf []byte // gives the origin a real allocation to track
}

func (o *fakeGenericOrigin) Records(dbID, _ int64, fn func(indexeddb.Record) error) error {
	if o.f.recErr != nil {
		return o.f.recErr
	}
	for _, r := range o.f.records[dbID] {
		if err := fn(r); err != nil {
			return err
		}
	}
	return nil
}
func (o *fakeGenericOrigin) Decode(_ int64, raw []byte) (any, error) { return o.f.decode(raw) }
func (o *fakeGenericOrigin) Close() error                            { o.f.closes++; return nil }
func (o *fakeGenericOrigin) Payload(_ int64, raw []byte) ([]byte, error) {
	return raw, o.f.payloadErr
}
func (o *fakeGenericOrigin) Stats() leveldb.Stats { _ = len(o.buf); return o.f.stats }

func (f *fakeGeneric) install(t *testing.T) {
	t.Helper()
	if f.decode == nil {
		f.decode = func(raw []byte) (any, error) { return string(raw), nil }
	}
	oc, oo := censusFn, openBatch
	t.Cleanup(func() { censusFn, openBatch = oc, oo })
	censusFn = func(string) (map[int64]int64, []indexeddb.Database, error) {
		if f.censusEr != nil {
			return nil, nil, f.censusEr
		}
		return f.held, f.dbs, nil
	}
	openBatch = func(_ string, keep func(int64, int64) bool) (genericOrigin, error) {
		if f.openErr != nil {
			return nil, f.openErr
		}
		runtime.GC()
		if len(f.opens) > 0 {
			f.released = append(f.released, f.prev.Value() == nil)
		}
		var names []string
		for _, d := range f.dbs {
			for _, st := range d.Stores {
				if keep(d.ID, st.ID) {
					names = append(names, d.Name)
					break
				}
			}
		}
		f.opens = append(f.opens, names)
		o := &fakeGenericOrigin{f: f, buf: make([]byte, 1024)}
		f.prev = weak.Make(o)
		return o, nil
	}
}

func gdb(id int64, manager string, stores ...string) indexeddb.Database {
	d := indexeddb.Database{ID: id, Name: dbName(manager, tenant1, user1)}
	for i, s := range stores {
		d.Stores = append(d.Stores, indexeddb.Store{ID: int64(i + 1), Name: s})
	}
	return d
}

func strRec(key, val string) indexeddb.Record { return indexeddb.Record{Key: key, Raw: []byte(val)} }

func (f *fakeGeneric) run(t *testing.T, account *Account, budget int64) (genericRead, error) {
	t.Helper()
	f.install(t)
	var g genericRead
	res, err := ReadGeneric(context.Background(), "/snap", account, budget, g.opts(), func(r GenericRecord) error {
		g.recs = append(g.recs, r)
		return nil
	})
	g.res = res
	return g, err
}

func TestReadGenericBatches(t *testing.T) {
	const mib = 1 << 20
	f := &fakeGeneric{
		dbs:  []indexeddb.Database{gdb(1, "a-manager", "s"), gdb(2, "b-manager", "s"), gdb(3, "c-manager", "s")},
		held: map[int64]int64{1: 40 * mib, 2: 40 * mib, 3: 100 * mib},
		records: map[int64][]indexeddb.Record{
			1: {strRec("k", "one")}, 2: {strRec("k", "two")}, 3: {strRec("k", "three")},
		},
	}
	g, err := f.run(t, nil, 64*mib)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.opens) != 3 {
		t.Fatalf("opens = %v", f.opens)
	}
	for i, want := range []string{"a-manager", "b-manager", "c-manager"} {
		if len(f.opens[i]) != 1 || !strings.Contains(f.opens[i][0], want) {
			t.Errorf("batch %d = %v want %s", i, f.opens[i], want)
		}
	}
	for i, ok := range f.released {
		if !ok {
			t.Errorf("origin %d was still referenced when origin %d opened", i, i+1)
		}
	}
	var order []string
	for _, r := range g.recs {
		order = append(order, strings.Trim(string(r.ValueJSON), `"`))
	}
	if fmt.Sprint(order) != `[one two three]` {
		t.Fatalf("order = %v", order)
	}
}

func TestReadGenericGroupsSmallDatabases(t *testing.T) {
	f := &fakeGeneric{
		dbs:     []indexeddb.Database{gdb(1, "a-manager", "s"), gdb(2, "b-manager", "s"), gdb(3, "c-manager", "s")},
		held:    map[int64]int64{1: 10, 2: 10, 3: 10},
		records: map[int64][]indexeddb.Record{},
	}
	if _, err := f.run(t, nil, 25); err != nil {
		t.Fatal(err)
	}
	if len(f.opens) != 2 || len(f.opens[0]) != 2 || len(f.opens[1]) != 1 {
		t.Fatalf("opens = %v", f.opens)
	}
}

func TestReadGenericDecodeFailureStillSeen(t *testing.T) {
	f := &fakeGeneric{
		dbs:  []indexeddb.Database{gdb(1, "a-manager", "s")},
		held: map[int64]int64{1: 1},
		records: map[int64][]indexeddb.Record{1: {
			strRec("bad", "x"), strRec("good", "y"),
		}},
		decode: func(raw []byte) (any, error) {
			if string(raw) == "x" {
				return nil, &indexeddb.OmissionError{Omission: indexeddb.Omission{Code: indexeddb.CodeUnknownEnvelope}}
			}
			return string(raw), nil
		},
	}
	g, err := f.run(t, nil, DefaultGenericBudget)
	if err != nil {
		t.Fatal(err)
	}
	name := f.dbs[0].Name
	if len(g.recs) != 2 || g.recs[0].ValueJSON != nil || string(g.recs[1].ValueJSON) != `"y"` {
		t.Fatalf("records = %+v", g.recs)
	}
	if g.res.Omissions[indexeddb.CodeUnknownEnvelope] != 1 || !g.res.Complete[name] || len(g.seen[name]["s"]) != 2 {
		t.Fatalf("res = %+v", g.res)
	}
}

func TestReadGenericDecodeFatalIsolatesDatabase(t *testing.T) {
	f := &fakeGeneric{
		dbs:  []indexeddb.Database{gdb(1, "a-manager", "s"), gdb(2, "b-manager", "s")},
		held: map[int64]int64{1: 1, 2: 1},
		records: map[int64][]indexeddb.Record{
			1: {strRec("k", "boom")}, 2: {strRec("k", "fine")},
		},
		decode: func(raw []byte) (any, error) {
			if string(raw) == "boom" {
				return nil, errors.New("boom")
			}
			return string(raw), nil
		},
	}
	g, err := f.run(t, nil, DefaultGenericBudget)
	if err != nil {
		t.Fatalf("a broken database stopped the read: %v", err)
	}
	a, b := f.dbs[0].Name, f.dbs[1].Name
	if g.res.Complete[a] || !g.res.Complete[b] || g.res.Omissions[omitDatabaseUnreadable] != 1 {
		t.Fatalf("res = %+v", g.res)
	}
	if len(g.recs) != 1 || g.recs[0].Database != b {
		t.Fatalf("records = %+v", g.recs)
	}
	if seen, ok := g.seen[a]; !ok || seen != nil || len(g.seen[b]["s"]) != 1 {
		t.Fatalf("OnDatabase seen = %v", g.seen)
	}
	if len(g.res.Present) != 2 {
		t.Fatalf("present = %v", g.res.Present)
	}
}

func TestReadGenericUnencodableValue(t *testing.T) {
	f := &fakeGeneric{
		dbs:     []indexeddb.Database{gdb(1, "a-manager", "s")},
		held:    map[int64]int64{1: 1},
		records: map[int64][]indexeddb.Record{1: {strRec("k", "x")}},
		decode:  func([]byte) (any, error) { return make(chan int), nil },
	}
	g, err := f.run(t, nil, DefaultGenericBudget)
	if err != nil || len(g.recs) != 1 || g.recs[0].ValueJSON != nil || g.res.Omissions[omitUnencodableValue] != 1 {
		t.Fatalf("err=%v res=%+v recs=%+v", err, g.res, g.recs)
	}
}

func TestReadGenericBadKeyIncomplete(t *testing.T) {
	bad := indexeddb.Record{Err: &indexeddb.OmissionError{Omission: indexeddb.Omission{Code: indexeddb.CodeBadKey}}}
	unencodable := indexeddb.Record{Key: make(chan int), Raw: []byte("v")}
	f := &fakeGeneric{
		dbs:  []indexeddb.Database{gdb(1, "a-manager", "s"), gdb(2, "b-manager", "s"), gdb(3, "c-manager", "s")},
		held: map[int64]int64{1: 1, 2: 1, 3: 1},
		records: map[int64][]indexeddb.Record{
			1: {bad, strRec("k", "v")}, 2: {unencodable}, 3: {strRec("k", "v")},
		},
	}
	g, err := f.run(t, nil, DefaultGenericBudget)
	if err != nil {
		t.Fatal(err)
	}
	if g.res.Complete[f.dbs[0].Name] || g.res.Complete[f.dbs[1].Name] || !g.res.Complete[f.dbs[2].Name] {
		t.Fatalf("complete = %v", g.res.Complete)
	}
	if g.res.Omissions[indexeddb.CodeBadKey] != 2 || len(g.recs) != 2 {
		t.Fatalf("omissions = %v recs = %d", g.res.Omissions, len(g.recs))
	}
}

func TestReadGenericPlainRecordErrorMarksDatabaseUnreadable(t *testing.T) {
	f := &fakeGeneric{
		dbs:     []indexeddb.Database{gdb(1, "a-manager", "s")},
		held:    map[int64]int64{1: 1},
		records: map[int64][]indexeddb.Record{1: {{Err: errors.New("plain")}}},
	}
	g, err := f.run(t, nil, DefaultGenericBudget)
	if err != nil || g.res.Omissions[omitDatabaseUnreadable] != 1 || g.res.Complete[f.dbs[0].Name] {
		t.Fatalf("err=%v res=%+v", err, g.res)
	}
}

func TestReadGenericAccountSkipsUnparsed(t *testing.T) {
	other := indexeddb.Database{ID: 3, Name: dbName("c-manager", tenant2, user2), Stores: []indexeddb.Store{{ID: 1, Name: "s"}}}
	bare := indexeddb.Database{ID: 2, Name: "plain-db", Stores: []indexeddb.Store{{ID: 1, Name: "s"}}}
	deniedBare := indexeddb.Database{ID: 4, Name: "keyval-store", Stores: []indexeddb.Store{{ID: 1, Name: "keyval"}}}
	mk := func() *fakeGeneric {
		return &fakeGeneric{
			dbs:  []indexeddb.Database{gdb(1, "a-manager", "s"), bare, other, deniedBare},
			held: map[int64]int64{1: 1, 2: 1, 3: 1, 4: 1},
			records: map[int64][]indexeddb.Record{
				1: {strRec("k", "v")}, 2: {strRec("k", "v")}, 3: {strRec("k", "v")},
			},
		}
	}
	g, err := mk().run(t, &Account{TenantID: tenant1, UserID: user1}, DefaultGenericBudget)
	if err != nil || len(g.recs) != 1 || g.recs[0].Account == nil || g.recs[0].Account.UserID != user1 {
		t.Fatalf("err=%v recs=%+v", err, g.recs)
	}
	if len(g.res.Present) != 1 || g.res.Omissions[omitDeniedDatabase] != 0 {
		t.Fatalf("res = %+v", g.res)
	}
	// Without an account the unparsed database is read, with a nil account, and the bare denied
	// database is counted.
	g, err = mk().run(t, nil, DefaultGenericBudget)
	if err != nil || len(g.recs) != 3 || g.res.Omissions[omitDeniedDatabase] != 1 {
		t.Fatalf("err=%v recs=%d res=%+v", err, len(g.recs), g.res)
	}
	for _, r := range g.recs {
		if r.Database == "plain-db" && r.Account != nil {
			t.Fatal("unparsed database has an account")
		}
	}
}

func TestReadGenericSkipsTypedAndDeniedInsideScope(t *testing.T) {
	f := &fakeGeneric{
		dbs: []indexeddb.Database{
			gdb(1, "replychain-manager", "replychains-2"),
			gdb(2, "token-cache-manager", "entries"),
			gdb(3, "a-manager", "events", "sessionTokens"),
		},
		held:    map[int64]int64{1: 1, 2: 1, 3: 1},
		records: map[int64][]indexeddb.Record{1: {strRec("k", "v")}, 2: {strRec("k", "v")}, 3: {strRec("k", "v")}},
	}
	g, err := f.run(t, nil, DefaultGenericBudget)
	if err != nil || len(g.recs) != 1 || g.recs[0].Store != "events" {
		t.Fatalf("err=%v recs=%+v", err, g.recs)
	}
	if g.res.Omissions[omitDeniedDatabase] != 1 || g.res.Omissions[omitDeniedStore] != 1 || len(f.opens) != 1 {
		t.Fatalf("res=%+v opens=%v", g.res, f.opens)
	}
}

func TestReadGenericTruncatedTail(t *testing.T) {
	f := &fakeGeneric{
		dbs:     []indexeddb.Database{gdb(1, "a-manager", "s"), gdb(2, "b-manager", "s")},
		held:    map[int64]int64{1: 10, 2: 10},
		records: map[int64][]indexeddb.Record{},
		stats:   leveldb.Stats{TruncatedLogTails: 1},
	}
	g, err := f.run(t, nil, 15)
	if err != nil || g.res.Omissions[omitTruncatedLogTail] != 1 {
		t.Fatalf("err=%v omissions=%v", err, g.res.Omissions)
	}
}

func TestReadGenericCancel(t *testing.T) {
	f := &fakeGeneric{
		dbs:     []indexeddb.Database{gdb(1, "a-manager", "s"), gdb(2, "b-manager", "s")},
		held:    map[int64]int64{1: 10, 2: 10},
		records: map[int64][]indexeddb.Record{1: {strRec("k", "v"), strRec("k2", "v")}, 2: {strRec("k", "v")}},
	}
	f.install(t)
	// Cancelled before the first batch.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ReadGeneric(ctx, "/snap", nil, 15, GenericOptions{}, func(GenericRecord) error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	// Cancelled from the callback: the in-flight record loop and the next batch both stop.
	ctx, cancel = context.WithCancel(context.Background())
	n := 0
	_, err := ReadGeneric(ctx, "/snap", nil, 15, GenericOptions{}, func(GenericRecord) error { n++; cancel(); return nil })
	if !errors.Is(err, context.Canceled) || n != 1 {
		t.Fatalf("err=%v n=%d", err, n)
	}
	// Cancelled while the snapshot loads: report the cancellation, not a damaged cache.
	ctx, cancel = context.WithCancel(context.Background())
	cancel()
	f.censusEr = errors.New("snapshot removed")
	if _, err := ReadGeneric(ctx, "/snap", nil, 15, GenericOptions{}, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("census err = %v", err)
	}
	f.censusEr = nil
	f.openErr = errors.New("snapshot removed")
	ctx, cancel = context.WithCancel(context.Background())
	cancel()
	batch := []genericDB{{db: f.dbs[0]}}
	if err := readBatch(ctx, "/snap", batch, &GenericResult{}, GenericOptions{}, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("open err = %v", err)
	}
	// Cancelled while a database is read: the cancellation, not an unreadable database.
	f.openErr = nil
	f.recErr = errors.New("file removed")
	ctx, cancel = context.WithCancel(context.Background())
	cancel()
	res := &GenericResult{Omissions: map[string]int{}, Complete: map[string]bool{}}
	if err := readBatch(ctx, "/snap", []genericDB{{db: f.dbs[0], stores: f.dbs[0].Stores}}, res, GenericOptions{}, nil); !errors.Is(err, context.Canceled) || res.Omissions[omitDatabaseUnreadable] != 0 {
		t.Fatalf("read err = %v omissions = %v", err, res.Omissions)
	}
}

// A batch that cannot be opened makes every database in it unreadable and the read goes on with
// the next batch; the other databases and the typed read are not lost.
func TestReadGenericOpenFailureMarksBatchUnreadable(t *testing.T) {
	f := &fakeGeneric{
		dbs:     []indexeddb.Database{gdb(1, "a-manager", "s"), gdb(2, "b-manager", "s"), gdb(3, "c-manager", "s")},
		held:    map[int64]int64{1: 10, 2: 10, 3: 100},
		records: map[int64][]indexeddb.Record{3: {strRec("k", "v")}},
	}
	f.install(t)
	real := openBatch
	opens := 0
	openBatch = func(dir string, keep func(int64, int64) bool) (genericOrigin, error) {
		if opens++; opens == 1 {
			return nil, errors.New("open failed")
		}
		return real(dir, keep)
	}
	var g genericRead
	res, err := ReadGeneric(context.Background(), "/snap", nil, 25, g.opts(), func(r GenericRecord) error {
		g.recs = append(g.recs, r)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Omissions[omitDatabaseUnreadable] != 2 || res.Complete[f.dbs[0].Name] || res.Complete[f.dbs[1].Name] || !res.Complete[f.dbs[2].Name] || len(g.recs) != 1 {
		t.Fatalf("res = %+v recs = %v", res, g.recs)
	}
	if _, ok := g.seen[f.dbs[0].Name]; !ok || g.seen[f.dbs[0].Name] != nil {
		t.Fatalf("seen = %v", g.seen)
	}
}

func TestReadGenericOnDatabaseErrorStops(t *testing.T) {
	f := &fakeGeneric{
		dbs:     []indexeddb.Database{gdb(1, "a-manager", "s")},
		held:    map[int64]int64{1: 1},
		records: map[int64][]indexeddb.Record{1: {strRec("k", "v")}},
	}
	f.install(t)
	boom := errors.New("mark failed")
	if _, err := ReadGeneric(context.Background(), "/snap", nil, 1, GenericOptions{OnDatabase: func(string, bool, map[string]map[string]struct{}) error { return boom }}, func(GenericRecord) error { return nil }); !errors.Is(err, boom) {
		t.Fatalf("complete database: %v", err)
	}
	f.recErr = errors.New("unreadable")
	if _, err := ReadGeneric(context.Background(), "/snap", nil, 1, GenericOptions{OnDatabase: func(string, bool, map[string]map[string]struct{}) error { return boom }}, func(GenericRecord) error { return nil }); !errors.Is(err, boom) {
		t.Fatalf("unreadable database: %v", err)
	}
	f.recErr = nil
	f.openErr = errors.New("open failed")
	if _, err := ReadGeneric(context.Background(), "/snap", nil, 1, GenericOptions{OnDatabase: func(string, bool, map[string]map[string]struct{}) error { return boom }}, func(GenericRecord) error { return nil }); !errors.Is(err, boom) {
		t.Fatalf("unopened batch: %v", err)
	}
}

// Typed databases are read for their other stores only; a typed database whose only store is
// consumed by its mapper contributes nothing; excluded and denied stores are never kept.
func TestReadGenericTypedDatabasesOtherStores(t *testing.T) {
	f := &fakeGeneric{
		dbs: []indexeddb.Database{
			gdb(1, "replychain-manager", "replychains-2"),
			gdb(2, "conversation-manager", "conversations", "drafts", "sessionTokens"),
			gdb(3, "a-manager", "events"),
		},
		held: map[int64]int64{1: 1, 2: 1, 3: 1},
		records: map[int64][]indexeddb.Record{
			1: {strRec("k", "typed")}, 2: {strRec("k", "v")}, 3: {strRec("k", "v")},
		},
	}
	f.install(t)
	var kept [][2]int64
	real := openBatch
	openBatch = func(dir string, keep func(int64, int64) bool) (genericOrigin, error) {
		for _, d := range f.dbs {
			for _, st := range d.Stores {
				if keep(d.ID, st.ID) {
					kept = append(kept, [2]int64{d.ID, st.ID})
				}
			}
		}
		return real(dir, keep)
	}
	var g genericRead
	res, err := ReadGeneric(context.Background(), "/snap", nil, 1<<20, g.opts(), func(r GenericRecord) error {
		g.recs = append(g.recs, r)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(kept) != "[[2 2] [3 1]]" {
		t.Fatalf("kept stores = %v", kept)
	}
	if len(g.recs) != 2 || g.recs[0].Store != "drafts" || g.recs[1].Store != "events" || res.Omissions[omitDeniedStore] != 1 || len(res.Present) != 2 {
		t.Fatalf("recs=%+v res=%+v", g.recs, res)
	}
}

// Values are scrubbed before they reach the callback, and the redactions are counted.
func TestReadGenericScrubsValues(t *testing.T) {
	f := &fakeGeneric{
		dbs:     []indexeddb.Database{gdb(1, "a-manager", "s")},
		held:    map[int64]int64{1: 1},
		records: map[int64][]indexeddb.Record{1: {strRec("k", "x"), strRec("k2", "y")}},
		decode: func(raw []byte) (any, error) {
			if string(raw) == "x" {
				return &v8.Object{Keys: []string{"access_token", "ok"}, Values: []any{"secret", "https://h/p?sig=abc"}}, nil
			}
			return "plain", nil
		},
	}
	g, err := f.run(t, nil, DefaultGenericBudget)
	if err != nil || len(g.recs) != 2 {
		t.Fatalf("err=%v recs=%v", err, g.recs)
	}
	if string(g.recs[0].ValueJSON) != `{"access_token":"[redacted]","ok":"https://h/p?sig=[redacted]"}` || g.res.Redacted != 2 {
		t.Fatalf("value=%s redacted=%d", g.recs[0].ValueJSON, g.res.Redacted)
	}
}

func TestReadGenericErrors(t *testing.T) {
	mk := func() *fakeGeneric {
		return &fakeGeneric{
			dbs:     []indexeddb.Database{gdb(1, "a-manager", "s")},
			held:    map[int64]int64{1: 1},
			records: map[int64][]indexeddb.Record{1: {strRec("k", "v")}},
		}
	}
	f := mk()
	f.censusEr = &leveldb.MissingFileError{Name: "000005.ldb"}
	if _, err := f.run(t, nil, 1); codeOf(t, err).Code != errs.CodeSnapshotInconsistent {
		t.Fatalf("census: %v", err)
	}
	f = mk()
	f.openErr = errors.New("open failed")
	g, err := f.run(t, nil, 1)
	if err != nil || g.res.Omissions[omitDatabaseUnreadable] != 1 || g.res.Complete[f.dbs[0].Name] {
		t.Fatalf("open: err=%v res=%+v", err, g.res)
	}
	f = mk()
	f.recErr = fmt.Errorf("leveldb: %w", &leveldb.MissingFileError{Name: "000005.ldb"})
	g, err = f.run(t, nil, 1)
	if err != nil || g.res.Complete[f.dbs[0].Name] || g.res.Omissions[omitDatabaseUnreadable] != 1 {
		t.Fatalf("records: err=%v res=%+v", err, g.res)
	}
	// Without an OnDatabase callback an unreadable database is still counted.
	f = mk()
	f.recErr = errors.New("unreadable")
	f.install(t)
	res, err := ReadGeneric(context.Background(), "/snap", nil, 1, GenericOptions{}, func(GenericRecord) error { return nil })
	if err != nil || res.Omissions[omitDatabaseUnreadable] != 1 {
		t.Fatalf("no callback: err=%v res=%+v", err, res)
	}
	f = mk()
	boom := errors.New("sink full")
	f.install(t)
	if _, err := ReadGeneric(context.Background(), "/snap", nil, 1, GenericOptions{}, func(GenericRecord) error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("callback: %v", err)
	}
}

func TestCanonKeyPassesOtherTypes(t *testing.T) {
	if canonKey(float64(3)) != float64(3) || canonKey(nil) != nil {
		t.Fatal("scalar keys changed")
	}
}

func TestOpenBatchReal(t *testing.T) {
	snap := fixtureSnapshot(t)
	o, err := openBatch(snap, func(int64, int64) bool { return false })
	if err != nil || o == nil {
		t.Fatalf("openBatch: %v", err)
	}
	if err := o.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := openBatch(t.TempDir(), func(int64, int64) bool { return false }); err == nil {
		t.Fatal("empty directory opened")
	}
}

func TestReadGenericScrubsKeysAndValues(t *testing.T) {
	const jwt = "eyJhbGciOiJub25lIn0.eyJzdWIiOiJ4In0.c2ln"
	f := &fakeGeneric{
		dbs:     []indexeddb.Database{gdb(1, "a-manager", "s")},
		held:    map[int64]int64{1: 1},
		records: map[int64][]indexeddb.Record{1: {strRec(jwt, "x"), strRec("Bearer abc", "y")}},
		decode: func(raw []byte) (any, error) {
			if string(raw) == "x" {
				return &v8.Map{Entries: [][2]any{{"Access_Token", &v8.Object{Keys: []string{"a"}, Values: []any{1.0}}}, {"k", "v"}}}, nil
			}
			return "Bearer zzz", nil
		},
	}
	g, err := f.run(t, nil, DefaultGenericBudget)
	if err != nil || len(g.recs) != 2 {
		t.Fatalf("err=%v recs=%+v", err, g.recs)
	}
	if string(g.recs[0].KeyJSON) != `"[redacted]"` || string(g.recs[1].KeyJSON) != `"[redacted]"` {
		t.Fatalf("keys: %s %s", g.recs[0].KeyJSON, g.recs[1].KeyJSON)
	}
	if string(g.recs[0].ValueJSON) != `{"$map":[["Access_Token","[redacted]"],["k","v"]]}` || string(g.recs[1].ValueJSON) != `"[redacted]"` {
		t.Fatalf("values: %s %s", g.recs[0].ValueJSON, g.recs[1].ValueJSON)
	}
	if g.res.Redacted != 4 {
		t.Fatalf("redacted = %d", g.res.Redacted)
	}
}

// Every batch's origin is closed, whichever way the batch ends: complete, unreadable, stopped
// by a callback error, or cancelled.
func TestReadGenericClosesEveryOrigin(t *testing.T) {
	boom := errors.New("callback failed")
	cases := map[string]func(f *fakeGeneric) (context.Context, func(GenericRecord) error){
		"complete": func(*fakeGeneric) (context.Context, func(GenericRecord) error) {
			return context.Background(), func(GenericRecord) error { return nil }
		},
		"unreadable": func(f *fakeGeneric) (context.Context, func(GenericRecord) error) {
			f.recErr = errors.New("unreadable")
			return context.Background(), func(GenericRecord) error { return nil }
		},
		"callback error": func(*fakeGeneric) (context.Context, func(GenericRecord) error) {
			return context.Background(), func(GenericRecord) error { return boom }
		},
		"cancelled": func(*fakeGeneric) (context.Context, func(GenericRecord) error) {
			ctx, cancel := context.WithCancel(context.Background())
			return ctx, func(GenericRecord) error { cancel(); return errors.New("after cancel") }
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			f := &fakeGeneric{
				dbs:     []indexeddb.Database{gdb(1, "a-manager", "s")},
				held:    map[int64]int64{1: 1},
				records: map[int64][]indexeddb.Record{1: {strRec("k", "v")}},
			}
			f.install(t)
			ctx, fn := setup(f)
			_, _ = ReadGeneric(ctx, "/snap", nil, 1, GenericOptions{}, fn)
			if len(f.opens) != 1 || f.closes != 1 {
				t.Fatalf("opens %d, closes %d", len(f.opens), f.closes)
			}
		})
	}
}
