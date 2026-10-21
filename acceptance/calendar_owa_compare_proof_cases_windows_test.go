//go:build acceptance && windows

package acceptance

import (
	"maps"
	"testing"
)

func TestOWACalendarPersistedComparatorRefusesUnprovedExpectations(t *testing.T) {
	p := owaFixtureProfile("mail", "SyntheticCalendar")
	good, err := owaReadExpectedCalendar(t.Context(), p.Source.LevelDBDir, p.Source.BlobDir)
	if err != nil {
		t.Fatal("synthetic_expected_reader_refused")
	}
	for _, failure := range []string{"lease", "missing_counts", "partial_rows", "decode_failed", "uid_conflict"} {
		t.Run(failure, func(t *testing.T) {
			expected := good
			expected.Counts = maps.Clone(good.Counts)
			switch failure {
			case "lease":
				expected.Released = false
			case "missing_counts":
				expected.Counts = nil
			case "partial_rows":
				expected.Rows = nil
			default:
				expected.Counts[failure] = 1
			}
			findings, compareErr := owaCompareCalendarContext(t.Context(), nil, "outlook/SyntheticCalendar", expected)
			if compareErr == nil || len(findings) != 0 {
				t.Fatal("unproved_expected_data_reached_archive_comparison")
			}
		})
	}
}
