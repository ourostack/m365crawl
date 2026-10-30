package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Folder sets and the copies of one message they hold.
//
// Outlook for Mac can rebuild an account inside its store: a new account root, a new set of
// folders (Inbox, Sent Items and the rest) and new keys for the same messages. The sets can sit in
// the store side by side for days before the older ones disappear, and a read takes only one of
// them (outlookmail.Collect picks the set with the most header copies). The archive keeps every
// folder record it ever saw, so one account can hold several Sent Items, and a message re-keyed
// into a new set while the archive could not match it is archived twice.
//
// The current set of an account is the one its latest read used: the folders whose parent chain
// reaches the root that read recorded (meta outlook_mail_root:<account>). Every other archived
// folder of the account is an older copy. When no recorded root is known, or no archived folder
// hangs from it, every folder counts as current and nothing is called a copy.
//
// A row in an older set is a copy of another row when both have the same nonempty Message-ID
// (compared with normalizeMailMessageID), sit in folders of the same role (mailCopyRole) and in
// different sets, and the other row ranks higher: a row of the current set first, then the row
// seen most recently, then the row archived last. Rows of one set are never merged with each
// other, so a message in Sent Items and Inbox of one set (sent to oneself) stays two rows. No row
// is deleted or re-keyed: the copy keeps its id and its own gone and evicted marks.

// mailFolderID is one archived folder.
type mailFolderID struct {
	account string
	key     uint32
}

// mailSets says where every archived folder sits.
type mailSets struct {
	kind    map[mailFolderID]string
	set     map[mailFolderID]uint32 // the root key the folder's set hangs from
	current map[mailFolderID]bool
	older   map[string]bool // accounts with at least one folder outside the current set
}

// isCurrent is true for a folder of the current set and for a key with no folder row: a message
// whose folder object the store does not hold is never called an older copy.
func (m mailSets) isCurrent(account string, key uint32) bool {
	id := mailFolderID{account, key}
	if _, known := m.kind[id]; !known {
		return true
	}
	return m.current[id]
}

// maxFolderDepth bounds a parent chain, like outlookmail's account root walk.
const maxFolderDepth = 16

// folderChain follows parent links from key through the folders of one account. reaches is true
// when root is key itself or one of its ancestors, or the key the chain ends on (the first parent
// that is not a folder). end is that last key: the root of the folder's set.
func folderChain(parents map[uint32]uint32, key, root uint32) (end uint32, reaches bool) {
	cur := key
	for range maxFolderDepth {
		if cur == root {
			return cur, true
		}
		p := parents[cur]
		if _, isFolder := parents[p]; !isFolder {
			return p, p == root
		}
		cur = p
	}
	return cur, cur == root
}

// mailRoots reads the root every account's latest read recorded.
func mailRoots(ctx context.Context, q mailQuerier) (map[string]uint32, error) {
	roots := map[string]uint32{}
	err := mailEach(ctx, q, `select key, value from meta where key like ?`, []any{mailRootKey + "%"}, func(r *sql.Rows) error {
		var key, value string
		if err := r.Scan(&key, &value); err != nil {
			return err
		}
		root, err := strconv.ParseUint(value, 10, 32)
		if err != nil {
			return errors.New("invalid stored Outlook mail root key")
		}
		roots[strings.TrimPrefix(key, mailRootKey)] = uint32(root)
		return nil
	})
	return roots, err
}

