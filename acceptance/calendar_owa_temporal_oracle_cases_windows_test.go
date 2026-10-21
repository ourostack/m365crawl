//go:build acceptance && windows

package acceptance

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/outlookowacal"
	"github.com/ourostack/m365crawl/internal/v8"
)

func owaTemporalLiteral() *v8.Object {
	return &v8.Object{
		Keys: []string{"UID", "Start", "End", "LastModifiedTime", "IsAllDayEvent", "StartTimeZoneId", "IsCancelled", "IsOnlineMeeting"},
		Values: []any{"AA11", "2026-03-08T08:00:00Z", "2026-03-09T07:00:00Z", "2026-03-01T12:00:00Z",
			true, "Pacific Standard Time", false, true},
	}
}

func owaTemporalSet(value *v8.Object, key string, field any) {
	for i, name := range value.Keys {
		if name == key {
			value.Values[i] = field
			return
		}
	}
	value.Keys, value.Values = append(value.Keys, key), append(value.Values, field)
}

func TestOWACalendarTemporalOracleLiteral(t *testing.T) {
	oracle, err := owaNewWindowsZoneOracle(t.Context())
	if err != nil {
		t.Fatal("synthetic_oracle_setup_refused")
	}
	for _, tc := range []struct{ zone, start, end, first, last string }{
		{"Pacific Standard Time", "2026-03-08T08:00:00Z", "2026-03-09T07:00:00Z", "2026-03-08", "2026-03-09"},
		{"America/Los_Angeles", "2026-11-01T07:00:00Z", "2026-11-02T08:00:00Z", "2026-11-01", "2026-11-02"},
		{"India Standard Time", "2026-10-19T18:30:00Z", "2026-10-20T18:30:00Z", "2026-10-20", "2026-10-21"},
	} {
		value := owaTemporalLiteral()
		owaTemporalSet(value, "Start", tc.start)
		owaTemporalSet(value, "End", tc.end)
		owaTemporalSet(value, "StartTimeZoneId", tc.zone)
		got, err := owaProjectTemporal(t.Context(), oracle, value)
		if err != nil || got.UID != "aa11" || got.Start.Format(time.RFC3339Nano) != tc.start || got.End.Format(time.RFC3339Nano) != tc.end ||
			got.Modified.Format(time.RFC3339Nano) != "2026-03-01T12:00:00Z" || got.AllDay != 1 || got.StartDate != tc.first || got.EndDate != tc.last ||
			got.Zone != tc.zone || got.ZoneIdentity == nil || got.Flags["IsCancelled"] != 0 || got.Flags["IsOnlineMeeting"] != 1 ||
			got.Flags["IsOrganizer"] != -1 || got.Invalid != 0 || got.UnknownZones != 0 || got.Unaligned != 0 {
			t.Fatal("independent_temporal_literal_projection_mismatch")
		}
	}
}

func TestOWACalendarTemporalOracleUncertainty(t *testing.T) {
	oracle, err := owaNewWindowsZoneOracle(t.Context())
	if err != nil {
		t.Fatal("synthetic_oracle_setup_refused")
	}
	for _, tc := range []struct {
		key            string
		field          any
		invalid, zones int
	}{
		{"StartTimeZoneId", nil, 0, 0},
		{"StartTimeZoneId", "Synthetic Unknown Zone", 0, 1},
		{"StartTimeZoneId", 12.0, 1, 0},
		{"EndTimeZoneId", "UTC", 0, 0},
		{"EndTimeZoneId", "Synthetic Unknown Zone", 0, 1},
		{"EndTimeZoneId", 12.0, 1, 0},
		{"End", "2026-03-09T08:00:00Z", 0, 0},
		{"End", "2026-03-08T08:00:00Z", 0, 0},
	} {
		value := owaTemporalLiteral()
		owaTemporalSet(value, tc.key, tc.field)
		got, err := owaProjectTemporal(t.Context(), oracle, value)
		if err != nil || got.AllDay != -1 || got.StartDate != "" || got.EndDate != "" || got.Unaligned != 1 ||
			got.Invalid != tc.invalid || got.UnknownZones != tc.zones {
			t.Fatal("uncertain_temporal_shape_became_positive_date")
		}
	}
	value := owaTemporalLiteral()
	owaTemporalSet(value, "IsAllDayEvent", nil)
	owaTemporalSet(value, "IsCancelled", "false")
	got, err := owaProjectTemporal(t.Context(), oracle, value)
	if err != nil || got.AllDay != -1 || got.Flags["IsCancelled"] != -1 || got.Invalid != 1 || got.Unaligned != 0 {
		t.Fatal("missing_or_malformed_boolean_became_false")
	}
}

