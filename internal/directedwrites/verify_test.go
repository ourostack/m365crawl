package directedwrites

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	da "github.com/ourostack/m365crawl/internal/directedactions"
	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/workiq"
)

// unknownWrite sends "hello there" to Notes to self with a lost answer, leaving an unknown write 1.
func unknownWrite(t *testing.T) (*server, Options) {
	t.Helper()
	srv, opts := newServer(t, true, true)
	turnOn(t, opts)
	srv.answer = workiq.WriteAnswer{Kind: workiq.WriteUnknown, Code: "workiq_session_lost"}
	got, err := Send(context.Background(), opts, notesRequest("hello there"))
	if err != nil || got.Outcome != da.Unknown {
		t.Fatal(got, err)
	}
	srv.opened, srv.fetched = nil, nil
	return srv, opts
}

func later(d time.Duration) func() time.Time {
	at := time.Now().Add(d)
	return func() time.Time { return at }
}

func TestVerifyFindsTheOneMatchingMessage(t *testing.T) {
	srv, opts := unknownWrite(t)
	ctx := context.Background()
	srv.pages[notesList] = reply{data: list(
		message("1702", "2026-10-31T11:02:00Z", "someone", "hello there"),
		message("1701", "2026-10-31T11:01:00Z", "u", "hello   there"),
		message("100", "2026-10-31T10:00:00Z", "u", "hello there"))}
	got, err := Verify(ctx, opts, 1)
	if err != nil || got.Outcome != da.Sent || got.Verification != "found" || got.ServerID != "1701" || got.ExitClass != da.ExitDone || got.RequestID != "1" {
		t.Fatalf("%+v %v", got, err)
	}
	if srv.opened[0] != "ME@example.invalid" {
		t.Fatal("verify was not pinned to the write's account", srv.opened)
	}
	if row := logged(t, opts)[0]; row.Outcome != "sent" || row.ServerID != "1701" || row.ErrorCode != "workiq_session_lost" {
		t.Fatalf("%+v", row)
	}
	srv.opened = nil
	if got, err := Verify(ctx, opts, 1); err != nil || got.Outcome != da.Sent || len(srv.opened) != 0 {
		t.Fatal("a settled write started WorkIQ again", got, err)
	}
}

func TestVerifyProvesNotSentOnlyWithAllTheEvidence(t *testing.T) {
	ctx := context.Background()
	other := message("1701", "2026-10-31T11:01:00Z", "someone", "unrelated")
	watermark := message("100", "2026-10-31T10:00:00Z", "u", "old")
	for _, tc := range []struct {
		name    string
		pages   map[string]reply
		after   time.Duration
		outcome da.Outcome
	}{
		{"proof", map[string]reply{notesList: {data: list(other, watermark)}}, 11 * time.Minute, da.Failed},
		{"too soon", map[string]reply{notesList: {data: list(other, watermark)}}, time.Minute, da.Unknown},
		{"end of chat", map[string]reply{notesList: {data: list(other)}}, 11 * time.Minute, da.Failed},
		{"a message from the person", map[string]reply{notesList: {data: list(message("1701", "2026-10-31T11:01:00Z", "u", "other words"), watermark)}}, 11 * time.Minute, da.Unknown},
		{"a deleted message from the person", map[string]reply{notesList: {data: list(`{"id":"1701","createdDateTime":"2026-10-31T11:01:00Z","deletedDateTime":"2026-10-31T11:05:00Z","from":{"user":{"id":"u"}},"body":{"content":"hello there"}}`, watermark)}}, 11 * time.Minute, da.Unknown},
		{"a message without a time", map[string]reply{notesList: {data: list(`{"id":"1701","from":{"user":{"id":"u"}},"body":{"content":"x"}}`, watermark)}}, 11 * time.Minute, da.Unknown},
		{"two matches", map[string]reply{notesList: {data: list(message("1702", "2026-10-31T11:02:00Z", "u", "hello there"), message("1701", "2026-10-31T11:01:00Z", "u", "hello there"), watermark)}}, 11 * time.Minute, da.Unknown},
		{"listing never reaches the watermark", map[string]reply{notesList: {data: list(other), next: "/chats/48:notes/messages?$skiptoken=1"},
			"/chats/48:notes/messages?$skiptoken=1": {data: list(other), next: "/chats/48:notes/messages?$skiptoken=2"},
			"/chats/48:notes/messages?$skiptoken=2": {data: list(other), next: "/chats/48:notes/messages?$skiptoken=3"},
			"/chats/48:notes/messages?$skiptoken=3": {data: list(other), next: "/chats/48:notes/messages?$skiptoken=4"},
			"/chats/48:notes/messages?$skiptoken=4": {data: list(other), next: "/chats/48:notes/messages?$skiptoken=5"}}, 11 * time.Minute, da.Unknown},
		{"second page reaches it", map[string]reply{notesList: {data: list(other), next: "/chats/48:notes/messages?$skiptoken=1"},
			"/chats/48:notes/messages?$skiptoken=1": {data: list(watermark)}}, 11 * time.Minute, da.Failed},
		{"listing fails", map[string]reply{notesList: {status: 503, err: errs.Usage("busy")}}, 11 * time.Minute, da.Unknown},
		{"listing unreadable", map[string]reply{notesList: {data: `{"value":1}`}}, 11 * time.Minute, da.Unknown},
		{"message unreadable", map[string]reply{notesList: {data: list(`{"id":""}`)}}, 11 * time.Minute, da.Unknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, opts := unknownWrite(t)
			for k, v := range tc.pages {
				srv.pages[k] = v
			}
			now = later(tc.after)
			got, err := Verify(ctx, opts, 1)
			if err != nil || got.Outcome != tc.outcome {
				t.Fatalf("%+v %v", got, err)
			}
			if tc.outcome == da.Failed && (got.Verification != "not_sent_proven" || got.ExitClass != da.ExitFailed) {
				t.Fatalf("%+v", got)
			}
		})
	}
}

