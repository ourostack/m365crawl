package teamscal

import (
	"testing"
	"time"
)

func TestTimeZoneSeedTargetsLoad(t *testing.T) {
	if len(seedZones) != 9 {
		t.Fatalf("seed table has %d rows, want the 9 observed names", len(seedZones))
	}
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

func TestTimeZoneSeedValues(t *testing.T) {
	want := map[string]string{
		"PacificSt": "America/Los_Angeles", "CentralEuropeSt": "Europe/Budapest", "ChinaSt": "Asia/Shanghai",
		"GmtSt": "Europe/London", "UsMountainSt": "America/Phoenix", "EasternSt": "America/New_York",
		"GTBSt": "Europe/Bucharest", "Utc": "UTC", "IndianSt": "Asia/Kolkata",
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

func TestTimeZoneUnknown(t *testing.T) {
	for _, name := range []string{"FixtureUnknownSt", "", "Local", "  "} {
		if iana, ok := TimeZoneIANA(name); ok || iana != "" {
			t.Errorf("TimeZoneIANA(%q) = %q, %v; want unknown", name, iana, ok)
		}
	}
}
