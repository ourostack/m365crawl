package teamscal

import (
	"time"
	// Embed the zone database so resolution does not depend on the host having one (Windows hosts
	// often do not).
	_ "time/tzdata"
)

// seedZones maps Teams' short zone names to IANA ids. Each short name is read as the Windows zone
// id it abbreviates ("St" for "Standard Time"); the comment on each row names that Windows id.
// Only the first nine rows were observed in a real cache (assumption A6); every row after them
// is derived by the same abbreviation rule and is unverified. The real-cache acceptance compares
// the offset at each event's time, and extending the table is a data-only change.
var seedZones = map[string]string{
	// Observed in a real cache.
	"PacificSt":       "America/Los_Angeles", // Pacific Standard Time
	"CentralEuropeSt": "Europe/Budapest",     // Central Europe Standard Time
	"ChinaSt":         "Asia/Shanghai",       // China Standard Time
	"GmtSt":           "Europe/London",       // GMT Standard Time
	"UsMountainSt":    "America/Phoenix",     // US Mountain Standard Time
	"EasternSt":       "America/New_York",    // Eastern Standard Time
	"GTBSt":           "Europe/Bucharest",    // GTB Standard Time
	"Utc":             "UTC",                 // UTC
	"IndianSt":        "Asia/Kolkata",        // India Standard Time

	// Derived by the same rule, unverified.
	"MountainSt":      "America/Denver",                 // Mountain Standard Time
	"CentralSt":       "America/Chicago",                // Central Standard Time
	"AtlanticSt":      "America/Halifax",                // Atlantic Standard Time
	"AlaskanSt":       "America/Anchorage",              // Alaskan Standard Time
	"HawaiianSt":      "Pacific/Honolulu",               // Hawaiian Standard Time
	"WEuropeSt":       "Europe/Berlin",                  // W. Europe Standard Time
	"RomanceSt":       "Europe/Paris",                   // Romance Standard Time
	"EEuropeSt":       "Europe/Chisinau",                // E. Europe Standard Time
	"FLESt":           "Europe/Kyiv",                    // FLE Standard Time
	"RussianSt":       "Europe/Moscow",                  // Russian Standard Time
	"IsraelSt":        "Asia/Jerusalem",                 // Israel Standard Time
	"ArabianSt":       "Asia/Dubai",                     // Arabian Standard Time
	"SingaporeSt":     "Asia/Singapore",                 // Singapore Standard Time
	"TokyoSt":         "Asia/Tokyo",                     // Tokyo Standard Time
	"KoreaSt":         "Asia/Seoul",                     // Korea Standard Time
	"AUSEasternSt":    "Australia/Sydney",               // AUS Eastern Standard Time
	"NewZealandSt":    "Pacific/Auckland",               // New Zealand Standard Time
	"ESouthAmericaSt": "America/Sao_Paulo",              // E. South America Standard Time
	"ArgentinaSt":     "America/Argentina/Buenos_Aires", // Argentina Standard Time
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
	// Step 2 must behave the same on every host, and Go's loader also accepts legacy names
	// ("EST5EDT", "Factory") and, on a case-insensitive file system, lower-cased ids. Only a
	// canonical "Area/Location" id, or UTC, counts.
	if name != "UTC" && !canonicalIANA(name) {
		return "", nil, false
	}
	// Every listed id loads: the list comes from the zone data embedded above, and a test checks it.
	loc, _ := time.LoadLocation(name)
	return name, loc, true
}

// canonicalIANA reports whether name is an "Area/Location" id in canonicalZones.
func canonicalIANA(name string) bool { return canonicalZones[name] }
