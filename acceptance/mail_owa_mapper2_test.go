//go:build acceptance

package acceptance

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/outlookowa"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/syncer"
	"github.com/ourostack/m365crawl/internal/v8"
)

func TestOWAMailMapper2IndependentSelection(t *testing.T) {
	for _, field := range []string{"ToRecipients", "CcRecipients", "BccRecipients", "Attachments", "UID", "InternetMessageId"} {
		t.Run(field, func(t *testing.T) {
			f := owaFields{}
			value := f.get(owaDetailOnly(), field)
			for _, unavailable := range []any{nil, v8.Undefined{}} {
				for _, side := range []string{"header", "detail", "equal"} {
					h := owaObject("ParentFolderId", owaIdentity("F"), "HasAttachments", false)
					d := owaObject()
					if side == "header" {
						h.Keys, h.Values = append(h.Keys, field), append(h.Values, value)
						d.Keys, d.Values = append(d.Keys, field), append(d.Values, unavailable)
					} else {
						d.Keys, d.Values = append(d.Keys, field), append(d.Values, value)
						h.Keys, h.Values = append(h.Keys, field), append(h.Values, unavailable)
						if side == "equal" {
							h.Values[len(h.Values)-1] = value
						}
					}
					m, err := owaRawHeader(h, d)
					if err != nil {
						t.Fatal("six_field_contract_refused")
					}
					switch field {
					case "UID":
						if m.fields[12] != "synthetic-detail-uid" {
							t.Fatal("uid_selection_mismatch")
						}
					case "InternetMessageId":
						if m.fields[0] != "<synthetic-detail@example.invalid>" {
							t.Fatal("message_id_selection_mismatch")
						}
					case "Attachments":
						if len(m.attachments) != 1 {
							t.Fatal("attachment_selection_mismatch")
						}
					default:
						if len(m.recipients) != 1 {
							t.Fatal("recipient_selection_mismatch")
						}
					}
				}
			}
			h := owaObject("ParentFolderId", owaIdentity("F"), "HasAttachments", false, field, value)
			for _, d := range []any{owaObject(field, int64(19)), owaObject(field, "conflict")} {
				if _, err := owaRawHeader(h, d); err == nil {
					t.Fatal("malformed_or_conflicting_detail_blessed")
				}
			}
		})
	}
}

func TestOWAMailMapper2PersistentOldSuccessReread(t *testing.T) {
	c := owaCapture(t, owaFixtureProfile("mail", "Synthetic"))
	owaSyntheticTable(t, c, owaDetailOnly(), false)
	var err error
	c.census, err = owaCensusContext(c.ctx, c.profile.Source.LevelDBDir, c.profile.Source.BlobDir)
	if err != nil {
		t.Fatal("mapper2_raw_census_refused")
	}
	c.digest = owaPayloadDigestContext(t, c.ctx, c.profile.Source)
	path := filepath.Join(owaScratch(t), "persisted-v1.db")
	opts := syncer.Options{Root: filepath.Join(c.root, "no-teams"), DBPath: path, OutlookRoot: c.root, OutlookEnabled: true, OutlookMinReadInterval: -1}
	rep, _, err := syncer.Run(c.ctx, opts)
	source, found := mailSource(rep)
	if err != nil || !found || source.Status != syncer.StatusOK {
		t.Fatal("initial_success_missing")
	}
	// Seed the persisted mapper-v1 result: that version missed detail-only metadata. This is a controlled old success receipt, not an injected source lifecycle or fabricated live observation.
	owaCorrupt(t, path, `delete from mail_recipients where message_rowid in (select m.rowid from mail_messages m join mail_source_ids i on i.account=m.account and i.kind='message' and i.key=m.detail_key where i.source_id='Message/Sparse==')`)
	owaCorrupt(t, path, `delete from mail_attachments where message_rowid in (select m.rowid from mail_messages m join mail_source_ids i on i.account=m.account and i.kind='message' and i.key=m.detail_key where i.source_id='Message/Sparse==')`)
	owaCorrupt(t, path, `update mail_messages set ical_uid='',internet_message_id='' where ical_uid='synthetic-detail-uid'`)
	w, err := store.Open(c.ctx, path)
	if err != nil {
		t.Fatal("old_success_archive_open_refused")
	}
	at := time.Now().UTC()
	err = w.RecordRun(c.ctx, store.Run{StartedAt: at, FinishedAt: at, Source: source.Source, Fingerprint: "acceptance-controlled-mapper-v1-success", Status: syncer.StatusOK})
	closeErr := w.Close()
	if err != nil || closeErr != nil {
		t.Fatal("old_success_receipt_refused")
	}
	before := owaArchive(t, path)
	if len(owaCompareAll(before, []owaObservation{c})) == 0 {
		t.Fatal("persisted_v1_metadata_gap_not_exercised")
	}
	owaCloseArchive(t, before)
	rep, _, err = syncer.Run(c.ctx, opts)
	source, found = mailSource(rep)
	if err != nil || !found || source.Status != syncer.StatusOK || source.Counts == nil || source.Counts.Mail == nil || source.Counts.Mail.Updated != 1 || source.Counts.Mail.Added != 0 || source.Counts.Mail.Replaced != 0 {
		t.Fatal("eligible_mapper2_reread_missing")
	}
	after := owaArchive(t, path)
	if fields := owaCompareAll(after, []owaObservation{c}); len(fields) != 0 {
		t.Fatalf("mapper2_persistent_import_mismatch fields=%v", fields)
	}
	owaCloseArchive(t, after)
	rep, _, err = syncer.Run(c.ctx, opts)
	source, found = mailSource(rep)
	if err != nil || !found || source.Status != syncer.StatusUnchanged || source.Counts != nil || c.digest != owaPayloadDigestContext(t, c.ctx, c.profile.Source) {
		t.Fatal("mapper2_receipt_did_not_restore_unchanged_skip")
	}
	t.Log("persisted_old_success_gap=1 eligible_reread=1 updated=1 imported_detail_fields=6 unchanged_skip=1 source_bytes_unchanged=1")
}

