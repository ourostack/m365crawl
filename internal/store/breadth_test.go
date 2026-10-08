package store

import (
	"context"
	"testing"
)

func TestTeamsBreadth(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	if b := must(s.TeamsBreadth(ctx)); b != (TeamsBreadth{}) {
		t.Fatalf("empty archive = %+v", b)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := s.db.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`insert into accounts(tenant_id, user_id) values ('t','u1'), ('t','u2')`)
	conv := func(user, id, kind string) {
		exec(`insert into conversations(tenant_id, user_id, id, kind, updated_at) values ('t',?,?,?,'x')`, user, id, kind)
	}
	msg := func(user, conv, id, typ string, deleted bool) {
		var del any
		if deleted {
			del = "x"
		}
		exec(`insert into messages(tenant_id, user_id, conversation_id, id, sent_at, deleted_at, message_type, updated_at) values ('t',?,?,?,'x',?,?,'x')`, user, conv, id, del, typ)
	}
	conv("u1", "19:team@thread.v2", "Space")
	conv("u1", "19:general@thread.tacv2", "Topic")
	conv("u1", "19:legacy@thread.skype", "Chat")    // a channel by its id, whatever its kind says
	conv("u1", "19:planning@thread.tacv2", "topic") // kinds compare without case
	conv("u1", "19:group@thread.v2", "Chat")
	conv("u1", "19:a_b@unq.gbl.spaces", "OneOnOne")
	conv("u1", "19:meeting_x@thread.v2", "meeting")
	conv("u1", "48:notes", "Chat")
	conv("u1", "48:notifications", "Chat") // a system pseudo-conversation: never counted
	conv("u1", "48:calllogs", "Chat")
	// A conversation both accounts hold counts once.
	conv("u1", "19:shared@thread.v2", "Chat")
	conv("u2", "19:shared@thread.v2", "Chat")
	conv("u2", "19:team2@thread.v2", "Space")

	msg("u1", "19:group@thread.v2", "1", "RichText/Html", false)
	msg("u1", "19:meeting_x@thread.v2", "2", "RichText/Media_CallRecording", false)
	msg("u1", "19:meeting_x@thread.v2", "3", "RichText/Media_CallTranscript", false)
	msg("u1", "19:meeting_x@thread.v2", "4", "RichText/Media_CallRecording", true) // a deleted recording is no recording
	msg("u1", "19:shared@thread.v2", "5", "RichText/Html", false)
	msg("u2", "19:shared@thread.v2", "5", "RichText/Html", false)             // the same message in the other account
	msg("u1", "48:notifications", "1", "RichText/Html", false)                // mirrors a real message
	msg("u1", "48:notifications", "9", "RichText/Media_CallRecording", false) // and is never counted
	exec(`insert into people(tenant_id, id) values ('t','p1'), ('t','p2'), ('t2','p2')`)

	got := must(s.TeamsBreadth(ctx))
	want := TeamsBreadth{Accounts: 2, Teams: 2, Channels: 3, Chats: 4, Meetings: 1, Messages: 4, People: 2, Recordings: 1, Transcripts: 1}
	if got != want {
		t.Fatalf("got  %+v\nwant %+v", got, want)
	}
	exec(`drop table people`)
	if _, err := s.TeamsBreadth(ctx); err == nil {
		t.Fatal("no error from an archive without people")
	}
}
