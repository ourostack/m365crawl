package outlookowacal

import (
	"testing"

	"github.com/ourostack/m365crawl/internal/calendar"
)

func TestCalendarPreviewAcceptsDecodedJavaScriptSurrogatesWithoutDroppingText(t *testing.T) {
	for _, tc := range []struct {
		source, want string
		loss         int
	}{
		{"synthetic " + string([]byte{0xed, 0xa0, 0x80}), "synthetic \ufffd", 0},
		{"synthetic " + string([]byte{0xed, 0xbf, 0xbf}), "synthetic \ufffd", 0},
		{"synthetic \U0001f642", "synthetic \U0001f642", 0},
		{"synthetic " + string([]byte{0xff}), "", 1},
	} {
		value := event()
		replace(value, "Preview", tc.source)
		got, notes, err := MapEvent("synthetic-account", value)
		if err != nil || got.BodyPreview != tc.want || notes.Unmapped != tc.loss ||
			unknown(got, calendar.FieldBodyPreview) != (tc.loss != 0) {
			t.Fatal("decoded_utf16_preview_lost_or_malformed_bytes_accepted")
		}
	}
}
