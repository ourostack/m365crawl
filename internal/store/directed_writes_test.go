package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func sampleDirectedWrite(key string) DirectedWrite {
	return DirectedWrite{Contract: "directed-actions/v0", Action: "send_teams_message", Effect: "visible_to_others",
		Agent: "agent", Reason: "asked", Identity: "a@example.invalid", Conversation: "48:notes",
		Recipients: []string{"a@example.invalid"}, Text: "hello", IntentKey: key, UserID: "u", TenantID: "t",
		AccountPin: "a@example.invalid", Outcome: "unknown", Phase: "in_flight", WatermarkID: "1", WatermarkAt: "2026-10-31T00:00:00Z", Version: "dev"}
}

func TestDirectedWritesStartEmptyAndRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	old := directedWriteNow
	directedWriteNow = func() time.Time { return time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { directedWriteNow = old })
	if got, err := s.DirectedWrites(ctx, 0); err != nil || len(got) != 0 {
		t.Fatal(got, err)
	}
	if _, ok, err := s.DirectedWriteByID(ctx, 1); ok || err != nil {
		t.Fatal(ok, err)
	}
	var seen []DirectedWrite
	id, err := s.BeginDirectedWrite(ctx, sampleDirectedWrite("k1"), func(earlier []DirectedWrite) error { seen = earlier; return nil })
	if err != nil || id != 1 || len(seen) != 0 {
		t.Fatal(id, err, seen)
	}
	second := sampleDirectedWrite("")
	second.Action, second.Effect, second.Conversation, second.Recipients = "save_mail_draft", "only_you", "", nil
	if _, err := s.BeginDirectedWrite(ctx, second, func(earlier []DirectedWrite) error { seen = earlier; return nil }); err != nil || len(seen) != 1 {
		t.Fatal(err, seen)
	}
	got, ok, err := s.DirectedWriteByID(ctx, 1)
	want := sampleDirectedWrite("k1")
	want.ID, want.CreatedAt, want.UpdatedAt = 1, directedWriteNow(), directedWriteNow()
	if err != nil || !ok || got.Text != "hello" || got.Recipients[0] != "a@example.invalid" || got.CreatedAt != want.CreatedAt ||
		got.UserID != "u" || got.WatermarkAt != want.WatermarkAt || got.Outcome != "unknown" || got.Phase != "in_flight" {
		t.Fatalf("%+v %v", got, err)
	}
	list, err := s.DirectedWrites(ctx, 1)
	if err != nil || len(list) != 1 || list[0].ID != 2 || list[0].Recipients == nil {
		t.Fatal(list, err)
	}
	if n := rowCount(t, s, `select count(*) from directed_write_events where cause='accepted'`); n != 2 {
		t.Fatal("events", n)
	}
}

func TestDirectedWriteGateRefusesWithoutInserting(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	refused := errors.New("refused")
	if _, err := s.BeginDirectedWrite(ctx, sampleDirectedWrite("k"), func([]DirectedWrite) error { return refused }); !errors.Is(err, refused) {
		t.Fatal(err)
	}
	if n := rowCount(t, s, `select count(*) from directed_writes`); n != 0 {
		t.Fatal("inserted", n)
	}
}

func TestDirectedWriteIntentKeyIsHeldUntilFailed(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	pass := func([]DirectedWrite) error { return nil }
	if _, err := s.BeginDirectedWrite(ctx, sampleDirectedWrite("k"), pass); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginDirectedWrite(ctx, sampleDirectedWrite("k"), pass); err == nil {
		t.Fatal("a held intent key was inserted twice")
	}
	ok, err := s.UpdateDirectedWrite(ctx, 1, "unknown", "in_flight", DirectedWriteChange{Outcome: "failed", Phase: "answered", ServerStatus: 400, ErrorCode: "BadRequest", Cause: "server_refused"})
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if _, err := s.BeginDirectedWrite(ctx, sampleDirectedWrite("k"), pass); err != nil {
		t.Fatal("a failed write kept its intent key", err)
	}
}

