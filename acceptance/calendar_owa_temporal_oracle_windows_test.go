//go:build acceptance && windows

package acceptance

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

type owaTemporalExpected struct {
	UID                   string
	Start, End, Modified  time.Time
	AllDay                int8
	Flags                 map[string]int8
	Zone                  string
	ZoneDeclared          bool
	ZoneIdentity          *owaNativeZoneIdentity
	StartDate, EndDate    string
	Invalid, UnknownZones int
	Unaligned             int
}

func owaOracleInstant(value any) (time.Time, bool) {
	text, ok := value.(string)
	if !ok || !utf8.ValidString(text) {
		return time.Time{}, false
	}
	instant, err := time.Parse(time.RFC3339Nano, text)
	if err != nil || instant.UTC().Year() < 1 || instant.UTC().Year() > 9999 {
		return time.Time{}, false
	}
	return instant.UTC(), true
}

func (out *owaTemporalExpected) flag(value any) int8 {
	if !owaSurveyPresent(value) {
		return -1
	}
	if flag, ok := value.(bool); ok {
		if flag {
			return 1
		}
		return 0
	}
	out.Invalid++
	return -1
}

func (out *owaTemporalExpected) zone(ctx context.Context, oracle *owaWindowsZoneOracle, value any) (*owaNativeZoneIdentity, error) {
	text, ok := value.(string)
	if !ok || !utf8.ValidString(text) {
		out.Invalid++
		return nil, nil
	}
	identity, err := oracle.identity(ctx, text)
	if err != nil {
		if err.Error() == "calendar_oracle_zone_unknown" || err.Error() == "calendar_oracle_zone_unmapped" {
			out.UnknownZones++
			return nil, nil
		}
		return nil, err
	}
	if identity.Alias {
		return nil, errors.New("calendar_oracle_alias_policy_unproved")
	}
	return &identity, nil
}

func owaProjectTemporal(ctx context.Context, oracle *owaWindowsZoneOracle, value any) (owaTemporalExpected, error) {
	var out owaTemporalExpected
	if err := ctx.Err(); err != nil {
		return out, err
	}
	fields, ok := owaSurveyObject(value)
	if !ok {
		return out, errors.New("calendar_oracle_object_refused")
	}
	uid, ok := fields["UID"].(string)
	if !ok || uid == "" {
		return out, errors.New("calendar_oracle_identity_refused")
	}
	if _, err := hex.DecodeString(uid); err != nil {
		return out, errors.New("calendar_oracle_identity_refused")
	}
	out.UID = strings.ToLower(uid)
	start, validStart := owaOracleInstant(fields["Start"])
	end, validEnd := owaOracleInstant(fields["End"])
	modified, validModified := owaOracleInstant(fields["LastModifiedTime"])
	if !validStart || !validEnd || !validModified || end.Before(start) {
		return out, errors.New("calendar_oracle_time_refused")
	}
	out.Start, out.End, out.Modified = start, end, modified
	out.AllDay = out.flag(fields["IsAllDayEvent"])
	out.Flags = map[string]int8{}
	for _, field := range []string{"IsCancelled", "IsOnlineMeeting", "IsOrganizer", "HasAttachments"} {
		out.Flags[field] = out.flag(fields[field])
	}
	if raw := fields["StartTimeZoneId"]; owaSurveyPresent(raw) {
		if text, ok := raw.(string); ok && utf8.ValidString(text) {
			out.Zone, out.ZoneDeclared = text, true
		}
		identity, err := out.zone(ctx, oracle, raw)
		if err != nil {
			return out, err
		}
		out.ZoneIdentity = identity
	}
	if out.AllDay != 1 {
		return out, nil
	}
	proved := out.ZoneIdentity != nil
	if raw := fields["EndTimeZoneId"]; proved && owaSurveyPresent(raw) {
		identity, err := out.zone(ctx, oracle, raw)
		if err != nil {
			return out, err
		}
		proved = identity != nil && identity.Canonical == out.ZoneIdentity.Canonical
	}
	if proved {
		localStart, err := oracle.local(ctx, out.Zone, start)
		if err != nil {
			return out, err
		}
		localEnd, err := oracle.local(ctx, out.Zone, end)
		if err != nil {
			return out, err
		}
		for _, instant := range []time.Time{localStart, localEnd} {
			if instant.Year() < 1 || instant.Year() > 9999 || instant.Hour() != 0 ||
				instant.Minute() != 0 || instant.Second() != 0 || instant.Nanosecond() != 0 {
				proved = false
			}
		}
		first, last := localStart.Format(time.DateOnly), localEnd.Format(time.DateOnly)
		if proved && last > first {
			out.StartDate, out.EndDate = first, last
			return out, nil
		}
	}
	out.AllDay, out.Unaligned = -1, out.Unaligned+1
	return out, nil
}
