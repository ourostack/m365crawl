package teamscal

import (
	"time"
	// Embed the zone database so resolution does not depend on the host having one (Windows hosts
	// often do not).
	_ "time/tzdata"
)

// seedZones maps the nine short zone names observed in a real Teams cache to IANA ids. Each short
// name is read as the Windows zone id it abbreviates ("St" for "Standard Time"); that reading is
// an inference (assumption A6), checked by the real-cache acceptance, which compares the offset
// at each event's time. Extending the table is a data-only change.
var seedZones = map[string]string{
	"PacificSt":       "America/Los_Angeles", // Pacific Standard Time
	"CentralEuropeSt": "Europe/Budapest",     // Central Europe Standard Time
	"ChinaSt":         "Asia/Shanghai",       // China Standard Time
	"GmtSt":           "Europe/London",       // GMT Standard Time
	"UsMountainSt":    "America/Phoenix",     // US Mountain Standard Time
	"EasternSt":       "America/New_York",    // Eastern Standard Time
	"GTBSt":           "Europe/Bucharest",    // GTB Standard Time
	"Utc":             "UTC",                 // UTC
	"IndianSt":        "Asia/Kolkata",        // India Standard Time
}

// TimeZoneIANA resolves a Teams zone name in three steps: an exact entry in the seed table, a name
// that already loads as an IANA id, else unknown (ok false, empty id).
func TimeZoneIANA(name string) (iana string, ok bool) {
	iana, _, ok = resolveZone(name)
	return iana, ok
}

func resolveZone(name string) (string, *time.Location, bool) {
	if target, found := seedZones[name]; found {
		name = target
	}
	// "" and "Local" would load as UTC and the host zone, which are not what the name says.
	if name == "" || name == "Local" {
		return "", nil, false
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return "", nil, false
	}
	return name, loc, true
}
