package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/sharepointcontent"
)

func cloudRequest(t *testing.T, kind, selection string) sharepointcontent.CanonicalRequest {
	t.Helper()
	url := "https://fixture.sharepoint.com/sites/sample/SitePages/Page.aspx?ticket=discard"
	if kind == "stream" {
		url = "https://fixture.sharepoint.com/sites/sample/_layouts/15/stream.aspx?id=%2Fsites%2Fsample%2FVideos%2FVideo.mp4&ticket=discard"
	}
	request, err := sharepointcontent.NormalizeRequest(sharepointcontent.Request{Kind: kind, URL: url, TranscriptID: selection})
	if err != nil {
		t.Fatal(err)
	}
	return request
}
func cloudResult(kind, selection string) sharepointcontent.Result {
	title, drive, item := "Synthetic searchableword", "drive", "item"
	result := sharepointcontent.Result{Kind: kind, State: "metadata_only", HTTPStatus: 200,
		SourceAccount: sharepointcontent.SourceAccount{Host: "fixture.sharepoint.com", WebID: "cccccccc-cccc-cccc-cccc-cccccccccccc", ID: 11, LoginName: "synthetic"},
		File: sharepointcontent.NativeFile{SiteID: "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb", WebID: "cccccccc-cccc-cccc-cccc-cccccccccccc", FileID: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
			Path: "/sites/sample/SitePages/Page.aspx", Title: &title, DriveID: &drive, ItemID: &item}}
	if kind == "stream" {
		result.File.Path = "/sites/sample/Videos/Video.mp4"
		result.State = "no_transcript"
		if selection != "" {
			result.State = "transcript_observations"
			result.Transcript = &sharepointcontent.Transcript{ID: selection, Entries: []sharepointcontent.TranscriptEntry{}}
		}
	} else {
		typ := int64(4)
		result.State = "page_observations"
		result.PageControls = []sharepointcontent.PageControl{{Ordinal: 0, Type: &typ, State: "text", Texts: []string{"searchableword body"}}}
	}
	return result
}
func beginCloud(t *testing.T, s *Store, request sharepointcontent.CanonicalRequest, at time.Time) KnowledgeAttempt {
	t.Helper()
	kind := CloudPageKind
	if request.Request.Kind == "stream" {
		kind = CloudStreamKind
	}
	a, err := s.BeginCloudAttempt(context.Background(), KnowledgeInput{Kind: kind, Platform: "browser", Locator: request.LocatorKey, At: at}, request)
	if err != nil {
		t.Fatal(err)
	}
	return a
}
func commitCloud(t *testing.T, s *Store, a KnowledgeAttempt, request sharepointcontent.CanonicalRequest, result sharepointcontent.Result, at time.Time) {
	t.Helper()
	if err := s.CommitCloudCapture(context.Background(), a, CloudCapture{Request: request, Result: result, CompletedAt: at}); err != nil {
		t.Fatal(err)
	}
}
func TestCloudStreamMetadataWithoutSelection(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	request := cloudRequest(t, "stream", "")
	a := beginCloud(t, s, request, base)
	commitCloud(t, s, a, request, cloudResult("stream", ""), base)
	snap, err := s.CloudSnapshot(ctx, CloudFilter{})
	if err != nil || len(snap.Rows) != 1 || len(snap.Sources) != 1 {
		t.Fatalf("metadata snapshot: %v", err)
	}
	src := snap.Sources[0]
	if src.Source.ID <= 0 || src.CurrentTranscriptID != nil || src.LastGoodObservationID != nil || src.SelectionMatch != "unavailable" || src.Source.SourceObservedAt != nil {
		t.Fatal("metadata invented selected body or observation clock")
	}
	safe, err := s.CloudRequest(ctx, a.LocatorID)
	if err != nil || safe != request {
		t.Fatal("safe retry changed")
	}
}
func TestCloudBodySelectionRetention(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	request := cloudRequest(t, "stream", "")
	a := beginCloud(t, s, request, base)
	commitCloud(t, s, a, request, cloudResult("stream", "T"), base)
	snap, _ := s.CloudSnapshot(ctx, CloudFilter{})
	id := snap.Rows[0].ID
	for i, state := range []string{"no_transcript", "metadata_only", "no_access", "transcript_selection_required"} {
		result := cloudResult("stream", "")
		result.State = state
		if state == "no_access" {
			result.HTTPStatus = 403
		}
		at := base.Add(time.Duration(i+1) * time.Hour)
		commitCloud(t, s, beginCloud(t, s, request, at), request, result, at)
		row, source, err := s.CloudDetail(ctx, id)
		if err != nil || !row.Retained || row.Result.Transcript.ID != "T" || source.CurrentTranscriptID != nil || source.SelectionMatch != "unavailable" || *source.LastGoodObservationID != id {
			t.Fatalf("retained body spliced: %v", err)
		}
	}
	at := base.Add(5 * time.Hour)
	commitCloud(t, s, beginCloud(t, s, request, at), request, cloudResult("stream", "U"), at)
	_, src, err := s.CloudDetail(ctx, id)
	if err != nil || src.SelectionMatch != "differs" || *src.CurrentTranscriptID != "U" {
		t.Fatal("historical T not distinguished from U")
	}
	result := cloudResult("stream", "")
	result.SourceAccount.LoginName = "other"
	at = base.Add(6 * time.Hour)
	commitCloud(t, s, beginCloud(t, s, request, at), request, result, at)
	sources, err := s.CloudSources(ctx, 0)
	if err != nil || len(sources) != 2 || sources[1].LastGoodObservationID != nil {
		t.Fatal("body borrowed across instance")
	}
	for _, selection := range []string{"T", "U"} {
		r := cloudRequest(t, "stream", selection)
		at = at.Add(time.Hour)
		commitCloud(t, s, beginCloud(t, s, r, at), r, cloudResult("stream", selection), at)
	}
	sources, _ = s.CloudSources(ctx, 0)
	if len(sources) != 4 {
		t.Fatal("requested registrations aliased")
	}
}
func TestCloudCaptureFrontier(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	request := cloudRequest(t, "page", "")
	result := cloudResult("page", "")
	old := beginCloud(t, s, request, base)
	first := beginCloud(t, s, request, base.Add(time.Hour))
	commitCloud(t, s, first, request, result, base.Add(time.Hour))
	if err := s.CommitCloudCapture(ctx, old, CloudCapture{request, result, base}); !errors.Is(err, ErrStaleKnowledgeAttempt) {
		t.Fatal(err)
	}
	snap, _ := s.CloudSnapshot(ctx, CloudFilter{})
	id := snap.Rows[0].ID
	title := "B"
	result.File.Title = &title
	at := base.Add(2 * time.Hour)
	commitCloud(t, s, beginCloud(t, s, request, at), request, result, at)
	at = base.Add(3 * time.Hour)
	commitCloud(t, s, beginCloud(t, s, request, at), request, cloudResult("page", ""), at)
	row, source, err := s.CloudDetail(ctx, id)
	if err != nil || row.CaptureID != first.ID || !row.CapturedAt.Equal(base.Add(time.Hour)) || row.Retained || source.Source.Coverage != "partial" {
		t.Fatal("A-B-A or page scope changed")
	}
}
func TestCloudZeroHitCoverage(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	r := cloudRequest(t, "stream", "")
	a := beginCloud(t, s, r, base)
	if err := s.RecordCloudFailure(ctx, a, KnowledgeOutcome{Status: "failed", Reason: "cloud_unreadable", At: base}); err != nil {
		t.Fatal(err)
	}
	snap, err := s.CloudSnapshot(ctx, CloudFilter{Query: "absent"})
	if err != nil || len(snap.Rows) != 0 || len(snap.Sources) != 1 || snap.Sources[0].RegistrationID != a.LocatorID {
		t.Fatal("zero hit lost registration")
	}
	if _, err := s.CloudRequest(ctx, 999); !errors.Is(err, ErrKnowledgeSourceUnknown) {
		t.Fatal(err)
	}
}
func TestCloudSnapshotPublication(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	r := cloudRequest(t, "page", "")
	a := beginCloud(t, s, r, base)
	commitCloud(t, s, a, r, cloudResult("page", ""), base)
	if _, err := s.db.Exec("create trigger cloud_fault before insert on cloud_capture_metadata begin select raise(abort,'synthetic'); end"); err != nil {
		t.Fatal(err)
	}
	at := base.Add(time.Hour)
	a = beginCloud(t, s, r, at)
	changed := cloudResult("page", "")
	title := "rollback"
	changed.File.Title = &title
	if err := s.CommitCloudCapture(ctx, a, CloudCapture{r, changed, at}); err == nil {
		t.Fatal("publication fault masked")
	}
	snap, err := s.CloudSnapshot(ctx, CloudFilter{})
	if err != nil || len(snap.Rows) != 1 || snap.Sources[0].Source.AdmittedID == a.ID {
		t.Fatal("partial publication")
	}
	if _, err := s.db.Exec("update cloud_observations set result_json='{}'"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CloudSnapshot(ctx, CloudFilter{}); err == nil {
		t.Fatal("corrupt archive accepted")
	}
}

func TestCloudZeroHitRejectsCorruptBodyState(t *testing.T) {
	for _, statement := range []string{
		"update cloud_source_body_state set last_good_observation_id=999",
		"update cloud_source_body_state set current_transcript_id='invented'",
		"update cloud_capture_metadata set requested_transcript_id='invented'",
		"update cloud_capture_metadata set body_observation_id=null",
	} {
		t.Run(statement, func(t *testing.T) {
			s := newStore(t)
			r := cloudRequest(t, "stream", "")
			commitCloud(t, s, beginCloud(t, s, r, base), r, cloudResult("stream", "T"), base)
			if _, err := s.db.Exec(statement); err != nil {
				t.Fatal(err)
			}

			if _, err := s.CloudSnapshot(context.Background(), CloudFilter{Query: "absent"}); err == nil {
				t.Fatal("corrupt state accepted at zero hits")
			}
		})
	}
}

func TestCloudCanonicalIdentityAllowsEquivalentSafeEncoding(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	r := cloudRequest(t, "page", "")
	commitCloud(t, s, beginCloud(t, s, r, base), r, cloudResult("page", ""), base)
	alias, err := sharepointcontent.NormalizeRequest(sharepointcontent.Request{Kind: "page", URL: "https://FIXTURE.sharepoint.com:443/sites/sample/SitePages/P%61ge.aspx"})
	if err != nil || alias.LocatorKey != r.LocatorKey {
		t.Fatal("decoded locator changed")
	}
	a := beginCloud(t, s, alias, base.Add(time.Hour))
	if err := s.CommitCloudCapture(ctx, a, CloudCapture{alias, cloudResult("page", ""), base.Add(time.Hour)}); err != nil {
		t.Fatal("same canonical request rejected")
	}
	wrong, _ := sharepointcontent.NormalizeRequest(sharepointcontent.Request{Kind: "page", URL: "https://fixture.sharepoint.com/sites/sample/SitePages/Different.aspx"})
	wrongResult := cloudResult("page", "")
	wrongResult.File.Path = "/sites/sample/SitePages/Different.aspx"
	if err := s.CommitCloudCapture(ctx, beginCloud(t, s, r, base.Add(2*time.Hour)), CloudCapture{wrong, wrongResult, base.Add(2 * time.Hour)}); err == nil {
		t.Fatal("different locator admitted")
	}
}
