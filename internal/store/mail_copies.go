package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
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
// A row's roles (mailCopyRole) are those of every folder it is in: its stored folder and its
// membership (mail_membership.go). A row in an older set is a copy of another row with the same
// nonempty Message-ID (compared with normalizeMailMessageID) in a different set, chosen in this
// order: a current-set row that shares a role with it, so mail sent to oneself pairs Sent with
// Sent even when the current row is shown from Inbox; any current-set row, whatever its folder,
// because the current set says where the message is now (moved, or deleted after the rebuild),
// except when either row is in Sent Items (a current row without a Sent Items copy may only mean
// the cache dropped that copy, since each folder has its own cached range); a row of another
// older set that shares a role and ranks higher (seen more recently, then archived later). Rows
// of one set never merge, so two rows of one set with one Message-ID (a user folder copy) stay
// two rows. Current-set rows count whatever their gone and evicted marks. No row is deleted or
// re-keyed: the copy keeps its id and its own marks.

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

// mailCopies is the folder sets plus, for every row that is a copy, the row that stands for it,
// and where copies of one message disagree on received time, the time chosen for it.
type mailCopies struct {
	sets mailSets
	// members says the archive has the membership table; member holds the membership of the
	// rows of accounts with an older set, by rowid.
	members bool
	member  map[int64][]uint32
	newer   map[int64]string // rowid of a copy to the id (account:detail_key) of the row that stands for it
	// received maps the rowid of a row that stands for copies disagreeing on received time to the
	// received time chosen for the message (chooseReceived), when it differs from the row's own.
	received map[int64]time.Time
	// disagree holds the rowid of every row, standing or copy, of a message whose copies disagree.
	disagree map[int64]bool
}

// copyRow is one row as the copy rule compares it.
type copyRow struct {
	rowid          int64
	id             string
	set            uint32
	current        bool
	seen           string
	received, sent time.Time
}

// loadMailCopies finds the copies. Only accounts with an older folder set are read row by row.
func loadMailCopies(ctx context.Context, q mailQuerier) (mailCopies, error) {
	sets, err := loadMailSets(ctx, q)
	c := mailCopies{sets: sets, member: map[int64][]uint32{}, newer: map[int64]string{}, received: map[int64]time.Time{}, disagree: map[int64]bool{}}
	if err != nil {
		return c, err
	}
	if c.members, err = hasMailMembership(ctx, q); err != nil {
		return c, err
	}
	accounts := make([]string, 0, len(sets.older))
	for a := range sets.older {
		accounts = append(accounts, a)
	}
	sort.Strings(accounts)
	for _, account := range accounts {
		if c.members {
			members, err := loadMailMembers(ctx, q, account)
			if err != nil {
				return c, err
			}
			for rowid, ms := range members {
				for _, m := range ms {
					c.member[rowid] = append(c.member[rowid], m.key)
				}
			}
		}
		byID := map[string][]copyCand{}
		err := mailEach(ctx, q, `select rowid, detail_key, internet_message_id, folder_key, state_seen_at, received_at, sent_at from mail_messages where account=? and internet_message_id<>''`, []any{account}, func(r *sql.Rows) error {
			var row copyRow
			var key, folder uint32
			var msgID string
			var received, sent sql.NullString
			if err := r.Scan(&row.rowid, &key, &msgID, &folder, &row.seen, &received, &sent); err != nil {
				return err
			}
			row.received, row.sent = parseTime(received), parseTime(sent)
			id := mailFolderID{account, folder}
			kind, known := sets.kind[id]
			norm := normalizeMailMessageID(msgID)
			if !known || norm == "" {
				return nil // a folder the store does not hold sits in no known set
			}
			row.id, row.set, row.current = fmt.Sprintf("%s:%d", account, key), sets.set[id], sets.current[id]
			byID[norm] = append(byID[norm], copyCand{c.roles(account, row.rowid, folder, kind), row})
			return nil
		})
		if err != nil {
			return c, err
		}
		for _, g := range byID {
			sort.Slice(g, func(i, j int) bool {
				a, b := g[i].row, g[j].row
				if a.current != b.current {
					return a.current
				}
				if a.seen != b.seen {
					return a.seen > b.seen
				}
				return a.rowid > b.rowid
			})
			for i, r := range g {
				if r.row.current {
					continue
				}
				if n, ok := newerCopy(g[:i], r.roles, r.row.set); ok {
					c.newer[r.row.rowid] = n
				}
			}
			c.chooseReceived(g)
		}
	}
	return c, nil
}

