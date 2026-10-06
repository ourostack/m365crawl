package teamscal

import "testing"

func TestClaimedStoresExcludeDecoy(t *testing.T) {
	want := map[[2]string]Kind{
		{"calendar", "calendar"}:                                KindEvent,
		{"calendar", "calendar-internal-data"}:                  KindCalendarInternal,
		{"meetforwork-manager", "meetforwork-meeting-catch-up"}: KindCatchUp,
		{"meeting-recap-manager", "meeting-recap-catchup"}:      KindRecap,
	}
	got := ClaimedStores()
	if len(got) != len(want) {
		t.Fatalf("ClaimedStores() has %d entries, want %d: %v", len(got), len(want), got)
	}
	for _, c := range got {
		if want[[2]string{c.Manager, c.Store}] != c.Kind {
			t.Errorf("unexpected claim %+v", c)
		}
		if k, ok := Claimed(c.Manager, c.Store); !ok || k != c.Kind {
			t.Errorf("Claimed(%q,%q) = %v, %v", c.Manager, c.Store, k, ok)
		}
	}
	// The generic-records decoy that the fixture carries is not a Teams calendar.
	for _, decoy := range [][2]string{{"calendar-manager", "events"}, {"calendar", "events"}, {"calendar-manager", "calendar"}, {"", ""}} {
		if k, ok := Claimed(decoy[0], decoy[1]); ok {
			t.Errorf("Claimed(%q,%q) = %v, want unclaimed", decoy[0], decoy[1], k)
		}
	}
}

func TestClaimedStoresReturnsACopy(t *testing.T) {
	a := ClaimedStores()
	a[0].Store = "changed"
	if ClaimedStores()[0].Store == "changed" {
		t.Fatal("ClaimedStores exposes its backing array")
	}
}

func TestMapperVersionIsPositive(t *testing.T) {
	if MapperVersion < 1 {
		t.Fatalf("MapperVersion = %d", MapperVersion)
	}
}

func TestRecapTimeToleranceDefault(t *testing.T) {
	if RecapTimeTolerance.Seconds() != 60 {
		t.Fatalf("RecapTimeTolerance = %v, want 60s until the acceptance run measures it", RecapTimeTolerance)
	}
}
