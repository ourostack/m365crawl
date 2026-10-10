//go:build acceptance

package acceptance

import (
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ourostack/m365crawl/internal/outlookowa"
	"github.com/ourostack/m365crawl/internal/teamsdesktop"
	"github.com/ourostack/m365crawl/internal/v8"
)

func owaFixtureProfile(variant, name string) outlookowa.Profile {
	dir := filepath.Join("..", "testdata", "outlook-owa-fixture", variant)
	idb := filepath.Join(dir, "IndexedDB")
	p := outlookowa.Profile{Name: name, Dir: dir, Source: teamsdesktop.Source{
		Profile: name, Origin: "https_outlook.office.com_0",
		LevelDBDir: filepath.Join(idb, "https_outlook.office.com_0.indexeddb.leveldb"),
		BlobDir:    filepath.Join(idb, "https_outlook.office.com_0.indexeddb.blob"),
	}}
	if variant == "empty" || variant == "missing-store" {
		p.Source.BlobDir = ""
	}
	return p
}

// This literal census comes from hand-checking the unchanged Chromium fixture, not Collect.
func TestOWAMailLiteralCensus(t *testing.T) {
	c := owaObserve(t, owaFixtureProfile("mail", "SyntheticA"))
	want := map[string]int{
		"messages": 5, "folders": 7, "folder_ids": 8, "body_records": 4,
		"body_joined": 4, "body_missing": 1, "body_orphan": 0, "body_inline": 3, "body_none": 2,
		"recipients": 4, "to": 2, "cc": 1, "bcc": 1, "read": 1, "unread": 1, "unknown": 3,
		"attachments": 1, "size_zero": 1, "inline": 1, "has_attachments": 1,
		"conversation_present": 1, "conversation_groups": 1,
	}
	if !reflect.DeepEqual(c.census.counts(), want) {
		t.Fatal("literal_census_mismatch")
	}
}

func TestOWAMailAcceptanceHarness(t *testing.T) {
	db := filepath.Join(owaScratch(t), "owa.db")
	a := owaObserve(t, owaFixtureProfile("mail", "SyntheticA"))
	b := owaObserve(t, owaFixtureProfile("mail", "SyntheticB"))
	owaExercise(t, db, a)
	owaExercise(t, db, b)
	st := owaArchive(t, db)
	defer owaCloseArchive(t, st)
	if findings := owaCompareAll(st, []owaObservation{a, b}); len(findings) != 0 {
		t.Fatalf("archive_mismatch fields=%v", findings)
	}
	if n := owaScalar(t, st, `select count(*) from mail_messages`); n != 10 {
		t.Fatalf("account_isolation messages=%d", n)
	}
	if n := owaScalar(t, st, `select count(*) from mail_source_ids`); n != 28 {
		t.Fatalf("account_isolation assigned_keys=%d", n)
	}
	// Independent literals prove derivation, not just agreement between archive and FTS.
	for _, q := range []string{
		`select count(*) from mail_messages where body_text='Synthetic normal cached HTML'`,
		`select count(*) from mail_messages where body_text='Synthetic cached truncated HTML'`,
		`select count(*) from mail_messages where length(body_text)=800016`,
	} {
		if n := owaScalar(t, st, q); n != 2 {
			t.Fatalf("literal_body_text count=%d", n)
		}
	}
}

