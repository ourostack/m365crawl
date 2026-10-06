package outlookcal

import (
	"time"

	"github.com/ourostack/teamscrawl/internal/teamscal"
)

// windowsZones maps the Windows zone-name strings the event object holds (+780) to IANA
// ids. The Teams mapper's seed table names the same Windows zones in its comments; this is
// that table keyed by the full name. The first nine rows are the zones observed in a Teams
// cache; the rest follow from the Windows names and are unverified. Each target is checked
// by teamscal.TimeZoneIANA, so a row whose id is not a canonical IANA id resolves to
// nothing.
var windowsZones = map[string]string{
	"Pacific Standard Time":        "America/Los_Angeles",
	"Central Europe Standard Time": "Europe/Budapest",
	"China Standard Time":          "Asia/Shanghai",
	"GMT Standard Time":            "Europe/London",
	"US Mountain Standard Time":    "America/Phoenix",
	"Eastern Standard Time":        "America/New_York",
	"GTB Standard Time":            "Europe/Bucharest",
	"UTC":                          "UTC",
	"India Standard Time":          "Asia/Kolkata",

	"Mountain Standard Time":         "America/Denver",
	"Central Standard Time":          "America/Chicago",
	"Atlantic Standard Time":         "America/Halifax",
	"Alaskan Standard Time":          "America/Anchorage",
	"Hawaiian Standard Time":         "Pacific/Honolulu",
	"W. Europe Standard Time":        "Europe/Berlin",
	"Romance Standard Time":          "Europe/Paris",
	"E. Europe Standard Time":        "Europe/Chisinau",
	"FLE Standard Time":              "Europe/Kyiv",
	"Russian Standard Time":          "Europe/Moscow",
	"Israel Standard Time":           "Asia/Jerusalem",
	"Arabian Standard Time":          "Asia/Dubai",
	"Singapore Standard Time":        "Asia/Singapore",
	"Tokyo Standard Time":            "Asia/Tokyo",
	"Korea Standard Time":            "Asia/Seoul",
	"AUS Eastern Standard Time":      "Australia/Sydney",
	"New Zealand Standard Time":      "Pacific/Auckland",
	"E. South America Standard Time": "America/Sao_Paulo",
	"Argentina Standard Time":        "America/Argentina/Buenos_Aires",
}

// ZoneIANA resolves a Windows zone name through the table above, then through the Teams
// mapper's resolver (which accepts a Teams short name or a canonical IANA id). ok is
// false, with an empty id, when the name resolves to nothing.
func ZoneIANA(name string) (iana string, loc *time.Location, ok bool) {
	if target, found := windowsZones[name]; found {
		name = target
	}
	iana, ok = teamscal.TimeZoneIANA(name)
	if !ok {
		return "", nil, false
	}
	loc, _ = time.LoadLocation(iana) // every id the resolver accepts loads (teamscal embeds the zone data)
	return iana, loc, true
}