// receivedAgreement is how far apart copies' received times may be and still agree; sendSkew is
// how far before the send time a received time may lie and still agree with it (clock skew).
const (
	receivedAgreement = time.Minute
	sendSkew          = 15 * time.Minute
)

// chooseReceived looks at the rows of one Message-ID (best first, as loadMailCopies ranks them),
// groups each copy with the row that stands for it, and for a group whose received times are more
// than receivedAgreement apart records the disagreement and the received time chosen for the
// message: the earliest received time of the group at or after the send time (less sendSkew),
// since a message cannot arrive before it was sent and Outlook stores a draft's start or a
// rebuild's guess as the received time of some copies; when no copy qualifies, the standing
// row's own. The majority of copies does not decide: a wrong time is often repeated across sets.
func (c mailCopies) chooseReceived(g []copyCand) {
	if len(g) < 2 {
		return
	}
	byID := make(map[string]copyRow, len(g))
	for _, r := range g {
		byID[r.row.id] = r.row
	}
	groups := map[int64][]copyRow{}
	var order []int64
	for _, r := range g {
		top := r.row
		for range len(g) {
			n, ok := c.newer[top.rowid]
			if !ok {
				break
			}
			top = byID[n]
		}
		if _, seen := groups[top.rowid]; !seen {
			order = append(order, top.rowid)
		}
		groups[top.rowid] = append(groups[top.rowid], r.row)
	}
	for _, standing := range order {
		rows := groups[standing]
		var lo, hi time.Time
		for _, r := range rows {
			lo, hi = earliest(lo, r.received), latest(hi, r.received)
		}
		if len(rows) < 2 || hi.Sub(lo) <= receivedAgreement {
			continue
		}
		var own, sent time.Time
		for _, r := range rows {
			if r.rowid == standing {
				own = r.received
			}
			if sent.IsZero() {
				sent = r.sent
			}
		}
		chosen := own
		if !sent.IsZero() {
			var best time.Time
			for _, r := range rows {
				if !r.received.IsZero() && !r.received.Before(sent.Add(-sendSkew)) {
					best = earliest(best, r.received)
				}
			}
			if !best.IsZero() {
				chosen = best
			}
		}
		for _, r := range rows {
			c.disagree[r.rowid] = true
		}
		if !chosen.IsZero() && !chosen.Equal(own) {
			c.received[standing] = chosen
		}
	}
}

