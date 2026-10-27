package store

import (
	"testing"
	"time"
)

func TestWorkIQEffectiveMarks(t *testing.T) {
	miss := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	gone := miss.Add(24 * time.Hour)
	for _, tc := range []struct {
		name        string
		serverAt    time.Time
		cacheSeen   bool
		cacheMark   bool
		firstMiss   bool
		serverGone  bool
		wantMark    bool
		wantUnknown bool
	}{
		{"older server", miss.Add(-time.Hour), true, true, true, false, true, false},
		{"equal server", miss, true, true, true, false, true, false},
		{"strictly later server", miss.Add(time.Hour), true, true, true, false, false, false},
		{"mixed without cache mark", miss, true, false, false, true, false, false},
		{"server only gone", miss, false, false, false, true, true, false},
		{"legacy cannot prove later", miss.Add(time.Hour), true, true, false, false, true, true},
		{"legacy later than confirmed mark", gone.Add(time.Hour), true, true, false, false, false, false},
		{"later inferred server loss", miss.Add(time.Hour), true, true, true, true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := WorkIQReduceInput{Cache: WorkIQSourceState{Seen: tc.cacheSeen}, Server: WorkIQSourceState{Seen: true, SeenAt: &tc.serverAt}}
			if tc.cacheMark {
				in.Cache.GoneAt, in.Cache.EvictedAt = &gone, &gone
			}
			if tc.firstMiss {
				in.Cache.FirstMissAt = &miss
			}
			if tc.serverGone {
				in.Server.GoneAt = &gone
			}
			got := reduceWorkIQState(in)
			if (got.GoneAt != nil) != tc.wantMark || got.UnknownFirstMiss != tc.wantUnknown {
				t.Fatal("effective source absence did not follow the strict first-miss rule")
			}
			if tc.cacheSeen && (got.EvictedAt != nil) != (tc.cacheMark && tc.wantMark) {
				t.Fatal("cache eviction did not follow the same later-sighting rule")
			}
			if tc.cacheMark && (in.Cache.GoneAt == nil || !in.Cache.GoneAt.Equal(gone)) {
				t.Fatal("reduction erased retained source absence evidence")
			}
			wantProvenance := "both"
			if !tc.cacheSeen {
				wantProvenance = "workiq_server"
			}
			if got.Provenance != wantProvenance {
				t.Fatal("winner or absence changed sighting-based provenance")
			}
		})
	}
	got := reduceWorkIQState(WorkIQReduceInput{Cache: WorkIQSourceState{Seen: true, GoneAt: &gone, EvictedAt: &gone}})
	if got.GoneAt == nil || got.EvictedAt == nil || got.Provenance != "desktop_cache" {
		t.Fatal("cache-only archive semantics changed")
	}
}

func TestWorkIQDeletionDomains(t *testing.T) {
	deleted := base
	for _, tc := range []struct {
		name       string
		cache      WorkIQSourceState
		server     WorkIQSourceState
		comparable bool
		want       bool
	}{
		{"cache plus unqualified server", WorkIQSourceState{Seen: true, DeletedAt: &deleted, DeletedVersion: 10}, WorkIQSourceState{Seen: true, Version: 99}, false, true},
		{"same source newer", WorkIQSourceState{Seen: true, DeletedAt: &deleted, DeletedVersion: 10, Version: 11}, WorkIQSourceState{}, false, false},
		{"same source tie", WorkIQSourceState{Seen: true, DeletedAt: &deleted, DeletedVersion: 10, Version: 10}, WorkIQSourceState{}, false, true},
		{"same source older", WorkIQSourceState{Seen: true, DeletedAt: &deleted, DeletedVersion: 10, Version: 9}, WorkIQSourceState{}, false, true},
		{"unknown deletion version", WorkIQSourceState{Seen: true, DeletedAt: &deleted, Version: 99}, WorkIQSourceState{}, false, true},
		{"qualified server newer", WorkIQSourceState{Seen: true, DeletedAt: &deleted, DeletedVersion: 10}, WorkIQSourceState{Seen: true, Version: 11}, true, false},
		{"qualified server tie", WorkIQSourceState{Seen: true, DeletedAt: &deleted, DeletedVersion: 10}, WorkIQSourceState{Seen: true, Version: 10}, true, true},
		{"server tombstone", WorkIQSourceState{Seen: true, Version: 99}, WorkIQSourceState{Seen: true, DeletedAt: &deleted, DeletedVersion: 10}, false, true},
		{"server own newer", WorkIQSourceState{}, WorkIQSourceState{Seen: true, DeletedAt: &deleted, DeletedVersion: 10, Version: 11}, false, false},
		{"qualified cache newer", WorkIQSourceState{Seen: true, Version: 11}, WorkIQSourceState{Seen: true, DeletedAt: &deleted, DeletedVersion: 10}, true, false},
		{"absence is not deletion", WorkIQSourceState{Seen: true, GoneAt: &deleted}, WorkIQSourceState{Seen: true, GoneAt: &deleted}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := reduceWorkIQState(WorkIQReduceInput{Cache: tc.cache, Server: tc.server, CrossVersionComparable: tc.comparable})
			if (got.DeletedAt != nil) != tc.want {
				t.Fatal("source deletion was cleared without strictly newer qualified live evidence")
			}
		})
	}
}

