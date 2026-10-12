package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/teamsdesktop"
)

// Synthetic raw message revisions. None of them comes from a real cache.
const (
	appConversation   = "19:app-evidence@thread.v2"
	appSender         = "28:synthetic-app-sender"
	failedRaw         = `{"properties":{},"properties":{}}` // a duplicate key is refused, never read
	partialNoClaimRaw = `{"properties":{"fromAppMetadata":"not-an-object"}}`
	partialClaimRaw   = `{"properties":{"fromAppMetadata":{"id":"app-partial","name":7}}}`
	absentRaw         = `{"id":"plain"}`
)

func appRaw(name string) string {
	return `{"properties":{"fromAppMetadata":{"id":"app-1","name":"` + name + `"}}}`
}

func appMessage(id string, version int64, sender, raw string) teamsdesktop.Message {
	m := msg(acctA, appConversation, id, "text "+id+" v"+strconv.FormatInt(version, 10), base.Add(time.Duration(version)*time.Minute))
	m.Version, m.SenderID, m.Raw = version, sender, []byte(raw)
	return m
}

func applyApp(t *testing.T, s *Store, id string, version int64, sender, raw string) Counts {
	t.Helper()
	n, err := s.ApplyMessages(context.Background(), []teamsdesktop.Message{appMessage(id, version, sender, raw)})
	if err != nil {
		t.Fatalf("apply %s v%d: %v", id, version, err)
	}
	return n
}

func appEnvelope(t *testing.T, s *Store, id string) MessageApplicationEnvelope {
	t.Helper()
	rows, _, err := s.Messages(context.Background(), Filter{Account: &acctA, Limit: 500, IncludeDeleted: true})
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	for _, r := range rows {
		if r.ID == id {
			return r.Application
		}
	}
	t.Fatalf("message %s not returned", id)
	return MessageApplicationEnvelope{}
}

func servedName(env MessageApplicationEnvelope) string {
	if env.Served == nil || env.Served.Payload.AppName == nil {
		return ""
	}
	return *env.Served.Payload.AppName
}

func countRows(t *testing.T, s *Store, q string, args ...any) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