func TestVerifyIgnoresAMessageAnotherWriteHolds(t *testing.T) {
	srv, opts := unknownWrite(t)
	ctx := context.Background()
	// Write 2 is the same text, sent and verified as message 1701.
	srv.answer = workiq.WriteAnswer{Kind: workiq.WriteCreated, Status: 201, ServerID: "1701", ChatID: "48:notes", FromUserID: "u"}
	srv.pages["/chats/48:notes/messages/1701"] = reply{data: message("1701", "2026-10-31T11:01:00Z", "u", "hello there")}
	req := notesRequest("hello there")
	req.Identity = "me.upn@example.invalid" // an alias is still the same request
	if got, _ := Send(ctx, opts, req); got.Code != da.CodeOutcomeUnknown {
		t.Fatalf("an alias dodged the unknown guard: %+v", got)
	}
	store1 := store.DirectedWrite{Contract: da.Contract, Action: SendTeamsMessage, Effect: "visible_to_others", Agent: "a", Reason: "r",
		Identity: selfAddr, Conversation: "48:notes", Recipients: []string{selfAddr}, Text: "hello there", UserID: "u", Outcome: "sent", Phase: "verified", ServerID: "1701"}
	if err := store.WithWorkIQWriter(ctx, opts.DBPath, func(ctx context.Context, s *store.Store) error {
		_, err := s.BeginDirectedWrite(ctx, store1, func([]store.DirectedWrite) error { return nil })
		return err
	}); err != nil {
		t.Fatal(err)
	}
	srv.pages[notesList] = reply{data: list(message("1701", "2026-10-31T11:01:00Z", "u", "hello there"), message("100", "2026-10-31T10:00:00Z", "u", "old"))}
	now = later(time.Hour)
	if got, err := Verify(ctx, opts, 1); err != nil || got.Outcome != da.Unknown {
		t.Fatalf("claimed a message another write holds: %+v %v", got, err)
	}
}

func TestVerifyByServerID(t *testing.T) {
	ctx := context.Background()
	srv, opts := newServer(t, true, true)
	turnOn(t, opts)
	srv.pages["/chats/48:notes/messages/1700"] = reply{status: 503, err: errs.Usage("busy")}
	if got, _ := Send(ctx, opts, notesRequest("hello there")); got.Outcome != da.Unknown || got.ServerID != "1700" {
		t.Fatal(got)
	}
	if got, err := Verify(ctx, opts, 1); err != nil || got.Outcome != da.Unknown {
		t.Fatal(got, err)
	}
	srv.pages["/chats/48:notes/messages/1700"] = reply{data: message("1700", "2026-10-31T11:00:00Z", "u", "hello there")}
	if got, err := Verify(ctx, opts, 1); err != nil || got.Outcome != da.Sent || got.Verification != "found" || got.ServerID != "1700" {
		t.Fatal(got, err)
	}
}

