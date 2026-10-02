package calendar

import (
	"testing"
	"time"
)

func TestMergeNewestWins(t *testing.T) {
	older := Event{Source: SourceOutlook, Subject: "old", LastModified: tp(t, "2026-10-01T10:00:00Z")}
	newer := Event{Source: SourceTeams, Subject: "new", LastModified: tp(t, "2026-10-02T10:00:00Z")}
	for _, rows := range [][]Event{{older, newer}, {newer, older}} {
		// Freshness points the other way; a newer LastModified still wins.
		got := Merge(rows, map[Source]time.Time{SourceOutlook: mustTime(t, "2026-10-03T00:00:00Z")})
		if got.Subject != "new" || got.Source != SourceTeams {
			t.Fatalf("got %+v", got)
		}
	}
}

func TestMergeTieUsesCacheFreshness(t *testing.T) {
	lm := tp(t, "2026-10-01T10:00:00Z")
	outlook := Event{Source: SourceOutlook, Subject: "outlook", LastModified: lm}
	teams := Event{Source: SourceTeams, Subject: "teams", LastModified: lm}
	fresh := map[Source]time.Time{SourceTeams: mustTime(t, "2026-10-02T00:00:00Z"), SourceOutlook: mustTime(t, "2026-10-01T00:00:00Z")}
	for _, rows := range [][]Event{{outlook, teams}, {teams, outlook}} {
		if got := Merge(rows, fresh); got.Source != SourceTeams {
			t.Fatalf("tie must follow freshness, got %s", got.Source)
		}
	}
	// A missing LastModified is a tie too.
	noTime := Event{Source: SourceOutlook, Subject: "outlook"}
	if got := Merge([]Event{noTime, teams}, fresh); got.Source != SourceTeams {
		t.Fatalf("nil LastModified must fall to freshness, got %s", got.Source)
	}
}

func TestMergeTieFallsToOutlook(t *testing.T) {
	lm := tp(t, "2026-10-01T10:00:00Z")
	outlook := Event{Source: SourceOutlook, Subject: "outlook", LastModified: lm}
	teams := Event{Source: SourceTeams, Subject: "teams", LastModified: lm}
	same := mustTime(t, "2026-10-02T00:00:00Z")
	for _, fresh := range []map[Source]time.Time{nil, {SourceTeams: same, SourceOutlook: same}} {
		for _, rows := range [][]Event{{outlook, teams}, {teams, outlook}} {
			if got := Merge(rows, fresh); got.Source != SourceOutlook {
				t.Fatalf("tie must fall to outlook, got %s", got.Source)
			}
		}
	}
	// Two rows of one source keep input order.
	a := Event{Source: SourceTeams, Subject: "a"}
	b := Event{Source: SourceTeams, Subject: "b"}
	if got := Merge([]Event{a, b}, nil); got.Subject != "a" {
		t.Fatalf("got %q", got.Subject)
	}
}

func TestMergeFillsEmptyFields(t *testing.T) {
	base := Event{Source: SourceOutlook, SourceID: "o1", Subject: "Sync", LastModified: tp(t, "2026-10-02T10:00:00Z")}
	other := Event{
		Source: SourceTeams, SourceID: "t1", GlobalID: "uid", TimeZone: "UTC", Subject: "ignored", Organizer: "ada",
		AttendeesJSON: `["bob"]`, Location: "Room 1", OnlineMeetingURL: "https://x", TeamsThreadID: "19:meeting_a@thread.v2",
		SeriesKey: "series", Response: "accepted", ShowAs: "busy", BodyPreview: "hello",
		LastModified: tp(t, "2026-10-01T10:00:00Z"), Cancelled: true,
	}
	got := Merge([]Event{other, base}, nil)
	want := base
	want.GlobalID, want.TimeZone, want.Organizer = "uid", "UTC", "ada"
	want.AttendeesJSON, want.Location, want.OnlineMeetingURL = `["bob"]`, "Room 1", "https://x"
	want.TeamsThreadID, want.SeriesKey, want.Response = "19:meeting_a@thread.v2", "series", "accepted"
	want.ShowAs, want.BodyPreview = "busy", "hello"
	if got != want {
		t.Fatalf("got  %+v\nwant %+v", got, want)
	}
	if got.SourceID != "o1" || got.Subject != "Sync" || got.Cancelled {
		t.Fatal("base-owned fields must not be overwritten")
	}
	// Fill order follows precedence: with three rows the second-best fills before the worst.
	third := Event{Source: SourceTeams, Location: "worst", LastModified: tp(t, "2026-09-01T00:00:00Z")}
	if got := Merge([]Event{third, base, other}, nil); got.Location != "Room 1" {
		t.Fatalf("Location = %q", got.Location)
	}
}

func TestMergeCancelledOnlyIfFreshest(t *testing.T) {
	live := Event{Source: SourceOutlook, LastModified: tp(t, "2026-10-02T10:00:00Z")}
	cancelled := Event{Source: SourceTeams, Cancelled: true, LastModified: tp(t, "2026-10-01T10:00:00Z")}
	if got := Merge([]Event{cancelled, live}, nil); got.Cancelled {
		t.Fatal("stale cancellation must not win")
	}
	cancelled.LastModified = tp(t, "2026-10-03T10:00:00Z")
	if got := Merge([]Event{live, cancelled}, nil); !got.Cancelled {
		t.Fatal("freshest cancellation must win")
	}
}

func TestMergeSingleAndEmpty(t *testing.T) {
	e := Event{Source: SourceTeams, Subject: "solo"}
	if got := Merge([]Event{e}, nil); got != e {
		t.Fatalf("got %+v", got)
	}
	if got := Merge(nil, nil); got != (Event{}) {
		t.Fatalf("got %+v", got)
	}
}