func digestOf(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func TestMessageAppRevisionPointers(t *testing.T) {
	t.Run("A-B-A-failed-C serves A", func(t *testing.T) {
		s := newStore(t)
		applyApp(t, s, "m1", 1, appSender, appRaw("Alpha"))
		a := appEnvelope(t, s, "m1")
		if a.State != AppStateMapped || servedName(a) != "Alpha" || a.Retained || a.Served.Revision.RawDigest != digestOf(appRaw("Alpha")) {
			t.Fatalf("A: %+v", a)
		}
		applyApp(t, s, "m1", 2, appSender, appRaw("Beta"))
		if b := appEnvelope(t, s, "m1"); servedName(b) != "Beta" || b.Served.ID == a.Served.ID {
			t.Fatalf("B: %+v", b)
		}
		applyApp(t, s, "m1", 3, appSender, appRaw("Alpha"))
		again := appEnvelope(t, s, "m1")
		if again.Served.ID != a.Served.ID || !again.Served.DerivedAt.Equal(a.Served.DerivedAt) {
			t.Fatalf("A again did not reuse the immutable representation: %+v vs %+v", again.Served, a.Served)
		}
		applyApp(t, s, "m1", 4, appSender, failedRaw)
		c := appEnvelope(t, s, "m1")
		if c.State != AppStateFailed || c.FailureCode != "teams_app_raw_invalid" || !c.Retained || c.RetainedReason != RetainedCurrentFailed ||
			c.Served == nil || c.Served.ID != a.Served.ID || servedName(c) != "Alpha" || c.Current.RawDigest != digestOf(failedRaw) {
			t.Fatalf("failed C must serve retained A, not derivation-time B: %+v served=%+v", c, c.Served)
		}
		if n := countRows(t, s, `select count(*) from message_app_observations`); n != 3 {
			t.Fatalf("representations = %d, want 3 (A, B, C)", n)
		}
	})
	t.Run("first failure has no payload", func(t *testing.T) {
		s := newStore(t)
		applyApp(t, s, "m1", 1, appSender, failedRaw)
		env := appEnvelope(t, s, "m1")
		if env.State != AppStateFailed || env.Retained || env.Served == nil || env.Served.State != AppStateFailed || env.Served.Payload.hasClaims() {
			t.Fatalf("first failure: %+v", env)
		}
	})
	t.Run("current partial suppresses fallback but keeps last good", func(t *testing.T) {
		s := newStore(t)
		applyApp(t, s, "m1", 1, appSender, appRaw("Alpha"))
		applyApp(t, s, "m1", 2, appSender, partialNoClaimRaw)
		p := appEnvelope(t, s, "m1")
		if p.State != AppStatePartial || p.Retained || p.Served.State != AppStatePartial || len(p.Served.Payload.Losses) == 0 || servedName(p) != "" {
			t.Fatalf("partial: %+v", p.Served)
		}
		applyApp(t, s, "m1", 3, appSender, failedRaw)
		if f := appEnvelope(t, s, "m1"); !f.Retained || servedName(f) != "Alpha" {
			t.Fatalf("partial without claims must not clear last good: %+v", f)
		}
		applyApp(t, s, "m1", 4, appSender, partialClaimRaw)
		applyApp(t, s, "m1", 5, appSender, failedRaw)
		f := appEnvelope(t, s, "m1")
		if !f.Retained || f.Served.State != AppStatePartial || f.Served.Payload.AppID == nil || *f.Served.Payload.AppID != "app-partial" {
			t.Fatalf("partial with an admitted claim must become last good: %+v", f.Served)
		}
	})
	t.Run("current absent suppresses and clears", func(t *testing.T) {
		s := newStore(t)
		applyApp(t, s, "m1", 1, appSender, appRaw("Alpha"))
		applyApp(t, s, "m1", 2, appSender, absentRaw)
		a := appEnvelope(t, s, "m1")
		if a.State != AppStateAbsent || a.Retained || a.Served.State != AppStateAbsent || a.Served.Payload.hasClaims() {
			t.Fatalf("absent: %+v", a)
		}
		applyApp(t, s, "m1", 3, appSender, failedRaw)
		if f := appEnvelope(t, s, "m1"); f.Retained || f.Served.State != AppStateFailed {
			t.Fatalf("absent must clear last good: %+v", f)
		}
	})
	t.Run("sender change keeps the original source sender", func(t *testing.T) {
		s := newStore(t)
		applyApp(t, s, "m1", 1, appSender, appRaw("Alpha"))
		applyApp(t, s, "m1", 2, "28:other-sender", failedRaw)
		env := appEnvelope(t, s, "m1")
		if !env.Retained || env.Served.Revision.SenderID != appSender || env.Current.SenderID != "28:other-sender" {
			t.Fatalf("sender change: %+v served=%+v", env, env.Served)
		}
	})
	t.Run("pointer to another message is corrupt", func(t *testing.T) {
		s := newStore(t)
		applyApp(t, s, "m1", 1, appSender, appRaw("Alpha"))
		applyApp(t, s, "m2", 1, appSender, appRaw("Other"))
		applyApp(t, s, "m1", 2, appSender, failedRaw)
		other := appEnvelope(t, s, "m2").Served.ID
		s.qExec(t, `update message_app_current set last_good_id=`+strconv.FormatInt(other, 10)+` where message_rowid=(select rowid from messages where id='m1')`)
		env := appEnvelope(t, s, "m1")
		if env.State != AppStateFailed || env.FailureCode != AppProjectionCorrupt || env.Served != nil || env.Retained {
			t.Fatalf("wrong-group pointer served: %+v", env)
		}
		if appEnvelope(t, s, "m2").State != AppStateMapped {
			t.Fatal("healthy message lost its evidence")
		}
	})
	t.Run("active mapper failure falls back to an older mapper version", func(t *testing.T) {
		s := newStore(t)
		applyApp(t, s, "m1", 1, appSender, appRaw("Alpha"))
		next := MessageApplicationVersion + 1
		setApplicationVersion(t, next)
		if env := appEnvelope(t, s, "m1"); env.State != AppStateNotDerived || env.Served != nil || env.ActiveMapperVersion != next {
			t.Fatalf("obsolete-version current must be not_derived: %+v", env)
		}
		applyApp(t, s, "m1", 2, appSender, failedRaw)
		env := appEnvelope(t, s, "m1")
		if env.State != AppStateFailed || !env.Retained || env.Served.Revision.MapperVersion != MessageApplicationVersion || env.Current.MapperVersion != next || servedName(env) != "Alpha" {
			t.Fatalf("older mapper fallback: %+v served=%+v", env, env.Served)
		}
	})
}

func setApplicationVersion(t *testing.T, v int) {
	t.Helper()
	old := activeApplicationVersion
	activeApplicationVersion = v
	t.Cleanup(func() { activeApplicationVersion = old })
}

func TestMessageAppAcceptedRevisionOnly(t *testing.T) {
	s := newStore(t)
	applyApp(t, s, "m1", 2, appSender, appRaw("Beta"))
	beta := appEnvelope(t, s, "m1")
	hash := func() string {
		var h string
		if err := s.db.QueryRow(`select content_hash from messages where id='m1'`).Scan(&h); err != nil {
			t.Fatal(err)
		}
		return h
	}
	before, reps := hash(), countRows(t, s, `select count(*) from message_app_observations`)
	assertUnchanged := func(what string, n Counts) {
		t.Helper()
		env := appEnvelope(t, s, "m1")
		if n.Unchanged != 1 || hash() != before || env.Served.ID != beta.Served.ID || countRows(t, s, `select count(*) from message_app_observations`) != reps {
			t.Fatalf("%s moved evidence: counts=%+v env=%+v", what, n, env)
		}
	}
	assertUnchanged("older conflicting revision", applyApp(t, s, "m1", 1, appSender, appRaw("Alpha")))
	assertUnchanged("equal unchanged revision", applyApp(t, s, "m1", 2, appSender, appRaw("Beta")))

	// A sticky tombstone: the deletion is kept by an equal-version copy that lacks it.
	deleted := appMessage("m2", 3, appSender, appRaw("Gamma"))
	deleted.DeletedAt = base
	must(s.ApplyMessages(context.Background(), []teamsdesktop.Message{deleted}))
	gamma := appEnvelope(t, s, "m2")
	n := must(s.ApplyMessages(context.Background(), []teamsdesktop.Message{appMessage("m2", 3, appSender, appRaw("Gamma"))}))
	if n.Unchanged != 1 || appEnvelope(t, s, "m2").Served.ID != gamma.Served.ID {
		t.Fatalf("sticky tombstone copy moved evidence: %+v", n)
	}

	// Incoming zero-length bytes are stored as NULL raw, and the projection agrees.
	applyApp(t, s, "m3", 1, appSender, "")
	empty := appEnvelope(t, s, "m3")
	if empty.Current.RawPresent || empty.Current.RawDigest != "" || empty.State != AppStateFailed || empty.FailureCode != "teams_app_raw_missing" {
		t.Fatalf("normalized empty incoming raw: %+v", empty)
	}
	if countRows(t, s, `select count(*) from messages where id='m3' and raw_json is null and raw_digest is null`) != 1 ||
		countRows(t, s, `select count(*) from message_app_observations where message_id='m3' and raw_present=0 and raw_digest=''`) != 1 {
		t.Fatal("NULL raw did not keep an absent-raw representation key")
	}

	// A retained SQL empty string is present empty raw: its digest is the SHA256 of no bytes.
	m3 := int64(countRows(t, s, `select rowid from messages where id='m3'`))
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	state, err := publishMessageApplication(context.Background(), tx, m3, messageIdentity{acctA.TenantID, acctA.UserID, appConversation, "m3", appSender}, []byte{}, true, base)
	if err != nil || state != AppStateFailed {
		t.Fatalf("publish present empty: %s %v", state, err)
	}
	must0(tx.Commit())
	if countRows(t, s, `select count(*) from message_app_observations where message_id='m3' and raw_present=1 and raw_digest=? and failure_code='teams_app_raw_invalid'`, digestOf("")) != 1 {
		t.Fatal("present empty raw did not get the empty-bytes digest and raw_invalid")
	}
}

func TestMessageAppPayloadRoundTripsMaxInt64(t *testing.T) {
	s := newStore(t)
	raw := `{"properties":{"botCitations":[{"id":9223372036854775807,"title":"Max","link":null},{"id":0,"title":"Zero","link":"","contentType":"text/html"}]}}`
	applyApp(t, s, "m1", 1, appSender, raw)
	env := appEnvelope(t, s, "m1")
	c := env.Served.Payload.Citations
	if env.State != AppStateMapped || len(c) != 2 || c[0].ID != "9223372036854775807" || c[0].Link != nil || c[1].ID != "0" || c[1].Link == nil || *c[1].Link != "" ||
		env.Served.Payload.SourceSlots == nil || *env.Served.Payload.SourceSlots != 2 {
		t.Fatalf("citations: %+v", env.Served.Payload)
	}
	var stored string
	if err := s.db.QueryRow(`select projection_json from message_app_observations where rowid=?`, env.Served.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stored, `"id":"9223372036854775807"`) {
		t.Fatalf("stored JSON lost the decimal string id: %s", stored)
	}
	var back ApplicationPayload
	if err := json.Unmarshal([]byte(stored), &back); err != nil || back.Citations[0].ID != "9223372036854775807" {
		t.Fatalf("round trip: %+v %v", back, err)
	}
}

func TestMessageAppCorruptStoredEvidence(t *testing.T) {
	cases := []struct {
		name, setup string
	}{
		{"json-not-canonical", `update message_app_observations set projection_json=' '||projection_json`},
		{"json-unknown-field", `update message_app_observations set projection_json='{"x":1}'`},
		{"json-missing-arrays", `update message_app_observations set projection_json='{"app_id":null,"app_name":null,"reply_to_id":null,"citations":null,"losses":null,"source_slots":null}'`},
		{"citation-id-not-decimal", `update message_app_observations set projection_json=replace(projection_json,'"id":"5"','"id":"05"')`},
		{"citation-id-overflow", `update message_app_observations set projection_json=replace(projection_json,'"id":"5"','"id":"9223372036854775808"')`},
		{"citation-position-order", `update message_app_observations set projection_json=replace(projection_json,'"position":1','"position":0')`},
		{"citation-beyond-slots", `update message_app_observations set projection_json=replace(projection_json,'"source_slots":2','"source_slots":1')`},
		{"citations-without-slots", `update message_app_observations set projection_json=replace(projection_json,'"source_slots":2','"source_slots":null')`},
		{"mapped-with-losses", `update message_app_observations set projection_json=replace(projection_json,'"losses":[]','"losses":[{"code":"x","count":1}]')`},
		{"bad-loss", `update message_app_observations set state='partial', projection_json=replace(projection_json,'"losses":[]','"losses":[{"code":"","count":1}]')`},
		{"unordered-losses", `update message_app_observations set state='partial', projection_json=replace(projection_json,'"losses":[]','"losses":[{"code":"b","count":1},{"code":"a","count":1}]')`},
		{"partial-without-losses", `update message_app_observations set state='partial'`},
		{"absent-with-claims", `update message_app_observations set state='absent'`},
		{"failed-with-claims", `update message_app_observations set state='failed', failure_code='teams_app_raw_invalid'`},
		{"failed-without-code", `update message_app_observations set state='failed', projection_json='{"app_id":null,"app_name":null,"reply_to_id":null,"citations":[],"losses":[],"source_slots":null}'`},
		{"failure-code-on-success", `update message_app_observations set failure_code='teams_app_raw_invalid'`},
		{"failure-code-shape", `update message_app_observations set state='failed', failure_code='Bad Code', projection_json='{"app_id":null,"app_name":null,"reply_to_id":null,"citations":[],"losses":[],"source_slots":null}'`},
		{"derived-at", `update message_app_observations set derived_at='yesterday'`},
		{"digest", `update message_app_observations set raw_digest='00'`},
		{"sender", `update message_app_observations set sender_id='someone-else'`},
		{"group", `update message_app_observations set message_id='other'`},
		{"missing-current-row", `pragma foreign_keys=off; update message_app_current set current_id=999 where message_rowid=(select rowid from messages where id='m1'); update message_app_observations set rowid=rowid`},
		{"raw-present-mismatch", `update message_app_observations set raw_present=0`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newStore(t)
			applyApp(t, s, "m1", 1, appSender, `{"properties":{"fromAppMetadata":{"id":"app-1"},"botCitations":[{"id":5,"title":"First"},{"id":6,"title":"Second"}]}}`)
			applyApp(t, s, "m2", 1, appSender, appRaw("Healthy"))
			tamper(t, s, tc.setup+` where message_id='m1' or rowid=(select current_id from message_app_current where message_rowid=(select rowid from messages where id='m1'))`)
			env := appEnvelope(t, s, "m1")
			if env.State != AppStateFailed || env.FailureCode != AppProjectionCorrupt || env.Served != nil {
				t.Fatalf("corrupt stored evidence was trusted: %+v", env)
			}
			if appEnvelope(t, s, "m2").State != AppStateMapped {
				t.Fatal("a healthy message lost its evidence")
			}
		})
	}
}

