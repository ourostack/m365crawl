package store

import (
	"context"
	"encoding/base64"
	"reflect"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/engagecontent"
)

func selectedStoredResult(t *testing.T) engagecontent.Result {
	t.Helper()
	id := base64.RawURLEncoding.EncodeToString([]byte(`{"_type":"Thread","id":"42"}`))
	target, err := engagecontent.ParseThreadURL("https://engage.cloud.microsoft/main/threads/" + id)
	if err != nil {
		t.Fatal(err)
	}
	r := engageResult("viewer")
	r.Threads = r.Threads[:1]
	r.Threads[0].ID = id
	r.Threads[0].Blocks = []string{"starter"}
	r.Qualification = engagecontent.SelectedQualification
	r.Selection = &target
	parent := "starter"
	r.Threads[0].Selected = &engagecontent.SelectedThread{Messages: []engagecontent.SelectedMessage{
		{ID: "starter", ThreadID: id, Blocks: []string{"starter"}, BodyState: "observed"},
		{ID: "reply", ThreadID: id, ParentID: &parent, Blocks: []string{"replyuniqueneedle"}, BodyState: "observed"},
	}, Replies: engagecontent.ReplyPage{EdgesObserved: 1, EdgesKnown: true}}
	return r
}

func TestEngageSelectedImmutableSearchAndHomeCompatibility(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	home := engageResult("home-viewer")
	commitEngage(t, s, home, base)
	r := selectedStoredResult(t)
	a, err := s.BeginEngageThreadAttempt(ctx, KnowledgeInput{Kind: EngageThreadKind, Platform: "browser", Locator: r.Selection.URL, At: base.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CommitEngageCapture(ctx, a, EngageCapture{r, base.Add(time.Hour), base.Add(time.Hour + time.Second)}); err != nil {
		t.Fatal(err)
	}
	found, err := s.EngageSnapshot(ctx, EngageFilter{Query: "replyuniqueneedle"})
	if err != nil || len(found.Rows) != 1 || found.Rows[0].Thread.Selected == nil {
		t.Fatal("reply search", err)
	}
	original := found.Rows[0]
	again, err := s.BeginEngageThreadAttempt(ctx, KnowledgeInput{Kind: EngageThreadKind, Platform: "browser", Locator: r.Selection.URL, At: base.Add(2 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CommitEngageCapture(ctx, again, EngageCapture{r, base.Add(2 * time.Hour), base.Add(2*time.Hour + time.Second)}); err != nil {
		t.Fatal(err)
	}
	detail, err := s.EngageDetail(ctx, original.ID)
	if err != nil || !reflect.DeepEqual(original, detail.Observation) {
		t.Fatal("immutable reuse changed", err)
	}
	if detail.Registration.Target != r.Selection.URL {
		t.Fatal("target lost")
	}
	if detail.Source.CurrentSession.BlocksTotal != 2 {
		t.Fatal("reply blocks missing from session summary")
	}
	all, err := s.EngageSnapshot(ctx, EngageFilter{})
	if err != nil || len(all.Rows) != 3 {
		t.Fatal("Home compatibility", err)
	}
	fail, err := s.BeginEngageThreadAttempt(ctx, KnowledgeInput{Kind: EngageThreadKind, Platform: "browser", Locator: r.Selection.URL, At: base.Add(3 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecordEngageThreadFailure(ctx, fail, EngageFailure{"failed", "engage_capture_failed", base.Add(3 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	after, err := s.EngageDetail(ctx, original.ID)
	if err != nil || after.Registration.AccountState != "unknown" || !reflect.DeepEqual(detail.Source, after.Source) {
		t.Fatal("unknown failure contaminated source", err)
	}
}
