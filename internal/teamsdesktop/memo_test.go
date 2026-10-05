package teamsdesktop

import (
	"bytes"
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"maps"
	"reflect"
	"regexp"
	"slices"
	"testing"

	"github.com/ourostack/teamscrawl/internal/indexeddb"
)

// changesSignature applies change to the live rule tables, reports whether the signature moved,
// and restores the tables.
func changesSignature(t *testing.T, change func(r *ruleSet)) bool {
	t.Helper()
	saved := rules
	defer func() { rules = saved }()
	// The tables are shared maps and slices: copy them so the change cannot reach the saved ones.
	rules.SecretKeys = maps.Clone(saved.SecretKeys)
	rules.SiblingNameFields = slices.Clone(saved.SiblingNameFields)
	rules.DeniedPrefixes = slices.Clone(saved.DeniedPrefixes)
	rules.DeniedTerms = slices.Clone(saved.DeniedTerms)
	rules.DeniedTokens = slices.Clone(saved.DeniedTokens)
	before := MemoSignature(2)
	change(&rules)
	return !bytes.Equal(before, MemoSignature(2))
}

func TestMemoSignatureIsStableAndCoversTheVersions(t *testing.T) {
	base := MemoSignature(2)
	if len(base) == 0 || !bytes.Equal(base, MemoSignature(2)) {
		t.Fatal("the signature is not stable")
	}
	if bytes.Equal(base, MemoSignature(3)) {
		t.Error("the derivation version does not feed the signature")
	}
	old := decoderVersion
	decoderVersion = old + 1
	defer func() { decoderVersion = old }()
	if bytes.Equal(base, MemoSignature(2)) {
		t.Error("DecoderVersion does not feed the signature")
	}
}

// Every way a rule can change moves the signature: a brand-new key name, a new regular
// expression, a new deny term, and the other tables. The values are ones no probe list could have
// anticipated.
func TestMemoSignatureSeesEveryRuleChange(t *testing.T) {
	for name, change := range map[string]func(r *ruleSet){
		"a new secret key name":      func(r *ruleSet) { r.SecretKeys["session_key"] = true },
		"a secret key name removed":  func(r *ruleSet) { delete(r.SecretKeys, "password") },
		"a new regular expression":   func(r *ruleSet) { r.Sig = regexp.MustCompile(`(?i)([?&]sig=)[^&"\\\s#]+|([?&]token=)[^&]+`) },
		"another regular expression": func(r *ruleSet) { r.JWT = regexp.MustCompile(`eyJ[A-Za-z0-9_-]+`) },
		"the bearer expression":      func(r *ruleSet) { r.Bearer = regexp.MustCompile(`"bearer"`) },
		"the secret name expression": func(r *ruleSet) { r.SecretName = regexp.MustCompile(`"password"`) },
		"a new deny term":            func(r *ruleSet) { r.DeniedTerms = append(r.DeniedTerms, "calendar") },
		"a new deny token":           func(r *ruleSet) { r.DeniedTokens = append(r.DeniedTokens, "load") },
		"a new deny prefix":          func(r *ruleSet) { r.DeniedPrefixes = append(r.DeniedPrefixes, "teams:keys") },
		"the depth limit":            func(r *ruleSet) { r.MaxStringifiedDepth++ },
		"a sibling name field":       func(r *ruleSet) { r.SiblingNameFields = append(r.SiblingNameFields, "label") },
		"the sibling value field":    func(r *ruleSet) { r.SiblingValueField = "secret" },
		"the replacement text":       func(r *ruleSet) { r.Replacement = "[gone]" },
	} {
		if !changesSignature(t, change) {
			t.Errorf("%s does not change the signature", name)
		}
	}
	if changesSignature(t, func(*ruleSet) {}) {
		t.Error("an unchanged rule set changed the signature")
	}
}

// The signature reads the whole rule set by reflection: a field added to the set moves it without
// anyone touching MemoSignature. The walker refuses a kind it cannot hash instead of skipping it.
func TestMemoSignatureCoversEveryFieldOfTheRegistry(t *testing.T) {
	typ := reflect.TypeOf(ruleSet{})
	for i := range typ.NumField() {
		f := typ.Field(i)
		saved := rules
		v := reflect.ValueOf(&rules).Elem().Field(i)
		before := MemoSignature(2)
		switch v.Kind() {
		case reflect.String:
			v.SetString(v.String() + "x")
		case reflect.Int:
			v.SetInt(v.Int() + 1)
		case reflect.Pointer:
			v.Set(reflect.ValueOf(regexp.MustCompile(`changed-` + f.Name)))
		case reflect.Slice:
			v.Set(reflect.Append(v, reflect.ValueOf("added-"+f.Name)))
		case reflect.Map:
			v.Set(reflect.ValueOf(map[string]bool{"added-" + f.Name: true}))
		default:
			t.Fatalf("%s has kind %s: teach this test and hashValue about it", f.Name, v.Kind())
		}
		if bytes.Equal(before, MemoSignature(2)) {
			t.Errorf("registry field %s does not feed the signature", f.Name)
		}
		rules = saved
	}
	if !bytes.Equal(MemoSignature(2), MemoSignature(2)) {
		t.Fatal("restoring the rules did not restore the signature")
	}
	for _, v := range []any{make(chan int), 1.5, map[int]bool{}, struct{ P *int }{}, []func(){nil}} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("hashValue accepted a %T", v)
				}
			}()
			hashValue(io.Discard, "x", reflect.ValueOf(v))
		}()
	}
}

