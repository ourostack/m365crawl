package calendar

import (
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// compositePrefix starts every key minted without a global id.
const compositePrefix = "composite|"

// Key identifies one occurrence across sources. With a global id it is the id plus the occurrence's
// original start, so moving an occurrence keeps its key. Without one it is a hash of organizer,
// normalized subject and original-or-actual start, so both tools mint the same key from the same
// data.
func Key(e Event) string {
	if e.GlobalID == "" {
		return compositeKey(e)
	}
	return e.GlobalID + "|" + originalSuffix(e)
}

// originalSuffix is the occurrence part of a global key: the original date of an all-day
// occurrence, the original instant of a timed one, empty for a single event.
func originalSuffix(e Event) string {
	if e.OriginalStart == nil {
		return ""
	}
	if e.AllDay.Is(true) {
		// The date as the source stated it, in the zone it came in, so UTC and local midnight agree.
		return e.OriginalStart.Format(dateLayout)
	}
	return e.OriginalStart.UTC().Format(time.RFC3339)
}

// compositeKey is the global-id-free key of e; it is also stored beside every row so a later
// global-id arrival can find the rows to upgrade.
func compositeKey(e Event) string {
	suffix := originalSuffix(e)
	if suffix == "" {
		if e.AllDay.Is(true) {
			suffix = e.StartDate
		} else {
			suffix = e.Start.UTC().Format(time.RFC3339)
		}
	}
	subject := strings.Join(strings.Fields(strings.ToLower(e.Subject)), " ")
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(e.Organizer)) + "\x00" + subject + "\x00" + suffix))
	return compositePrefix + hex.EncodeToString(sum[:])[:24]
}

var threadIDPattern = regexp.MustCompile(`19:meeting_[A-Za-z0-9_-]+@thread\.v2`)

// ParseTeamsThreadID extracts the meeting thread id (19:meeting_...@thread.v2) from a Teams join
// URL, percent-decoding first, or returns "" when there is none.
func ParseTeamsThreadID(joinURL string) string {
	decoded, err := url.PathUnescape(joinURL)
	if err != nil {
		// A stray percent sign elsewhere in the URL; the id may still be present as written.
		decoded = joinURL
	}
	return threadIDPattern.FindString(decoded)
}
