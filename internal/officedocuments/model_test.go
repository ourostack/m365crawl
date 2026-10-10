package officedocuments

import (
	"testing"
	"time"
)

func TestTimestampEvidence(t *testing.T) {
	for _, tt := range []struct {
		raw   string
		value time.Time
		known bool
	}{
		{"2026-10-09T12:34:56.0000001Z", time.Date(2026, 10, 9, 12, 34, 56, 100, time.UTC), true},
		{"2026-10-09T05:34:56-07:00", time.Date(2026, 10, 9, 12, 34, 56, 0, time.UTC), true},
		{"0001-01-01T00:00:00Z", time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC), true},
		{"0000-01-01T00:00:00Z", time.Time{}, false},
		{"2026-10-09T12:34:56", time.Time{}, false},
		{"2026-10-09T1:34:56Z", time.Time{}, false},
		{"2026-10-09T12:34:56,1Z", time.Time{}, false},
		{"2026-10-09T12:34:56+24:00", time.Time{}, false},
		{"2026-10-09T12:34:56+00:60", time.Time{}, false},
		{"invalid", time.Time{}, false},
		{"", time.Time{}, false},
	} {
		got, known := timestamp(tt.raw)
		if got.Raw != tt.raw || !got.Value.Equal(tt.value) || known != tt.known {
			t.Fatalf("timestamp(%q) = %#v, known %t; want %v, %t", tt.raw, got, known, tt.value, tt.known)
		}
	}
}