func TestMessageAppCorruptLastGood(t *testing.T) {
	for name, setup := range map[string]string{
		"missing":      `pragma foreign_keys=off; update message_app_current set last_good_id=999`,
		"json":         `update message_app_observations set projection_json='{}' where state='mapped'`,
		"not-eligible": `update message_app_current set last_good_id=current_id`,
	} {
		t.Run(name, func(t *testing.T) {
			s := newStore(t)
			applyApp(t, s, "m1", 1, appSender, appRaw("Alpha"))
			applyApp(t, s, "m1", 2, appSender, failedRaw)
			tamper(t, s, setup)
			env := appEnvelope(t, s, "m1")
			if env.State != AppStateFailed || env.FailureCode != AppProjectionCorrupt || env.Served != nil || env.Retained {
				t.Fatalf("corrupt last good was served: %+v", env)
			}
		})
	}
}

func TestMessageAppNotDerived(t *testing.T) {
	s := newStore(t)
	applyApp(t, s, "m1", 1, appSender, appRaw("Alpha"))
	applyApp(t, s, "m2", 1, appSender, appRaw("Beta"))
	s.qExec(t, `update messages set raw_digest=null where id='m1'`)
	s.qExec(t, `delete from message_app_current where message_rowid=(select rowid from messages where id='m2')`)
	for _, id := range []string{"m1", "m2"} {
		env := appEnvelope(t, s, id)
		if env.State != AppStateNotDerived || env.Served != nil || !env.BackfillPending || !env.Current.RawPresent {
			t.Fatalf("%s: %+v", id, env)
		}
	}
}