// loadMailSets places every archived folder in its set.
func loadMailSets(ctx context.Context, q mailQuerier) (mailSets, error) {
	m := mailSets{kind: map[mailFolderID]string{}, set: map[mailFolderID]uint32{}, current: map[mailFolderID]bool{}, older: map[string]bool{}}
	roots, err := mailRoots(ctx, q)
	if err != nil {
		return m, err
	}
	parents := map[string]map[uint32]uint32{}
	err = mailEach(ctx, q, `select account, folder_key, parent_key, kind from mail_folders`, nil, func(r *sql.Rows) error {
		var id mailFolderID
		var parent uint32
		var kind string
		if err := r.Scan(&id.account, &id.key, &parent, &kind); err != nil {
			return err
		}
		if parents[id.account] == nil {
			parents[id.account] = map[uint32]uint32{}
		}
		parents[id.account][id.key] = parent
		m.kind[id] = kind
		return nil
	})
	if err != nil {
		return m, err
	}
	for account, ps := range parents {
		root, recorded := roots[account]
		anyCurrent := false
		for key := range ps {
			id := mailFolderID{account, key}
			end, reaches := folderChain(ps, key, root)
			m.set[id] = end
			if recorded && reaches {
				m.set[id], m.current[id], anyCurrent = root, true, true
			}
		}
		for key := range ps {
			id := mailFolderID{account, key}
			switch {
			case !anyCurrent:
				m.current[id] = true // no claim without a known current set
			case !m.current[id]:
				m.older[account] = true
			}
		}
	}
	return m, nil
}

// mailCopyRole is the role a folder kind plays when copies are matched: To Me holds copies of
// Inbox messages, so both are inbox; sent, drafts, archive and deleted are their own. Other and
// unknown folders have no role (their folders cannot be told apart by kind) and never match.
func mailCopyRole(kind string) string {
	switch kind {
	case "inbox", "to_me":
		return "inbox"
	case "sent", "drafts", "archive", "deleted":
		return kind
	}
	return ""
}

// normalizeMailMessageID is the comparison form of a Message-ID: trimmed, without matching outer
// angle brackets, in lower case. The stored header is never rewritten.
func normalizeMailMessageID(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && s[0] == '<' && s[len(s)-1] == '>' {
		s = strings.TrimSpace(s[1 : len(s)-1])
	}
	return strings.ToLower(s)
}

// mailCopies is the folder sets plus, for every row that is a copy, the row that stands for it.
type mailCopies struct {
	sets  mailSets
	newer map[int64]string // rowid of a copy to the id (account:detail_key) of the row that stands for it
}

// copyRow is one row as the copy rule compares it.
type copyRow struct {
	rowid   int64
	id      string
	set     uint32
	current bool
	seen    string
}

// loadMailCopies finds the copies. Only accounts with an older folder set are read row by row.
func loadMailCopies(ctx context.Context, q mailQuerier) (mailCopies, error) {
	sets, err := loadMailSets(ctx, q)
	c := mailCopies{sets: sets, newer: map[int64]string{}}
	if err != nil {
		return c, err
	}
	accounts := make([]string, 0, len(sets.older))
	for a := range sets.older {
		accounts = append(accounts, a)
	}
	sort.Strings(accounts)
	for _, account := range accounts {
		groups := map[string][]copyRow{}
		err := mailEach(ctx, q, `select rowid, detail_key, internet_message_id, folder_key, state_seen_at from mail_messages where account=? and internet_message_id<>''`, []any{account}, func(r *sql.Rows) error {
			var row copyRow
			var key, folder uint32
			var msgID string
			if err := r.Scan(&row.rowid, &key, &msgID, &folder, &row.seen); err != nil {
				return err
			}
			id := mailFolderID{account, folder}
			role, norm := mailCopyRole(sets.kind[id]), normalizeMailMessageID(msgID)
			if role == "" || norm == "" {
				return nil
			}
			row.id, row.set, row.current = fmt.Sprintf("%s:%d", account, key), sets.set[id], sets.current[id]
			groups[role+"\x00"+norm] = append(groups[role+"\x00"+norm], row)
			return nil
		})
		if err != nil {
			return c, err
		}
		for _, g := range groups {
			sort.Slice(g, func(i, j int) bool {
				a, b := g[i], g[j]
				if a.current != b.current {
					return a.current
				}
				if a.seen != b.seen {
					return a.seen > b.seen
				}
				return a.rowid > b.rowid
			})
			for i, r := range g {
				if r.current {
					continue
				}
				for _, n := range g[:i] {
					if n.set != r.set {
						c.newer[r.rowid] = n.id
						break
					}
				}
			}
		}
	}
	return c, nil
}

