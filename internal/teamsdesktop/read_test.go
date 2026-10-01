package teamsdesktop

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/ourostack/teamscrawl/internal/errs"
	"github.com/ourostack/teamscrawl/internal/indexeddb"
	"github.com/ourostack/teamscrawl/internal/leveldb"
)

const (
	tenant1 = "00000000-0000-4000-8000-000000000001"
	tenant2 = "00000000-0000-4000-8000-000000000002"
	user1   = "00000000-0000-4000-8000-0000000000a1"
	user2   = "00000000-0000-4000-8000-0000000000a2"
)

func dbName(manager, tenant, user string) string {
	return "Teams:" + manager + ":react-web-client:" + tenant + ":" + user + ":en-us"
}

func fixtureSnapshot(t *testing.T) string {
	t.Helper()
	snap, cleanup, err := Snapshot(context.Background(), fixtureSource(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	return snap
}

func TestReadFixtureKinds(t *testing.T) {
	snap := fixtureSnapshot(t)
	counts := map[string]int{}
	om, err := Read(context.Background(), snap, nil, func(a Account, kind string, v any) error {
		if v == nil {
			t.Errorf("nil value for %s", kind)
		}
		counts[fmt.Sprintf("%s|%s|%s", kind, a.TenantID, a.UserID)]++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(om) != 0 {
		t.Fatalf("omissions = %v", om)
	}
	want := map[string]int{
		KindReplyChain + "|" + tenant1 + "|" + user1:   51,
		KindReplyChain + "|" + tenant2 + "|" + user2:   51,
		KindConversation + "|" + tenant1 + "|" + user1: 7,
		KindConversation + "|" + tenant2 + "|" + user2: 7,
		KindActivity + "|" + tenant1 + "|" + user1:     8,
		KindActivity + "|" + tenant2 + "|" + user2:     8,
	}
	if fmt.Sprint(counts) != fmt.Sprint(want) {
		t.Fatalf("counts = %v\nwant    %v", counts, want)
	}
}

func TestReadAccountFilter(t *testing.T) {
	snap := fixtureSnapshot(t)
	n := 0
	acct := Account{TenantID: tenant2, UserID: user2}
	_, err := Read(context.Background(), snap, &acct, func(a Account, kind string, v any) error {
		if a.TenantID != tenant2 || a.UserID != user2 || a.Locale != "en-us" {
			t.Errorf("unexpected account %+v", a)
		}
		n++
		return nil
	})
	if err != nil || n != 66 {
		t.Fatalf("n=%d err=%v", n, err)
	}
}

// recordingOrigin wraps the real origin and records which databases are touched.
type recordingOrigin struct {
	origin
	names   map[int64]string
	decoded []string
	walked  []string
}

func (r *recordingOrigin) Records(dbID, storeID int64, fn func(indexeddb.Record) error) error {
	r.walked = append(r.walked, r.names[dbID])
	return r.origin.Records(dbID, storeID, fn)
}

func (r *recordingOrigin) Decode(dbID int64, raw []byte) (any, error) {
	r.decoded = append(r.decoded, r.names[dbID])
	return r.origin.Decode(dbID, raw)
}

func TestFixtureNeverDecodesAuth(t *testing.T) {
	snap := fixtureSnapshot(t)
	real, err := indexeddb.Open(snap+"/leveldb", snap+"/blob")
	if err != nil {
		t.Fatal(err)
	}
	dbs, _ := real.Databases()
	rec := &recordingOrigin{origin: real, names: map[int64]string{}}
	authSeen := 0
	for _, d := range dbs {
		rec.names[d.ID] = d.Name
		if strings.HasPrefix(d.Name, "Teams:auth:") {
			authSeen++
		}
	}
	if authSeen == 0 {
		t.Fatal("fixture lost its decoy auth database")
	}
	_, err = readOrigin(context.Background(), rec, nil, func(a Account, kind string, v any) error {
		if s := fmt.Sprintf("%#v", v); strings.Contains(s, "FIXTURE-SECRET-TOKEN") {
			t.Errorf("decoded value contains the decoy token (%s)", kind)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.decoded) == 0 || len(rec.walked) == 0 {
		t.Fatal("nothing decoded")
	}
	for _, n := range append(append([]string{}, rec.decoded...), rec.walked...) {
		m, _, ok := ParseDatabaseName(n)
		if !ok || (m != "replychain-manager" && m != "conversation-manager" && m != "activity-manager") {
			t.Errorf("non-allowlisted database touched: manager %q", m)
		}
	}
}

// fakeOrigin is a scripted origin for error paths the fixture cannot produce.
type fakeOrigin struct {
	dbs     []indexeddb.Database
	records map[int64][]indexeddb.Record
	decode  func(dbID int64, raw []byte) (any, error)
	recErr  error
	stats   leveldb.Stats
}

func (f *fakeOrigin) Databases() ([]indexeddb.Database, error) { return f.dbs, nil }
func (f *fakeOrigin) Records(dbID, _ int64, fn func(indexeddb.Record) error) error {
	if f.recErr != nil {
		return f.recErr
	}
	for _, r := range f.records[dbID] {
		if err := fn(r); err != nil {
			return err
		}
	}
	return nil
}
func (f *fakeOrigin) Decode(dbID int64, raw []byte) (any, error) { return f.decode(dbID, raw) }
func (f *fakeOrigin) Stats() leveldb.Stats                       { return f.stats }

func TestReadStoreMissing(t *testing.T) {
	f := &fakeOrigin{dbs: []indexeddb.Database{
		{ID: 1, Name: dbName("replychain-manager", tenant1, user1), Stores: []indexeddb.Store{{ID: 1, Name: "replychains-2"}}},
		{ID: 2, Name: dbName("conversation-manager", tenant1, user1), Stores: []indexeddb.Store{{ID: 1, Name: "something-else"}}},
	}}
	f.decode = func(int64, []byte) (any, error) { return map[string]any{}, nil }
	_, err := readOrigin(context.Background(), f, nil, func(Account, string, any) error { return nil })
	c := codeOf(t, err)
	if c.Code != errs.CodeStoreMissing || c.Exit != 1 {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(c.Message, user1) || strings.Contains(c.Message, tenant1) {
		t.Fatalf("message leaks ids: %q", c.Message)
	}
}

func TestReadAbsentManagerIsFine(t *testing.T) {
	f := &fakeOrigin{dbs: []indexeddb.Database{
		{ID: 1, Name: dbName("auth", tenant1, user1), Stores: []indexeddb.Store{{ID: 1, Name: "tokens"}}},
	}}
	om, err := readOrigin(context.Background(), f, nil, func(Account, string, any) error { t.Fatal("called"); return nil })
	if err != nil || om == nil || len(om) != 0 {
		t.Fatalf("om=%v err=%v", om, err)
	}
}

func TestReadCountsOmissions(t *testing.T) {
	f := &fakeOrigin{
		dbs: []indexeddb.Database{
			{ID: 1, Name: dbName("replychain-manager", tenant1, user1), Stores: []indexeddb.Store{{ID: 1, Name: "replychains-2"}}},
			{ID: 2, Name: dbName("activity-manager", tenant1, user1), Stores: []indexeddb.Store{{ID: 3, Name: "feed-items"}}},
		},
		records: map[int64][]indexeddb.Record{
			1: {
				{Key: "a", Raw: []byte("ok")},
				{Key: "b", Raw: []byte("empty")},
				{Key: "c", Raw: []byte("blob")},
				{Key: "d", Raw: []byte("ok")},
				{Err: &indexeddb.OmissionError{Omission: indexeddb.Omission{Code: indexeddb.CodeBadKey}}},
			},
			2: {{Key: "x", Raw: []byte("ok")}, {Key: "y", Raw: []byte("unmapped")}},
		},
		stats: leveldb.Stats{TruncatedLogTails: 2},
	}
	f.decode = func(_ int64, raw []byte) (any, error) {
		switch string(raw) {
		case "empty":
			return nil, &indexeddb.OmissionError{Omission: indexeddb.Omission{Code: indexeddb.CodeEmptyValue}}
		case "blob":
			return nil, &indexeddb.OmissionError{Omission: indexeddb.Omission{Code: indexeddb.CodeBlobMissing}}
		}
		return string(raw), nil
	}
	kinds := map[string]int{}
	om, err := readOrigin(context.Background(), f, nil, func(_ Account, kind string, v any) error {
		if v == "unmapped" {
			return fmt.Errorf("wrapped: %w", &UnmappedError{Reason: "no id"})
		}
		kinds[kind]++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"empty_value": 1, "blob_missing": 1, "bad_key": 1, "truncated_log_tail": 2, "unmapped_record": 1}
	if fmt.Sprint(om) != fmt.Sprint(want) {
		t.Fatalf("omissions = %v, want %v", om, want)
	}
	if kinds[KindReplyChain] != 2 || kinds[KindActivity] != 1 {
		t.Fatalf("kinds = %v", kinds)
	}
}

func TestReadUnsupportedCompression(t *testing.T) {
	f := &fakeOrigin{
		dbs:    []indexeddb.Database{{ID: 1, Name: dbName("conversation-manager", tenant1, user1), Stores: []indexeddb.Store{{ID: 1, Name: "conversations"}}}},
		recErr: fmt.Errorf("walk: %w", leveldb.ErrUnsupportedCompression),
	}
	_, err := readOrigin(context.Background(), f, nil, func(Account, string, any) error { return nil })
	if c := codeOf(t, err); c.Code != errs.CodeUnsupportedBlockCompression || c.Exit != 1 {
		t.Fatalf("err = %v", err)
	}
}

func TestReadCallbackErrorStops(t *testing.T) {
	f := &fakeOrigin{
		dbs:     []indexeddb.Database{{ID: 1, Name: dbName("conversation-manager", tenant1, user1), Stores: []indexeddb.Store{{ID: 1, Name: "conversations"}}}},
		records: map[int64][]indexeddb.Record{1: {{Key: "a", Raw: []byte("x")}, {Key: "b", Raw: []byte("y")}}},
		decode:  func(int64, []byte) (any, error) { return 1, nil },
	}
	boom := errors.New("boom")
	n := 0
	_, err := readOrigin(context.Background(), f, nil, func(Account, string, any) error { n++; return boom })
	if !errors.Is(err, boom) || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
}

func TestReadContextCancelled(t *testing.T) {
	snap := fixtureSnapshot(t)
	ctx, cancel := context.WithCancel(context.Background())
	n := 0
	_, err := Read(ctx, snap, nil, func(Account, string, any) error {
		n++
		cancel()
		return nil
	})
	if !errors.Is(err, context.Canceled) || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
}

// Read loads only the databases it will decode: the allowlisted managers, for the chosen account.
func TestKeepDatabase(t *testing.T) {
	acct := Account{TenantID: tenant2, UserID: user2}
	for _, c := range []struct {
		name    string
		account *Account
		want    bool
	}{
		{dbName("replychain-manager", tenant1, user1), nil, true},
		{dbName("conversation-manager", tenant1, "8:orgid:"+user1), nil, true},
		{dbName("activity-manager", tenant1, user1), nil, true},
		{dbName("auth", tenant1, user1), nil, false},
		{dbName("calling-manager", tenant1, user1), nil, false},
		{"Teams:auth:secret", nil, false},
		{"some-other-db", nil, false},
		{dbName("replychain-manager", tenant1, user1), &acct, false},
		{dbName("replychain-manager", tenant2, user2), &acct, true},
	} {
		if got := keepDatabase(c.account)(c.name); got != c.want {
			t.Errorf("keepDatabase(%v)(%q) = %v want %v", c.account, c.name, got, c.want)
		}
	}
}

// Cancelling removes the snapshot, so a value re-read after cancellation fails with a missing
// file; the read reports the cancellation, not snapshot_inconsistent.
func TestReadCancelledDuringValueReread(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f := &fakeOrigin{
		dbs:    []indexeddb.Database{{ID: 1, Name: dbName("replychain-manager", tenant1, user1), Stores: []indexeddb.Store{{ID: 1, Name: "replychains-2"}}}},
		recErr: fmt.Errorf("leveldb: %w", &leveldb.MissingFileError{Name: "000005.ldb"}),
	}
	_, err := readOrigin(ctx, f, nil, func(Account, string, any) error { return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

// A value that cannot be re-read from the snapshot fails the read as snapshot_inconsistent; it
// is never counted as an omission.
func TestReadUnreadableValueFails(t *testing.T) {
	f := &fakeOrigin{
		dbs:    []indexeddb.Database{{ID: 1, Name: dbName("replychain-manager", tenant1, user1), Stores: []indexeddb.Store{{ID: 1, Name: "replychains-2"}}}},
		recErr: fmt.Errorf("leveldb: %w", &leveldb.MissingFileError{Name: "000005.ldb"}),
	}
	om, err := readOrigin(context.Background(), f, nil, func(Account, string, any) error { return nil })
	c := codeOf(t, err)
	if c.Code != errs.CodeSnapshotInconsistent || len(om) != 0 {
		t.Fatalf("err = %v, omissions %v", err, om)
	}
}

// Cancelling removes the snapshot while its tables are still loading; the load then fails as a
// missing file, and Read must report the cancellation instead of a damaged cache.
func TestReadReportsCancellationWhenTheSnapshotLoadFails(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Read(ctx, t.TempDir(), nil, func(Account, string, any) error { return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	// Without a cancellation the same failure is still classified.
	_, err = Read(context.Background(), t.TempDir(), nil, func(Account, string, any) error { return nil })
	if err == nil || errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}