func TestDirectedWriteUpdateIsCompareAndSet(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	if _, err := s.BeginDirectedWrite(ctx, sampleDirectedWrite(""), func([]DirectedWrite) error { return nil }); err != nil {
		t.Fatal(err)
	}
	change := DirectedWriteChange{Outcome: "sent", Phase: "verified", Verification: "found", ServerID: "m1", ServerStatus: 201, Cause: "verified"}
	if ok, err := s.UpdateDirectedWrite(ctx, 1, "unknown", "answered", change); ok || err != nil {
		t.Fatal("updated from the wrong phase", ok, err)
	}
	if ok, err := s.UpdateDirectedWrite(ctx, 1, "unknown", "in_flight", change); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if ok, err := s.UpdateDirectedWrite(ctx, 1, "unknown", "in_flight", change); ok || err != nil {
		t.Fatal("updated twice", ok, err)
	}
	// An empty server id or status keeps the stored one.
	if ok, err := s.UpdateDirectedWrite(ctx, 1, "sent", "verified", DirectedWriteChange{Outcome: "sent", Phase: "settled", Verification: "person", Cause: "person"}); !ok || err != nil {
		t.Fatal(ok, err)
	}
	got, _, _ := s.DirectedWriteByID(ctx, 1)
	if got.ServerID != "m1" || got.ServerStatus != 201 || got.Phase != "settled" || got.Verification != "person" {
		t.Fatalf("%+v", got)
	}
	if n := rowCount(t, s, `select count(*) from directed_write_events where write_id=1`); n != 3 {
		t.Fatal("events", n)
	}
}

func TestDirectedWriteLogIsAppendOnly(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	if _, err := s.BeginDirectedWrite(ctx, sampleDirectedWrite(""), func([]DirectedWrite) error { return nil }); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`update directed_write_events set cause='x'`, `delete from directed_write_events`, `delete from directed_writes`} {
		if _, err := s.db.Exec(q); err == nil || !strings.Contains(err.Error(), "directed_write") {
			t.Fatal(q, err)
		}
	}
}

func TestDirectedWritesOnOlderOrDamagedArchives(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	s.qExec(t, `update schema_migrations set version=24`)
	if got, err := s.DirectedWrites(ctx, 0); err != nil || got != nil {
		t.Fatal("an older archive has no writes", got, err)
	}
	if _, ok, err := s.DirectedWriteByID(ctx, 1); ok || err != nil {
		t.Fatal(ok, err)
	}
	if _, err := s.BeginDirectedWrite(ctx, sampleDirectedWrite(""), nil); err == nil {
		t.Fatal("began a write on an archive without the log")
	}
	s.qExec(t, `update schema_migrations set version=26`)
	s.qExec(t, `drop table directed_write_events`)
	if _, err := s.DirectedWrites(ctx, 0); err == nil {
		t.Fatal("an incomplete log read as empty")
	}
}

func TestDirectedWriteFailuresAreReported(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	pass := func([]DirectedWrite) error { return nil }
	if _, err := s.BeginDirectedWrite(ctx, sampleDirectedWrite(""), pass); err != nil {
		t.Fatal(err)
	}
	s.qExec(t, `drop trigger directed_writes_no_delete`)
	s.qExec(t, `update directed_writes set recipients_json='{'`)
	if _, err := s.DirectedWrites(ctx, 0); err == nil {
		t.Fatal("bad recipients read")
	}
	if _, _, err := s.DirectedWriteByID(ctx, 1); err == nil {
		t.Fatal("bad recipients read")
	}
	if _, err := s.BeginDirectedWrite(ctx, sampleDirectedWrite(""), pass); err == nil {
		t.Fatal("began over an unreadable log")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.BeginDirectedWrite(cancelled, sampleDirectedWrite(""), pass); err == nil {
		t.Fatal("began with a cancelled context")
	}
	if _, err := s.UpdateDirectedWrite(cancelled, 1, "unknown", "in_flight", DirectedWriteChange{}); err == nil {
		t.Fatal("updated with a cancelled context")
	}
	if _, err := s.UpdateDirectedWrite(ctx, 1, "unknown", "in_flight", DirectedWriteChange{Outcome: "bogus", Phase: "answered"}); err == nil {
		t.Fatal("an invalid outcome was stored")
	}
	s.qExec(t, `update directed_writes set recipients_json='[]'`)
	if _, err := s.BeginDirectedWrite(ctx, DirectedWrite{Effect: "nobody"}, pass); err == nil {
		t.Fatal("an invalid effect was stored")
	}
	s.qExec(t, `create trigger refuse_events before insert on directed_write_events begin select raise(abort,'no'); end`)
	if _, err := s.BeginDirectedWrite(ctx, sampleDirectedWrite(""), pass); err == nil {
		t.Fatal("began without its event")
	}
	if _, err := s.UpdateDirectedWrite(ctx, 1, "unknown", "in_flight", DirectedWriteChange{Outcome: "sent", Phase: "answered"}); err == nil {
		t.Fatal("updated without its event")
	}
}

func TestDirectedWriteSchemaFailuresAreReported(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	s.qExec(t, `alter table directed_writes rename column text to body`)
	if _, err := s.DirectedWrites(ctx, 0); err == nil {
		t.Fatal("read a log without its columns")
	}
	s.qExec(t, `drop table schema_migrations`)
	if _, err := s.BeginDirectedWrite(ctx, sampleDirectedWrite(""), func([]DirectedWrite) error { return nil }); err == nil {
		t.Fatal("began without a schema version")
	}
}