func TestOWAMailMapper2ConflictIsNotCleanCoverage(t *testing.T) {
	for _, field := range []string{"ToRecipients", "CcRecipients", "BccRecipients", "Attachments", "UID", "InternetMessageId"} {
		t.Run(field, func(t *testing.T) {
			s := owaCapture(t, owaFixtureProfile("mail", "Synthetic"))
			// Normal already has each header metadata field. An unequal joined detail must not be clean coverage.
			d := owaDetailOnly()
			d.Values[0] = owaIdentity("Message/Normal==")
			f := owaFields{}
			d = owaObject("ItemId", owaIdentity("Message/Normal=="), field, f.get(d, field))
			owaSyntheticTable(t, s, d, false)
			_, err := owaCensusContext(s.ctx, s.profile.Source.LevelDBDir, s.profile.Source.BlobDir)
			var refusal *owaCoverageRefusal
			if !errors.As(err, &refusal) || !reflect.DeepEqual(refusal.fields, []string{field}) {
				t.Fatal("conflict_coverage_not_refused")
			}
			o, err := outlookowa.Open(s.ctx, s.profile.Source.LevelDBDir, s.profile.Source.BlobDir)
			if err != nil {
				t.Fatal("conflict_origin_refused")
			}
			res, err := outlookowa.Collect(s.ctx, o, s.account, false)
			closeErr := o.Close()
			if err != nil || closeErr != nil || !res.Doubtful || len(res.Losses) != 1 || res.Losses[0].Code != "outlook_owa_detail_conflict" || res.Losses[0].Count != 1 {
				t.Fatal("fixed_product_conflict_missing")
			}
		})
	}
}

func TestOWAMailMapper2DetailAccountsAndAuthority(t *testing.T) {
	var observations []owaObservation
	path := filepath.Join(owaScratch(t), "detail-accounts.db")
	for _, name := range []string{"SyntheticA", "SyntheticB"} {
		c := owaCapture(t, owaFixtureProfile("mail", name))
		d := owaDetailOnly()
		d.Keys, d.Values = append(d.Keys, "HasAttachments", "ReplyTo"), append(d.Values, true, []any{owaObject("Name", "not-recipient")})
		owaSyntheticTable(t, c, d, false)
		var err error
		c.census, err = owaCensusContext(c.ctx, c.profile.Source.LevelDBDir, c.profile.Source.BlobDir)
		if err != nil {
			t.Fatal("detail_account_census_refused")
		}
		m := c.census.messages["Message/Sparse=="]
		if len(m.recipients) != 3 || len(m.attachments) != 1 || m.fields[16] != int64(0) || m.fields[13] != nil || m.fields[14] != "unknown" || m.fields[12] != "synthetic-detail-uid" || m.text != "synthetic detail" {
			t.Fatal("detail_account_literal_mismatch")
		}
		c.digest = owaPayloadDigestContext(t, c.ctx, c.profile.Source)
		owaExercise(t, path, c)
		observations = append(observations, c)
	}
	st := owaArchive(t, path)
	defer owaCloseArchive(t, st)
	if fields := owaCompareAll(st, observations); len(fields) != 0 {
		t.Fatalf("detail_account_archive_mismatch fields=%v", fields)
	}
}

