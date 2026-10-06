package teamscal

import (
	"testing"
	"time"
)

func TestEventDay(t *testing.T) {
	la, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, start, typ string
		zone             *time.Location
		day              string
		ok               bool
	}{
		{"date object, UTC", `{"$date":"2026-09-10T23:30:00Z"}`, "Single", time.UTC, "2026-09-10", true},
		{"date object, Pacific", `{"$date":"2026-09-11T03:30:00Z"}`, "Occurrence", la, "2026-09-10", true},
		{"bare string", "2026-09-10T09:00:00Z", "Exception", time.UTC, "2026-09-10", true},
		{"epoch milliseconds", "1788858000000", "", time.UTC, "2026-09-08", true},
		{"master has no day", `{"$date":"2026-09-10T09:00:00Z"}`, "RecurringMaster", time.UTC, "", false},
		{"master, any case", `{"$date":"2026-09-10T09:00:00Z"}`, " recurringmaster ", time.UTC, "", false},
		{"no start", "", "Single", time.UTC, "", false},
		{"unusable start", "soon", "Single", time.UTC, "", false},
		{"trailing data", `{"$date":"2026-09-10T09:00:00Z"} x`, "Single", time.UTC, "", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			day, ok := EventDay(c.start, c.typ, c.zone)
			if day != c.day || ok != c.ok {
				t.Fatalf("EventDay = %q, %v; want %q, %v", day, ok, c.day, c.ok)
			}
		})
	}
}

func TestSyncStamp(t *testing.T) {
	if at, ok := SyncStamp([]byte(`{"key":"lastSuccesfulSyncTimestamp","value":{"$date":"2026-09-01T09:30:00Z"}}`)); !ok || at.Format(time.RFC3339) != "2026-09-01T09:30:00Z" {
		t.Fatalf("stamp %v %v", at, ok)
	}
	for _, bad := range []string{`{"value":"never"}`, `{}`, `[1]`, `nonsense`} {
		if _, ok := SyncStamp([]byte(bad)); ok {
			t.Errorf("%s gave a stamp", bad)
		}
	}
}
