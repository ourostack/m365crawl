//go:build acceptance

package acceptance

import (
	"context"
	"errors"
	"testing"

	"github.com/ourostack/m365crawl/internal/outlookowacal"
)

func TestOWACalendarIndependentScalarPrivacyLiterals(t *testing.T) {
	jwt := "eyJSYNTHETIC.eyJEXAMPLE.SYNTHETIC"
	for _, tc := range []struct {
		source, expected string
		redactions       int
	}{
		{"Synthetic <>& café", "Synthetic <>& café", 0},
		{"[EXTERNAL] Synthetic", "[EXTERNAL] Synthetic", 0},
		{"[redacted]", "[redacted]", 0},
		{"Bearer SYNTHETIC", "[redacted]", 1},
		{"bEaReR\tSYNTHETIC", "[redacted]", 1},
		{"not bearer SYNTHETIC", "not bearer SYNTHETIC", 0},
		{"see " + jwt + " end", "see [redacted] end", 1},
		{"https://example.invalid/?x=1&sig=SYNTHETIC&end=2", "https://example.invalid/?x=1&sig=[redacted]&end=2", 1},
		{"https://example.invalid/?SIG=FIRST&sig=SECOND", "https://example.invalid/?SIG=[redacted]&sig=[redacted]", 2},
		{"https://example.invalid/?design=value", "https://example.invalid/?design=value", 0},
	} {
		clean, count, err := owaOraclePrivacyText(t.Context(), tc.source)
		if err != nil || clean != tc.expected || count != tc.redactions {
			t.Fatal("independent_scalar_privacy_literal_mismatch")
		}
		value := owaObject("UID", "AA11", "Start", "2026-10-20T00:00:00Z", "End", "2026-10-21T00:00:00Z",
			"LastModifiedTime", "2026-10-19T00:00:00Z", "Subject", tc.source)
		actual, notes, err := outlookowacal.MapEvent("outlook/Synthetic", value)
		if err != nil || actual.Subject != tc.expected || notes.Redacted != tc.redactions {
			t.Fatal("scalar_privacy_actual_differential_mismatch")
		}
	}
}

func TestOWACalendarIndependentScalarPrivacyRefusals(t *testing.T) {
	for _, source := range []string{`{"password":"SYNTHETIC"}`, `[["password","SYNTHETIC"]]`,
		"https://example.invalid/?x=1&amp;sig=SYNTHETIC", "Bearer\fSYNTHETIC", string([]byte{0xff})} {
		if _, _, err := owaOraclePrivacyText(t.Context(), source); err == nil {
			t.Fatal("unproved_privacy_form_became_expected_data")
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := owaOraclePrivacyText(ctx, "Synthetic"); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled_privacy_projection_accepted")
	}
}

func TestOWACalendarTextScrubSignedQueryLiteral(t *testing.T) {
	for _, tc := range []struct{ source, expected string }{
		{"https://example.invalid/?sig=SYNTHETIC&x=1", "https://example.invalid/?sig=[redacted]&x=1"},
		{"https://example.invalid/?x=1&sig=SYNTHETIC", "https://example.invalid/?x=1&sig=[redacted]"},
		{"https://example.invalid/?x=1&SIG=SYNTHETIC#fragment", "https://example.invalid/?x=1&SIG=[redacted]#fragment"},
	} {
		value := owaObject("UID", "AA11", "Start", "2026-10-20T00:00:00Z", "End", "2026-10-21T00:00:00Z",
			"LastModifiedTime", "2026-10-19T00:00:00Z", "Subject", tc.source)
		actual, notes, err := outlookowacal.MapEvent("outlook/Synthetic", value)
		if err != nil || actual.Subject != tc.expected || notes.Redacted != 1 {
			t.Fatal("synthetic_calendar_signed_query_not_redacted")
		}
	}
}