// TestMessageAppReadSnapshot publishes a newer revision from a second handle between the canonical
// message selection and the evidence load: the selected body must stay paired with its own evidence.
func TestMessageAppReadSnapshot(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	must(s.ApplyConversations(ctx, []teamsdesktop.Conversation{conv(acctA, appConversation, "Meeting", "App chat")}))
	applyApp(t, s, "m1", 1, appSender, appRaw("Alpha"))
	reader, err := OpenReadOnly(ctx, s.cs.Path())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	reads := map[string]func() ([]MessageRow, error){
		"messages": func() ([]MessageRow, error) {
			rows, _, err := reader.Messages(ctx, Filter{Account: &acctA})
			return rows, err
		},
		"search": func() ([]MessageRow, error) {
			rows, _, err := reader.Search(ctx, "text", Filter{Account: &acctA})
			return rows, err
		},
		"thread": func() ([]MessageRow, error) {
			rows, _, err := reader.Thread(ctx, appConversation, "m1", Filter{Account: &acctA})
			return rows, err
		},
		"calendar-recent": func() ([]MessageRow, error) {
			chat, err := reader.chatOf(ctx, []string{acctA.TenantID + "/" + acctA.UserID}, appConversation)
			if err != nil {
				return nil, err
			}
			return chat.Recent, nil
		},
	}
	version := int64(1)
	for name, read := range reads {
		t.Run(name, func(t *testing.T) {
			before := appEnvelope(t, s, "m1")
			version++
			next := version
			old := afterMessageSelection
			afterMessageSelection = func() {
				afterMessageSelection = old
				applyApp(t, s, "m1", next, appSender, appRaw("Name v"+strconv.FormatInt(next, 10)))
			}
			t.Cleanup(func() { afterMessageSelection = old })
			rows, err := read()
			if err != nil || len(rows) != 1 {
				t.Fatalf("read: %d rows, %v", len(rows), err)
			}
			if rows[0].Application.Current.RawDigest != before.Current.RawDigest || rows[0].Application.Served.ID != before.Served.ID ||
				!strings.HasSuffix(rows[0].ContentText, "v"+strconv.FormatInt(next-1, 10)) {
				t.Fatalf("selected body paired with a later revision's evidence: text=%q env=%+v", rows[0].ContentText, rows[0].Application)
			}
			after := appEnvelope(t, s, "m1")
			if servedName(after) != "Name v"+strconv.FormatInt(next, 10) {
				t.Fatalf("publication was lost: %+v", after)
			}
		})
	}
}

