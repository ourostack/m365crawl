//go:build acceptance && windows

package acceptance

import (
	"context"
	"errors"
	"testing"
	"time"
	"unsafe"
)

func TestOWACalendarWindowsZoneOracleLiteralClocks(t *testing.T) {
	oracle, err := owaNewWindowsZoneOracle(t.Context())
	if err != nil {
		t.Fatal("windows_zone_oracle_setup_refused")
	}
	for _, tc := range []struct{ zone, utc, local string }{
		{"UTC", "2026-10-20T00:00:00Z", "2026-10-20T00:00:00Z"},
		{"Pacific Standard Time", "2026-03-08T08:00:00Z", "2026-03-08T00:00:00-08:00"},
		{"Pacific Standard Time", "2026-03-09T07:00:00Z", "2026-03-09T00:00:00-07:00"},
		{"Pacific Standard Time", "2026-11-01T07:00:00Z", "2026-11-01T00:00:00-07:00"},
		{"Pacific Standard Time", "2026-11-02T08:00:00Z", "2026-11-02T00:00:00-08:00"},
		{"India Standard Time", "2026-10-19T18:30:00.123456789Z", "2026-10-20T00:00:00.123456789+05:30"},
		{"Tokyo Standard Time", "2026-10-19T15:00:00Z", "2026-10-20T00:00:00+09:00"},
		{"AUS Eastern Standard Time", "2026-10-03T14:00:00Z", "2026-10-04T00:00:00+10:00"},
		{"AUS Eastern Standard Time", "2026-10-04T13:00:00Z", "2026-10-05T00:00:00+11:00"},
	} {
		t.Run(tc.utc+"/"+tc.zone, func(t *testing.T) {
			instant, err := time.Parse(time.RFC3339Nano, tc.utc)
			if err != nil {
				t.Fatal(err)
			}
			local, err := oracle.local(t.Context(), tc.zone, instant)
			if err != nil || local.Format(time.RFC3339Nano) != tc.local || !local.Equal(instant) {
				t.Fatal("independent_windows_local_clock_literal_mismatch")
			}
		})
	}
}

func TestOWACalendarWindowsZoneOracleRefusesUnprovedInputs(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := owaNewWindowsZoneOracle(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled_zone_enumeration_accepted")
	}
	oracle, err := owaNewWindowsZoneOracle(t.Context())
	if err != nil {
		t.Fatal("windows_zone_oracle_setup_refused")
	}
	at := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	for _, zone := range []string{"", "Local", "Synthetic Unknown Zone", "america/los_angeles"} {
		if _, err := oracle.local(t.Context(), zone, at); err == nil || err.Error() != "calendar_oracle_zone_unknown" {
			t.Fatal("unknown_zone_became_host_or_utc_fallback")
		}
	}
	for _, year := range []int{1, 1600, 10000} {
		if _, err := oracle.local(t.Context(), "UTC", time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC)); err == nil {
			t.Fatal("unsupported_native_year_accepted")
		}
	}
	if _, err := oracle.local(ctx, "UTC", at); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled_native_conversion_accepted")
	}
}

func TestOWACalendarWindowsZoneOracleABI(t *testing.T) {
	var zone owaWindowsDynamicZone
	if unsafe.Sizeof(zone) != 432 || unsafe.Offsetof(zone.KeyName) != 172 {
		t.Fatal("native_dynamic_timezone_abi_mismatch")
	}
}
