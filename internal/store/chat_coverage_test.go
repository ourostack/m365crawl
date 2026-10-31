package store

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"
)

const (
	covTenant, covUser = "t1", "u1"
	covAccount         = covTenant + "/" + covUser
	covSelf            = "8:orgid:" + covUser
)

var covT0 = time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)

func covConv(t *testing.T, s *Store, id, kind string, last time.Time) {
	t.Helper()
	must(s.db.ExecContext(context.Background(), `insert into conversations(tenant_id,user_id,id,kind,last_message_at,updated_at) values(?,?,?,?,?,?)`,
		covTenant, covUser, id, kind, fmtTime(last), nowText()))
}

// covMsg adds a message; seq 0 stores raw JSON without a sequence number.
func covMsg(t *testing.T, s *Store, conv, id, sender, typ string, at time.Time, seq int, deleted bool) {
	t.Helper()
	raw := `{"id":"` + id + `"}`
	if seq > 0 {
		raw = fmt.Sprintf(`{"id":%q,"sequenceId":%d}`, id, seq)
	}
	var del any
	if deleted {
		del = fmtTime(at)
	}
	must(s.db.ExecContext(context.Background(), `insert into messages(tenant_id,user_id,conversation_id,id,sender_id,message_type,sent_at,deleted_at,raw_json,updated_at) values(?,?,?,?,?,?,?,?,?,?)`,
		covTenant, covUser, conv, id, sender, typ, fmtTime(at), del, raw, nowText()))
}

func TestChatEvidenceCountsAuthoredMessagesPerScope(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	day := covT0.Add(12 * time.Hour)
	covConv(t, s, "19:chat@unq.gbl.spaces", "Chat", day)
	covConv(t, s, "19:meeting_x@thread.v2", "Meeting", day)
	covConv(t, s, "19:chan@thread.tacv2", "Topic", day)
	covConv(t, s, "48:notes", "StreamOfNotes", day)
	covConv(t, s, "48:notifications", "Thread", day)
	covConv(t, s, "19:comm@EngageCommunity", "EngageCommunity", day)
	covMsg(t, s, "19:chat@unq.gbl.spaces", "c1", covSelf, "RichText/Html", day, 1, false)
	covMsg(t, s, "19:chat@unq.gbl.spaces", "c2", "8:orgid:other", "Text", day.Add(time.Minute), 2, false)
	covMsg(t, s, "19:chat@unq.gbl.spaces", "c3", covSelf, "RichText/Html", day.Add(2*time.Minute), 3, true)
	covMsg(t, s, "19:chat@unq.gbl.spaces", "c4", covSelf, "Event/Call", day.Add(3*time.Minute), 4, false)
	covMsg(t, s, "48:notes", "n1", covSelf, "Text", day, 1, false)
	covMsg(t, s, "48:notifications", "x1", covSelf, "Text", day, 1, false)
	covMsg(t, s, "19:meeting_x@thread.v2", "m1", covSelf, "RichText/Media_CallRecording", day, 1, false)
	covMsg(t, s, "19:meeting_x@thread.v2", "m2", covSelf, "Text", day, 2, false)
	covMsg(t, s, "19:chan@thread.tacv2", "h1", "8:orgid:other", "Text", day, 1, false)
	covMsg(t, s, "19:chat@unq.gbl.spaces", "old", covSelf, "Text", covT0.AddDate(0, 0, -3), 0, false)

	for scope, want := range map[string]ChatEvidence{
		ChatScopeChats:    {Items: 3, MyItems: 2, Deleted: 1},
		ChatScopeMeetings: {Items: 1, MyItems: 1},
		ChatScopeChannels: {Items: 1},
	} {
		got := must(s.ChatEvidence(ctx, covTenant, covUser, scope, covT0, covT0.AddDate(0, 0, 1)))
		if got != want {
			t.Fatalf("%s: %+v, want %+v", scope, got, want)
		}
	}
	// An open start (the zero time) counts everything before the end.
	if got := must(s.ChatEvidence(ctx, covTenant, covUser, ChatScopeChats, time.Time{}, covT0.AddDate(0, 0, 1))); got.Items != 4 {
		t.Fatalf("open start %+v", got)
	}
}

