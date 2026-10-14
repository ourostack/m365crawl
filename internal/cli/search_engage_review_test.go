package cli

import (
	"reflect"
	"testing"
	"time"
)

// Compare through the actual stable sorter: moving a ahead of b means strict less.
func engageReviewLess(a, b searchHit) bool {
	if a.item == b.item {
		return false
	}
	pair := []searchHit{b, a}
	sortSearchHits(pair)
	return pair[0].item == a.item
}

func TestEngageReviewMixedClockLaws(t *testing.T) {
	parse := func(raw string) time.Time {
		t.Helper()
		at, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			t.Fatal(err)
		}
		return at
	}
	early := parse("0000-01-01T00:30:00+01:00")
	late := parse("9999-12-31T23:30:00-01:00")
	normal := parse("2026-01-01T00:00:00Z")
	hits := []searchHit{
		{source: searchContent, item: "legacy-unknown"},
		{source: searchContent, at: early, localText: true, item: "legacy-early"},
		{source: searchContent, at: early, engage: true, engageKnown: true, item: "engage-early"},
		{source: searchContent, engage: true, engageKnown: true, item: "engage-zero"},
		{source: searchContent, engage: true, item: "engage-unknown"},
		{source: searchContent, at: late, item: "legacy-late"},
		{source: searchContent, at: late, engage: true, engageKnown: true, item: "engage-late"},
		{source: searchChats, at: normal, item: "chat"},
		{source: searchMail, at: normal, item: "mail"},
		{source: searchContent, at: normal, item: "cloud"},
		{source: searchContent, at: normal, localText: true, item: "local"},
	}
	for _, a := range hits {
		for _, b := range hits {
			if engageReviewLess(a, b) && engageReviewLess(b, a) {
				t.Errorf("asymmetric pair: %v, %v", a.item, b.item)
			}
			for _, c := range hits {
				if engageReviewLess(a, b) && engageReviewLess(b, c) && !engageReviewLess(a, c) {
					t.Errorf("non-transitive triple: %v < %v < %v", a.item, b.item, c.item)
				}
				abEquivalent := !engageReviewLess(a, b) && !engageReviewLess(b, a)
				bcEquivalent := !engageReviewLess(b, c) && !engageReviewLess(c, b)
				if abEquivalent && bcEquivalent && (engageReviewLess(a, c) || engageReviewLess(c, a)) {
					t.Errorf("non-transitive equivalence: %v ~ %v ~ %v", a.item, b.item, c.item)
				}
			}
		}
	}
	for _, permutation := range [][3]int{{0, 1, 2}, {0, 2, 1}, {1, 0, 2}, {1, 2, 0}, {2, 0, 1}, {2, 1, 0}} {
		actual := []searchHit{hits[permutation[0]], hits[permutation[1]], hits[permutation[2]]}
		sortSearchHits(actual)
		got := []any{actual[0].item, actual[1].item, actual[2].item}
		if !reflect.DeepEqual(got, []any{"legacy-early", "engage-early", "legacy-unknown"}) {
			t.Errorf("permutation %v: got %v", permutation, got)
		}
	}
	sortSearchHits(hits)
	var got []any
	for _, h := range hits {
		got = append(got, h.item)
	}
	want := []any{"legacy-late", "engage-late", "chat", "mail", "cloud", "local", "engage-zero", "legacy-early", "engage-early", "legacy-unknown", "engage-unknown"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("range/knownness/normal legacy ties: got %v want %v", got, want)
	}
}

func TestEngageReviewActualMixedMergedLimit(t *testing.T) {
	for _, raw := range []string{"0000-01-01T00:30:00+01:00", "0001-01-01T00:00:00Z", "9999-12-31T23:30:00-01:00", "2026-01-01T00:00:00Z"} {
		t.Run(raw, func(t *testing.T) {
			e := cloudCLIEnv(t)
			engageReviewCloud(t, e, "")
			if raw != "0001-01-01T00:00:00Z" {
				engageReviewCloud(t, e, raw)
			}

			engageReviewFetch(t, e, raw)
			want := []any{"cloud-observation:2", "engage-observation:1", "cloud-observation:1"}
			if raw == "0001-01-01T00:00:00Z" {
				// Legacy lacks independent clock presence; Engage retains it explicitly.
				want = []any{"engage-observation:1", "cloud-observation:1"}
			}
			for _, limit := range []string{"1", "3"} {
				for _, args := range [][]string{{"content", "list"}, {"content", "search", "syntheticword"}, {"search", "syntheticword", "--source", "content"}} {
					code, out, stderr := e.run(append(args, "--limit", limit)...)
					if code != 0 {
						t.Fatal(stderr)
					}
					var got []any
					for _, item := range items(t, decode(t, out)) {
						got = append(got, item["id"])
					}
					expected := want
					if limit == "1" {
						expected = want[:1]
					}
					if !reflect.DeepEqual(got, expected) {
						t.Errorf("actual %v limit%s: got %v want %v", args, limit, got, expected)
					}
				}
			}
		})
	}
}

// Per-family limits must not hide the third operand of the original cycle.
func TestEngageReviewActualCrossFamilyLimitedClock(t *testing.T) {
	e := cloudCLIEnv(t)
	engageReviewLocal(t, e, true)
	engageReviewCloud(t, e, "0000-01-01T00:30:00+01:00")
	engageReviewFetch(t, e, "0000-01-01T00:30:00+01:00")
	for _, limit := range []string{"1", "3"} {
		for _, args := range [][]string{{"content", "list"}, {"content", "search", "syntheticword"}, {"search", "syntheticword", "--source", "content"}} {
			code, out, stderr := e.run(append(args, "--limit", limit)...)
			if code != 0 {
				t.Fatal(stderr)
			}
			var got []any
			for _, item := range items(t, decode(t, out)) {
				got = append(got, item["id"])
			}
			want := []any{"cloud-observation:1", "engage-observation:1", "local-text-observation:1"}
			if limit == "1" {
				want = want[:1]
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("cross-family actual %v limit%s: got %v want %v", args, limit, got, want)
			}
		}
	}
}
