//go:build acceptance && windows

package acceptance

import (
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/syncer"
)

func TestOWACalendarPersistedIndependentDifferential(t *testing.T) {
	c := owaObserve(t, owaFixtureProfile("mail", "SyntheticCalendar"))
	expected, err := owaReadExpectedCalendar(c.ctx, c.profile.Source.LevelDBDir, c.profile.Source.BlobDir)
	*c.readersReleased = *c.readersReleased && expected.Released
	if err != nil || !expected.Released || len(expected.Rows) != 2 {
		t.Fatal("independent_calendar_expectations_refused")
	}

	db := filepath.Join(owaScratch(t), "calendar.db")
	opts := syncer.Options{Root: filepath.Join(c.root, "no-teams"), DBPath: db, OutlookRoot: c.root, OutlookEnabled: true, OutlookMinReadInterval: -1}
	var history [][]any
	for step := range 3 {
		opts.FullRead = step == 2
		rep, _, runErr := syncer.Run(c.ctx, opts)
		owaSyncChecks(t, rep, runErr, c, step)
		st := owaArchive(t, db)
		findings, compareErr := owaCompareCalendarContext(c.ctx, st, c.account, expected)
		if compareErr != nil || len(findings) != 0 {
			t.Fatalf("independent_calendar_sql_mismatch fields=%v refused=%t", findings, compareErr != nil)
		}
		rows, queryErr := owaQueryContext(c.ctx, st, `select source_id,first_seen_at,field_clocks_json,detail_as_of,removed_at
			from calendar_source_events order by source_id`)
		owaCloseArchive(t, st)
		if queryErr != nil || len(rows) != 2 {
			t.Fatal("calendar_history_projection_refused")
		}
		if step == 0 {
			history = rows
		} else if !reflect.DeepEqual(rows, history) {
			t.Fatal("repeat_or_full_read_changed_first_seen_or_semantic_history")
		}
	}
	write, openErr := store.Open(c.ctx, db)
	if openErr != nil {
		t.Fatal("synthetic_requalification_archive_open_refused")
	}
	t.Cleanup(func() { owaCloseArchive(t, write) })
	prior, priorErr := write.OutlookState(c.ctx, c.profile.Name, "outlook|"+c.profile.Name, c.account)
	receipt, receiptErr := write.OutlookReadReceipt(c.ctx, c.account)
	if priorErr != nil || receiptErr != nil {
		t.Fatal("synthetic_prior_read_state_refused")
	}
	now := time.Now()
	_, priorErr = write.CommitOutlook(c.ctx, store.OutlookBatch{
		Account: c.account, Origin: receipt.Origin, At: now, FreshAt: now, Zone: time.Local,
		Stamp: store.OutlookStamp(4, time.Local), PreserveIdentity: true,
	}, func(store.CalendarResult) store.Run {
		return store.Run{Source: "outlook|" + c.profile.Name, StartedAt: now, FinishedAt: now, Fingerprint: prior.Fingerprint, Status: syncer.StatusOK}
	})
	owaCloseArchive(t, write)
	if priorErr != nil {
		t.Fatal("synthetic_prior_receipt_commit_refused")
	}
	owaCorrupt(t, db, `update calendar_source_events set all_day=null,start_date='',end_date='',is_private=0 where source_id='bb22'`)
	opts.FullRead = false
	rep, _, runErr := syncer.Run(c.ctx, opts)
	if runErr != nil || len(rep.Sources) != 2 {
		t.Fatal("calendar_requalification_sync_refused")
	}
	calRequalified, mailUnchanged := false, false
	for _, source := range rep.Sources {
		if source.Error != nil || len(source.Omissions) != 0 {
			t.Fatal("calendar_requalification_source_refused")
		}
		if source.Source == "outlook|SyntheticCalendar" {
			calRequalified = source.Status == syncer.StatusOK
		}
		if source.Source == "outlook|SyntheticCalendar|mail" {
			mailUnchanged = source.Status == syncer.StatusUnchanged
		}
	}
	if !calRequalified || !mailUnchanged {
		t.Fatal("calendar_requalification_did_not_preserve_independent_mail_lane")
	}
	st := owaArchive(t, db)
	findings, compareErr := owaCompareCalendarContext(c.ctx, st, c.account, expected)
	rows, queryErr := owaQueryContext(c.ctx, st, `select source_id,first_seen_at,field_clocks_json,detail_as_of,removed_at
		from calendar_source_events order by source_id`)
	owaCloseArchive(t, st)
	if compareErr != nil || len(findings) != 0 || queryErr != nil || !reflect.DeepEqual(rows, history) {
		t.Fatalf("calendar_requalification_changed_expected_data_or_history fields=%v", findings)
	}
	owaCapturedIntegrity(t, &c)

	p := owaFixtureProfile("mail", "SyntheticCalendar")
	p.Dir = filepath.Join("..", "testdata", "outlook-owa-calendar-local-fixture", "calendar-empty")
	p.Source.LevelDBDir = filepath.Join(p.Dir, "IndexedDB", "https_outlook.office.com_0.indexeddb.leveldb")
	p.Source.BlobDir = filepath.Join(p.Dir, "IndexedDB", "https_outlook.office.com_0.indexeddb.blob")
	empty := owaObserve(t, p)
	emptyExpected, emptyErr := owaReadExpectedCalendar(empty.ctx, empty.profile.Source.LevelDBDir, empty.profile.Source.BlobDir)
	*empty.readersReleased = *empty.readersReleased && emptyExpected.Released
	if emptyErr != nil || !emptyExpected.Released || len(emptyExpected.Rows) != 0 {
		t.Fatal("genuine_empty_calendar_not_independently_proved")
	}
	opts.OutlookRoot, opts.FullRead = empty.root, true
	rep, _, runErr = syncer.Run(empty.ctx, opts)
	if runErr == nil || len(rep.Sources) != 2 {
		t.Fatal("empty_after_history_was_not_refused")
	}
	calRefused, mailRead := false, false
	for _, source := range rep.Sources {
		if source.Source == "outlook|SyntheticCalendar" {
			calRefused = source.Status == syncer.StatusFailed && source.Error != nil && source.Error.Code == "outlook_layout_unsupported"
		}
		if source.Source == "outlook|SyntheticCalendar|mail" {
			mailRead = source.Status == syncer.StatusOK && source.Error == nil && len(source.Omissions) == 0
		}
	}
	if !calRefused || !mailRead {
		t.Fatal("calendar_empty_history_guard_or_independent_mail_read_changed")
	}
	st = owaArchive(t, db)
	findings, compareErr = owaCompareCalendarContext(empty.ctx, st, c.account, expected)
	owaCloseArchive(t, st)
	if compareErr != nil || len(findings) != 0 {
		t.Fatalf("source_absence_erased_or_removed_historical_events fields=%v", findings)
	}
	owaCapturedIntegrity(t, &empty)
}

