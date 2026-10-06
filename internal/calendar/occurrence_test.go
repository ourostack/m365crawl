package calendar

import (
	"strings"
	"testing"
	"time"
)

// This file pins the OccurrenceID encoding, which is assumption A10 in the plan: the series id is
// an Exchange global object id in uppercase hex, and the occurrence's original date replaces hex
// characters 32 to 39 as YYYY (4 hex), MM (2 hex), DD (2 hex). If the real-cache acceptance shows
// a different encoding, the expected strings below (all built through fakeSeries and dateBytes)
// are the only things to change besides occurrence.go.

// fakeSeries builds a synthetic series id of n hex characters: a 32-character class id, zeroed
// date bytes, then a recognizable tail.
func fakeSeries(n int) string {
	const class = "040000008200E00074C5B7101A82E008" // 32 characters, synthetic
	tail := strings.Repeat("AB12", n)[:n-40]
	return class + "00000000" + tail
}

// dateBytes is the expected hex for 2026-10-05: year 0x07EA, month 0x0A, day 0x05.
const dateBytes = "07EA0A05"

func TestOccurrenceIDEncodesDate(t *testing.T) {
	series := fakeSeries(112)
	got := OccurrenceID(series, time.Date(2026, 10, 5, 16, 0, 0, 0, time.UTC))
	want := series[:32] + dateBytes + series[40:]
	if got != want || len(got) != 112 {
		t.Fatalf("got %q\nwant %q", got, want)
	}
	// Lower-case input is accepted and the result is upper case.
	if low := OccurrenceID(strings.ToLower(series), time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)); low != want {
		t.Fatalf("lower-case series: %q", low)
	}
	// The date is the wall-clock date of the value as given, in its own zone.
	zone := time.FixedZone("UTC-7", -7*3600)
	if g := OccurrenceID(series, time.Date(2026, 10, 5, 23, 0, 0, 0, zone)); g != want {
		t.Fatalf("zone date: %q", g)
	}
}

func TestOccurrenceIDKeepsLength112And200(t *testing.T) {
	for _, n := range []int{40, 112, 200} {
		series := fakeSeries(n)
		got := OccurrenceID(series, time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC))
		if len(got) != n || !strings.HasPrefix(got, series[:32]+"07EA011F") || got[40:] != series[40:] {
			t.Fatalf("n=%d got %q", n, got)
		}
	}
}

func TestOccurrenceIDRoundTrip(t *testing.T) {
	for _, n := range []int{112, 200} {
		series := fakeSeries(n)
		id := OccurrenceID(series, time.Date(2026, 12, 31, 8, 0, 0, 0, time.UTC))
		gotSeries, date, ok := SplitOccurrenceID(id)
		if !ok || gotSeries != series || date != "2026-12-31" {
			t.Fatalf("n=%d: %q %q %v", n, gotSeries, date, ok)
		}
	}
}

func TestOccurrenceIDRejectsBadSeries(t *testing.T) {
	when := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	good := fakeSeries(112)
	bad := map[string]string{
		"empty":           "",
		"short":           good[:38],
		"just short":      good[:39],
		"not hex":         good[:50] + "Z" + good[51:],
		"nonzero date":    good[:32] + dateBytes + good[40:],
		"one nonzero bit": good[:39] + "1" + good[40:],
	}
	for name, series := range bad {
		if got := OccurrenceID(series, when); got != "" {
			t.Errorf("%s: got %q, want empty", name, got)
		}
	}
	if got := OccurrenceID(good, time.Time{}); got != "" {
		t.Errorf("zero time: got %q", got)
	}
	if got := OccurrenceID(good, time.Date(70000, 1, 1, 0, 0, 0, 0, time.UTC)); got != "" {
		t.Errorf("year over 0xFFFF: got %q", got)
	}
}

func TestSplitOccurrenceIDRejects(t *testing.T) {
	series := fakeSeries(112)
	bad := map[string]string{
		"empty":         "",
		"short":         series[:30],
		"not hex":       series[:60] + "Q" + series[61:],
		"series id":     series, // date bytes zero: this is a series id, not an occurrence id
		"month 13":      series[:32] + "07EA0D05" + series[40:],
		"day 32":        series[:32] + "07EA0A20" + series[40:],
		"feb 30":        series[:32] + "07EA021E" + series[40:],
		"month zero":    series[:32] + "07EA0005" + series[40:],
		"year zero":     series[:32] + "00000105" + series[40:],
		"day zero":      series[:32] + "07EA0A00" + series[40:],
		"only year set": series[:32] + "07EA0000" + series[40:],
	}
	for name, id := range bad {
		if s, d, ok := SplitOccurrenceID(id); ok {
			t.Errorf("%s: accepted as %q %q", name, s, d)
		}
	}
}
