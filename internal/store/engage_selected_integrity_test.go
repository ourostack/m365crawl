package store

import (
	"context"
	"encoding/base64"
	"testing"

	"github.com/ourostack/m365crawl/internal/engagecontent"
)

func TestSelectedRegistrationAndFailureRefusals(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	r := selectedStoredResult(t)
	for _, input := range []KnowledgeInput{
		{Kind: EngageThreadKind, Platform: "browser", Locator: "https://example.test/", At: base},
		{Kind: EngageKind, Platform: "browser", Locator: r.Selection.URL, At: base},
		{Kind: EngageThreadKind, Platform: "other", Locator: r.Selection.URL, At: base},
		{Kind: EngageThreadKind, Platform: "browser", Locator: r.Selection.URL, AcquisitionSignature: "unexpected", At: base},
	} {
		if _, err := s.BeginEngageThreadAttempt(ctx, input); err == nil {
			t.Fatal("invalid registration admitted")
		}
	}
	if err := s.RecordEngageThreadFailure(ctx, KnowledgeAttempt{}, EngageFailure{Status: "complete"}); err == nil {
		t.Fatal("invalid failure admitted")
	}
	a, err := s.BeginEngageThreadAttempt(ctx, KnowledgeInput{Kind: EngageThreadKind, Platform: "browser", Locator: r.Selection.URL, At: base})
	if err != nil {
		t.Fatal(err)
	}
	id := base64.RawURLEncoding.EncodeToString([]byte(`{"_type":"Thread","id":"43"}`))
	target, _ := engagecontent.ParseThreadURL("https://engage.cloud.microsoft/main/threads/" + id)
	r.Selection = &target
	r.Threads[0].ID = id
	for i := range r.Threads[0].Selected.Messages {
		r.Threads[0].Selected.Messages[i].ThreadID = id
	}
	if err := s.CommitEngageCapture(ctx, a, EngageCapture{r, base, base}); err == nil {
		t.Fatal("result published under another requested target")
	}
}

func TestSelectedRegistryAndSessionTypeIntegrity(t *testing.T) {
	for _, mode := range []string{"invalid-target", "wrong-session-family"} {
		t.Run(mode, func(t *testing.T) {
			s := newStore(t)
			ctx := context.Background()
			r := selectedStoredResult(t)
			a, err := s.BeginEngageThreadAttempt(ctx, KnowledgeInput{Kind: EngageThreadKind, Platform: "browser", Locator: r.Selection.URL, At: base})
			if err != nil {
				t.Fatal(err)
			}
			if mode == "invalid-target" {
				_, err = s.db.Exec(`update knowledge_locators set locator='https://example.test/';update engage_locator_metadata set target_url='https://example.test/'`)
			} else {
				if err := s.CommitEngageCapture(ctx, a, EngageCapture{r, base, base}); err != nil {
					t.Fatal(err)
				}
				var id int64
				if err := s.db.QueryRow(`select id from knowledge_sources where locator_id=?`, a.LocatorID).Scan(&id); err != nil {
					t.Fatal(err)
				}
				r.Selection = nil
				r.Qualification = "native_home_observations"
				r.Threads[0].Selected = nil
				encoded := engageJSON(r)
				digest := engageDigestFor(id, EngageThreadQualification, encoded)
				_, err = s.db.Exec(`update engage_sessions set result_json=?,representation_digest=?`, encoded, digest)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.EngageSources(ctx); err == nil {
				t.Fatal("corrupt selected namespace admitted")
			}
		})
	}
}