func TestVerifyRefusals(t *testing.T) {
	ctx := context.Background()
	srv, opts := unknownWrite(t)
	if _, err := Verify(ctx, opts, 9); err == nil || !strings.Contains(err.Error(), "no directed write 9") {
		t.Fatal(err)
	}
	srv.me["ME@example.invalid"] = `{"id":"someone-else","mail":"me@example.invalid"}`
	_, err := Verify(ctx, opts, 1)
	requireCode(t, err, "workiq_identity_mismatch")
	srv.openErr["ME@example.invalid"] = errs.Usage("gone")
	if _, err := Verify(ctx, opts, 1); err == nil {
		t.Fatal("verified without WorkIQ")
	}
	calls := 0
	srv.openErr = map[string]error{}
	srv.me["ME@example.invalid"] = meJSON
	readArchive = func(ctx context.Context, path string, fn func(*store.Store) error) error {
		calls++
		if calls == 3 {
			return errors.New("broken log")
		}
		return readOnly(ctx, path, fn)
	}
	if _, err := Verify(ctx, opts, 1); err == nil || err.Error() != "broken log" {
		t.Fatal(err)
	}
	readArchive = func(context.Context, string, func(*store.Store) error) error { return errors.New("broken") }
	if _, err := Verify(ctx, opts, 1); err == nil || err.Error() != "broken" {
		t.Fatal(err)
	}
	readArchive = readOnly
	if err := store.WithWorkIQWriter(ctx, opts.DBPath, func(ctx context.Context, s *store.Store) error { return s.SetWorkIQEnabled(ctx, false) }); err != nil {
		t.Fatal(err)
	}
	_, err = Verify(ctx, opts, 1)
	requireCode(t, err, "workiq_disabled")

	srv, opts = newServer(t, true, false)
	turnOn(t, opts)
	srv.answer = workiq.WriteAnswer{Kind: workiq.WriteUnknown, Code: "workiq_timeout"}
	if got, _ := Send(ctx, opts, draftRequest()); got.Outcome != da.Unknown {
		t.Fatal(got)
	}
	srv.opened = nil
	if got, err := Verify(ctx, opts, 1); err != nil || got.Outcome != da.Unknown || len(srv.opened) != 0 {
		t.Fatal("a draft without a server id is the person's to settle", got, err)
	}
}

func TestSettleIsForUnknownWritesOnly(t *testing.T) {
	ctx := context.Background()
	_, opts := unknownWrite(t)
	got, err := Settle(ctx, opts, 1, false)
	if err != nil || got.Outcome != da.Failed || got.Phase != "settled" || got.Verification != "person" || got.ExitClass != da.ExitFailed {
		t.Fatal(got, err)
	}
	if _, err := Settle(ctx, opts, 1, true); err == nil || !strings.Contains(err.Error(), "already failed") {
		t.Fatal(err)
	}
	if _, err := Settle(ctx, opts, 2, true); err == nil {
		t.Fatal("settled a missing write")
	}
	_, opts = unknownWrite(t)
	if got, err := Settle(ctx, opts, 1, true); err != nil || got.Outcome != da.Sent || got.ExitClass != da.ExitDone {
		t.Fatal(got, err)
	}
	_, opts = unknownWrite(t)
	writeArchive = func(context.Context, string, func(context.Context, *store.Store) error) error {
		return errs.Usage("locked")
	}
	if _, err := Settle(ctx, opts, 1, true); err == nil {
		t.Fatal("settled without saving")
	}
	writeArchive = func(ctx context.Context, path string, fn func(context.Context, *store.Store) error) error {
		if err := store.WithWorkIQWriter(ctx, path, func(ctx context.Context, s *store.Store) error {
			_, err := s.UpdateDirectedWrite(ctx, 1, "unknown", "answered", store.DirectedWriteChange{Outcome: "sent", Phase: "verified", Cause: "race"})
			return err
		}); err != nil {
			return err
		}
		return store.WithWorkIQWriter(ctx, path, fn)
	}
	if _, err := Settle(ctx, opts, 1, false); err == nil || !strings.Contains(err.Error(), "changed while") {
		t.Fatal(err)
	}
}

