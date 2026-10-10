package onenote

import (
	"testing"
	"time"
)

func TestFiletimeKnownEpoch(t *testing.T) {
	for _, tc := range []struct {
		ticks int64
		want  time.Time
	}{
		{116444736000000000, time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC)},
		{116444736010000007, time.Date(1970, 1, 1, 0, 0, 1, 700, time.UTC)},
		{1, time.Date(1601, 1, 1, 0, 0, 0, 100, time.UTC)},
		{2650467743999999999, time.Date(9999, 12, 31, 23, 59, 59, 999999900, time.UTC)},
	} {
		got, valid := filetime(tc.ticks)
		if !valid || !got.Equal(tc.want) || got.Location() != time.UTC {
			t.Fatalf("FILETIME %d = %v, valid %v; want %v", tc.ticks, got, valid, tc.want)
		}
	}
}

func TestFiletimeUnknownAndBoundaries(t *testing.T) {
	for _, ticks := range []int64{0, -1, -9223372036854775808, 2650467744000000000, 9223372036854775807} {
		got, valid := filetime(ticks)
		if valid || !got.IsZero() {
			t.Fatalf("invalid FILETIME %d = %v, valid %v", ticks, got, valid)
		}
	}
}
