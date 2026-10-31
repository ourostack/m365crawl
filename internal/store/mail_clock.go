package store

import (
	"sort"
	"strings"
	"time"
)

// The clock a message is dated by: the time --since and --until select it by, the order lists
// sort it in, and the date the folder-copy check counts it on.
//
// Sent Items is dated by when the message was sent (sent_at): the received time Outlook stores on
// a sent message is often when its draft was started (days or weeks before it was sent) or a time
// after it was sent, on another day. Every other folder, Drafts included, is dated by when the
// message was received: a draft has not been sent. A Sent Items row without a send time falls back
// to its received time. The received time is the one chosen across the message's folder copies
// when they disagree (mailCopies.chooseReceived), otherwise the row's own.

// MailClock is the column a folder kind is dated by: sent_at for sent, received_at otherwise.
func MailClock(kind string) string {
	if kind == "sent" {
		return "sent_at"
	}
	return "received_at"
}

// datedAt is the time a row of a folder kind is dated by, given its received time (as chosen) and
// its send time.
func datedAt(kind string, received, sent time.Time) time.Time {
	if MailClock(kind) == "sent_at" && !sent.IsZero() {
		return sent
	}
	return received
}

// receivedAt is the received time a row is dated by: the one chosen for its message when its
// copies disagree and it stands for them, otherwise its own.
func (c mailCopies) receivedAt(rowid int64, own time.Time) time.Time {
	if t, ok := c.received[rowid]; ok {
		return t
	}
	return own
}

// dated is the SQL form of datedAt over mail_messages m joined to mail_folders f (mailJoin): join
// is a left join that carries the chosen received times (empty when there are none), with its
// arguments, and expr the time as stored text, comparable with timeLayout values.
func (c mailCopies) dated() (join string, args []any, expr string) {
	received := `m.received_at`
	if len(c.received) > 0 {
		ids := make([]int64, 0, len(c.received))
		for id := range c.received {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		values := make([]string, len(ids))
		for i, id := range ids {
			values[i] = `(?,?)`
			args = append(args, id, c.received[id].UTC().Format(timeLayout))
		}
		join = ` left join (values ` + strings.Join(values, ",") + `) d on d.column1=m.rowid`
		received = `coalesce(d.column2, m.received_at)`
	}
	return join, args, `(case when f.kind='sent' and m.sent_at is not null then m.sent_at else ` + received + ` end)`
}
