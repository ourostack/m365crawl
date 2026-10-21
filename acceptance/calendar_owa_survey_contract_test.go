//go:build acceptance

package acceptance

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/indexeddb"
	"github.com/ourostack/m365crawl/internal/v8"
)

func TestOWACalendarSurveyTypedShapes(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value any
		want  map[string]int64
	}{
		{"primitive", "synthetic-private-value", map[string]int64{"non_objects": 1}},
		{"duplicate-field", owaObject("UID", "aa11", "UID", "bb22"), map[string]int64{"non_objects": 1}},
		{"unknown-uid", owaObject("UID", v8.Undefined{}), map[string]int64{"uid_missing": 1, "all_day_unknown": 1}},
		{"bad-types", owaObject("UID", false, "Start", "not-date", "End", int64(17), "IsAllDayEvent", "true"), map[string]int64{"uid_invalid": 1, "start_invalid": 1, "end_invalid": 1, "all_day_invalid": 1}},
		{"utc-year-outside-range", owaObject("Start", "0001-01-01T00:00:00+14:00", "End", "9999-12-31T23:59:00-14:00"), map[string]int64{"start_invalid": 1, "end_invalid": 1}},
		{"reversed", owaObject("Start", "2031-03-06T00:00:00Z", "End", "2031-03-05T00:00:00Z", "IsAllDayEvent", true), map[string]int64{"reversed_intervals": 1, "all_day_unaligned": 1}},
		{"explicit-timed-midnight", owaObject("Start", "2031-03-06T00:00:00Z", "End", "2031-03-07T00:00:00Z", "IsAllDayEvent", false), map[string]int64{"utc_midnight_intervals": 1, "all_day_false": 1, "all_day_true": 0}},
		{"unaligned-all-day", owaObject("Start", "2031-03-06T09:30:00Z", "End", "2031-03-06T10:00:00Z", "IsAllDayEvent", true), map[string]int64{"all_day_true": 1, "all_day_unaligned": 1}},
		{"unknown-body", owaObject("Body", owaObject()), map[string]int64{"body_unknown": 1}},
		{"plain-body", owaObject("Body", owaObject("BodyType", "TEXT", "Value", "synthetic-private-value")), map[string]int64{"body_text": 1}},
		{"truncated-body", owaObject("Body", owaObject("BodyType", "HTML", "Value", "synthetic-private-value", "IsTruncated", true)), map[string]int64{"body_truncated": 1, "body_html": 0}},
		{"bad-body", owaObject("Body", false), map[string]int64{"body_invalid": 1}},
		{"incomplete-body", owaObject("Body", owaObject("BodyType", "HTML")), map[string]int64{"body_invalid": 1}},
		{"bad-truncation", owaObject("Body", owaObject("BodyType", "HTML", "Value", "", "IsTruncated", "false")), map[string]int64{"body_invalid": 1}},
		{"unsupported-body", owaObject("Body", owaObject("BodyType", "OTHER", "Value", "")), map[string]int64{"body_invalid": 1}},
		{"unknown-attendees", owaObject(), map[string]int64{"attendees_unknown": 1}},
		{"partial-attendees", owaObject("RequiredAttendees", []any{}), map[string]int64{"attendees_partial": 1, "attendees_complete": 0}},
		{"bad-list", owaObject("RequiredAttendees", "not-a-list"), map[string]int64{"attendees_invalid": 1}},
		{"bad-row", owaObject("RequiredAttendees", []any{false}), map[string]int64{"attendees_invalid": 1}},
		{"bad-mailbox", owaObject("RequiredAttendees", []any{owaObject("Mailbox", "not-an-object")}), map[string]int64{"attendees_invalid": 1}},
		{"bad-name", owaObject("RequiredAttendees", []any{owaObject("Mailbox", owaObject("Name", true))}), map[string]int64{"attendees_invalid": 1}},
		{"blank-mailbox", owaObject("RequiredAttendees", []any{owaObject("Mailbox", owaObject("Name", " ", "EmailAddress", ""))}, "OptionalAttendees", []any{}, "Resources", []any{}), map[string]int64{"attendees_invalid": 1, "attendees_complete": 0}},
		{"bad-optionals", owaObject("StartTimeZoneId", false, "CalendarItemType", int64(19), "Organizer", owaObject("Mailbox", false), "ResponseType", true), map[string]int64{"time_zone_invalid": 1, "item_type_invalid": 1, "organizer_invalid": 1, "response_invalid": 1}},
		{"present-optionals", owaObject("StartTimeZoneId", "synthetic-private-zone", "CalendarItemType", "Single", "Organizer", owaObject("Mailbox", owaObject("Name", "synthetic-private-name")), "ResponseType", "Accept"), map[string]int64{"time_zone_present": 1, "item_type_present": 1, "organizer_present": 1, "response_present": 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			counts := map[string]int64{}
			if err := owaSurveyShape(context.Background(), counts, map[string][32]byte{}, tc.value, []byte("synthetic-private-payload")); err != nil {
				t.Fatal("typed_survey_refused")
			}
			for field, want := range tc.want {
				if counts[field] != want {
					t.Fatalf("typed_counter_mismatch field=%s got=%d want=%d", field, counts[field], want)
				}
			}
			for field := range counts {
				found := false
				for _, allowed := range owaCalendarSurveyFields {
					found = found || field == allowed
				}
				if !found || strings.Contains(field, "synthetic-private") {
					t.Fatal("unbounded_or_private_counter")
				}
			}
		})
	}
}

func TestOWACalendarSurveyAlreadyCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	profile := owaFixtureProfile("mail", "Synthetic")
	survey, err := owaSurveyCalendar(ctx, profile.Source.LevelDBDir, profile.Source.BlobDir)
	if err == nil || !survey.Released || !strings.Contains(err.Error(), "context_refused") {
		t.Fatal("pre_canceled_survey_not_refused_at_context_boundary")
	}
}

func TestOWACalendarSurveyMetadataRefusalsDoNotOpenValues(t *testing.T) {
	old := owaSurveyOpen
	t.Cleanup(func() { owaSurveyOpen = old })
	opened := false
	owaSurveyOpen = func(string, string, indexeddb.OpenOptions) (*indexeddb.Origin, error) {
		opened = true
		return nil, errors.New("synthetic-private-opener")
	}
	for _, variant := range []string{"unknown", "multiple", "missing-source"} {
		profile := owaFixtureProfile(variant, "Synthetic")
		if variant == "missing-source" {
			profile.Source.LevelDBDir, profile.Source.BlobDir = filepath.Join(t.TempDir(), "missing"), ""
		}
		survey, err := owaSurveyCalendar(context.Background(), profile.Source.LevelDBDir, profile.Source.BlobDir)
		if err == nil || !survey.Released || opened || strings.Contains(err.Error(), "synthetic-private") {
			t.Fatal("metadata_refusal_opened_values_or_became_success")
		}
	}
}

func TestOWACalendarSurveyAllowsOnlyEvents(t *testing.T) {
	old := owaSurveyOpen
	t.Cleanup(func() { owaSurveyOpen = old })
	checked := false
	owaSurveyOpen = func(level, blob string, opts indexeddb.OpenOptions) (*indexeddb.Origin, error) {
		_, dbs, err := indexeddb.Census(level)
		if err != nil || opts.KeepStore == nil {
			t.Fatal("survey_allowlist_missing")
		}
		kept := 0
		for _, db := range dbs {
			for _, store := range db.Stores {
				if opts.KeepStore(db.ID, store.ID) {
					kept++
					if store.Name != "events" {
						t.Fatal("non_event_value_store_authorized")
					}
				}
			}
		}
		if kept != 1 {
			t.Fatal("ambiguous_event_value_allowlist")
		}
		checked = true
		return old(level, blob, opts)
	}
	profile := owaFixtureProfile("mail", "Synthetic")
	survey, err := owaSurveyCalendar(context.Background(), profile.Source.LevelDBDir, profile.Source.BlobDir)
	if err != nil || !checked || !survey.Released || survey.Counts["records"] != 2 || survey.Counts["decode_failed"] != 0 {
		t.Fatal("event_only_survey_refused_or_read_decoys")
	}
}

func TestOWACalendarSurveyDuplicateCounters(t *testing.T) {
	for _, tc := range []struct {
		payloads  []string
		conflicts int64
	}{
		{[]string{"first", "first", "different"}, 1},
		{[]string{"first", "different", "different"}, 2},
		{[]string{"first", "different", "first"}, 1},
	} {
		counts, seen := map[string]int64{}, map[string][32]byte{}
		for i, payload := range tc.payloads {
			if owaSurveyShape(context.Background(), counts, seen, owaObject("UID", "AA11"), []byte(payload)) != nil {
				t.Fatal("duplicate_survey_refused")
			}
			if counts["uid_valid"] != int64(i+1) {
				t.Fatal("duplicate_identity_observation_lost")
			}
		}
		if counts["uid_duplicate"] != 2 || counts["uid_conflict"] != tc.conflicts {
			t.Fatal("duplicate_conflict_reference_changed")
		}
	}
}

func TestOWACalendarSurveyReleaseAndCancellation(t *testing.T) {
	originalOpen, originalClose := owaSurveyOpen, owaSurveyClose
	t.Cleanup(func() { owaSurveyOpen, owaSurveyClose = originalOpen, originalClose })
	profile := owaFixtureProfile("empty", "Synthetic")
	for _, mode := range []string{"open-error", "nil-reader", "close-error", "close-panic", "cancel-during-open", "cancel-and-close-error"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			owaSurveyOpen = func(level, blob string, opts indexeddb.OpenOptions) (*indexeddb.Origin, error) {
				if mode == "open-error" {
					return nil, errors.New("synthetic-private-open")
				}
				if mode == "nil-reader" {
					return nil, nil
				}
				origin, err := originalOpen(level, blob, opts)
				if strings.HasPrefix(mode, "cancel") {
					cancel()
				}
				return origin, err
			}
			owaSurveyClose = func(origin *indexeddb.Origin) error {
				if err := originalClose(origin); err != nil {
					t.Error("actual_fixture_reader_close_failed")
				}
				if mode == "close-panic" {
					panic("synthetic-private-close")
				}
				if mode == "close-error" || mode == "cancel-and-close-error" {
					return errors.New("synthetic-private-close")
				}
				return nil
			}
			survey, err := owaSurveyCalendar(ctx, profile.Source.LevelDBDir, profile.Source.BlobDir)
			wantReleased := mode == "cancel-during-open"
			if err == nil || survey.Released != wantReleased || strings.Contains(err.Error(), "synthetic-private") {
				t.Fatalf("unsafe_survey_outcome released=%v err=%v", survey.Released, err)
			}
			if mode == "cancel-and-close-error" && (!strings.Contains(err.Error(), "context_refused") || !strings.Contains(err.Error(), "close_refused")) {
				t.Fatal("primary_cancellation_or_release_failure_lost")
			}
		})
	}
}
