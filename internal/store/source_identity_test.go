package store

import (
	"context"
	"slices"
	"testing"
	"time"
)

func TestSourceIdentityRecordsKeysOnceAndReadsLegacyTeamsSources(t *testing.T) {
	ctx := context.Background()
	path := writableArchivePath(t)
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	got, err := s.SourceIdentity(ctx)
	if err != nil || len(got.Recorded) != 0 || len(got.TeamsSources) != 0 {
		t.Fatalf("empty archive = %+v, %v", got, err)
	}
	at := time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC)
	for _, r := range []Run{
		{Source: "WV2Profile_tfw|https_teams.microsoft.com_0", Status: "ok"},
		{Source: "WV2Profile_tfw|https_teams.microsoft.com_0", Status: "unchanged"},
		{Source: "Other|https_teams.cloud.microsoft_0", Status: "failed"}, // never synced: not evidence
		{Source: "outlook|Main Profile|people", Status: "ok"},
		{Source: "", Status: "ok"},
	} {
		r.StartedAt, r.FinishedAt = at, at
		if err := s.RecordRun(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	got, err = s.SourceIdentity(ctx)
	if err != nil || len(got.Recorded) != 0 || !slices.Equal(got.TeamsSources, []string{"WV2Profile_tfw|https_teams.microsoft.com_0"}) {
		t.Fatalf("legacy archive = %+v, %v", got, err)
	}
	if err := s.RecordSourceIdentity(ctx, []string{"teams:aa", "outlook:bb"}, at); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordSourceIdentity(ctx, []string{"teams:aa"}, at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordSourceIdentity(ctx, nil, at); err != nil {
		t.Fatal(err)
	}
	got, err = s.SourceIdentity(ctx)
	if err != nil || len(got.Recorded) != 2 || !got.Recorded["teams:aa"] || !got.Recorded["outlook:bb"] {
		t.Fatalf("recorded = %+v, %v", got, err)
	}
	var first string
	if err := s.db.QueryRow(`select value from meta where key='source_identity:teams:aa'`).Scan(&first); err != nil || first != "2026-10-31T09:00:00.000Z" {
		t.Fatalf("first recording kept = %q, %v", first, err)
	}
	ro, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ro.Close() }()
	if got, err := ro.SourceIdentity(ctx); err != nil || len(got.Recorded) != 2 {
		t.Fatalf("read-only = %+v, %v", got, err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := ro.SourceIdentity(cancelled); err == nil {
		t.Fatal("a cancelled read succeeded")
	}
}