func TestOWAMailMapper2HTMLArchiveCorpus(t *testing.T) {
	for i, tc := range []struct{ source, expected string }{
		{"<p>one</p>", "one"},
		{"<p>a<b>b</b>c</p><p>d</p>", "abc d"},
		{"<div>a<br>b</div>", "a b"},
		{"<p>a&nbsp;b</p>", "a b"},
		{"<p>&lt;tag&gt;&amp;</p>", "<tag>&"},
		{"<p>a<!--excluded-->b</p>", "ab"},
		{`<p style="display:none">hidden</p>`, "hidden"},
		{"<script>ignored</script><p>shown", "shown"},
		{"<p>unclosed", "unclosed"},
		{`<img src="https://example.invalid/never-fetched">`, ""},
		{"<div>A<style>.x { hidden }</style>B</div>", "A B"},
		{"<h1>heading</h1><ul><li>one<li>two</ul>", "heading one two"},
		{"2 < 3 &amp;&amp; 4 > 1", "2 < 3 && 4 > 1"},
		{"<", "<"},
		{"<p>&#x41;&#66;&amp;lt;</p>", "AB&lt;"},
	} {
		t.Run(string(rune('A'+i)), func(t *testing.T) {
			got, err := owaIndependentText(owaWorkContext(t), tc.source)
			if err != nil || got != tc.expected {
				t.Fatal("html_source_literal_mismatch")
			}
			c := owaCapture(t, owaFixtureProfile("mail", "Synthetic"))
			owaSyntheticTable(t, c, owaObject("ItemId", owaIdentity("Message/Normal=="), "NormalizedBody", owaObject("Value", tc.source, "BodyType", "HTML")), false)
			c.census, err = owaCensusContext(c.ctx, c.profile.Source.LevelDBDir, c.profile.Source.BlobDir)
			if err != nil {
				t.Fatal("html_source_census_refused")
			}
			c.digest = owaPayloadDigestContext(t, c.ctx, c.profile.Source)
			owaExercise(t, filepath.Join(owaScratch(t), "html.db"), c)
		})
	}
}

func TestOWAMailMapper2EncodingAndMalformedRefusals(t *testing.T) {
	for _, tc := range []struct{ source, code string }{
		{"\x00", "body_derivation_encoding_unproved"},
		{"\xff", "body_derivation_encoding_unproved"},
		{"<p broken", "body_tokenization_unproved"},
		{"<!--unfinished", "body_tokenization_unproved"},
		{"<svg>foreign</svg>", "body_foreign_content_unproved"},
		{"<textarea>raw</textarea>", "body_rawtext_semantics_unproved"},
	} {
		_, err := owaIndependentText(context.Background(), tc.source)
		if err == nil || err.Error() != tc.code {
			t.Fatal("html_fixed_refusal_mismatch")
		}
	}
	for _, field := range []string{"ToRecipients", "CcRecipients", "BccRecipients"} {
		if _, err := owaRawHeader(owaObject("ParentFolderId", owaIdentity("F"), "HasAttachments", false), owaObject(field, []any{nil})); err == nil {
			t.Fatal("malformed_detail_recipient_blessed")
		}
	}
}

func TestOWAMailMapper2LargeRawBodyBudget(t *testing.T) {
	c := owaCapture(t, owaFixtureProfile("mail", "Synthetic"))
	large := "<p>" + strings.Repeat("synthetic ", 100000) + "</p>"
	owaSyntheticTable(t, c, owaObject("ItemId", owaIdentity("Message/Normal=="), "NormalizedBody", owaObject("Value", large)), false)
	c.census = owaRawCensus{}
	var err error
	c.census, err = owaCensusContext(c.ctx, c.profile.Source.LevelDBDir, c.profile.Source.BlobDir)
	if err != nil || len(c.census.messages["Message/Normal=="].html) <= 256<<10 {
		t.Fatal("legitimate_large_cached_body_refused")
	}
}

func TestOWAMailMapper2EmptyIsPresentAndMalformedRefused(t *testing.T) {
	for _, field := range []string{"ToRecipients", "CcRecipients", "BccRecipients", "Attachments", "UID", "InternetMessageId"} {
		t.Run(field, func(t *testing.T) {
			var empty any = []any{}
			if field == "UID" || field == "InternetMessageId" {
				empty = ""
			}
			header := owaObject("ParentFolderId", owaIdentity("F"), "HasAttachments", false)
			m, err := owaRawHeader(header, owaObject(field, empty))
			if err != nil || len(m.recipients) != 0 || len(m.attachments) != 0 || m.fields[0] != "" || m.fields[12] != "" {
				t.Fatal("present_empty_detail_refused")
			}
			f := owaFields{}
			header.Keys, header.Values = append(header.Keys, field), append(header.Values, f.get(owaDetailOnly(), field))
			if _, err := owaRawHeader(header, owaObject(field, empty)); err == nil {
				t.Fatal("empty_detail_silently_fell_back")
			}
			s := owaCapture(t, owaFixtureProfile("mail", "Synthetic"))
			owaSyntheticTable(t, s, owaObject("ItemId", owaIdentity("Message/Sparse=="), field, true), false)
			if _, err := owaCensusContext(s.ctx, s.profile.Source.LevelDBDir, s.profile.Source.BlobDir); err == nil {
				t.Fatal("malformed_actual_detail_census_blessed")
			}
			o, err := outlookowa.Open(s.ctx, s.profile.Source.LevelDBDir, s.profile.Source.BlobDir)
			if err != nil {
				t.Fatal("malformed_detail_origin_refused")
			}
			res, err := outlookowa.Collect(s.ctx, o, s.account, false)
			closeErr := o.Close()
			if err != nil || closeErr != nil || len(res.Losses) != 1 || res.Losses[0].Code != "outlook_mail_unmapped" || res.Losses[0].Count != 1 {
				t.Fatal("malformed_detail_fixed_loss_missing")
			}
		})
	}
}
