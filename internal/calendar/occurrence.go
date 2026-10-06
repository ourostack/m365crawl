package calendar

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// The occurrence id: the per-occurrence id Teams uses as iCalUID. These ids are Exchange global
// object ids written as hex (Teams writes them in lower case, Outlook's store in upper case; both
// functions answer in upper case, and a caller that compares with Teams ids lower-cases the result,
// as the Outlook mapper does). In that layout (MS-OXOCAL) a 16-byte class id is followed
// by the year (2 bytes, big-endian), the month (1 byte) and the day (1 byte), then creation time,
// reserved bytes, a size and the data. A series id has the four date bytes zero; the
// per-occurrence id is the series id with those bytes replaced by the occurrence's original date.
//
// This encoding is an inference from measured data (assumption A10 in the plan), so it lives only
// here and in occurrence_test.go. If the real cache disagrees, change dateStart, dateEnd and
// encodeDate/decodeDate and the expected strings in the tests.
const (
	// dateStart and dateEnd are the hex character offsets of the date bytes: characters 32 to 39.
	dateStart = 32
	dateEnd   = 40
	// minIDLen is the shortest id that holds the class id, the date and at least some data.
	minIDLen = 40
	noDate   = "00000000"
)

// OccurrenceID returns the per-occurrence id for the series seriesID and the occurrence's
// original start, taking the wall-clock date of originalStart in its own zone. It returns "" when
// seriesID is not hex of at least 40 characters, when its date bytes are not zero (it is already
// an occurrence id), or when the date cannot be encoded, so a bad id never produces a
// plausible-looking key.
func OccurrenceID(seriesID string, originalStart time.Time) string {
	if !isHex(seriesID) || len(seriesID) < minIDLen || seriesID[dateStart:dateEnd] != noDate {
		return ""
	}
	year, month, day := originalStart.Date()
	if originalStart.IsZero() || year < 0 || year > 0xFFFF {
		return ""
	}
	upper := strings.ToUpper(seriesID)
	return upper[:dateStart] + fmt.Sprintf("%04X%02X%02X", year, int(month), day) + upper[dateEnd:]
}

// SplitOccurrenceID is the inverse of OccurrenceID: it returns the series id (date bytes zeroed,
// upper case) and the embedded original date as YYYY-MM-DD. ok is false when id is not hex of at
// least 40 characters or does not embed a valid calendar date.
func SplitOccurrenceID(id string) (seriesID, date string, ok bool) {
	if !isHex(id) || len(id) < minIDLen {
		return "", "", false
	}
	upper := strings.ToUpper(id)
	year, _ := strconv.ParseInt(upper[dateStart:dateStart+4], 16, 32)
	month, _ := strconv.ParseInt(upper[dateStart+4:dateStart+6], 16, 32)
	day, _ := strconv.ParseInt(upper[dateStart+6:dateEnd], 16, 32)
	if year < 1 || month < 1 || day < 1 {
		return "", "", false
	}
	t := time.Date(int(year), time.Month(month), int(day), 0, 0, 0, 0, time.UTC)
	if t.Year() != int(year) || int(t.Month()) != int(month) || t.Day() != int(day) {
		return "", "", false
	}
	return upper[:dateStart] + noDate + upper[dateEnd:], t.Format(dateLayout), true
}

// isHex reports whether s is non-empty and made only of hex digits.
func isHex(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		isDigit, isLower, isUpper := r >= '0' && r <= '9', r >= 'a' && r <= 'f', r >= 'A' && r <= 'F'
		if !isDigit && !isLower && !isUpper {
			return false
		}
	}
	return true
}
