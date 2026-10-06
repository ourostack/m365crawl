package acceptance

import (
	"testing"
	"time"
)

func TestBucketOfEdges(t *testing.T) {
	for _, c := range []struct {
		d    time.Duration
		want string
	}{
		{0, "<1s"},
		{999 * time.Millisecond, "<1s"},
		{time.Second, "1-10s"},
		{9 * time.Second, "1-10s"},
		{10 * time.Second, "10-60s"},
		{time.Minute, "1-5m"},
		{5*time.Minute - time.Nanosecond, "1-5m"},
		{5 * time.Minute, ">5m"},
	} {
		if got := bucketOf(c.d, recapDiffEdges, recapDiffOver); got != c.want {
			t.Errorf("%v: %s, want %s", c.d, got, c.want)
		}
	}
	if got := bucketOf(-time.Second, recordingLagEdges, recordingLagOver); got != "during" {
		t.Errorf("negative lag: %s", got)
	}
	if got := bucketOf(3*time.Hour, recordingLagEdges, recordingLagOver); got != ">2h" {
		t.Errorf("long lag: %s", got)
	}
	if got := bucketOf(6*time.Second, lastModifiedEdges, lastModifiedOver); got != "5-60s" {
		t.Errorf("last-modified: %s", got)
	}
	if got := bucketOf(-time.Hour, ageEdges, ageOver); got != "future" {
		t.Errorf("age: %s", got)
	}
}

func TestHistogramPrintsEveryBucketInOrder(t *testing.T) {
	h := newHistogram(lastModifiedEdges, lastModifiedOver)
	h.add("<1s")
	h.add("<1s")
	h.add(">60s")
	if got, want := h.String(), "<1s=2 1-5s=0 5-60s=0 >60s=1"; got != want {
		t.Errorf("%q, want %q", got, want)
	}
	if h.total() != 3 {
		t.Errorf("total %d", h.total())
	}
}

func TestAbsDuration(t *testing.T) {
	if absDuration(-time.Second) != time.Second || absDuration(time.Second) != time.Second {
		t.Error("absDuration")
	}
}

func TestSafeLabel(t *testing.T) {
	for in, want := range map[string]string{
		"PacificSt":     "PacificSt",
		"Europe/London": "Europe/London",
		"syncState":     "syncState",
		"":              "<other>",
		"has space":     "<other>",
		"a@b.example":   "<other>",
		"ünï":           "<other>",
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa": "<other>",
	} {
		if got := safeLabel(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

func TestWithinTolerance(t *testing.T) {
	if !within(118800, 118767, 0.001) || within(119000, 118767, 0.001) {
		t.Error("tolerance of 0.1% around 118767")
	}
	if !within(0, 0, 0.5) || within(1, 0, 0.5) {
		t.Error("a zero want accepts only zero")
	}
	if !within(99, 100, 0.01) || within(98, 100, 0.01) {
		t.Error("lower edge")
	}
}

func TestRatio(t *testing.T) {
	if ratio(1, 4) != 0.25 || ratio(1, 0) != 0 {
		t.Error("ratio")
	}
}

func TestCountsLineSortsAndSanitizes(t *testing.T) {
	if got := countsLine(map[string]int{"b": 2, "a": 1, "x y": 3}); got != "a=1 b=2 <other>=3" {
		t.Errorf("%q", got)
	}
	if countsLine(nil) != "none" {
		t.Error("empty map")
	}
}

func TestNewerThanCopy(t *testing.T) {
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	for name, c := range map[string]struct {
		teams, outlook time.Time
		want           bool
	}{
		"later":         {at.Add(time.Second), at, true},
		"equal":         {at, at, false},
		"earlier":       {at.Add(-time.Hour), at, false},
		"unknown teams": {time.Time{}, at, false},
		"unknown copy":  {at, time.Time{}, false},
	} {
		if got := newerThanCopy(c.teams, c.outlook); got != c.want {
			t.Errorf("%s: %v, want %v", name, got, c.want)
		}
	}
}