// latest is the later of two times, a zero time standing for none.
func latest(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// copyCand is one row of a Message-ID with the roles of the folders it is in.
type copyCand struct {
	roles []string
	row   copyRow
}

// roles is the copy roles of a row: those of its stored folder (of kind) and of its membership,
// without the empty role.
func (c mailCopies) roles(account string, rowid int64, folder uint32, kind string) []string {
	var out []string
	add := func(k string) {
		if r := mailCopyRole(k); r != "" && !slices.Contains(out, r) {
			out = append(out, r)
		}
	}
	add(kind)
	for _, key := range c.member[rowid] {
		if key != folder {
			add(c.sets.kind[mailFolderID{account, key}])
		}
	}
	return out
}

// sharesRole reports whether two role lists have a role in common.
func sharesRole(a, b []string) bool {
	for _, r := range a {
		if slices.Contains(b, r) {
			return true
		}
	}
	return false
}

// newerCopy picks, from the rows of one Message-ID that rank above an older row (best first;
// current-set rows always rank first), the row that the older row, of roles and set, is a copy
// of (see the rule above). The any-folder step never crosses into or out of Sent Items: a current
// row without a Sent Items copy says nothing certain about whether the message is still in Sent
// Items, since the cache keeps each folder's own range.
func newerCopy(g []copyCand, roles []string, set uint32) (string, bool) {
	var anyCurrent, olderSameRole string
	sent := slices.Contains(roles, "sent")
	for _, n := range g {
		switch {
		case n.row.set == set:
		case n.row.current && sharesRole(roles, n.roles):
			return n.row.id, true
		case n.row.current && anyCurrent == "" && !sent && !slices.Contains(n.roles, "sent"):
			anyCurrent = n.row.id
		case !n.row.current && olderSameRole == "" && sharesRole(roles, n.roles):
			olderSameRole = n.row.id
		}
	}
	if anyCurrent != "" {
		return anyCurrent, true
	}
	return olderSameRole, olderSameRole != ""
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

// mark sets OlderCopy, CopyOf, ReceivedAtDisagrees and DatedAt on rows.
func (c mailCopies) mark(rows []MailRow) {
	for i := range rows {
		r := &rows[i]
		r.OlderCopy = !c.sets.isCurrent(r.Account, r.FolderKey)
		r.CopyOf = c.newer[r.Rowid]
		r.ReceivedAtDisagrees = c.disagree[r.Rowid]
		r.DatedAt = datedAt(r.inSent(), c.receivedAt(r.Rowid, r.ReceivedAt), r.SentAt)
	}
}

// MailCopyScope names an account and a folder kind to check.
type MailCopyScope struct{ Account, Kind string }

// MailCopyRange is the range [Since, Until) a check covers, on the clock mail list dates messages
// by (mail_clock.go; a zero end is open), and
// whether gone and evicted messages of the current folder set count, as in MailFilter. Messages
// only an older set holds count whatever their marks: once Outlook replaces a set, a trusted read
// finds its messages missing and marks them, which says nothing about whether the current set
// holds them. OlderCopiesMarked says how many of them the list hides behind those marks.
type MailCopyRange struct {
	Since, Until                time.Time
	IncludeGone, IncludeEvicted bool
}

// MailCopyCheck compares, for one account and folder role (inbox or sent) over a range, the
// messages the current folder set holds with the ones only older sets hold, each message counted
// once, the way mail list counts them: a copy counts through the row that stands for it, so a
// message the current set holds in any folder, or held and since marked gone, is never missing.
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
	// LackingThrough is the latest date, on DatedBy's clock, of a message LackingInCurrentRange
	// counts (zero when none): the disagreement concerns nothing dated after it.
	LackingThrough time.Time
	// OlderCopiesMarked is how many of OnlyInOlderCopies a sync marked gone or evicted (after the
	// set left the store) and the list therefore hides unless the range includes them.
	OlderCopiesMarked int
	// Marked is how many messages a sync marked gone or evicted in every row the check counted:
	// the ones mail list hides without --include-gone --include-evicted when the range includes
	// them (MailRoleCount).
	Marked          int
	CurrentOldestAt time.Time
	State           string
	// DatedBy is the clock the range selects messages by (MailClock): sent_at for sent, received_at
	// for inbox, as mail list dates them.
	DatedBy string
}

// checkedRole is true for the roles a copy check runs for: Inbox (with To Me) and Sent Items, whose
// coverage is a range the cache holds. The coverage of Drafts, Deleted Items and Archive is only
// the oldest item they hold, and an empty folder has none, so it says nothing about a day.
func checkedRole(role string) bool { return role == "inbox" || role == "sent" }

// MailCopyChecks checks each distinct account and checked role in scopes that has an older folder
// copy, in the order of scopes. Another kind, or an account without older copies, gives nothing.
func (s *Store) MailCopyChecks(ctx context.Context, scopes []MailCopyScope, rng MailCopyRange) ([]MailCopyCheck, error) {
	copies, err := loadMailCopies(ctx, s.db)
	if err != nil {
		return nil, err
	}
	var out []MailCopyCheck
	done := map[MailCopyScope]bool{}
	for _, sc := range scopes {
		k := MailCopyScope{sc.Account, mailCopyRole(sc.Kind)}
		if !checkedRole(k.Kind) || done[k] || !copies.sets.hasOlder(k.Account, k.Kind) {
			continue
		}
		done[k] = true
		c, err := s.mailCopyCheck(ctx, copies, k, rng)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

// MailRoleCount counts, for one account and role (inbox or sent) over a range, every message the
// archive holds once, the way MailCopyChecks does, gone and evicted ones included (Marked says
// how many of them carry such a mark), whether or not the role has older copies. State is the
// copy check's. An account without a folder of the role holds nothing.
func (s *Store) MailRoleCount(ctx context.Context, account, role string, since, until time.Time) (MailCopyCheck, error) {
	copies, err := loadMailCopies(ctx, s.db)
	if err != nil {
		return MailCopyCheck{}, err
	}
	k := MailCopyScope{account, role}
	for id, kind := range copies.sets.kind {
		if id.account == account && mailCopyRole(kind) == role {
			return s.mailCopyCheck(ctx, copies, k, MailCopyRange{Since: since, Until: until, IncludeGone: true, IncludeEvicted: true})
		}
	}
	return MailCopyCheck{Account: account, Kind: role, DatedBy: MailClock(role), State: "consistent"}, nil
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

func (s *Store) mailCopyCheck(ctx context.Context, copies mailCopies, k MailCopyScope, rng MailCopyRange) (MailCopyCheck, error) {
	c := MailCopyCheck{Account: k.Account, Kind: k.Kind, DatedBy: MailClock(k.Kind)}
	sets := copies.sets
	inRole := func(key uint32) (bool, bool) {
		id := mailFolderID{k.Account, key}
		return mailCopyRole(sets.kind[id]) == k.Kind, sets.current[id]
	}
	// A row is in the role when any folder it is in has the role; it is current when its stored
	// folder is (its membership is of the same set).
	rowInRole := func(rowid int64, key uint32) (bool, bool) {
		role, current := inRole(key)
		return role || slices.Contains(copies.roles(k.Account, rowid, key, sets.kind[mailFolderID{k.Account, key}]), k.Kind), current
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
	var keys []uint32
	for id, kind := range sets.kind {
		if id.account == k.Account && mailCopyRole(kind) == k.Kind {
			keys = append(keys, id.key)
		}
	}
	cond, condArgs := inFolders(map[string][]uint32{k.Account: keys}, false, copies.members)
	rows, args := copies.datedRows(cond, condArgs, rng.Since, rng.Until)
	type message struct {
		current, lacking, shown, unmarked bool
		dated                             time.Time // the latest of its rows' dates on the range's clock
	}
	msgs := map[string]*message{}
	err = mailEach(ctx, s.db, `select m.rowid, m.internet_message_id, m.folder_key, m.received_at, m.gone_at is not null, m.evicted_at is not null, p.at from (`+rows+`) p join mail_messages m on m.rowid=p.rid`, args, func(r *sql.Rows) error { //nolint:gosec // G202: fragments are package constants and integers; values are placeholders
		var rowid int64
		var msgID string
		var key uint32
		var received, dated sql.NullString
		var gone, evicted bool
		if err := r.Scan(&rowid, &msgID, &key, &received, &gone, &evicted, &dated); err != nil {
			return err
		}
		role, current := rowInRole(rowid, key)
		hidden := gone && !rng.IncludeGone || evicted && !rng.IncludeEvicted
		// A copy counts through the row that stands for it. The list's gone and evicted switches
		// apply to the current set; an older set's marks follow it leaving the store.
		if !role || copies.newer[rowid] != "" || current && hidden {
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
		m.shown = m.shown || !hidden
		m.unmarked = m.unmarked || !gone && !evicted
		if d := parseTime(dated); d.After(m.dated) {
			m.dated = d
		}
		// The current copy's range is a range of received times (the oldest message the cache held),
		// so whether a message lies inside it is judged by its received time, whatever the clock
		// that selected it.
		at := copies.receivedAt(rowid, parseTime(received))
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
			if m.dated.After(c.LackingThrough) {
				c.LackingThrough = m.dated
			}
		default:
			c.OnlyInOlderCopies++
		}
		if !m.current && !m.shown {
			c.OlderCopiesMarked++
		}
		if !m.unmarked {
			c.Marked++
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
