//go:build acceptance && windows

package acceptance

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestOWACalendarNativeZoneIdentityLiterals(t *testing.T) {
	oracle, err := owaNewWindowsZoneOracle(t.Context())
	if err != nil {
		t.Fatal("native_zone_setup_refused")
	}
	for _, tc := range []struct {
		input, windows, canonical, representative string
		alias                                     bool
	}{
		{"Pacific Standard Time", "Pacific Standard Time", "America/Los_Angeles", "America/Los_Angeles", false},
		{"America/Los_Angeles", "Pacific Standard Time", "America/Los_Angeles", "America/Los_Angeles", false},
		{"US/Pacific", "Pacific Standard Time", "America/Los_Angeles", "America/Los_Angeles", true},
		{"America/Vancouver", "Pacific Standard Time", "America/Vancouver", "America/Los_Angeles", false},
		{"UTC", "UTC", "Etc/UTC", "Etc/UTC", false},
		{"Etc/UTC", "UTC", "Etc/UTC", "Etc/UTC", false},
		{"Asia/Tokyo", "Tokyo Standard Time", "Asia/Tokyo", "Asia/Tokyo", false},
	} {
		t.Run(tc.input, func(t *testing.T) {
			id, err := oracle.identity(t.Context(), tc.input)
			if err != nil || id.Windows != tc.windows || id.Canonical != tc.canonical ||
				id.Representative != tc.representative || id.Alias != tc.alias {
				t.Fatal("independent_native_zone_identity_mismatch")
			}
		})
	}
}

func TestOWACalendarNativeZoneIdentityRefusesInvalidInputs(t *testing.T) {
	oracle, err := owaNewWindowsZoneOracle(t.Context())
	if err != nil {
		t.Fatal("native_zone_setup_refused")
	}
	for _, input := range []string{"", "Local", "Synthetic Unknown Zone", "GMT+07:00", "UTC\x00junk", strings.Repeat("x", 256), string([]byte{0xff})} {
		if _, err := oracle.identity(t.Context(), input); err == nil {
			t.Fatal("invalid_or_custom_zone_became_positive_identity")
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := oracle.identity(ctx, "America/Los_Angeles"); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled_native_identity_accepted")
	}
}

func TestOWACalendarIANAClockDoesNotGuessRegionalRules(t *testing.T) {
	oracle, err := owaNewWindowsZoneOracle(t.Context())
	if err != nil {
		t.Fatal("native_zone_setup_refused")
	}
	instant := time.Date(2026, 3, 9, 7, 0, 0, 123456789, time.UTC)
	for _, input := range []string{"America/Los_Angeles", "US/Pacific"} {
		local, err := oracle.local(t.Context(), input, instant)
		if err != nil || local.Format(time.RFC3339Nano) != "2026-03-09T00:00:00.123456789-07:00" || !local.Equal(instant) {
			t.Fatal("native_iana_clock_or_precision_mismatch")
		}
	}
	if _, err := oracle.local(t.Context(), "America/Vancouver", instant); err == nil || err.Error() != "calendar_oracle_regional_zone_unsupported" {
		t.Fatal("regional_iana_rules_guessed_from_windows_representative")
	}
}

func TestOWACalendarICUResultBoundaries(t *testing.T) {
	output := []uint16{'U', 'T', 'C', 0}
	for _, tc := range []struct {
		count  uintptr
		status int32
		code   string
	}{
		{0, 0, "calendar_oracle_zone_unmapped"},
		{3, 1, "calendar_oracle_zone_unknown"},
		{3, 7, "calendar_oracle_icu_refused"},
		{4, 0, "calendar_oracle_icu_refused"},
	} {
		if _, err := owaICUOutput(t.Context(), tc.count, tc.status, output); err == nil || err.Error() != tc.code {
			t.Fatal("icu_empty_error_or_truncation_became_identity")
		}
	}
	if value, err := owaICUOutput(t.Context(), 3, -128, output); err != nil || value != "UTC" {
		t.Fatal("successful_icu_warning_mistaken_for_error")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := owaICUOutput(ctx, 3, 0, output); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled_icu_result_accepted")
	}
}
