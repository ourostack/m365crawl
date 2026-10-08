package transcripts

import (
	"strconv"
	"strings"
)

// RawEntry is one entry as the in-page script returns it: the speaker, the start and end offsets
// as SharePoint writes them (hh:mm:ss with up to seven fraction digits), and the text.
type RawEntry struct {
	S string `json:"s"`
	B string `json:"b"`
	E string `json:"e"`
	T string `json:"t"`
}

// DecodeEntries turns the script's entries into Entries. An offset that does not parse becomes nil
// and is counted in the second return.
func DecodeEntries(raw []RawEntry) ([]Entry, int) {
	out := make([]Entry, 0, len(raw))
	bad := 0
	offset := func(s string) *int64 {
		ms, ok := parseOffset(s)
		if !ok {
			bad++
			return nil
		}
		return &ms
	}
	for _, r := range raw {
		out = append(out, Entry{Speaker: r.S, StartMS: offset(r.B), EndMS: offset(r.E), Text: r.T})
	}
	return out, bad
}

// parseOffset reads hh:mm:ss(.fffffff) as milliseconds. Minutes and seconds are below 60; the
// fraction has one to seven digits and is cut, not rounded, to milliseconds.
func parseOffset(s string) (int64, bool) {
	clock, frac, hasFrac := strings.Cut(s, ".")
	f := strings.Split(clock, ":")
	if len(f) != 3 || (hasFrac && (len(frac) == 0 || len(frac) > 7)) {
		return 0, false
	}
	var n [3]int64
	for i, p := range f {
		v, ok := digits(p)
		if !ok {
			return 0, false
		}
		n[i] = v
	}
	if n[1] > 59 || n[2] > 59 {
		return 0, false
	}
	ms := ((n[0]*60+n[1])*60 + n[2]) * 1000
	if hasFrac {
		if _, ok := digits(frac); !ok {
			return 0, false
		}
		v, _ := strconv.ParseInt((frac + "00")[:3], 10, 64) // three digits always parse
		ms += v
	}
	return ms, true
}

// digits parses a non-empty run of ASCII digits.
func digits(s string) (int64, bool) {
	if s == "" || strings.TrimLeft(s, "0123456789") != "" {
		return 0, false
	}
	v, err := strconv.ParseInt(s, 10, 64)
	return v, err == nil
}
