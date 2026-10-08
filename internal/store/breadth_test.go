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
	for i, kind := range []string{"Space", "Topic", "Topic", "Chat", "Chat", "Chat", "Meeting", "OneOnOne"} {
		exec(`insert into conversations(tenant_id, user_id, id, kind, updated_at) values ('t','u1',?,?,'x')`, string(rune('a'+i)), kind)
	}
	for i, typ := range []string{"RichText/Html", "RichText/Html", "RichText/Media_CallRecording", "RichText/Media_CallTranscript"} {
		exec(`insert into messages(tenant_id, user_id, conversation_id, id, sent_at, message_type, updated_at) values ('t','u1','g',?, 'x', ?, 'x')`, string(rune('a'+i)), typ)
	}
	// A deleted recording is no recording.
	exec(`insert into messages(tenant_id, user_id, conversation_id, id, sent_at, deleted_at, message_type, updated_at) values ('t','u1','g','z','x','x','RichText/Media_CallRecording','x')`)
	exec(`insert into people(tenant_id, id) values ('t','p1'), ('t','p2'), ('t','p3')`)
	got := must(s.TeamsBreadth(ctx))
	want := TeamsBreadth{Accounts: 2, Teams: 1, Channels: 2, Chats: 3, Meetings: 1, Conversations: 8, Messages: 5, People: 3, Recordings: 1, Transcripts: 1}
	if got != want {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
	exec(`drop table people`)
	if _, err := s.TeamsBreadth(ctx); err == nil {
		t.Fatal("no error from an archive without people")
	}
}
