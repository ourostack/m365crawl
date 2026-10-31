package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/workiq"
)

func TestWorkIQDefaultChatWindowResumesOnlyItsUnfinishedOriginalBounds(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	must0(s.ApplyAccount(ctx, acctA))
	account := acctA.TenantID + "/" + acctA.UserID
	now := base.Add(123456789 * time.Nanosecond)
	fresh := must(s.ResolveWorkIQChatWindow(ctx, account, now))
	if fresh.Resumed || !fresh.Start.Equal(now.Add(-365*24*time.Hour)) || !fresh.End.Equal(now) {
		t.Fatal("fresh default window did not preserve its actual fixed bounds")
	}
	must0(s.SetWorkIQEnabled(ctx, true))
	scope := WorkIQScope{Family: "chats", Account: account, Qualification: WorkIQDefaultChatQualification,
		WindowStart: fresh.Start, WindowEnd: fresh.End}
	attempt := must(s.BeginWorkIQAttempt(ctx, scope, now))
	verifyWorkIQTestAttempt(t, s, attempt)
	envelope := must(json.Marshal(map[string]any{"structuredContent": map[string]any{"results": []any{
		map[string]any{"statusCode": 200, "data": map[string]any{"value": []any{}}},
	}}}))
	page := must(workiq.DecodePage(ctx, envelope))
	must0(s.StageWorkIQPage(ctx, WorkIQPageBatch{AttemptID: attempt, Resource: "/chats/chat/messages", Page: page, ObservedAt: now}))
	nextNow := now.Add(24 * time.Hour)
	resumed := must(s.ResolveWorkIQChatWindow(ctx, account, nextNow))
	if !resumed.Resumed || !resumed.Start.Equal(fresh.Start) || !resumed.End.Equal(fresh.End) {
		t.Fatal("default retry abandoned its unfinished fixed window or lost source-clock precision")
	}
	var id int64
	must0(s.db.QueryRowContext(ctx, `select max(id) from workiq_pages`).Scan(&id))
	must(s.PrepareWorkIQChatPage(ctx, attempt, id, []byte(`{"id":"chat","tenantId":"tenant-1","chatType":"group"}`), 128))
	must0(s.AdmitWorkIQFrontier(ctx, attempt, "/chats/chat/messages", 1))
	done := must(s.ResolveWorkIQChatWindow(ctx, account, nextNow))
	if done.Resumed || !done.Start.Equal(nextNow.Add(-365*24*time.Hour)) || !done.End.Equal(nextNow) {
		t.Fatal("completed default backfill blocked a new current window")
	}
}

func TestWorkIQChatFixedWindowRejectsFutureEnd(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	must0(s.ApplyAccount(ctx, acctA))
	must0(s.SetWorkIQEnabled(ctx, true))
	if _, err := s.BeginWorkIQAttempt(ctx, WorkIQScope{Family: "chats", Account: acctA.TenantID + "/" + acctA.UserID,
		WindowStart: base.Add(-time.Hour), WindowEnd: base.Add(time.Hour)}, base); err == nil {
		t.Fatal("a new source window admitted a future end as current evidence")
	}
}

