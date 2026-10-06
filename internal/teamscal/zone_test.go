package teamscal

import (
	"testing"
	"time"
)

func TestTimeZoneSeedTargetsLoad(t *testing.T) {
	for name, target := range seedZones {
		if _, err := time.LoadLocation(target); err != nil {
			t.Errorf("%s -> %s does not load: %v", name, target, err)
		}
		iana, ok := TimeZoneIANA(name)
		if !ok || iana != target {
			t.Errorf("TimeZoneIANA(%q) = %q, %v; want %q", name, iana, ok, target)
		}
	}
}

func TestTimeZoneSeedTargetsAreCanonical(t *testing.T) {
	for name, target := range seedZones {
		if target != "UTC" && !canonicalIANA(target) {
			t.Errorf("%s -> %s is not a canonical id", name, target)
		}
	}
}

func TestTimeZoneSeedValues(t *testing.T) {
	want := map[string]string{
		"PacificSt": "America/Los_Angeles", "CentralEuropeSt": "Europe/Budapest", "ChinaSt": "Asia/Shanghai",
		"GmtSt": "Europe/London", "UsMountainSt": "America/Phoenix", "EasternSt": "America/New_York",
		"GTBSt": "Europe/Bucharest", "Utc": "UTC", "IndianSt": "Asia/Kolkata",
	}
	if len(seedZones) < len(want) {
		t.Fatalf("seed table lost rows: %d", len(seedZones))
	}
	for name, target := range want {
		if seedZones[name] != target {
			t.Errorf("seed %s = %q, want %q", name, seedZones[name], target)
		}
	}
}

func TestTimeZoneDirectIANA(t *testing.T) {
	if iana, ok := TimeZoneIANA("Europe/Paris"); !ok || iana != "Europe/Paris" {
		t.Fatalf("TimeZoneIANA(Europe/Paris) = %q, %v", iana, ok)
	}
}

func TestTimeZoneUTCAndDirectIDs(t *testing.T) {
	for _, id := range []string{"UTC", "Europe/Paris", "America/Argentina/Buenos_Aires", "Etc/GMT+5"} {
		if got, ok := TimeZoneIANA(id); !ok || got != id {
			t.Errorf("TimeZoneIANA(%q) = %q, %v", id, got, ok)
		}
	}
}

func TestTimeZoneUnknown(t *testing.T) {
	for _, name := range []string{"FixtureUnknownSt", "", "Local", "  ", "Factory", "EST5EDT", "europe/paris", "America/new_york", "UTC/", "Europe/Nowhere", "utc"} {
		if iana, ok := TimeZoneIANA(name); ok || iana != "" {
			t.Errorf("TimeZoneIANA(%q) = %q, %v; want unknown", name, iana, ok)
		}
	}
}

func TestCanonicalZoneListLoads(t *testing.T) {
	if len(canonicalZones) < 300 {
		t.Fatalf("zone list has %d entries", len(canonicalZones))
	}
	for name := range canonicalZones {
		if _, err := time.LoadLocation(name); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	for _, bad := range []string{"america/new_york", "AMERICA/NEW_YORK", "Europe/Nowhere"} {
		if _, ok := TimeZoneIANA(bad); ok {
			t.Errorf("%s accepted", bad)
		}
	}
}
