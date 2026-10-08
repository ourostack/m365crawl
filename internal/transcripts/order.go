package transcripts

import (
	"regexp"
	"sort"
	"strings"
)

// nameStamp is the UTC start stamp a recording's file name embeds: 20261103_100200.
var nameStamp = regexp.MustCompile(`(\d{8})[_-](\d{6})`)

func stampOf(name string) string {
	m := nameStamp.FindStringSubmatch(name)
	if m == nil {
		return ""
	}
	return m[1] + m[2]
}

// Assemble turns the parts read from one account's notices into that account's rows. Two notices
// of the same file of a call are one part, and the later notice wins. Each call's parts are
// numbered from 1 by start time; equal start times fall back to the stamp in the file name, then
// to the notice's send time, then to its message id. A call in notices that has no part carrying a
// transcript gets one row of quality RefUnresolved after its parts, so it is listed, not dropped.
// The rows come back by call id, then ordinal.
func Assemble(parts []Part, notices map[string]Notice) []Part {
	type key struct{ call, part string }
	latest := map[key]Part{}
	for _, p := range parts {
		k := key{p.CallID, p.PartKey}
		old, seen := latest[k]
		if !seen || p.SentAt.After(old.SentAt) || (p.SentAt.Equal(old.SentAt) && p.MessageID > old.MessageID) {
			latest[k] = p
		}
	}
	byCall := map[string][]Part{}
	for _, p := range latest {
		byCall[p.CallID] = append(byCall[p.CallID], p)
	}
	for call, n := range notices {
		has := false
		for _, p := range byCall[call] {
			has = has || p.HasTranscript()
		}
		if !has {
			byCall[call] = append(byCall[call], Part{CallID: call, ThreadID: n.ThreadID, MessageID: n.MessageID, SentAt: n.SentAt,
				PartKey: "call:" + call, RefQuality: RefUnresolved, ContentTypes: "Transcript"}) // whether it was recorded is not known
		}
	}
	calls := make([]string, 0, len(byCall))
	for call := range byCall {
		calls = append(calls, call)
	}
	sort.Strings(calls)
	var out []Part
	for _, call := range calls {
		ps := byCall[call]
		sort.Slice(ps, func(i, j int) bool {
			a, b := ps[i], ps[j]
			if (a.RefQuality == RefUnresolved) != (b.RefQuality == RefUnresolved) {
				return b.RefQuality == RefUnresolved
			}
			if !a.StartsAt.Equal(b.StartsAt) {
				return a.StartsAt.Before(b.StartsAt)
			}
			if c := strings.Compare(stampOf(a.OriginalName), stampOf(b.OriginalName)); c != 0 {
				return c < 0
			}
			if !a.SentAt.Equal(b.SentAt) {
				return a.SentAt.Before(b.SentAt)
			}
			return a.MessageID < b.MessageID
		})
		for i := range ps {
			ps[i].Ordinal = i + 1
		}
		out = append(out, ps...)
	}
	return out
}