func TestMessageAppOlderArchiveReadonly(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	applyApp(t, s, "m1", 1, appSender, appRaw("Alpha"))
	applyApp(t, s, "m2", 1, appSender, "")
	for _, q := range []string{`drop table message_app_current`, `drop table message_app_observations`, `alter table messages drop column raw_digest`, `update schema_migrations set version=11`} {
		s.qExec(t, q)
	}
	path := s.cs.Path()
	must0(s.Close())
	before := fileDigest(t, path)
	r, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	rows, _, err := r.Messages(ctx, Filter{Account: &acctA})
	if err != nil || len(rows) != 2 {
		t.Fatalf("legacy read: %d %v", len(rows), err)
	}
	for _, row := range rows {
		env := row.Application
		if env.State != AppStateNotDerived || !env.BackfillPending || env.Served != nil || env.Current.RawPresent != (row.ID == "m1") {
			t.Fatalf("%s: %+v", row.ID, env)
		}
	}
	must0(r.Close())
	if fileDigest(t, path) != before {
		t.Fatal("a read-only read changed the archive")
	}
	var version int
	if err := sqlOpen(t, path).QueryRow(`select coalesce(max(version),0) from schema_migrations`).Scan(&version); err != nil || version != 11 {
		t.Fatalf("schema version = %d %v", version, err)
	}
}

