package store

import (
	"context"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/teamsdesktop"
)

func TestMessagesByKey(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	m1 := msg(acctA, "c1", "m1", "one", base)
	m2 := msg(acctA, "c1", "m2", "two", base.Add(time.Minute))
	m2.DeletedAt = base.Add(time.Hour)
	other := msg(acctB, "c1", "m1", "same conversation id, other account", base)
	must(s.ApplyMessages(ctx, []teamsdesktop.Message{m1, m2, other}))

	rows, err := s.MessagesByKey(ctx, []string{key(acctA, "c1", "m2"), key(acctA, "c1", "m1"), key(acctA, "c1", "gone"), "not-a-key"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].ID != "m2" || rows[1].ID != "m1" {
		t.Fatalf("rows (request order, missing keys skipped): %+v", rows)
	}
	if rows[0].DeletedAt.IsZero() {
		t.Error("a deleted message must come back with deleted_at")
	}
	if rows[1].UserID != acctA.UserID || rows[1].ContentText != "one" {
		t.Errorf("the other account's row leaked: %+v", rows[1])
	}
	if rows, err := s.MessagesByKey(ctx, nil); err != nil || len(rows) != 0 {
		t.Fatalf("empty: %v %v", rows, err)
	}
	// More keys than one query chunk.
	var keys []string
	for i := 0; i < 450; i++ {
		keys = append(keys, key(acctA, "c1", "m1"))
	}
	if rows, err := s.MessagesByKey(ctx, keys); err != nil || len(rows) != 450 {
		t.Fatalf("chunked: %d %v", len(rows), err)
	}
}

func TestActivityByKey(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	a1 := teamsdesktop.Activity{TenantID: acctA.TenantID, UserID: acctA.UserID, ID: "a1", Type: "mention", At: base, Raw: []byte(`{}`)}
	a2 := a1
	a2.ID, a2.At = "a2", base.Add(time.Minute)
	must(s.ApplyActivity(ctx, []teamsdesktop.Activity{a1, a2}))
	rows, err := s.ActivityByKey(ctx, []string{acctA.TenantID + "|" + acctA.UserID + "|a1", acctA.TenantID + "|" + acctA.UserID + "|a2", "bad"})
	if err != nil || len(rows) != 2 || rows[0].ID != "a1" || rows[1].ID != "a2" {
		t.Fatalf("%+v %v", rows, err)
	}
}
