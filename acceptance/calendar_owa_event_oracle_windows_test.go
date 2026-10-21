//go:build acceptance && windows

package acceptance

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"net/url"
	"sort"
	"strings"
	"time"
)

type owaEventExpected struct {
	Temporal              owaTemporalExpected
	Optional              owaOptionalExpected
	Body                  owaBodyExpected
	Series, Thread        string
	Known                 map[string]bool
	Unknown               []string
	Invalid, UnknownZones int
	Unaligned, Redacted   int
	PartialAttendees      int
}

func owaOracleMeetingThread(source string) string {
	text, err := url.PathUnescape(source)
	if err != nil {
		text = source
	}
	const prefix = "19:meeting_"
	for offset := 0; offset < len(text); {
		at := strings.Index(text[offset:], prefix)
		if at < 0 {
			return ""
		}
		at += offset
		end := at + len(prefix)
		for end < len(text) {
			char := text[end]
			if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '-' || char == '_' {
				end++
				continue
			}
			break
		}
		if end > at+len(prefix) && strings.HasPrefix(text[end:], "@thread.v2") {
			return text[at : end+len("@thread.v2")]
		}
		offset = at + len(prefix)
	}
	return ""
}

func owaOracleSeries(uid string) string {
	data, err := hex.DecodeString(uid)
	if err != nil || len(data) < 20 {
		return uid
	}
	year := int(binary.BigEndian.Uint16(data[16:18]))
	month, day := int(data[18]), int(data[19])
	if year < 1 || month < 1 || day < 1 {
		return uid
	}
	date := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
	if date.Year() != year || int(date.Month()) != month || date.Day() != day {
		return uid
	}
	for index := 16; index < 20; index++ {
		data[index] = 0
	}
	return hex.EncodeToString(data)
}

func owaProjectEvent(ctx context.Context, oracle *owaWindowsZoneOracle, value any) (owaEventExpected, error) {
	var out owaEventExpected
	temporal, err := owaProjectTemporal(ctx, oracle, value)
	if err != nil {
		return out, err
	}
	optional, err := owaProjectOptional(ctx, value)
	if err != nil {
		return out, err
	}
	body, err := owaProjectBody(ctx, value)
	if err != nil {
		return out, err
	}
	out.Temporal, out.Optional, out.Body = temporal, optional, body
	out.Series = owaOracleSeries(temporal.UID)
	if join := optional.Fields["join_url"]; join.Known {
		out.Thread = owaOracleMeetingThread(join.Value)
	}
	out.Known = map[string]bool{}
	for _, field := range []string{
		"subject", "location", "organizer", "organizer_address", "response", "show_as", "time_zone", "time_zone_iana",
		"utc_offset", "join_url", "short_join_url", "dial_in", "meeting_chat_id", "attendees", "rooms", "body",
		"body_preview", "attachments", "categories", "recurrence", "reminder",
	} {
		out.Known[field] = false
	}
	for _, field := range []string{"subject", "location", "organizer", "organizer_address", "response", "show_as", "join_url", "dial_in", "body_preview"} {
		out.Known[field] = optional.Fields[field].Known
	}
	out.Known["time_zone"], out.Known["time_zone_iana"] = temporal.ZoneDeclared, temporal.ZoneIdentity != nil
	out.Known["body"], out.Known["attendees"], out.Known["meeting_chat_id"] = body.Known, optional.AttendeesKnown, out.Thread != ""
	for field, known := range out.Known {
		if !known {
			out.Unknown = append(out.Unknown, field)
		}
	}
	sort.Strings(out.Unknown)
	out.Invalid = temporal.Invalid + optional.Invalid + body.Invalid
	out.UnknownZones, out.Unaligned, out.PartialAttendees = temporal.UnknownZones, temporal.Unaligned, optional.PartialAttendees
	out.Redacted = temporal.Redacted + optional.Redacted + body.Redacted
	if err := ctx.Err(); err != nil {
		return out, err
	}
	return out, nil
}
