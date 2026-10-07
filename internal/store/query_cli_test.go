package store

import (
	"context"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/teamsdesktop"
)

func TestLastSuccess(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	got, err := s.LastSuccess(ctx)
	if err != nil || !got.IsZero() {
		t.Fatalf("empty archive: %v %v", got, err)
	}
	must0(s.RecordRun(ctx, Run{StartedAt: base, FinishedAt: base.Add(time.Second), Status: "ok", Accounts: []string{"*"}}))
	must0(s.RecordRun(ctx, Run{StartedAt: base.Add(time.Hour), FinishedAt: base.Add(time.Hour + time.Second), Status: "failed", Accounts: []string{"*"}}))
	got, err = s.LastSuccess(ctx)
	if err != nil || !got.Equal(base.Add(time.Second)) {
		t.Fatalf("a failed run must not count: %v %v", got, err)
	}
	must0(s.RecordRun(ctx, Run{StartedAt: base.Add(2 * time.Hour), FinishedAt: base.Add(2*time.Hour + time.Second), Status: "unchanged", Accounts: []string{"*"}}))
	got, _ = s.LastSuccess(ctx)
	if !got.Equal(base.Add(2*time.Hour + time.Second)) {
		t.Fatalf("unchanged counts as success: %v", got)
	}
}

func TestMessageHTML(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	must(s.ApplyMessages(ctx, []teamsdesktop.Message{msg(acctA, "c", "m1", "hi", base), msg(acctB, "c", "m1", "other", base)}))
	keys := []MessageKey{{TenantID: acctA.TenantID, UserID: acctA.UserID, ConversationID: "c", ID: "m1"}, {TenantID: "x", UserID: "y", ConversationID: "c", ID: "zz"}}
	got, err := s.MessageHTML(ctx, keys)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[keys[0]] != "<p>hi</p>" {
		t.Fatalf("html = %v", got)
	}
}
