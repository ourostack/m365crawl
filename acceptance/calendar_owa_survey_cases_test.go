//go:build acceptance

package acceptance

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
)

func TestOWACalendarSurveyGenuineFixtureLiterals(t *testing.T) {
	complete := map[string]int64{
		"records": 2, "decoded": 2, "objects": 2, "uid_valid": 2,
		"start_valid": 2, "end_valid": 2, "modified_valid": 2, "utc_midnight_intervals": 1,
		"all_day_true": 1, "all_day_false": 1, "body_html": 1, "body_unknown": 1,
		"attendees_complete": 2, "attendee_rows": 3,
		"attendee_required": 1, "attendee_optional": 1, "attendee_resource": 1,
	}
	for _, variant := range []string{"calendar", "calendar-empty", "calendar-damaged", "calendar-empty-mail", "mail-decoy"} {
		t.Run(variant, func(t *testing.T) {
			profile := owaFixtureProfile("mail", "Synthetic")
			switch variant {
			case "calendar-empty-mail":
				profile = owaFixtureProfile("empty", "Synthetic")
			case "mail-decoy":
				profile = owaFixtureProfile("mail-decoy", "Synthetic")
			default:
				idb := filepath.Join("..", "testdata", "outlook-owa-calendar-fixture", variant, "IndexedDB")
				profile.Source.LevelDBDir = filepath.Join(idb, "https_outlook.office.com_0.indexeddb.leveldb")
				profile.Source.BlobDir = filepath.Join(idb, "https_outlook.office.com_0.indexeddb.blob")
				if variant == "calendar-empty" {
					profile.Source.BlobDir = ""
				}
			}
			want := map[string]int64{}
			for _, field := range owaCalendarSurveyFields {
				want[field] = 0
			}
			switch variant {
			case "calendar", "calendar-damaged":
				for key, n := range complete {
					want[key] = n
				}
				if variant == "calendar-damaged" {
					want["records"], want["decode_failed"] = 3, 1
				}
			case "mail-decoy":
				want["records"], want["decode_failed"] = 1, 1
			}
			survey, err := owaSurveyCalendar(context.Background(), profile.Source.LevelDBDir, profile.Source.BlobDir)
			if err != nil || !survey.Released {
				t.Fatalf("calendar_survey_refused: %v", err)
			}
			if !reflect.DeepEqual(survey.Counts, want) {
				t.Fatalf("fixed_counter_census_mismatch counts=%v", survey.Counts)
			}
		})
	}
}
