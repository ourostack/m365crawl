package teamscal

import (
	"testing"
	"time"
)

func utcTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestAllDayDates(t *testing.T) {
	pacific, _ := time.LoadLocation("America/Los_Angeles")
	tokyo, _ := time.LoadLocation("Asia/Tokyo")
	cases := []struct {
		name       string
		start, end string
		zone       *time.Location
		fallback   *time.Location
		wantStart  string
		wantEnd    string
		wantOK     bool
	}{
		{"utc midnight, event zone known but not aligned", "2026-03-10T00:00:00Z", "2026-03-11T00:00:00Z", pacific, nil, "2026-03-10", "2026-03-11", true},
		{"pacific midnight is 08:00Z in winter", "2026-01-10T08:00:00Z", "2026-01-11T08:00:00Z", pacific, nil, "2026-01-10", "2026-01-11", true},
		{"three days", "2026-03-10T00:00:00Z", "2026-03-13T00:00:00Z", nil, nil, "2026-03-10", "2026-03-13", true},
		{"three days over spring forward", "2026-03-07T08:00:00Z", "2026-03-10T07:00:00Z", pacific, nil, "2026-03-07", "2026-03-10", true},
		{"unknown zone with utc midnight", "2026-03-10T00:00:00Z", "2026-03-11T00:00:00Z", nil, nil, "2026-03-10", "2026-03-11", true},
		{"end missing counts one day", "2026-03-10T00:00:00Z", "0001-01-01T00:00:00Z", nil, nil, "2026-03-10", "2026-03-11", true},
		{"fallback zone midnight", "2026-03-09T15:00:00Z", "2026-03-10T15:00:00Z", nil, tokyo, "2026-03-10", "2026-03-11", true},
		{"unaligned", "2026-03-10T10:30:00Z", "2026-03-10T11:30:00Z", pacific, tokyo, "", "", false},
		{"known zone ignores the fallback", "2026-03-09T15:00:00Z", "2026-03-10T15:00:00Z", pacific, tokyo, "", "", false},
		{"one day across the autumn clock change", "2026-11-01T07:00:00Z", "2026-11-02T08:00:00Z", pacific, nil, "2026-11-01", "2026-11-02", true},
		{"three days across the autumn clock change", "2026-10-31T07:00:00Z", "2026-11-03T08:00:00Z", pacific, nil, "2026-10-31", "2026-11-03", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, e, ok := allDayDates(utcTime(c.start), utcTime(c.end), c.zone, c.fallback)
			if s != c.wantStart || e != c.wantEnd || ok != c.wantOK {
				t.Fatalf("allDayDates = %q, %q, %v; want %q, %q, %v", s, e, ok, c.wantStart, c.wantEnd, c.wantOK)
			}
		})
	}
}

func TestMapEventAllDayKeyAbsentIsUnknownAndTimed(t *testing.T) {
	e, notes, err := MapEventRecord(testAcct, "k", []byte(`{"iCalUID":"U","startTime":{"$date":"2026-03-10T00:00:00.000Z"},"endTime":{"$date":"2026-03-11T00:00:00.000Z"}}`), time.UTC)
	if err != nil || e.AllDay.Known() || e.StartDate != "" || notes.AllDayUnaligned || e.Start.IsZero() {
		t.Fatalf("err %v %+v %+v", err, e, notes)
	}
}
