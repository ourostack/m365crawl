// Pure helpers of the calendar and Outlook acceptance checks: histograms, label hygiene and
// tolerance arithmetic. They need neither the real cache nor the acceptance build tag, so CI
// exercises them (calendar_hist_test.go).
package acceptance

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// edge is the upper bound (exclusive) of a histogram bucket and the label it prints under.
type edge struct {
	below time.Duration
	label string
}

// Bucket edges of the histograms the plans ask for. A bucket label is a duration range, never data.
var (
	// recapDiffEdges buckets the start and end differences of a recap and its event (Teams plan:
	// under 1 s, 1 to 10 s, 10 to 60 s, 1 to 5 min, over 5 min).
	recapDiffEdges = []edge{{time.Second, "<1s"}, {10 * time.Second, "1-10s"}, {time.Minute, "10-60s"}, {5 * time.Minute, "1-5m"}}
	recapDiffOver  = ">5m"
	// lastModifiedEdges buckets the last-modified difference of a Teams and an Outlook copy of one
	// event (Outlook plan: under 1 s, 1 to 5 s, 5 to 60 s, over 60 s).
	lastModifiedEdges = []edge{{time.Second, "<1s"}, {5 * time.Second, "1-5s"}, {time.Minute, "5-60s"}}
	lastModifiedOver  = ">60s"
	// recordingLagEdges buckets how long after an occurrence ended its recording or transcript
	// message was sent. A negative lag is a message sent while the meeting was running.
	recordingLagEdges = []edge{{0, "during"}, {time.Minute, "0-1m"}, {5 * time.Minute, "1-5m"}, {30 * time.Minute, "5-30m"}, {2 * time.Hour, "30m-2h"}}
	recordingLagOver  = ">2h"
	// ageEdges buckets how far in the past a covered day lies.
	ageEdges = []edge{{0, "future"}, {30 * 24 * time.Hour, "0-30d"}, {90 * 24 * time.Hour, "30-90d"}, {365 * 24 * time.Hour, "90-365d"}}
	ageOver  = ">365d"
)

// bucketOf returns the label of the first edge d is strictly below, or over.
func bucketOf(d time.Duration, edges []edge, over string) string {
	for _, e := range edges {
		if d < e.below {
			return e.label
		}
	}
	return over
}

// histogram counts values by label and prints the labels in the order they were declared.
type histogram struct {
	order  []string
	counts map[string]int
}

func newHistogram(edges []edge, over string) *histogram {
	h := &histogram{counts: map[string]int{}}
	for _, e := range edges {
		h.order = append(h.order, e.label)
	}
	h.order = append(h.order, over)
	return h
}

func (h *histogram) add(label string) { h.counts[label]++ }

// total is the number of values counted.
func (h *histogram) total() int {
	n := 0
	for _, c := range h.counts {
		n += c
	}
	return n
}

// String prints "label=count" for every bucket, empty buckets included, so two runs line up.
func (h *histogram) String() string {
	parts := make([]string, 0, len(h.order))
	for _, l := range h.order {
		parts = append(parts, fmt.Sprintf("%s=%d", l, h.counts[l]))
	}
	return strings.Join(parts, " ")
}

// absDuration is |d|.
func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

// safeLabel returns s when it is a short name made of letters, digits and . _ - / : (a zone label
// or a field name) and "<other>" otherwise, so nothing that looks like content reaches a log line.
func safeLabel(s string) string {
	if s == "" || len(s) > 40 {
		return "<other>"
	}
	for _, r := range s {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._-/:", r)
		if !ok {
			return "<other>"
		}
	}
	return s
}

// within reports whether got is within tol (a fraction, 0.001 for 0.1%) of want. A want of zero
// accepts only zero.
func within(got, want int, tol float64) bool {
	if want == 0 {
		return got == 0
	}
	diff := float64(got - want)
	if diff < 0 {
		diff = -diff
	}
	return diff <= tol*float64(want)
}

// ratio is part over whole, zero for an empty whole.
func ratio(part, whole int) float64 {
	if whole == 0 {
		return 0
	}
	return float64(part) / float64(whole)
}

// countsLine prints a name-to-count map with sorted keys, every key made safe.
func countsLine(m map[string]int) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", safeLabel(k), m[k]))
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, " ")
}

// newerThanCopy says whether a Teams event was last modified after the newest last-modified of the
// whole Outlook copy. Such an event cannot be in that copy, whatever the mapper does: the two
// copies were taken at different times (snapshot skew). A zero time on either side is unknown, so
// the event is not excluded.
func newerThanCopy(teams, newestOutlook time.Time) bool {
	return !teams.IsZero() && !newestOutlook.IsZero() && teams.After(newestOutlook)
}