func TestOWACalendarTemporalOracleFixedRefusals(t *testing.T) {
	oracle, err := owaNewWindowsZoneOracle(t.Context())
	if err != nil {
		t.Fatal("synthetic_oracle_setup_refused")
	}
	for _, tc := range []struct {
		key   string
		field any
	}{
		{"UID", "SYNTHETIC-SECRET-NOT-HEX"}, {"UID", ""},
		{"Start", "SYNTHETIC-SECRET-NOT-TIME"}, {"End", "2026-03-07T08:00:00Z"},
		{"LastModifiedTime", nil}, {"StartTimeZoneId", "US/Pacific"},
		{"StartTimeZoneId", "America/Vancouver"},
	} {
		value := owaTemporalLiteral()
		owaTemporalSet(value, tc.key, tc.field)
		if _, err := owaProjectTemporal(t.Context(), oracle, value); err == nil {
			t.Fatal("unproved_required_or_alias_region_projection_accepted")
		}
	}
	if _, err := owaProjectTemporal(t.Context(), oracle, false); err == nil {
		t.Fatal("nonobject_projection_accepted")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := owaProjectTemporal(ctx, oracle, owaTemporalLiteral()); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled_projection_accepted")
	}
}

func TestOWACalendarTemporalOracleDifferentialSynthetic(t *testing.T) {
	oracle, err := owaNewWindowsZoneOracle(t.Context())
	if err != nil {
		t.Fatal("synthetic_oracle_setup_refused")
	}
	for _, tc := range []struct {
		key   string
		field any
	}{
		{"EndTimeZoneId", "America/Los_Angeles"},
		{"StartTimeZoneId", nil},
		{"StartTimeZoneId", "Synthetic Unknown Zone"},
		{"StartTimeZoneId", 12.0},
		{"EndTimeZoneId", "UTC"},
		{"EndTimeZoneId", 12.0},
		{"End", "2026-03-09T08:00:00Z"},
		{"IsAllDayEvent", nil},
		{"IsCancelled", "false"},
	} {
		value := owaTemporalLiteral()
		owaTemporalSet(value, tc.key, tc.field)
		expected, err := owaProjectTemporal(t.Context(), oracle, value)
		if err != nil {
			t.Fatal("independent_synthetic_projection_refused")
		}
		actual, notes, err := outlookowacal.MapEvent("outlook/Synthetic", value)
		if err != nil || actual.SourceID != expected.UID || !actual.Start.Equal(expected.Start) || !actual.End.Equal(expected.End) ||
			actual.LastModified == nil || !actual.LastModified.Equal(expected.Modified) || actual.TimeZone != expected.Zone ||
			actual.StartDate != expected.StartDate || actual.EndDate != expected.EndDate ||
			actual.AllDay.Known() != (expected.AllDay != -1) || expected.AllDay != -1 && !actual.AllDay.Is(expected.AllDay == 1) ||
			notes.Unmapped != expected.Invalid || notes.UnknownZone != expected.UnknownZones || notes.AllDayUnaligned != expected.Unaligned {
			t.Fatal("temporal_core_differential_mismatch")
		}
		for _, flag := range []struct {
			name       string
			known, yes bool
		}{
			{"IsCancelled", actual.Cancelled.Known(), actual.Cancelled.Is(true)},
			{"IsOnlineMeeting", actual.IsOnlineMeeting.Known(), actual.IsOnlineMeeting.Is(true)},
			{"IsOrganizer", actual.IsOrganizer.Known(), actual.IsOrganizer.Is(true)},
			{"HasAttachments", actual.HasAttachments.Known(), actual.HasAttachments.Is(true)},
		} {
			want := expected.Flags[flag.name]
			if flag.known != (want != -1) || flag.known && flag.yes != (want == 1) {
				t.Fatal("boolean_knowledge_differential_mismatch")
			}
		}
	}
}