// An alpha.1-shaped archive has no meta table; its evidence is not derived, never a database error.
func TestMessageAppArchiveWithoutMeta(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	applyApp(t, s, "m1", 1, appSender, appRaw("Alpha"))
	s.qExec(t, `drop table meta`)
	rows, _, err := s.Messages(ctx, Filter{Account: &acctA})
	if err != nil || len(rows) != 1 {
		t.Fatalf("read without meta: %d %v", len(rows), err)
	}
	if env := rows[0].Application; env.State != AppStateNotDerived || !env.BackfillPending || env.Served != nil {
		t.Fatalf("without meta: %+v", env)
	}
}

// watch reads changed messages by key; those reads carry no application evidence and never read it.
func TestMessagesByKeyWithoutApplicationEvidence(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	applyApp(t, s, "m1", 1, appSender, appRaw("Alpha"))
	s.qExec(t, `drop table message_app_current`)
	rows, err := s.MessagesByKey(ctx, []string{acctA.TenantID + "|" + acctA.UserID + "|" + appConversation + "|m1"})
	if err != nil || len(rows) != 1 || rows[0].Application.State != "" {
		t.Fatalf("by-key read: %+v %v", rows, err)
	}
}

func fileDigest(t *testing.T, path string) string {
	t.Helper()
	var all []byte
	for _, p := range []string{path, path + "-wal"} {
		if data, err := os.ReadFile(p); err == nil { //nolint:gosec // G304: the test's own temporary archive
			all = append(all, data...)
		}
	}
	return digestOf(string(all))
}