// Scrub and Denied keep no rule outside the registry: scrub.go and deny.go declare no
// package-level variable or constant. A rule table added there would be invisible to the
// signature, so this fails until the table is moved into ruleSet (rules.go).
func TestRuleTablesLiveInTheRegistry(t *testing.T) {
	fset := token.NewFileSet()
	for _, file := range []string{"scrub.go", "deny.go"} {
		f, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			if g, ok := d.(*ast.GenDecl); ok && (g.Tok == token.VAR || g.Tok == token.CONST) {
				for _, spec := range g.Specs {
					for _, n := range spec.(*ast.ValueSpec).Names {
						t.Errorf("%s declares package-level %s: put the rule in ruleSet so the memo signature covers it", file, n.Name)
					}
				}
			}
		}
	}
}

func TestRecordDigest(t *testing.T) {
	sig := []byte("sig")
	d := recordDigest(sig, "db", []byte("payload"))
	if len(d) != DigestLen || !bytes.Equal(d, recordDigest(sig, "db", []byte("payload"))) {
		t.Fatalf("digest %x", d)
	}
	for name, other := range map[string][]byte{
		"signature": recordDigest([]byte("sig2"), "db", []byte("payload")),
		"database":  recordDigest(sig, "db2", []byte("payload")),
		"payload":   recordDigest(sig, "db", []byte("payload2")),
		// The database name and the payload do not run together.
		"boundary": recordDigest(sig, "dbp", []byte("ayload")),
	} {
		if bytes.Equal(d, other) {
			t.Errorf("digest ignores the %s", name)
		}
	}
}

