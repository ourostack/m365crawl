package store

import "testing"

// Catches treating an IndexedDB read as a zero-damage HxStore block census.
func TestCalendarSourcesOWABlockCensusProvenance(t *testing.T) {
	for _, tc := range []struct {
		name string
		read string
		want *float64
	}{
		{"legacy-blocks", `{"at":"2026-10-21T00:00:00Z","blocks_found":10,"blocks_invalid":1}`, new(0.1)},
		{"legacy-empty", `{"at":"2026-10-21T00:00:00Z","blocks_found":0,"blocks_invalid":0}`, new(0.0)},
		{"owa", `{"at":"2026-10-21T00:00:00Z","owa":true,"blocks_found":0,"blocks_invalid":0}`, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := qArchive(t)
			s.qExec(t, `insert into meta(key,value) values('outlook_read:outlook/Synthetic',?)`, tc.read)
			o := sourcesOf(t, s, CalendarSourcesFilter{})["outlook/Synthetic"].Outlook
			if o == nil || o.CensusAsOf.IsZero() {
				t.Fatalf("census missing: %+v", o)
			}
			if tc.want == nil {
				if o.BlocksRatio != nil {
					t.Fatalf("OWA read invented HxStore block health: %v", *o.BlocksRatio)
				}
			} else if o.BlocksRatio == nil || *o.BlocksRatio != *tc.want {
				t.Fatalf("legacy block ratio changed: %+v", o)
			}
		})
	}
}

func TestCalendarSourcesOWAFailurePolicyKeepsCensusProvenance(t *testing.T) {
	for _, tc := range []struct {
		name    string
		read    string
		failure string
		policy  string
		blocks  bool
	}{
		{"first-owa-failure", "", `{"code":"outlook_layout_unsupported","owa":true}`, "not_inferred", false},
		{"legacy-failure", "", `{"code":"outlook_layout_unsupported"}`, "inferred", false},
		{"owa-census", `{"at":"2026-10-21T00:00:00Z","owa":true}`, `{"code":"outlook_layout_unsupported"}`, "not_inferred", false},
		{"old-hxstore-census", `{"at":"2026-10-21T00:00:00Z","blocks_found":10,"blocks_invalid":1}`, `{"code":"outlook_layout_unsupported","owa":true}`, "not_inferred", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := qArchive(t)
			if tc.read != "" {
				s.qExec(t, `insert into meta(key,value) values('outlook_read:outlook/Synthetic',?)`, tc.read)
			}
			s.qExec(t, `insert into meta(key,value) values('outlook_failure:outlook/Synthetic',?)`, tc.failure)
			o := sourcesOf(t, s, CalendarSourcesFilter{})["outlook/Synthetic"].Outlook
			if o == nil || o.Failure == nil || o.Deletions != tc.policy || (o.BlocksRatio != nil) != tc.blocks {
				t.Fatalf("failure changed policy or census provenance: %+v", o)
			}
			if tc.blocks && *o.BlocksRatio != 0.1 {
				t.Fatalf("old HxStore census changed: %v", *o.BlocksRatio)
			}
		})
	}
}
