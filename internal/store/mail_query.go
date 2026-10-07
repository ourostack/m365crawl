package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/ourostack/m365crawl/internal/outlookmail"
)

// ErrMailNotFound is returned by MailGet and MailThread for a message the archive does not hold.
var ErrMailNotFound = errors.New("no such mail message")

// ErrUnknownMailFolder is returned by MailList for a folder filter that matches no folder name or
// kind. MailFolders lists the folders there are.
var ErrUnknownMailFolder = errors.New("no such mail folder")

// mailThreadCap bounds a reply chain and a subject group.
const mailThreadCap = 200

// MailFilter selects messages for MailList. Folder matches a folder name first (ignoring case),
// then a folder kind. Gone and evicted messages are left out unless asked for. Query is an FTS5
// search of subject, sender and body text, with the same syntax as Search.
type MailFilter struct {
	Account, Folder, From, Query    string
	Since, Until                    time.Time
	Unread, Flagged, HasAttachments bool
	IncludeGone, IncludeEvicted     bool
	Limit                           int
}

// MailRecipient is one recipient. Address is empty when the store gave none. KindRaw is the
// store's raw kind word; To and Cc are not yet told apart.
type MailRecipient struct {
	Name    string
	Address string
	KindRaw uint32
}

// MailAttachment is one attachment.
type MailAttachment struct {
	Key         uint32
	Name        string
	Size        int64
	ContentType string
	Inline      bool
	Downloaded  bool
}

// MailRow is one archived message. BodyText is filled by MailGet only. Recipients and
// Attachments are filled by every query that returns messages.
type MailRow struct {
	Rowid                                       int64
	Account                                     string
	DetailKey                                   uint32
	InternetMessageID                           string
	FolderKey                                   uint32
	Folder, FolderKind                          string
	ToMe                                        bool
	Subject, SubjectNorm                        string
	SenderName, SenderAddress                   string
	Preview, InReplyTo                          string
	ReceivedAt, SentAt                          time.Time
	Class, Importance, ICalUID                  string
	IsRead                                      *bool
	ReadState, Flag                             string
	HasAttachments                              bool
	BodyState, BodyText                         string
	FirstSeenAt, StateSeenAt, GoneAt, EvictedAt time.Time
	Recipients                                  []MailRecipient
	Attachments                                 []MailAttachment
}

// ID is the message's id as the commands print it: account:detail_key.
func (r MailRow) ID() string { return fmt.Sprintf("%s:%d", r.Account, r.DetailKey) }

// MailFolderRow is one folder with its counts: messages and unread messages the archive holds
// (gone and evicted ones left out), and the range the last read covered.
type MailFolderRow struct {
	Account            string
	FolderKey          uint32
	ParentKey          uint32
	Name, Kind         string
	Messages, Unread   int
	OldestAt, NewestAt time.Time
	ReadAt             time.Time
}

// CoverageRow is one folder's coverage: how far back the cache reached at the last read.
type CoverageRow struct {
	Account            string
	FolderKey          uint32
	Folder, Kind       string
	OldestAt, NewestAt time.Time
	Count              int
	ReadAt             time.Time
}

// MailParticipant is one distinct person in a thread.
type MailParticipant struct{ Name, Address string }

type mailQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// mailEach runs a query and calls fn for each row.
func mailEach(ctx context.Context, q mailQuerier, query string, args []any, fn func(*sql.Rows) error) error {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		if err := fn(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

const mailSelect = `select m.rowid, m.account, m.detail_key, m.internet_message_id, m.folder_key, coalesce(f.name,''), coalesce(f.kind,'unknown'), m.to_me,
  m.subject, m.subject_norm, m.sender_name, m.sender_address, m.preview, m.in_reply_to, m.received_at, m.sent_at, m.class, m.importance, m.ical_uid,
  m.is_read, m.read_state, m.flag, m.has_attachments, m.body_state, %s, m.first_seen_at, m.state_seen_at, m.gone_at, m.evicted_at`

const mailJoin = ` left join mail_folders f on f.account=m.account and f.folder_key=m.folder_key`

func (s *Store) mailRows(ctx context.Context, body bool, from string, args []any) ([]MailRow, error) {
	bodyCol := `''`
	if body {
		bodyCol = `m.body_text`
	}
	var out []MailRow
	err := mailEach(ctx, s.db, fmt.Sprintf(mailSelect, bodyCol)+from, args, func(r *sql.Rows) error {
		var m MailRow
		var recv, sent, first, seen, gone, evicted sql.NullString
		var isRead sql.NullInt64
		if err := r.Scan(&m.Rowid, &m.Account, &m.DetailKey, &m.InternetMessageID, &m.FolderKey, &m.Folder, &m.FolderKind, &m.ToMe,
			&m.Subject, &m.SubjectNorm, &m.SenderName, &m.SenderAddress, &m.Preview, &m.InReplyTo, &recv, &sent, &m.Class, &m.Importance, &m.ICalUID,
			&isRead, &m.ReadState, &m.Flag, &m.HasAttachments, &m.BodyState, &m.BodyText, &first, &seen, &gone, &evicted); err != nil {
			return err
		}
		m.ReceivedAt, m.SentAt, m.FirstSeenAt, m.StateSeenAt = parseTime(recv), parseTime(sent), parseTime(first), parseTime(seen)
		m.GoneAt, m.EvictedAt = parseTime(gone), parseTime(evicted)
		if isRead.Valid {
			v := isRead.Int64 == 1
			m.IsRead = &v
		}
		out = append(out, m)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, s.mailChildren(ctx, out)
}

// mailChildren fills the recipients and attachments of rows.
func (s *Store) mailChildren(ctx context.Context, rows []MailRow) error {
	if len(rows) == 0 {
		return nil
	}
	at := make(map[int64]int, len(rows))
	ids := make([]string, len(rows))
	for i, r := range rows {
		at[r.Rowid] = i
		ids[i] = fmt.Sprint(r.Rowid)
	}
	in := strings.Join(ids, ",")                                                                                                                                                                // row ids are integers
	err := mailEach(ctx, s.db, `select message_rowid, name, address, kind_raw from mail_recipients where message_rowid in (`+in+`) order by message_rowid, ord`, nil, func(r *sql.Rows) error { //nolint:gosec // G202: in is integers
		var id int64
		var rc MailRecipient
		var addr sql.NullString
		if err := r.Scan(&id, &rc.Name, &addr, &rc.KindRaw); err != nil {
			return err
		}
		rc.Address = addr.String
		rows[at[id]].Recipients = append(rows[at[id]].Recipients, rc)
		return nil
	})
	if err != nil {
		return err
	}
	return mailEach(ctx, s.db, `select message_rowid, attachment_key, name, size, content_type, inline, downloaded from mail_attachments where message_rowid in (`+in+`) order by message_rowid, attachment_key`, nil, func(r *sql.Rows) error { //nolint:gosec // G202: in is integers
		var id int64
		var a MailAttachment
		if err := r.Scan(&id, &a.Key, &a.Name, &a.Size, &a.ContentType, &a.Inline, &a.Downloaded); err != nil {
			return err
		}
		rows[at[id]].Attachments = append(rows[at[id]].Attachments, a)
		return nil
	})
}

// MailList lists archived messages, newest first.
func (s *Store) MailList(ctx context.Context, f MailFilter) ([]MailRow, error) {
	var w where
	from := ` from mail_messages m` + mailJoin
	if strings.TrimSpace(f.Query) != "" {
		match := buildFTSQuery(f.Query)
		if match == "" {
			return nil, searchUsage("mail query has no searchable terms")
		}
		w.add(`mail_fts match ?`, match)
		from = ` from mail_fts join mail_messages m on m.rowid=mail_fts.rowid` + mailJoin
	}
	if f.Account != "" {
		w.add(`m.account=?`, f.Account)
	}
	if f.Folder != "" {
		if err := s.mailFolderWhere(ctx, &w, f); err != nil {
			return nil, err
		}
	}
	if f.From != "" {
		like := "%" + strings.ToLower(escapeLike(f.From)) + "%"
		w.add(`(lower(m.sender_name) like ? escape '\' or lower(m.sender_address) like ? escape '\')`, like, like)
	}
	if !f.Since.IsZero() {
		w.add(`m.received_at>=?`, f.Since.UTC().Format(timeLayout))
	}
	if !f.Until.IsZero() {
		w.add(`m.received_at<?`, f.Until.UTC().Format(timeLayout))
	}
	if f.Unread {
		w.add(`m.is_read=0`)
	}
	if f.Flagged {
		w.add(`m.flag='flagged'`)
	}
	if f.HasAttachments {
		w.add(`m.has_attachments=1`)
	}
	if !f.IncludeGone {
		w.add(`m.gone_at is null`)
	}
	if !f.IncludeEvicted {
		w.add(`m.evicted_at is null`)
	}
	limit := f.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	return s.mailRows(ctx, false, from+w.sql()+` order by m.received_at desc, m.rowid desc limit ?`, append(w.args, limit))
}

// mailFolderWhere limits the list to the folders the filter names: by name first, then by kind.
func (s *Store) mailFolderWhere(ctx context.Context, w *where, f MailFilter) error {
	type fk struct {
		account string
		key     uint32
	}
	var byName, byKind []fk
	err := mailEach(ctx, s.db, `select account, folder_key, name, kind from mail_folders where (?='' or account=?) order by account, folder_key`, []any{f.Account, f.Account}, func(r *sql.Rows) error {
		var k fk
		var name, kind string
		if err := r.Scan(&k.account, &k.key, &name, &kind); err != nil {
			return err
		}
		if strings.EqualFold(name, f.Folder) {
			byName = append(byName, k)
		}
		if strings.EqualFold(kind, f.Folder) {
			byKind = append(byKind, k)
		}
		return nil
	})
	if err != nil {
		return err
	}
	keys := byName
	if len(keys) == 0 {
		keys = byKind
	}
	if len(keys) == 0 && strings.EqualFold(f.Folder, outlookmail.KindUnknown) {
		// Messages whose folder object the store does not hold have no folder row to match.
		w.add(`not exists(select 1 from mail_folders f2 where f2.account=m.account and f2.folder_key=m.folder_key)`)
		return nil
	}
	if len(keys) == 0 {
		return ErrUnknownMailFolder
	}
	conds := make([]string, len(keys))
	args := make([]any, 0, 2*len(keys))
	for i, k := range keys {
		conds[i] = `(m.account=? and m.folder_key=?)`
		args = append(args, k.account, k.key)
	}
	w.add(`(`+strings.Join(conds, " or ")+`)`, args...)
	return nil
}

// MailGet returns one message with its body text, recipients and attachments, whether or not it
// is gone or evicted.
func (s *Store) MailGet(ctx context.Context, account string, detailKey uint32) (MailRow, error) {
	rows, err := s.mailRows(ctx, true, ` from mail_messages m`+mailJoin+` where m.account=? and m.detail_key=?`, []any{account, detailKey})
	if err != nil {
		return MailRow{}, err
	}
	if len(rows) == 0 {
		return MailRow{}, ErrMailNotFound
	}
	return rows[0], nil
}

// MailThread returns the conversation a message belongs to, oldest first, and how it was grouped:
// "reply_chain" when In-Reply-To links join the message to at least one other Message-ID, else
// "subject" (messages with the same normalised subject that share a participant with the seed; an
// empty subject gives the seed alone). Gone and evicted messages are included.
func (s *Store) MailThread(ctx context.Context, account string, detailKey uint32) ([]MailRow, string, error) {
	seed, err := s.MailGet(ctx, account, detailKey)
	if err != nil {
		return nil, "", err
	}
	ids, linked, err := s.replyChain(ctx, seed)
	if err != nil {
		return nil, "", err
	}
	grouping := "reply_chain"
	if !linked {
		grouping = "subject"
		if ids, err = s.subjectGroup(ctx, seed); err != nil {
			return nil, "", err
		}
	}
	rows, err := s.mailRows(ctx, false, ` from mail_messages m`+mailJoin+` where m.rowid in (`+joinIDs(ids)+`) order by m.received_at, m.rowid`, nil) //nolint:gosec // G202: integers
	return rows, grouping, err
}

func joinIDs(ids []int64) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = fmt.Sprint(id)
	}
	return strings.Join(parts, ",")
}

// replyChain follows In-Reply-To links, both ways and transitively, from the seed. Every row that
// shares a Message-ID with a row in the chain is in it. linked says the chain holds a Message-ID
// other than the seed's: a chain with only the seed is no link.
func (s *Store) replyChain(ctx context.Context, seed MailRow) (ids []int64, linked bool, err error) {
	have := map[int64]bool{seed.Rowid: true}
	ids = []int64{seed.Rowid}
	ownIDs := map[string]bool{}
	for queue := []MailRow{seed}; len(queue) > 0 && len(ids) < mailThreadCap; queue = queue[1:] {
		cur := queue[0]
		if cur.InternetMessageID != "" {
			ownIDs[cur.InternetMessageID] = true
		}
		var near []MailRow
		near, err = s.mailRows(ctx, false, ` from mail_messages m`+mailJoin+` where m.account=? and ((m.internet_message_id<>'' and (m.internet_message_id=? or m.internet_message_id=?)) or (?<>'' and m.in_reply_to=?)) order by m.rowid`,
			[]any{cur.Account, cur.InternetMessageID, cur.InReplyTo, cur.InternetMessageID, cur.InternetMessageID})
		if err != nil {
			return nil, false, err
		}
		for _, n := range near {
			if have[n.Rowid] || len(ids) >= mailThreadCap {
				continue
			}
			have[n.Rowid] = true
			ids = append(ids, n.Rowid)
			queue = append(queue, n)
			if n.InternetMessageID != seed.InternetMessageID {
				linked = true
			}
		}
	}
	return ids, linked, nil
}

// subjectGroup returns the seed and the messages of its account with the same normalised subject
// that share an address (sender or recipient) with it.
func (s *Store) subjectGroup(ctx context.Context, seed MailRow) ([]int64, error) {
	if seed.SubjectNorm == "" {
		return []int64{seed.Rowid}, nil
	}
	same, err := s.mailRows(ctx, false, ` from mail_messages m`+mailJoin+` where m.account=? and m.subject_norm=? order by m.received_at desc, m.rowid desc limit ?`, []any{seed.Account, seed.SubjectNorm, mailThreadCap})
	if err != nil {
		return nil, err
	}
	mine := addressSet(seed)
	ids := []int64{seed.Rowid}
	for _, m := range same {
		if m.Rowid == seed.Rowid {
			continue
		}
		for a := range addressSet(m) {
			if mine[a] {
				ids = append(ids, m.Rowid)
				break
			}
		}
	}
	return ids, nil
}

// addressSet is the lower-case addresses of a message's sender and recipients.
func addressSet(m MailRow) map[string]bool {
	set := map[string]bool{}
	if a := strings.ToLower(m.SenderAddress); a != "" {
		set[a] = true
	}
	for _, r := range m.Recipients {
		if a := strings.ToLower(r.Address); a != "" {
			set[a] = true
		}
	}
	return set
}

// MailParticipants lists the distinct people (name and address) that sent or received the
// messages, in the order they first appear.
func MailParticipants(rows []MailRow) []MailParticipant {
	seen := map[MailParticipant]bool{}
	var out []MailParticipant
	add := func(name, addr string) {
		p := MailParticipant{name, addr}
		if (name == "" && addr == "") || seen[p] {
			return
		}
		seen[p] = true
		out = append(out, p)
	}
	for _, m := range rows {
		add(m.SenderName, m.SenderAddress)
		for _, r := range m.Recipients {
			add(r.Name, r.Address)
		}
	}
	return out
}

// MailFolders lists every archived folder with its counts and the range the last read covered.
func (s *Store) MailFolders(ctx context.Context) ([]MailFolderRow, error) {
	out := []MailFolderRow{}
	err := mailEach(ctx, s.db, `select f.account, f.folder_key, f.parent_key, f.name, f.kind,
  (select count(*) from mail_messages m where m.account=f.account and m.folder_key=f.folder_key and m.gone_at is null and m.evicted_at is null),
  (select count(*) from mail_messages m where m.account=f.account and m.folder_key=f.folder_key and m.gone_at is null and m.evicted_at is null and m.is_read=0),
  c.oldest_at, c.newest_at, c.read_at
from mail_folders f left join mail_coverage c on c.account=f.account and c.folder_key=f.folder_key`, nil, func(r *sql.Rows) error {
		var f MailFolderRow
		var oldest, newest, read sql.NullString
		if err := r.Scan(&f.Account, &f.FolderKey, &f.ParentKey, &f.Name, &f.Kind, &f.Messages, &f.Unread, &oldest, &newest, &read); err != nil {
			return err
		}
		f.OldestAt, f.NewestAt, f.ReadAt = parseTime(oldest), parseTime(newest), parseTime(read)
		out = append(out, f)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Account != b.Account {
			return a.Account < b.Account
		}
		if ra, rb := mailKindRank(a.Kind), mailKindRank(b.Kind); ra != rb {
			return ra < rb
		}
		if na, nb := strings.ToLower(a.Name), strings.ToLower(b.Name); na != nb {
			return na < nb
		}
		return a.FolderKey < b.FolderKey
	})
	return out, nil
}

func mailKindRank(kind string) int {
	for i, k := range []string{"inbox", "to_me", "drafts", "sent", "archive", "junk", "deleted"} {
		if k == kind {
			return i
		}
	}
	return 7
}

// MailCoverage lists, per folder, how far back the cache reached at the last read.
func (s *Store) MailCoverage(ctx context.Context) ([]CoverageRow, error) {
	out := []CoverageRow{}
	err := mailEach(ctx, s.db, `select c.account, c.folder_key, coalesce(f.name,''), coalesce(f.kind,'unknown'), c.oldest_at, c.newest_at, c.count, c.read_at
from mail_coverage c left join mail_folders f on f.account=c.account and f.folder_key=c.folder_key order by c.account, c.folder_key`, nil, func(r *sql.Rows) error {
		var c CoverageRow
		var oldest, newest, read sql.NullString
		if err := r.Scan(&c.Account, &c.FolderKey, &c.Folder, &c.Kind, &oldest, &newest, &c.Count, &read); err != nil {
			return err
		}
		c.OldestAt, c.NewestAt, c.ReadAt = parseTime(oldest), parseTime(newest), parseTime(read)
		out = append(out, c)
		return nil
	})
	return out, err
}