func TestStatusReadsTheSwitchAndLogOffline(t *testing.T) {
	ctx := context.Background()
	srv, opts := unknownWrite(t)
	srv.opened = nil
	got, err := Status(ctx, opts, 10)
	if err != nil || got.Writes != "on" || got.ChangedAt == nil || got.Unknown != 1 || len(got.Items) != 1 ||
		got.Items[0].Text != "hello there" || got.Items[0].Agent != "agent" || got.Items[0].RequestID != "1" || len(srv.opened) != 0 {
		t.Fatalf("%+v %v", got, err)
	}
	if got, _ := Status(ctx, opts, 0); len(got.Items) != 0 || got.Unknown != 1 {
		t.Fatal(got)
	}
	empty, err := Status(ctx, Options{DBPath: t.TempDir() + "/none.db"}, 10)
	if err != nil || empty.Writes != "off" || empty.ChangedAt != nil || empty.Items == nil || len(empty.Items) != 0 {
		t.Fatal(empty, err)
	}
	readArchive = func(context.Context, string, func(*store.Store) error) error { return errors.New("broken") }
	if _, err := Status(ctx, opts, 10); err == nil {
		t.Fatal("status hid a broken archive")
	}
	if _, err := Write(ctx, opts, 1); err == nil || err.Error() != "broken" {
		t.Fatal(err)
	}
	readArchive = readOnly
	if _, err := Write(ctx, Options{DBPath: t.TempDir() + "/none.db"}, 1); err == nil || !strings.Contains(err.Error(), "no directed write") {
		t.Fatal(err)
	}
}

func TestTargetReadsWithoutWriting(t *testing.T) {
	ctx := context.Background()
	srv, opts := newServer(t, true, true)
	got, err := Target(ctx, opts, " 48:notes ", "")
	if err != nil || !got.Known || got.Chat != "48:notes" || got.Identity != selfAddr || got.Version != "100" ||
		got.WatermarkAt != "2026-10-31T10:00:00Z" || len(got.Recipients) != 1 || len(srv.created) != 0 {
		t.Fatalf("%+v %v", got, err)
	}
	got, err = Target(ctx, opts, "48:saved", selfAddr)
	if err != nil || got.Known || len(got.Recipients) != 0 || got.Recipients == nil {
		t.Fatalf("%+v %v", got, err)
	}
	srv.openErr[selfAddr] = errs.Usage("x")
	if _, err := Target(ctx, opts, "48:notes", selfAddr); err == nil {
		t.Fatal("read without WorkIQ")
	}
	_, opts = newServer(t, false, true)
	_, err = Target(ctx, opts, "48:notes", "")
	requireCode(t, err, "workiq_disabled")
}

func TestClassOfQueuedIsDone(t *testing.T) {
	if class, tell := classOf(da.Queued); class != da.ExitDone || tell {
		t.Fatal(class, tell)
	}
}

func TestVerifyKeepsAWriteWithAnUnreadableWatermark(t *testing.T) {
	_, opts := unknownWrite(t)
	ctx := context.Background()
	bad := store.DirectedWrite{Contract: da.Contract, Action: SendTeamsMessage, Effect: "visible_to_others", Agent: "a", Reason: "r",
		Identity: selfAddr, Conversation: "48:notes", Recipients: []string{selfAddr}, Text: "x", UserID: "u", AccountPin: selfAddr,
		Outcome: "unknown", Phase: "in_flight", WatermarkAt: "yesterday"}
	if err := store.WithWorkIQWriter(ctx, opts.DBPath, func(ctx context.Context, s *store.Store) error {
		_, err := s.BeginDirectedWrite(ctx, bad, func([]store.DirectedWrite) error { return nil })
		return err
	}); err != nil {
		t.Fatal(err)
	}
	now = later(time.Hour)
	if got, err := Verify(ctx, opts, 2); err != nil || got.Outcome != da.Unknown {
		t.Fatal(got, err)
	}
}

func TestTheDefaultSessionIsTheGuardedWorkIQWriter(t *testing.T) {
	t.Setenv("M365CRAWL_WORKIQ_BIN", "")
	_, err := openSession(context.Background(), workiq.Invocation{}, "")
	requireCode(t, err, "workiq_hermetic_required")
}

func TestSettleWaitsForAWriteThatMayStillBeSending(t *testing.T) {
	ctx := context.Background()
	_, opts := newServer(t, true, true)
	inFlight := store.DirectedWrite{Contract: da.Contract, Action: SendTeamsMessage, Effect: "visible_to_others", Agent: "a", Reason: "r",
		Identity: selfAddr, Conversation: "48:notes", Recipients: []string{selfAddr}, Text: "x", UserID: "u", AccountPin: selfAddr,
		Outcome: "unknown", Phase: "in_flight"}
	if err := store.WithWorkIQWriter(ctx, opts.DBPath, func(ctx context.Context, s *store.Store) error {
		_, err := s.BeginDirectedWrite(ctx, inFlight, func([]store.DirectedWrite) error { return nil })
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := Settle(ctx, opts, 1, false); err == nil || !strings.Contains(err.Error(), "may still be in progress") {
		t.Fatal(err)
	}
	now = later(11 * time.Minute)
	if got, err := Settle(ctx, opts, 1, false); err != nil || got.Outcome != da.Failed {
		t.Fatal(got, err)
	}
}