// A conversation Teams says was active after its newest archived message is behind when that
// message is before the range's end; sequence gaps around the range count as possibly missing,
// gaps outside it do not.
func TestChatEvidenceFindsBehindConversationsAndSequenceGaps(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	day, next := covT0, covT0.AddDate(0, 0, 1)
	at := func(h int) time.Time { return day.Add(time.Duration(h) * time.Hour) }
	// Behind: Teams knows of a message at 20:00, the archive's newest is 10:00.
	covConv(t, s, "19:behind@unq.gbl.spaces", "Chat", at(20))
	covMsg(t, s, "19:behind@unq.gbl.spaces", "b1", covSelf, "Text", at(10), 5, false)
	// Never archived anything, active today.
	covConv(t, s, "19:empty@unq.gbl.spaces", "Chat", at(9))
	// Up to date, but numbers 3..4 are missing inside the day, and 8 is missing the next day.
	covConv(t, s, "19:gap@unq.gbl.spaces", "Chat", next.Add(5*time.Hour))
	covMsg(t, s, "19:gap@unq.gbl.spaces", "g1", covSelf, "Text", day.Add(-time.Hour), 1, false)
	covMsg(t, s, "19:gap@unq.gbl.spaces", "g2", covSelf, "Text", at(1), 2, false)
	covMsg(t, s, "19:gap@unq.gbl.spaces", "g5", covSelf, "Text", at(5), 5, false)
	covMsg(t, s, "19:gap@unq.gbl.spaces", "g6", covSelf, "Text", next.Add(time.Hour), 6, false)
	covMsg(t, s, "19:gap@unq.gbl.spaces", "g7", covSelf, "Text", next.Add(2*time.Hour), 7, false)
	covMsg(t, s, "19:gap@unq.gbl.spaces", "g9", covSelf, "Text", next.Add(5*time.Hour), 9, false)
	covMsg(t, s, "19:gap@unq.gbl.spaces", "gx", covSelf, "Text", at(6), 0, false) // no number: left out
	// A gap the day lies inside: no message that day, numbers 11..12 missing around it.
	covConv(t, s, "19:around@unq.gbl.spaces", "Chat", next.Add(3*time.Hour))
	covMsg(t, s, "19:around@unq.gbl.spaces", "a1", covSelf, "Text", day.Add(-2*time.Hour), 10, false)
	covMsg(t, s, "19:around@unq.gbl.spaces", "a2", covSelf, "Text", next.Add(3*time.Hour), 13, false)
	// Quiet before the day: not a candidate.
	covConv(t, s, "19:quiet@unq.gbl.spaces", "Chat", day.Add(-48*time.Hour))
	covMsg(t, s, "19:quiet@unq.gbl.spaces", "q1", covSelf, "Text", day.Add(-50*time.Hour), 1, false)
	covMsg(t, s, "19:quiet@unq.gbl.spaces", "q3", covSelf, "Text", day.Add(-48*time.Hour), 3, false)

	got := must(s.ChatEvidence(ctx, covTenant, covUser, ChatScopeChats, day, next))
	want := ChatEvidence{Items: 4, MyItems: 4, Behind: 2, GapConversations: 2, PossiblyMissing: 4}
	if got != want {
		t.Fatalf("%+v, want %+v", got, want)
	}
	// The next day: behind's newest message is before it, so the missing tail is not in it; the
	// gap 8 is.
	got = must(s.ChatEvidence(ctx, covTenant, covUser, ChatScopeChats, next, next.AddDate(0, 0, 1)))
	if got.Behind != 0 || got.GapConversations != 2 || got.PossiblyMissing != 3 {
		t.Fatalf("next day %+v", got)
	}
	must0(s.Close())
	if _, err := s.ChatEvidence(ctx, covTenant, covUser, ChatScopeChats, day, next); err == nil {
		t.Fatal("a closed archive answered")
	}
}

func TestTeamsReadsUsesSuccessfulRunsOfTheAccount(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	if got := must(s.TeamsReads(ctx, covAccount)); len(got.Reads) != 0 || got.Legacy {
		t.Fatalf("empty archive %+v", got)
	}
	must(s.db.ExecContext(ctx, `insert into accounts(tenant_id,user_id,first_seen_at,last_synced_at) values(?,?,?,?)`, covTenant, covUser, fmtTime(covT0), fmtTime(covT0.AddDate(0, 0, 3))))
	got := must(s.TeamsReads(ctx, covAccount))
	if !got.Legacy || !slices.Equal(got.Reads, []time.Time{covT0, covT0.AddDate(0, 0, 3)}) {
		t.Fatalf("legacy %+v", got)
	}
	src := "WV2Profile_tfw|https_teams.microsoft.com_0"
	run := func(day int, status string, accounts ...string) {
		must0(s.RecordRun(ctx, Run{StartedAt: covT0.AddDate(0, 0, day), FinishedAt: covT0.AddDate(0, 0, day), Source: src, Status: status, SourceAccounts: accounts}))
	}
	run(2, "ok")
	run(1, "ok_with_omissions", covAccount)
	run(3, "ok", "t1/other")
	run(4, "unchanged")
	run(5, "failed")
	must0(s.RecordRun(ctx, Run{StartedAt: covT0.AddDate(0, 0, 6), FinishedAt: covT0, Source: "outlook|p|mail", Status: "ok"}))
	got = must(s.TeamsReads(ctx, covAccount))
	if got.Legacy || !slices.Equal(got.Reads, []time.Time{covT0.AddDate(0, 0, 1), covT0.AddDate(0, 0, 2)}) || !got.LastUnchanged.Equal(covT0.AddDate(0, 0, 4)) {
		t.Fatalf("runs %+v", got)
	}
	must(s.db.ExecContext(ctx, `update sync_runs set started_at='bad' where status='unchanged'`))
	if got := must(s.TeamsReads(ctx, covAccount)); !got.LastUnchanged.IsZero() {
		t.Fatalf("unreadable time %+v", got)
	}
	must0(s.Close())
	if _, err := s.TeamsReads(ctx, covAccount); err == nil {
		t.Fatal("a closed archive answered")
	}
}
