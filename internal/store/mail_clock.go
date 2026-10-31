package store

import (
	"maps"
	"slices"
	"strconv"
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

// sentWhere is a condition on m that holds for a message in a folder of kind sent (with its
// arguments), or "" when the archive has no such folder. Folder keys are integers, written inline.
func (c mailCopies) sentWhere() (string, []any) {
	keys := map[string][]uint32{}
	for id, kind := range c.sets.kind {
		if kind == "sent" {
			keys[id.account] = append(keys[id.account], id.key)
		}
	}
	var conds []string
	var args []any
	for _, account := range slices.Sorted(maps.Keys(keys)) {
		ks := keys[account]
		slices.Sort(ks)
		list := make([]string, len(ks))
		for i, k := range ks {
			list[i] = strconv.FormatUint(uint64(k), 10)
		}
		conds = append(conds, `(m.account=? and m.folder_key in (`+strings.Join(list, ",")+`))`)
		args = append(args, account)
	}
	if len(conds) == 0 {
		return "", nil
	}
	return `(` + strings.Join(conds, " or ") + `)`, args
}

// datedRows is a compound select of (rid, at) over mail_messages m: every message that meets cond
// (a condition on m alone, with condArgs; "" for none) whose dated time (datedAt) lies in [since,
// until), a zero end being open, with that time as stored text. Its parts can each use an index:
// received-time folders filter and sort on received_at (mail_messages_received or
// mail_messages_folder), Sent Items is found through mail_messages_folder, and the few messages
// whose received time was chosen across copies are looked up by rowid from a list of values,
// which the other parts leave out. A caller adds ORDER BY 2, 1 to read it in order.
func (c mailCopies) datedRows(cond string, condArgs []any, since, until time.Time) (string, []any) {
	if cond == "" {
		cond = `1`
	}
	sent, sentArgs := c.sentWhere()
	ranged := func(expr string, conds []string, args []any) ([]string, []any) {
		if !since.IsZero() {
			conds, args = append(conds, expr+`>=?`), append(args, since.UTC().Format(timeLayout))
		}
		if !until.IsZero() {
			conds, args = append(conds, expr+`<?`), append(args, until.UTC().Format(timeLayout))
		}
		return conds, args
	}
	chosen := ""
	if len(c.received) > 0 {
		chosen = `m.rowid not in (` + joinIDs(slices.Sorted(maps.Keys(c.received))) + `)`
	}
	part := func(from, at string, conds []string, args []any) (string, []any) {
		conds, args = ranged(at, conds, args)
		return `select m.rowid as rid, ` + at + ` as at from ` + from + ` where ` + strings.Join(conds, " and "), args
	}
	var parts []string
	var args []any
	add := func(q string, a []any) { parts, args = append(parts, q), append(args, a...) }

	conds, a := []string{`(` + cond + `)`}, slices.Clone(condArgs)
	if sent != "" {
		conds, a = append(conds, `not `+sent), append(a, sentArgs...)
	}
	if chosen != "" {
		conds = append(conds, chosen)
	}
	add(part(`mail_messages m`, `m.received_at`, conds, a))
	if sent != "" {
		conds, a = []string{`(` + cond + `)`, sent}, append(slices.Clone(condArgs), sentArgs...)
		if chosen != "" {
			conds = append(conds, chosen)
		}
		add(part(`mail_messages m`, `coalesce(m.sent_at, m.received_at)`, conds, a))
	}
	if chosen != "" {
		// The time appears in the select list and in each bound, each time with the sent
		// condition's arguments; placeholders are bound in the order they appear.
		at, atArgs := `d.column2`, []any(nil)
		if sent != "" {
			at, atArgs = `(case when `+sent+` and m.sent_at is not null then m.sent_at else d.column2 end)`, sentArgs
		}
		ids := slices.Sorted(maps.Keys(c.received))
		values := make([]string, len(ids))
		a := slices.Clone(atArgs)
		for i, id := range ids {
			values[i] = `(?,?)`
			a = append(a, id, c.received[id].UTC().Format(timeLayout))
		}
		a = append(a, condArgs...)
		conds := []string{`(` + cond + `)`}
		for _, b := range []struct {
			op string
			t  time.Time
		}{{`>=?`, since}, {`<?`, until}} {
			if !b.t.IsZero() {
				conds = append(conds, at+b.op)
				a = append(append(a, atArgs...), b.t.UTC().Format(timeLayout))
			}
		}
		add(`select m.rowid as rid, `+at+` as at from (values `+strings.Join(values, ",")+`) d cross join mail_messages m on m.rowid=d.column1 where `+strings.Join(conds, " and "), a)
	}
	return strings.Join(parts, ` union all `), args
}