// copyIDs is the rowids of every copy, ascending, for a "not in" clause.
func (c mailCopies) copyIDs() []int64 {
	out := make([]int64, 0, len(c.newer))
	for id := range c.newer {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// notCopy is a condition on m.rowid that leaves the copies out, or "" when there are none.
func (c mailCopies) notCopy(col string) string {
	if len(c.newer) == 0 {
		return ""
	}
	return col + ` not in (` + joinIDs(c.copyIDs()) + `)`
}

// mark sets OlderCopy and CopyOf on rows.
func (c mailCopies) mark(rows []MailRow) {
	for i := range rows {
		rows[i].OlderCopy = !c.sets.isCurrent(rows[i].Account, rows[i].FolderKey)
		rows[i].CopyOf = c.newer[rows[i].Rowid]
	}
}

// MailCopyScope names an account and a folder kind to check.
type MailCopyScope struct{ Account, Kind string }

// MailCopyRange is the received-time range [Since, Until) a check covers (a zero end is open) and
// whether gone and evicted messages of the current folder set count, as in MailFilter. Messages of
// an older set count whatever their marks: once Outlook replaces a set, a trusted read finds its
// messages missing and marks them, which says nothing about whether the current set holds them.
type MailCopyRange struct {
	Since, Until                time.Time
	IncludeGone, IncludeEvicted bool
}

// MailCopyCheck compares, for one account and folder role over a range, the messages the current
// folder set holds with the ones only older sets hold, each message counted once.
//
// State is partial when an older copy holds a message dated inside the current copy's range (from
// CurrentOldestAt on) that the current copy lacks: the current cache is missing messages there.
// It is older_copy_only when the range lies before the current copy's range (or there is no
// current range) and an older copy's range reaches into it: only a copy Outlook replaced covered
// it. Otherwise it is consistent: nothing an older copy holds inside the current range is missing
// from the current copy (older copies may still add messages dated before it).
type MailCopyCheck struct {
	Account, Kind string // Kind is the role: inbox stands for Inbox and To Me
	// Copies is the number of archived folders of the role: the current one and the older ones.
	Copies                int
	Messages              int
	InCurrentCopy         int
	OnlyInOlderCopies     int
	LackingInCurrentRange int
	CurrentOldestAt       time.Time
	State                 string
}

// MailCopyChecks checks each distinct account and role in scopes that has an older folder copy,
// in the order of scopes. A kind with no role, or an account without older copies, gives nothing.
func (s *Store) MailCopyChecks(ctx context.Context, scopes []MailCopyScope, rng MailCopyRange) ([]MailCopyCheck, error) {
	sets, err := loadMailSets(ctx, s.db)
	if err != nil {
		return nil, err
	}
	var out []MailCopyCheck
	done := map[MailCopyScope]bool{}
	for _, sc := range scopes {
		k := MailCopyScope{sc.Account, mailCopyRole(sc.Kind)}
		if k.Kind == "" || done[k] || !sets.hasOlder(k.Account, k.Kind) {
			continue
		}
		done[k] = true
		c, err := s.mailCopyCheck(ctx, sets, k, rng)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

// hasOlder reports whether the account has a folder of the role outside its current set.
func (m mailSets) hasOlder(account, role string) bool {
	if !m.older[account] {
		return false
	}
	for id, kind := range m.kind {
		if id.account == account && mailCopyRole(kind) == role && !m.current[id] {
			return true
		}
	}
	return false
}

func (s *Store) mailCopyCheck(ctx context.Context, sets mailSets, k MailCopyScope, rng MailCopyRange) (MailCopyCheck, error) {
	c := MailCopyCheck{Account: k.Account, Kind: k.Kind}
	inRole := func(key uint32) (bool, bool) {
		id := mailFolderID{k.Account, key}
		return mailCopyRole(sets.kind[id]) == k.Kind, sets.current[id]
	}
	for id, kind := range sets.kind {
		if id.account == k.Account && mailCopyRole(kind) == k.Kind {
			c.Copies++
		}
	}
	var olderOldest time.Time
	err := mailEach(ctx, s.db, `select folder_key, oldest_at from mail_coverage where account=? and oldest_at is not null`, []any{k.Account}, func(r *sql.Rows) error {
		var key uint32
		var oldest sql.NullString
		if err := r.Scan(&key, &oldest); err != nil {
			return err
		}
		role, current := inRole(key)
		at := parseTime(oldest)
		switch {
		case !role:
		case current:
			c.CurrentOldestAt = earliest(c.CurrentOldestAt, at)
		default:
			olderOldest = earliest(olderOldest, at)
		}
		return nil
	})
	if err != nil {
		return c, err
	}
	var w where
	w.add(`account=?`, k.Account)
	if !rng.Since.IsZero() {
		w.add(`received_at>=?`, rng.Since.UTC().Format(timeLayout))
	}
	if !rng.Until.IsZero() {
		w.add(`received_at<?`, rng.Until.UTC().Format(timeLayout))
	}
	type message struct{ current, lacking bool }
	msgs := map[string]*message{}
	err = mailEach(ctx, s.db, `select rowid, internet_message_id, folder_key, received_at, gone_at is not null, evicted_at is not null from mail_messages`+w.sql(), w.args, func(r *sql.Rows) error {
		var rowid int64
		var msgID string
		var key uint32
		var received sql.NullString
		var gone, evicted bool
		if err := r.Scan(&rowid, &msgID, &key, &received, &gone, &evicted); err != nil {
			return err
		}
		role, current := inRole(key)
		// The list's gone and evicted switches apply to the current copy. An older copy's marks
		// follow its set leaving the store, so its messages count whatever their marks.
		if !role || current && (gone && !rng.IncludeGone || evicted && !rng.IncludeEvicted) {
			return nil
		}
		id := normalizeMailMessageID(msgID)
		if id == "" {
			id = "\x00" + strconv.FormatInt(rowid, 10) // no Message-ID: the row is its own message
		}
		m := msgs[id]
		if m == nil {
			m = &message{}
			msgs[id] = m
		}
		m.current = m.current || current
		at := parseTime(received)
		m.lacking = m.lacking || !current && !c.CurrentOldestAt.IsZero() && !at.IsZero() && !at.Before(c.CurrentOldestAt)
		return nil
	})
	if err != nil {
		return c, err
	}
	for _, m := range msgs {
		c.Messages++
		switch {
		case m.current:
			c.InCurrentCopy++
		case m.lacking:
			c.OnlyInOlderCopies++
			c.LackingInCurrentRange++
		default:
			c.OnlyInOlderCopies++
		}
	}
	beforeCurrent := c.CurrentOldestAt.IsZero() || !rng.Until.IsZero() && !rng.Until.After(c.CurrentOldestAt)
	olderReaches := !olderOldest.IsZero() && (rng.Until.IsZero() || olderOldest.Before(rng.Until))
	switch {
	case c.LackingInCurrentRange > 0:
		c.State = "partial"
	case beforeCurrent && olderReaches:
		c.State = "older_copy_only"
	default:
		c.State = "consistent"
	}
	return c, nil
}

// earliest is the earlier of two times, a zero time standing for none.
func earliest(a, b time.Time) time.Time {
	if a.IsZero() || !b.IsZero() && b.Before(a) {
		return b
	}
	return a
}

// otherSetFolders is the account's archived folders that a read positively places outside its
// own folder set: their parent chain does not reach the read's root while the read's own folders
// do. A read whose folders do not reach its root (or that has none) places nothing elsewhere.
func otherSetFolders(ctx context.Context, tx *sql.Tx, b MailBatch) (map[uint32]bool, error) {
	parents := map[uint32]uint32{}
	err := eachRow(ctx, tx, `select folder_key, parent_key from mail_folders where account=?`, []any{b.Account}, func(r *sql.Rows) error {
		var k, p uint32
		if err := r.Scan(&k, &p); err != nil {
			return err
		}
		parents[k] = p
		return nil
	})
	if err != nil {
		return nil, err
	}
	known := false
	for _, f := range b.Result.Folders {
		if _, reaches := folderChain(parents, f.Key, b.Result.RootKey); reaches {
			known = true
			break
		}
	}
	other := map[uint32]bool{}
	if !known {
		return other, nil
	}
	for k := range parents {
		if _, reaches := folderChain(parents, k, b.Result.RootKey); !reaches {
			other[k] = true
		}
	}
	return other, nil
}
