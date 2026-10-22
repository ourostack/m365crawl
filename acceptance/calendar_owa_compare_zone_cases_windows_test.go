//go:build acceptance && windows

package acceptance

import (
	"testing"

	"github.com/ourostack/m365crawl/internal/outlookowacal"
)

func TestOWACalendarPersistedPreferredZoneSpellingLiterals(t *testing.T) {
	oracle, err := owaNewWindowsZoneOracle(t.Context())
	if err != nil {
		t.Fatal("synthetic_zone_oracle_refused")
	}
	for _, tc := range []struct{ source, expected string }{
		{"UTC", "UTC"}, {"Etc/UTC", "Etc/UTC"}, {"Pacific Standard Time", "America/Los_Angeles"},
		{"America/Los_Angeles", "America/Los_Angeles"},
		{"Tokyo Standard Time", "Asia/Tokyo"}, {"Asia/Tokyo", "Asia/Tokyo"},
		{"Eastern Standard Time", "America/New_York"}, {"America/New_York", "America/New_York"},
		{"GMT Standard Time", "Europe/London"}, {"Europe/Paris", "Europe/Paris"},
		{"India Standard Time", "Asia/Kolkata"}, {"FLE Standard Time", "Europe/Kyiv"},
		{"Argentina Standard Time", "America/Argentina/Buenos_Aires"},
	} {
		value := owaTemporalLiteral()
		owaTemporalSet(value, "StartTimeZoneId", tc.source)
		owaTemporalSet(value, "IsAllDayEvent", false)
		expected, projectErr := owaProjectEvent(t.Context(), oracle, value)
		actual, _, actualErr := outlookowacal.MapEvent("outlook/Synthetic", value)
		row, rowErr := owaCalendarExpectedSQL("outlook/Synthetic", expected)
		if projectErr != nil || actualErr != nil || rowErr != nil ||
			row["time_zone_iana"] != tc.expected || actual.TimeZoneIANA != tc.expected {
			t.Fatal("preferred_zone_spelling_literal_differential_mismatch")
		}
		expected.Temporal.ZoneIdentity = &owaNativeZoneIdentity{Canonical: "Synthetic/Unproved"}
		if _, refusal := owaCalendarExpectedSQL("outlook/Synthetic", expected); refusal == nil {
			t.Fatal("unproved_zone_spelling_was_invented")
		}
	}
}
