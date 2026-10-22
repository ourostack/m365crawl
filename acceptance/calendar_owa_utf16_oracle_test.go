//go:build acceptance

package acceptance

import "testing"

func TestOWAOracleDecodedUTF16PreviewMatchesLiteralUnicodeText(t *testing.T) {
	for _, tc := range []struct {
		raw, want string
		invalid   int
	}{
		{"synthetic " + string([]byte{0xed, 0xa0, 0x80}), "synthetic \ufffd", 0},
		{"synthetic " + string([]byte{0xff}), "", 1},
	} {
		got, err := owaProjectOptional(t.Context(), owaObject("Preview", tc.raw))
		if err != nil || got.Invalid != tc.invalid || got.Fields["body_preview"].Value != tc.want ||
			got.Fields["body_preview"].Known != (tc.invalid == 0) {
			t.Fatal("independent_utf16_projection_or_damage_boundary_failed")
		}
	}
}
