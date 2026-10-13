package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestCloudCorrectionRetainedBodyBindingsAtZeroHits(t *testing.T) {
	for _, mutation := range []string{"typed-title", "digest", "indexed-title", "indexed-modified", "clock", "changed-clock", "qualification", "capture", "membership"} {
		t.Run(mutation, func(t *testing.T) {
			s := newStore(t)
			ctx := context.Background()
			r := cloudRequest(t, "stream", "")
			body := cloudResult("stream", "T")
			modified := "2026-01-01T00:00:00Z"
			parsed, _ := time.Parse(time.RFC3339Nano, modified)
			body.File.ModifiedRaw, body.File.ModifiedAt = &modified, &parsed
			first := beginCloud(t, s, r, base)
			commitCloud(t, s, first, r, body, base)
			commitCloud(t, s, beginCloud(t, s, r, base.Add(time.Hour)), r, cloudResult("stream", ""), base.Add(time.Hour))
			if snapshot, err := s.CloudSnapshot(ctx, CloudFilter{Query: "absent"}); err != nil || len(snapshot.Sources) != 1 || snapshot.Sources[0].LastGoodObservationID == nil {
				t.Fatalf("valid zero-hit setup: %v", err)
			}
			statement := ""
			switch mutation {
			case "typed-title":
				title := "validly typed but mutated retained title"
				body.File.Title = &title
				encoded, err := json.Marshal(body)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := s.db.Exec("update cloud_observations set result_json=? where rowid=1", string(encoded)); err != nil {
					t.Fatal(err)
				}
			case "digest":
				statement = "update cloud_observations set representation_digest='changed' where rowid=1"
			case "indexed-title":
				statement = "update cloud_observations set title='changed' where rowid=1"
			case "indexed-modified":
				statement = "update cloud_observations set file_modified_at='2026-01-02T00:00:00Z' where rowid=1"
			case "clock":
				statement = "update cloud_capture_metadata set completed_at='invalid' where capture_id=1"
			case "changed-clock":
				statement = "update cloud_capture_metadata set completed_at='2026-01-02T00:00:00Z' where capture_id=1"
			case "qualification":
				statement = "update knowledge_sources set qualification='changed'"
			case "capture":
				statement = "update cloud_observations set capture_id=2 where rowid=1"
			case "membership":
				statement = "delete from cloud_capture_members where capture_id=1 and observation_rowid=1"
			}
			if statement != "" {
				if _, err := s.db.Exec(statement); err != nil {
					t.Fatal(err)
				}
			}
			for _, mode := range []string{"sources", "zero-hit", "detail", "combined"} {
				t.Run(mode, func(t *testing.T) {
					var err error
					switch mode {
					case "sources":
						_, err = s.CloudSources(ctx, 0)
					case "zero-hit":
						_, err = s.CloudSnapshot(ctx, CloudFilter{Query: "absent"})
					case "detail":
						_, _, err = s.CloudDetail(ctx, 1)
					case "combined":
						_, err = s.CloudSnapshot(ctx, CloudFilter{})
					}
					if err == nil {
						t.Fatalf("%s accepted corrupt retained %s", mode, mutation)
					}
				})
			}
		})
	}
}

func TestCloudCorrectionChangedIncarnationRetention(t *testing.T) {
	for _, change := range []string{"account", "file"} {
		t.Run(change, func(t *testing.T) {
			s := newStore(t)
			ctx := context.Background()
			r := cloudRequest(t, "stream", "")
			original := cloudResult("stream", "T")
			first := beginCloud(t, s, r, base)
			commitCloud(t, s, first, r, original, base)
			next := cloudResult("stream", "")
			if change == "account" {
				next.SourceAccount.ID++
				next.SourceAccount.LoginName = "other synthetic account"
			} else {
				next.File.FileID = "dddddddd-dddd-dddd-dddd-dddddddddddd"
			}
			commitCloud(t, s, beginCloud(t, s, r, base.Add(time.Hour)), r, next, base.Add(time.Hour))
			row, source, err := s.CloudDetail(ctx, 1)
			if err != nil {
				t.Fatal(err)
			}
			if !row.Retained || source.Source.Status != "retained" || row.Result.Transcript.ID != "T" ||
				row.CaptureID != first.ID || !row.CapturedAt.Equal(base) ||
				row.Result.File.FileID != original.File.FileID || row.Result.SourceAccount != original.SourceAccount ||
				source.SelectionMatch != "matches" {
				t.Fatal("old incarnation is current or lost its own identity/selection/clocks")
			}
			snapshot, err := s.CloudSnapshot(ctx, CloudFilter{})
			if err != nil || len(snapshot.Rows) != 2 || len(snapshot.Sources) != 2 {
				t.Fatalf("incarnation snapshot: %v", err)
			}
			current := snapshot.Sources[1]
			if current.LastGoodObservationID != nil || current.CurrentTranscriptID != nil || current.SelectionMatch != "unavailable" {
				t.Fatal("new incarnation borrowed old selected body")
			}
			for _, item := range snapshot.Rows {
				if item.SourceID == current.Source.ID && item.Retained {
					t.Fatal("current incarnation is retained")
				}
			}
		})
	}
}

func TestCloudCorrectionCoherentFrontierCorruption(t *testing.T) {
	for _, mode := range []string{"nonbody-pointer", "current-membership"} {
		t.Run(mode, func(t *testing.T) {
			s := newStore(t)
			ctx := context.Background()
			r := cloudRequest(t, "stream", "")
			commitCloud(t, s, beginCloud(t, s, r, base), r, cloudResult("stream", "T"), base)
			next := cloudResult("stream", "")
			if mode == "current-membership" {
				next = cloudResult("stream", "U")
			}
			commitCloud(t, s, beginCloud(t, s, r, base.Add(time.Hour)), r, next, base.Add(time.Hour))
			statements := []string{"update cloud_source_body_state set last_good_observation_id=2"}
			if mode == "current-membership" {
				statements = []string{
					"update cloud_capture_members set observation_rowid=1 where capture_id=2",
					"update cloud_capture_metadata set current_transcript_id='T',body_observation_id=1 where capture_id=2",
					"update cloud_source_body_state set current_transcript_id='T',last_good_observation_id=1",
				}
			}
			for _, statement := range statements {
				if _, err := s.db.Exec(statement); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.CloudSources(ctx, 0); err == nil {
				t.Fatal("coherent row/digest is not enough to validate the body/frontier link")
			}
			if _, err := s.CloudSnapshot(ctx, CloudFilter{Query: "absent"}); err == nil {
				t.Fatal("zero hits hid invalid body/frontier link")
			}
		})
	}
}
