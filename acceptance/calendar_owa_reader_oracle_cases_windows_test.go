//go:build acceptance && windows

package acceptance

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/ourostack/m365crawl/internal/indexeddb"
)

func TestOWACalendarExpectedReaderGenuineLiterals(t *testing.T) {
	for _, tc := range []struct {
		family, variant string
		rows            int
		refused         bool
	}{
		{"outlook-owa-calendar-local-fixture", "calendar", 2, false},
		{"outlook-owa-calendar-local-fixture", "calendar-empty", 0, false},
		{"outlook-owa-calendar-local-fixture", "calendar-damaged", 0, true},
		{"outlook-owa-calendar-fixture", "calendar", 2, false},
		{"outlook-owa-fixture", "mail", 0, true},
		{"outlook-owa-fixture", "multiple", 0, true},
	} {
		t.Run(tc.family+"/"+tc.variant, func(t *testing.T) {
			idb := filepath.Join("..", "testdata", tc.family, tc.variant, "IndexedDB")
			out, err := owaReadExpectedCalendar(t.Context(), filepath.Join(idb, "https_outlook.office.com_0.indexeddb.leveldb"),
				filepath.Join(idb, "https_outlook.office.com_0.indexeddb.blob"))
			if (err != nil) != tc.refused || !out.Released || len(out.Rows) != tc.rows {
				t.Fatal("independent_reader_census_or_release_mismatch")
			}
			if tc.variant == "calendar" {
				if out.Counts["records"] != 2 || out.Rows[0].Temporal.UID != "aa11" || out.Rows[1].Temporal.UID != "bb22" ||
					out.Rows[0].Body.Text != "Synthetic & cached calendar" || out.Rows[0].Optional.Attendees[0].Type != "required" {
					t.Fatal("genuine_raw_projection_literals_mismatch")
				}
				if tc.family == "outlook-owa-calendar-local-fixture" {
					if out.Rows[1].Temporal.StartDate != "2031-03-06" || out.Rows[1].Temporal.AllDay != 1 {
						t.Fatal("genuine_declared_local_day_lost")
					}
				} else if out.Rows[1].Temporal.AllDay != -1 || out.Rows[1].Unaligned != 1 {
					t.Fatal("original_missing_zone_was_invented")
				}
			}
		})
	}
}

func TestOWACalendarExpectedReaderCloseFailureRetainsLease(t *testing.T) {
	prior := owaSurveyClose
	t.Cleanup(func() { owaSurveyClose = prior })
	owaSurveyClose = func(origin *indexeddb.Origin) error {
		if err := prior(origin); err != nil {
			return err
		}
		return errors.New("synthetic_close_unproved")
	}
	idb := filepath.Join("..", "testdata", "outlook-owa-calendar-local-fixture", "calendar", "IndexedDB")
	out, err := owaReadExpectedCalendar(t.Context(), filepath.Join(idb, "https_outlook.office.com_0.indexeddb.leveldb"),
		filepath.Join(idb, "https_outlook.office.com_0.indexeddb.blob"))
	if err == nil || out.Released || len(out.Rows) != 0 {
		t.Fatal("unproved_release_exposed_usable_expectations_or_cleanup_lease")
	}
}