// Dropped source identities and corrupted persisted relationships must not look successful.
func TestOWAMailRejectsCorruptedCensusAndArchive(t *testing.T) {
	c := owaObserve(t, owaFixtureProfile("mail", "SyntheticA"))
	db := filepath.Join(owaScratch(t), "owa.db")
	owaExercise(t, db, c)
	st := owaArchive(t, db)
	bad := c.census.clone()
	for id := range bad.messages {
		delete(bad.messages, id)
		break
	}
	if len(owaCompare(st, c.account, bad)) == 0 {
		t.Error("corrupted_census_accepted")
	}
	owaCloseArchive(t, st)
	for _, tc := range []struct{ name, query string }{
		{"folder", `update mail_messages set folder_key=4294967295`},
		{"parent", `update mail_folders set parent_key=4294967295`},
		{"recipient", `update mail_recipients set kind_raw=7, role='cc' where ord=0`},
		{"order", `update mail_recipients set ord=ord+5`},
		{"read", `update mail_messages set is_read=1, read_state='read' where read_state='unknown'`},
		{"attachment", `update mail_attachments set downloaded=1, size=99`},
		{"header", `update mail_messages set has_attachments=1-has_attachments`},
		{"conversation", `update mail_messages set conversation_id=''`},
		{"body", `update mail_fts set body_text=''`},
		{"html", `update mail_messages set body_html_gz=x'00' where body_state='inline'`},
		{"received", `update mail_messages set received_at=null where received_at is not null`},
		{"sent", `update mail_messages set sent_at=null where sent_at is not null`},
		{"uid", `update mail_messages set ical_uid=''`},
		{"class", `update mail_messages set class=''`},
		{"account", `update mail_messages set account='outlook/Unobserved'`},
		{"gone", `update mail_messages set gone_at='2030-01-01T00:00:00Z'`},
		{"absent", `insert into mail_absent(account,detail_key,first_missed_at) select account,detail_key,'2030-01-01T00:00:00Z' from mail_messages`},
		{"identity", `update mail_source_ids set source_id=source_id||'invalid' where kind='message'`},
		{"coverage", `update mail_coverage set count=count+1`},
		{"root", `update meta set value='4294967295' where key like 'outlook_mail_root:%'`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(owaScratch(t), "negative.db")
			owaExercise(t, path, c)
			owaCorrupt(t, path, tc.query)
			archive := owaArchive(t, path)
			defer owaCloseArchive(t, archive)
			if len(owaCompare(archive, c.account, c.census)) == 0 {
				t.Error("corrupted_archive_accepted")
			}
		})
	}
}

func TestOWAMailEmptyAndOptionalCoverage(t *testing.T) {
	c := owaObserve(t, owaFixtureProfile("empty", "Synthetic"))
	if len(c.census.messages) != 0 || len(c.census.folders) != 7 {
		t.Fatal("empty_census_mismatch")
	}
	owaExercise(t, filepath.Join(owaScratch(t), "empty.db"), c)
}

func TestOWAMailDiagnosticsAreFieldNames(t *testing.T) {
	c := owaObserve(t, owaFixtureProfile("mail", "Synthetic"))
	db := filepath.Join(owaScratch(t), "privacy.db")
	owaExercise(t, db, c)
	bad := c.census.clone()
	for id, m := range bad.messages {
		m.fields = append([]any(nil), m.fields...)
		m.fields[3] = "synthetic-private-payload-never-report"
		bad.messages[id] = m
		break
	}
	st := owaArchive(t, db)
	defer owaCloseArchive(t, st)
	if fields := owaCompare(st, c.account, bad); !reflect.DeepEqual(fields, []string{"fts", "subject"}) {
		t.Fatalf("diagnostic_field_contract_failed fields=%v", fields)
	}
}

func TestOWAMailRawOracleCharacterization(t *testing.T) {
	identity := func(id string) *v8.Object { return &v8.Object{Keys: []string{"Id"}, Values: []any{id}} }
	base := func() *v8.Object {
		return &v8.Object{Keys: []string{"ParentFolderId", "HasAttachments"}, Values: []any{identity("SyntheticFolder"), false}}
	}
	for _, read := range []any{nil, v8.Undefined{}} {
		h := base()
		h.Keys, h.Values = append(h.Keys, "IsRead"), append(h.Values, read)
		m, err := owaRawHeader(h, nil)
		if err != nil || m.fields[13] != nil || m.fields[14] != "unknown" || len(m.recipients) != 0 || len(m.attachments) != 0 || m.fields[8] != nil || m.fields[9] != nil {
			t.Fatal("optional_unavailable_refused_or_invented")
		}
	}
	for _, tc := range []struct {
		field string
		value any
	}{
		{"IsRead", "true"}, {"HasAttachments", nil}, {"DateTimeReceived", "invalid"}, {"ToRecipients", "invalid"},
		{"Attachments", []any{&v8.Object{Keys: []string{"AttachmentId", "Size"}, Values: []any{identity("SyntheticAttachment"), -1.0}}}},
	} {
		h := base()
		if tc.field == "HasAttachments" {
			h.Values[1] = tc.value
		} else {
			h.Keys, h.Values = append(h.Keys, tc.field), append(h.Values, tc.value)
		}
		if _, err := owaRawHeader(h, nil); err == nil {
			t.Fatal("typed_shape_refusal_missing")
		}
	}
	h := base()
	h.Keys[1] = "hasAttachments"
	if _, err := owaRawHeader(h, nil); err == nil {
		t.Fatal("case_inexact_field_accepted")
	}
	for _, folders := range []map[string]owaRawFolder{
		{"A": {parent: "B"}, "B": {parent: "A"}},
		{"A": {parent: "RootA"}, "B": {parent: "RootB"}},
	} {
		if _, err := (owaRawCensus{folders: folders}).root(); err == nil {
			t.Fatal("ambiguous_root_accepted")
		}
	}
}