func TestWorkIQContentWinnerNeverBlanksAndTiesFavorCache(t *testing.T) {
	for _, tc := range []struct {
		name, cache, server, winner, text string
		cacheVersion, serverVersion       int64
		qualified, gap                    bool
	}{
		{"unverified newer", "cache", "server", "desktop_cache", "cache", 10, 11, false, true},
		{"verified newer", "cache", "server", "workiq_server", "server", 10, 11, true, false},
		{"tie", "cache", "server", "desktop_cache", "cache", 10, 10, true, false},
		{"older", "cache", "server", "desktop_cache", "cache", 11, 10, true, false},
		{"never blank", "cache", "", "desktop_cache", "cache", 10, 11, true, false},
		{"fill missing", "", "server", "workiq_server", "server", 10, 11, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := reduceWorkIQState(WorkIQReduceInput{
				Cache:                  WorkIQSourceState{Seen: true, Text: tc.cache, Version: tc.cacheVersion},
				Server:                 WorkIQSourceState{Seen: true, Text: tc.server, Version: tc.serverVersion},
				CrossVersionComparable: tc.qualified,
			})
			if got.Winner != tc.winner || got.Text != tc.text || got.VersionUnverified != tc.gap || got.Provenance != "both" {
				t.Fatal("source content selection blanked, guessed a clock comparison or lost provenance")
			}
		})
	}
	got := reduceWorkIQState(WorkIQReduceInput{Server: WorkIQSourceState{Seen: true, Text: "server"}})
	if got.Provenance != "workiq_server" || got.Winner != "workiq_server" || got.Text != "server" {
		t.Fatal("server-only content was attributed to cache")
	}
}

func TestWorkIQExactSourceClockComparison(t *testing.T) {
	deletion := base
	later := base.Add(time.Nanosecond)
	for _, clock := range []*time.Time{&deletion, &later, new(time.Time)} {
		got := reduceWorkIQState(WorkIQReduceInput{
			Cache:                  WorkIQSourceState{Seen: true, DeletedAt: &deletion, DeletedVersionAt: &deletion, DeletedVersion: 10},
			Server:                 WorkIQSourceState{Seen: true, VersionAt: clock, Version: 11},
			CrossVersionComparable: true,
		})
		if (got.DeletedAt == nil) != clock.Equal(later) {
			t.Fatal("millisecond rounding or an unknown clock changed exact source-version ordering")
		}
	}
}

func TestWorkIQCrossSourceRestoreIsNotALiveServerSighting(t *testing.T) {
	firstMiss, gone, serverLastLive := base, base.Add(2*time.Hour), base.Add(time.Hour)
	got := reduceWorkIQState(WorkIQReduceInput{
		Cache:                  WorkIQSourceState{Seen: true, Version: 11, FirstMissAt: &firstMiss, GoneAt: &gone},
		Server:                 WorkIQSourceState{Seen: true, Version: 9, DeletedVersion: 10, DeletedAt: &gone, SeenAt: &serverLastLive},
		CrossVersionComparable: true,
	})
	if got.DeletedAt != nil {
		t.Fatal("a qualified newer cache version did not override the older server tombstone")
	}
	if got.GoneAt == nil {
		t.Fatal("a cross-source deletion override invented a newer live server sighting")
	}
}

func TestWorkIQUnknownFirstMissClockIsNotMigrationTime(t *testing.T) {
	mark, lastLive := base.Add(24*time.Hour), base.Add(time.Hour)
	got := reduceWorkIQState(WorkIQReduceInput{
		Cache:  WorkIQSourceState{Seen: true, GoneAt: &mark, FirstMissAt: new(time.Time)},
		Server: WorkIQSourceState{Seen: true, SeenAt: &lastLive},
	})
	if got.GoneAt == nil || !got.UnknownFirstMiss {
		t.Fatal("an unknown first-miss clock became fabricated old source evidence")
	}
}
