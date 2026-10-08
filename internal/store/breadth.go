package store

import "context"

// TeamsBreadth counts what the archive holds from Teams, for the doctor snapshot. Conversations
// are counted by kind: Space is a team, Topic a channel, Chat a chat and Meeting a meeting chat;
// Conversations counts every kind. Recordings and transcripts are the meeting messages that
// announce one, deleted ones left out.
type TeamsBreadth struct {
	Accounts, Teams, Channels, Chats, Meetings, Conversations int
	Messages, People, Recordings, Transcripts                 int
}

// TeamsBreadth counts the archive's Teams data.
func (s *Store) TeamsBreadth(ctx context.Context) (TeamsBreadth, error) {
	var b TeamsBreadth
	err := s.db.QueryRowContext(ctx, `select
  (select count(*) from accounts),
  (select count(*) from conversations where kind='Space'),
  (select count(*) from conversations where kind='Topic'),
  (select count(*) from conversations where kind='Chat'),
  (select count(*) from conversations where kind='Meeting'),
  (select count(*) from conversations),
  (select count(*) from messages),
  (select count(*) from people),
  (select count(*) from messages where message_type='RichText/Media_CallRecording' and deleted_at is null),
  (select count(*) from messages where message_type='RichText/Media_CallTranscript' and deleted_at is null)`).
		Scan(&b.Accounts, &b.Teams, &b.Channels, &b.Chats, &b.Meetings, &b.Conversations, &b.Messages, &b.People, &b.Recordings, &b.Transcripts)
	return b, err
}