func TestOWAMailRefusalIsNotZeroSuccess(t *testing.T) {
	for _, variant := range []string{"unknown", "multiple", "missing-store"} {
		t.Run(variant, func(t *testing.T) {
			p := owaFixtureProfile(variant, "Synthetic")
			s := owaCapture(t, p)
			if _, err := owaCensus(s.profile.Source.LevelDBDir, s.profile.Source.BlobDir); err == nil {
				t.Fatal("layout_refusal_missing")
			}
			owaExpectRefusal(t, s)
		})
	}
}

func TestOWAMailTypedLossIsNotZeroSuccess(t *testing.T) {
	s := owaCapture(t, owaFixtureProfile("mail", "Synthetic"))
	// Mutate only this owned private copy, never the committed fixture.
	if err := os.Remove(filepath.Join(s.profile.Source.BlobDir, "1", "00", "2")); err != nil {
		t.Fatal("synthetic_mutation_failed")
	}
	if _, err := owaCensus(s.profile.Source.LevelDBDir, s.profile.Source.BlobDir); err == nil {
		t.Fatal("decoder_refusal_missing")
	}
	owaExpectLoss(t, s)
}

func TestOWAMailSourceMutationEvidence(t *testing.T) {
	s := owaCapture(t, owaFixtureProfile("mail", "Synthetic"))
	before, _, err := outlookowa.Fingerprint(s.profile)
	if err != nil {
		t.Fatal("fingerprint_failed")
	}
	file := filepath.Join(s.profile.Source.LevelDBDir, "CURRENT")
	info, err := os.Stat(file)
	if err != nil {
		t.Fatal("synthetic_stat_failed")
	}
	at := info.ModTime().Add(1e9)
	if err := os.Chtimes(file, at, at); err != nil {
		t.Fatal("synthetic_mutation_failed")
	}
	after, _, err := outlookowa.Fingerprint(s.profile)
	if err != nil || owaStable(before, after) {
		t.Fatal("unstable_observation_accepted")
	}
}

func TestRealOWAMailCountsAndRelationships(t *testing.T) {
	requireReal(t)
	if err := owaRealAcceptancePermit(); err != nil {
		t.Fatal("real_acceptance_blocked_parent_lifecycle")
	}
	root := os.Getenv(outlookRootEnv)
	if root == "" {
		t.Skip("explicit M365CRAWL_OUTLOOK_ROOT required for OWA acceptance")
	}
	profiles, skipped, err := outlookowa.Discover(root)
	if err != nil || len(skipped) != 0 {
		t.Fatal("profile_discovery_refused")
	}
	db := filepath.Join(owaScratch(t), "owa.db")
	observed, unstable := 0, 0
	var observations []owaObservation
	for _, p := range profiles {
		if filter := os.Getenv(outlookProfileEnv); filter != "" && filter != p.Name {
			continue
		}
		c := owaObserve(t, p)
		owaExercise(t, db, c)
		if !c.stable {
			unstable++
		}
		t.Logf("census_counts=%v", c.census.counts())
		observations = append(observations, c)
		observed++
	}
	if observed == 0 {
		t.Fatal("no_observed_profiles")
	}
	st := owaArchive(t, db)
	defer owaCloseArchive(t, st)
	if fields := owaCompareAllContext(owaWorkContext(t), st, observations); len(fields) != 0 {
		t.Fatalf("account_isolation_mismatch fields=%v", fields)
	}
	t.Logf("observed_profiles=%d unstable_profiles=%d real_mailbox_completeness=unproved", observed, unstable)
	if unstable != 0 {
		t.Error("unstable_source_observation")
	}
}

// Keep the normal product API in the harness; SQL mutations above are synthetic negatives only.
func owaCorrupt(t *testing.T, path, query string) {
	t.Helper()
	st, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal("archive_open_failed")
	}
	defer func() {
		if err := st.Close(); err != nil {
			t.Error("archive_close_failed")
		}
	}()
	if _, err := st.ExecContext(owaWorkContext(t), query); err != nil {
		t.Fatal("synthetic_corruption_failed")
	}
}