func TestWorkIQChatWindowPoliciesAndScopeFailures(t *testing.T) {
	ctx := context.Background()
	account := acctA.TenantID + "/" + acctA.UserID
	for _, tc := range []struct {
		name string
		now  time.Time
		args []time.Time
	}{
		{"zero clock", time.Time{}, nil},
		{"multiple starts", base, []time.Time{base, base}},
		{"zero start", base, []time.Time{{}}},
		{"future start", base, []time.Time{base.Add(time.Second)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newStore(t)
			must0(s.ApplyAccount(ctx, acctA))
			if _, err := s.ResolveWorkIQChatWindow(ctx, account, tc.now, tc.args...); err == nil {
				t.Fatal("invalid fixed window request succeeded")
			}
		})
	}
	t.Run("unknown owner", func(t *testing.T) {
		s := newStore(t)
		if _, err := s.ResolveWorkIQChatWindow(ctx, account, base); err == nil {
			t.Fatal("unarchived owner received a window")
		}
	})
	t.Run("closed archive", func(t *testing.T) {
		s := newStore(t)
		must0(s.Close())
		if _, err := s.ResolveWorkIQChatWindow(ctx, account, base); err == nil {
			t.Fatal("closed archive received a window")
		}
	})
	t.Run("frontier read failure", func(t *testing.T) {
		s := newStore(t)
		must0(s.ApplyAccount(ctx, acctA))
		must(s.db.ExecContext(ctx, `alter table workiq_frontiers rename to unavailable_frontiers`))
		if _, err := s.ResolveWorkIQChatWindow(ctx, account, base); err == nil {
			t.Fatal("missing frontier storage appeared to be a fresh window")
		}
	})
	t.Run("frontier column failure", func(t *testing.T) {
		s := newStore(t)
		must0(s.ApplyAccount(ctx, acctA))
		must(s.db.ExecContext(ctx, `alter table workiq_frontiers rename column scope to unavailable_scope`))
		if _, err := s.ResolveWorkIQChatWindow(ctx, account, base); err == nil {
			t.Fatal("broken frontier query appeared to be a fresh window")
		}
	})
	for _, policy := range []string{WorkIQDefaultChatQualification, WorkIQAbsoluteChatQualification} {
		t.Run(policy, func(t *testing.T) {
			s := newStore(t)
			must0(s.ApplyAccount(ctx, acctA))
			must0(s.SetWorkIQEnabled(ctx, true))
			start := base.Add(-time.Hour)
			scope := WorkIQScope{Family: "chats", Account: account, Qualification: policy, WindowStart: start, WindowEnd: base}
			attempt := must(s.BeginWorkIQAttempt(ctx, scope, base))
			verifyWorkIQTestAttempt(t, s, attempt)
			page := must(workiq.DecodePage(ctx, []byte(`{"structuredContent":{"results":[{"statusCode":200,"data":{"value":[]}}]}}`)))
			must0(s.StageWorkIQPage(ctx, WorkIQPageBatch{AttemptID: attempt, Resource: "/chats/chat/messages", Page: page, ObservedAt: base}))
			later := base.Add(time.Hour)
			absolute := must(s.ResolveWorkIQChatWindow(ctx, account, later, start))
			def := must(s.ResolveWorkIQChatWindow(ctx, account, later))
			if absolute.Resumed != (policy == WorkIQAbsoluteChatQualification) || def.Resumed != (policy == WorkIQDefaultChatQualification) {
				t.Fatal("absolute and default cursors crossed policy namespaces")
			}
			changed := must(s.ResolveWorkIQChatWindow(ctx, account, later, start.Add(-time.Second)))
			if changed.Resumed || !changed.Start.Equal(start.Add(-time.Second)) || !changed.End.Equal(later) {
				t.Fatal("changed explicit start adopted another absolute cursor")
			}
			invalid := []WorkIQScope{
				{Family: "chats", Account: account, WindowEnd: base},
				{Family: "chats", Account: account, WindowStart: base, WindowEnd: base.Add(-time.Second)},
			}
			for _, bad := range invalid {
				if _, err := s.BeginWorkIQAttempt(ctx, bad, base); err == nil {
					t.Fatal("invalid fixed-window metadata was persisted")
				}
			}
		})
	}
}

func TestWorkIQChatWindowRejectsCorruptOriginalMetadata(t *testing.T) {
	ctx := context.Background()
	account := acctA.TenantID + "/" + acctA.UserID
	for _, change := range []string{
		`update workiq_attempts set window_start='bad'`,
		`update workiq_attempts set window_end='bad'`,
		`update workiq_attempts set window_start=null`,
		`update workiq_attempts set window_end='0001-01-01T00:00:00Z'`,
		`update workiq_attempts set window_end='2025-01-01T00:00:00Z'`,
		`update workiq_attempts set window_end='2099-01-01T00:00:00Z'`,
		`update workiq_attempts set family='mail'`,
		`update workiq_attempts set account_id='unknown'`,
		`update workiq_attempts set qualification='different'`,
		`update workiq_frontiers set scope='different'`,
	} {
		t.Run(change, func(t *testing.T) {
			s := newStore(t)
			must0(s.ApplyAccount(ctx, acctA))
			must0(s.SetWorkIQEnabled(ctx, true))
			attempt := must(s.BeginWorkIQAttempt(ctx, WorkIQScope{Family: "chats", Account: account,
				Qualification: WorkIQDefaultChatQualification, WindowStart: base.Add(-time.Hour), WindowEnd: base}, base))
			verifyWorkIQTestAttempt(t, s, attempt)
			page := must(workiq.DecodePage(ctx, []byte(`{"structuredContent":{"results":[{"statusCode":200,"data":{"value":[]}}]}}`)))
			must0(s.StageWorkIQPage(ctx, WorkIQPageBatch{AttemptID: attempt, Resource: "/chats/chat/messages", Page: page, ObservedAt: base}))
			must(s.db.ExecContext(ctx, change))
			if _, err := s.ResolveWorkIQChatWindow(ctx, account, base.Add(time.Hour)); err == nil {
				t.Fatal("invalid original bounds or ownership were resumed")
			}
		})
	}
}