// ReadWith asks Skip about every record with a key, hands it a digest when the bytes can be read,
// and does not decode, or call fn for, a record Skip takes.
func TestReadWithSkip(t *testing.T) {
	db := dbName("replychain-manager", tenant1, user1)
	newOrigin := func() *fakeOrigin {
		f := &fakeOrigin{
			dbs: []indexeddb.Database{{ID: 1, Name: db, Stores: []indexeddb.Store{{ID: 1, Name: "replychains-2"}}}},
			records: map[int64][]indexeddb.Record{1: {
				{Key: "a", Raw: []byte("one")},
				{Key: "b", Raw: []byte("two")},
				{Key: make(chan int), Raw: []byte("three")}, // a key that does not encode
				{Err: &indexeddb.OmissionError{Omission: indexeddb.Omission{Code: indexeddb.CodeBadKey}}},
			}},
		}
		f.decode = func(_ int64, raw []byte) (any, error) { return string(raw), nil }
		return f
	}
	type call struct {
		kind, database, key string
		digest              []byte
	}
	var calls []call
	var got []string
	skipB := func(_ Account, kind, database, keyJSON string, digest []byte) bool {
		calls = append(calls, call{kind, database, keyJSON, digest})
		return keyJSON == `"b"`
	}
	f := newOrigin()
	om, err := readOriginWith(context.Background(), f, nil, ReadOptions{Sig: []byte("s"), Skip: skipB}, func(_ Account, _ string, v any) error {
		got = append(got, v.(string))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "one" || got[1] != "three" {
		t.Fatalf("fn saw %v; the skipped record must not reach it", got)
	}
	if om["bad_key"] != 1 {
		t.Fatalf("omissions = %v", om)
	}
	if len(calls) != 3 {
		t.Fatalf("Skip called %d times: %+v", len(calls), calls)
	}
	if calls[0].kind != KindReplyChain || calls[0].database != db || calls[0].key != `"a"` || !bytes.Equal(calls[0].digest, recordDigest([]byte("s"), db, []byte("one"))) {
		t.Fatalf("first call = %+v", calls[0])
	}
	if calls[2].key != "" || calls[2].digest != nil {
		t.Fatalf("a key that does not encode must reach Skip with no key and no digest: %+v", calls[2])
	}

	// A value whose bytes cannot be read reaches Skip with no digest, and is decoded in full.
	calls = nil
	f = newOrigin()
	f.payloadErr = errors.New("blob gone")
	_, err = readOriginWith(context.Background(), f, nil, ReadOptions{Skip: skipB}, func(Account, string, any) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range calls {
		if c.digest != nil {
			t.Fatalf("a digest for unreadable bytes: %+v", c)
		}
	}
	// Without Skip nothing changes: every record is read.
	got = nil
	if _, err := readOrigin(context.Background(), newOrigin(), nil, func(_ Account, _ string, v any) error { got = append(got, v.(string)); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %v", got)
	}
}

// ReadGeneric asks Known about each record whose bytes can be read; a record Known takes is still
// seen, adds its redactions, and is not decoded or handed to fn. Records that are read in full
// carry the digest of their bytes, unless they are omissions.
func TestReadGenericKnown(t *testing.T) {
	newFake := func() *fakeGeneric {
		return &fakeGeneric{
			dbs:  []indexeddb.Database{gdb(1, "a-manager", "s")},
			held: map[int64]int64{1: 1},
			records: map[int64][]indexeddb.Record{1: {
				strRec("known", "k"), strRec("fresh", "f"), strRec("bad", "x"),
			}},
			decode: func(raw []byte) (any, error) {
				if string(raw) == "x" {
					return nil, &indexeddb.OmissionError{Omission: indexeddb.Omission{Code: indexeddb.CodeUnknownEnvelope}}
				}
				return string(raw), nil
			},
		}
	}
	sig := []byte("sig")
	var asked []string
	var g genericRead
	f := newFake()
	f.install(t)
	opts := g.opts()
	opts.Sig = sig
	opts.Known = func(database, store, keyJSON string, digest []byte) (int, bool) {
		asked = append(asked, store+keyJSON)
		if keyJSON == `"known"` {
			if !bytes.Equal(digest, recordDigest(sig, database, []byte("k"))) {
				t.Errorf("digest %x", digest)
			}
			return 4, true
		}
		return 0, false
	}
	res, err := ReadGeneric(context.Background(), "/snap", nil, DefaultGenericBudget, opts, func(r GenericRecord) error {
		g.recs = append(g.recs, r)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(asked) != 3 {
		t.Fatalf("Known asked %v", asked)
	}
	if res.Redacted != 4 {
		t.Fatalf("Redacted = %d, want the known record's 4", res.Redacted)
	}
	if len(g.recs) != 2 || string(g.recs[0].KeyJSON) != `"fresh"` || string(g.recs[1].KeyJSON) != `"bad"` {
		t.Fatalf("records = %+v", g.recs)
	}
	if !bytes.Equal(g.recs[0].Digest, recordDigest(sig, f.dbs[0].Name, []byte("f"))) {
		t.Fatalf("a record read in full carries no digest: %+v", g.recs[0])
	}
	if g.recs[1].Digest != nil || g.recs[1].ValueJSON != nil {
		t.Fatalf("an omission must carry no digest: %+v", g.recs[1])
	}
	if got := g.seen[f.dbs[0].Name]["s"]; len(got) != 3 {
		t.Fatalf("seen = %v; a skipped record is still seen", got)
	}
	if res.Omissions[indexeddb.CodeUnknownEnvelope] != 1 {
		t.Fatalf("omissions = %v", res.Omissions)
	}

	// Bytes that cannot be read are never offered to Known and are read in full.
	f = newFake()
	f.payloadErr = errors.New("blob gone")
	f.install(t)
	asked = nil
	var recs []GenericRecord
	_, err = ReadGeneric(context.Background(), "/snap", nil, DefaultGenericBudget, GenericOptions{Known: func(string, string, string, []byte) (int, bool) {
		asked = append(asked, "called")
		return 0, true
	}}, func(r GenericRecord) error { recs = append(recs, r); return nil })
	if err != nil || len(asked) != 0 || len(recs) != 3 {
		t.Fatalf("err=%v asked=%v recs=%d", err, asked, len(recs))
	}
	for _, r := range recs {
		if r.Digest != nil {
			t.Fatalf("digest without readable bytes: %+v", r)
		}
	}
}

// A real fixture snapshot: reading it with digests twice gives the same digests, and skipping
// every record leaves nothing to read.
func TestReadWithDigestsOnTheFixture(t *testing.T) {
	snap := fixtureSnapshot(t)
	digests := map[string][]byte{}
	read := func(skip func(key string, d []byte) bool) (n int) {
		_, err := ReadWith(context.Background(), snap, nil, ReadOptions{Sig: []byte("s"), Skip: func(_ Account, _, db, key string, d []byte) bool {
			if d == nil {
				t.Errorf("no digest for a fixture record")
			}
			return skip(db+"|"+key, d)
		}}, func(Account, string, any) error { n++; return nil })
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	full := read(func(k string, d []byte) bool { digests[k] = d; return false })
	if full == 0 {
		t.Fatal("nothing read")
	}
	if n := read(func(k string, d []byte) bool {
		if !bytes.Equal(digests[k], d) {
			t.Errorf("digest of %s moved between reads", k)
		}
		return true
	}); n != 0 {
		t.Fatalf("%d records were read although Skip took them all", n)
	}
}