func TestOWACalendarPersistedComparatorDetectsCorruption(t *testing.T) {
	for _, tc := range []struct {
		column, expression string
	}{
		{"event_key", "'synthetic-wrong-key'"},
		{"composite_key", "'synthetic-wrong-composite'"},
		{"global_id", "'synthetic-wrong-global'"},
		{"series_key", "'synthetic-wrong-series'"},
		{"start_at", "'2031-03-06T00:00:00.000Z'"},
		{"all_day", "null"},
		{"is_private", "0"},
		{"subject", "'synthetic-wrong-subject'"},
		{"body_text", "'synthetic-wrong-body'"},
		{"body_html", "'<p>synthetic-wrong-body</p>'"},
		{"attendees_json", "'[]'"},
		{"unknown_fields", "''"},
		{"removed_at", "'2031-03-06T00:00:00.000Z'"},
	} {
		t.Run(tc.column, func(t *testing.T) {
			c := owaObserve(t, owaFixtureProfile("mail", "SyntheticCalendar"))
			expected, err := owaReadExpectedCalendar(c.ctx, c.profile.Source.LevelDBDir, c.profile.Source.BlobDir)
			*c.readersReleased = *c.readersReleased && expected.Released
			if err != nil || !expected.Released {
				t.Fatal("independent_calendar_expectations_refused")
			}
			db := filepath.Join(owaScratch(t), "calendar.db")
			rep, _, runErr := syncer.Run(c.ctx, syncer.Options{
				Root: filepath.Join(c.root, "no-teams"), DBPath: db, OutlookRoot: c.root,
				OutlookEnabled: true, OutlookMinReadInterval: -1,
			})
			owaSyncChecks(t, rep, runErr, c, 0)
			owaCorrupt(t, db, "update calendar_source_events set "+tc.column+"="+tc.expression+" where source_id='aa11'")
			st := owaArchive(t, db)
			findings, compareErr := owaCompareCalendarContext(c.ctx, st, c.account, expected)
			owaCloseArchive(t, st)
			if compareErr != nil || !slices.Contains(findings, tc.column) {
				t.Fatalf("calendar_corruption_not_detected field=%s", tc.column)
			}
		})
	}
}
