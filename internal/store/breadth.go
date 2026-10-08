package store

import "context"

// TeamsBreadth counts what the archive holds from Teams, for the doctor snapshot. Each count is
// of distinct ids, so a conversation or message that two accounts both hold counts once, and the
// system pseudo-conversations (notifications, call log, annotations), whose messages mirror real
// ones, are never counted.
//
// A team is a conversation of kind Space; a channel is any other conversation the store's channel
// test (isChannelCond) matches; a meeting is a meeting chat; every other conversation is a chat,
// one-to-one and group chats alike. Messages, recordings and transcripts leave deleted ones out;
// a recording or transcript is the meeting message that announces one.
type TeamsBreadth struct {
	Accounts, Teams, Channels, Chats, Meetings int
	Messages, People, Recordings, Transcripts  int
}

// breadthQuery reads the counts in one pass over conversations and one over messages.
var breadthQuery = `select
  (select count(*) from accounts),
  b.teams, b.channels, b.chats, b.meetings,
  m.messages, m.recordings, m.transcripts,
  (select count(distinct id) from people)
from (
  select
    count(distinct case when lower(c.kind)='space' then c.id end) as teams,
    count(distinct case when lower(c.kind)<>'space' and ` + isChannelCond + ` then c.id end) as channels,
    count(distinct case when not ` + isChannelCond + ` and lower(c.kind)<>'meeting' then c.id end) as chats,
    count(distinct case when not ` + isChannelCond + ` and lower(c.kind)='meeting' then c.id end) as meetings
  from conversations c where ` + notSystemCond(`c.id`) + `
) b, (
  select
    count(distinct k) as messages,
    count(distinct case when message_type='RichText/Media_CallRecording' then k end) as recordings,
    count(distinct case when message_type='RichText/Media_CallTranscript' then k end) as transcripts
  from (select conversation_id||char(31)||id as k, message_type from messages
        where deleted_at is null and ` + notSystemCond(`conversation_id`) + `)
) m`

// TeamsBreadth counts the archive's Teams data.
func (s *Store) TeamsBreadth(ctx context.Context) (TeamsBreadth, error) {
	var b TeamsBreadth
	err := s.db.QueryRowContext(ctx, breadthQuery).
		Scan(&b.Accounts, &b.Teams, &b.Channels, &b.Chats, &b.Meetings, &b.Messages, &b.Recordings, &b.Transcripts, &b.People)
	return b, err
}