func sqlOpen(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", sqliteFileURI(path, "mode=ro"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// tamper runs statements on one connection, so a leading pragma foreign_keys=off lets a test
// plant a dangling pointer that the schema itself refuses.
func tamper(t *testing.T, s *Store, script string) {
	t.Helper()
	c, err := s.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	for _, q := range strings.Split(script, "; ") {
		if _, err := c.ExecContext(context.Background(), q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if _, err := c.ExecContext(context.Background(), `pragma foreign_keys=on`); err != nil {
		t.Fatal(err)
	}
}

func TestActiveApplicationVersion(t *testing.T) {
	old := activeApplicationVersion
	t.Cleanup(func() { activeApplicationVersion = old })
	activeApplicationVersion = 9
	if ActiveApplicationVersion() != 9 {
		t.Fatal("ActiveApplicationVersion does not report the active version")
	}
}

// countingReader counts what an evidence load asks of its read transaction.
type countingReader struct {
	*sql.Tx
	rows, prepares int
}

func (c *countingReader) QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row {
	c.rows++
	return c.Tx.QueryRowContext(ctx, q, args...)
}

func (c *countingReader) PrepareContext(ctx context.Context, q string) (*sql.Stmt, error) {
	c.prepares++
	return c.Tx.PrepareContext(ctx, q)
}

// An evidence load prepares its per-message statements once per read, whatever the row count.
func TestMessageAppLoadPreparesOnce(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	for i := range 6 {
		applyApp(t, s, "m"+strconv.Itoa(i), 1, appSender, appRaw("Alpha"))
	}
	rows, _, err := s.Messages(ctx, Filter{Account: &acctA})
	if err != nil || len(rows) != 6 {
		t.Fatalf("%d %v", len(rows), err)
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	c := &countingReader{Tx: tx}
	out, err := loadMessageApplications(ctx, c, rows)
	if err != nil || out[0].Application.State == AppStateNotDerived {
		t.Fatalf("load: %v %+v", err, out[0].Application)
	}
	if c.rows > 2 || c.prepares > 2 {
		t.Fatalf("6 messages cost %d direct queries and %d prepares", c.rows, c.prepares)
	}
}

// A reused representation whose stored JSON no longer decodes never becomes last good.
func TestMessageAppDamagedReuseNotLastGood(t *testing.T) {
	s := newStore(t)
	applyApp(t, s, "m1", 1, appSender, appRaw("Alpha")) // mapped, last good
	applyApp(t, s, "m1", 2, appSender, absentRaw)       // absent clears last good
	s.qExec(t, `update message_app_observations set projection_json='{"app_id":null}' where state='mapped'`)
	applyApp(t, s, "m1", 3, appSender, appRaw("Alpha")) // reuses the damaged mapped row
	if n := countRows(t, s, `select count(*) from message_app_current where last_good_id is not null`); n != 0 {
		t.Fatalf("a damaged reused representation became last good (%d)", n)
	}
}
