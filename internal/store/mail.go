package store

import (
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ourostack/m365crawl/internal/outlookmail"
)

// MailTextVersion numbers how a message's body text, its normalised subject and its full-text
// entry are derived from the stored HTML and subject. It is stored as meta.mail_text_version and
// per row as mail_messages.body_text_version; raise it whenever outlookmail.HTMLText or
// NormalizeSubject changes what it returns, and Rederive recomputes the rows below it.
const MailTextVersion = 1

const (
	// MailGoneWithholdMin and MailGoneWithholdPercent say when a read lost too much to be
	// deletions: more than MailGoneWithholdMin missing messages that are also more than
	// MailGoneWithholdPercent percent of the live messages the rule judges. Nothing is marked, and
	// no absence is recorded, then.
	MailGoneWithholdMin     = 20
	MailGoneWithholdPercent = 10

	// bodyPending is the body_state of a message whose body is a file that no read has opened yet.
	bodyPending = "pending"

	// mailRootKey is the meta key prefix of the account root of the last read.
	mailRootKey = "outlook_mail_root:"
)

// MailBatch is one Outlook account's mail, read whole from a store copy.
type MailBatch struct {
	Account string // "outlook/<profile directory name>"
	ReadAt  time.Time
	// FreshAt is the store file's modification time: when Outlook last wrote it. Two reads of the
	// same copy carry the same FreshAt, and only a read with a later one can confirm an absence.
	FreshAt time.Time
	Result  outlookmail.Result
	// Trusted says the read passed its guard and lost nothing (no loss codes), so it may say that
	// a message is gone or evicted. An untrusted batch still adds and updates.
	Trusted bool
}

// MailResult is what one CommitMail did. Gone and Evicted count the messages marked by this call.
// Withheld counts the missing messages a read lost too many of to be believed.
type MailResult struct {
	Added    int `json:"added"`
	Updated  int `json:"updated"`
	Replaced int `json:"replaced"`
	Gone     int `json:"gone"`
	Evicted  int `json:"evicted"`
	Withheld int `json:"withheld"`
}

// CommitMail writes one account's mail in a single transaction; a failure rolls everything back.
//
// A message is identified by (account, detail_key). A new detail key is a new message, unless the
// archive holds a row of the account with the same Message-ID and the same folder kind whose key
// this read no longer carries: Outlook re-keys messages, so that row takes the new key and counts
// as updated. The same key with a different Message-ID is a different message: the row is
// replaced in full (Replaced) and its text index rewritten.
//
// What the message says (subject, sender, recipients, times, Message-ID, In-Reply-To, invite id)
// never changes after it is stored. Its folder, to-me flag, read state, flag and importance are
// updated in place. Its body, has_attachments and attachments are filled when a later read has
// more: a body is taken only while the stored body_state is not inline or file, and attachments
// are added and have their downloaded flag updated.
//
// A trusted read that does not hold a live message records the first miss in mail_absent. A later
// trusted read of a different store copy (a later FreshAt) that also misses it confirms the
// absence: the message is gone when it was received inside the range its folder's read covers,
// and evicted when it is older (the cache dropped it). For deleted, junk, archive, drafts and
// other small folders the range is the account's combined range, the earliest oldest_at of its
// inbox, to_me and sent folders. A message that reappears clears both marks and its absence row.
// A read that misses more than MailGoneWithholdMin messages and more than MailGoneWithholdPercent
// percent of the live ones marks nothing.
func (s *Store) CommitMail(ctx context.Context, b MailBatch) (res MailResult, err error) {
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		var cerr error
		res, cerr = commitMail(ctx, tx, b)
		return cerr
	})
	if err != nil {
		return MailResult{}, err
	}
	return res, nil
}

// mailExisting is one stored message as the commit compares it.
type mailExisting struct {
	rowid                                  int64
	detailKey, folderKey                   uint32
	msgID, kind                            string
	toMe, hasAtt, hasRecips, gone, evicted bool
	isRead                                 sql.NullInt64
	readState, flag, imp, body             string
	received                               sql.NullString
	atts                                   map[uint32]bool // attachment key to downloaded
	seen                                   bool
}

type rekeyKey struct{ msgID, kind string }

func commitMail(ctx context.Context, tx *sql.Tx, b MailBatch) (MailResult, error) {
	var res MailResult
	at := b.ReadAt.UTC().Format(timeLayout)
	if err := upsertMailFolders(ctx, tx, b); err != nil {
		return res, err
	}
	changed, err := mailRootChanged(ctx, tx, b)
	if err != nil {
		return res, err
	}
	b.Trusted = b.Trusted && !changed
	byKey, err := loadMailExisting(ctx, tx, b.Account)
	if err != nil {
		return res, err
	}
	absent, err := loadMailAbsent(ctx, tx, b.Account)
	if err != nil {
		return res, err
	}
	inBatch := make(map[uint32]bool, len(b.Result.Messages))
	for _, m := range b.Result.Messages {
		inBatch[m.DetailKey] = true
	}
	rekey := map[rekeyKey][]*mailExisting{}
	live := 0
	for _, k := range sortedMailKeys(byKey) {
		ex := byKey[k]
		if !ex.gone && !ex.evicted {
			live++
		}
		if !inBatch[k] && ex.msgID != "" {
			rk := rekeyKey{ex.msgID, ex.kind}
			rekey[rk] = append(rekey[rk], ex)
		}
	}
	for _, m := range b.Result.Messages {
		ex := byKey[m.DetailKey]
		moved := false
		if ex == nil && m.MessageID != "" {
			if cand := rekey[rekeyKey{m.MessageID, m.Folder.Kind}]; len(cand) > 0 {
				ex, rekey[rekeyKey{m.MessageID, m.Folder.Kind}] = cand[0], cand[1:]
				old := ex.detailKey
				if err := rekeyMail(ctx, tx, b.Account, ex, m.DetailKey); err != nil {
					return res, err
				}
				delete(byKey, old)
				byKey[m.DetailKey] = ex
				moved = true
			}
		}
		d := deriveMail(m)
		switch {
		case ex == nil:
			if err := insertMail(ctx, tx, b.Account, m, d, at); err != nil {
				return res, err
			}
			res.Added++
		case ex.msgID != "" && m.MessageID != "" && ex.msgID != m.MessageID:
			ex.seen = true
			if err := replaceMail(ctx, tx, ex, m, d, at); err != nil {
				return res, err
			}
			res.Replaced++
		default:
			ex.seen = true
			changed, err := updateMail(ctx, tx, ex, m, d, at, moved)
			if err != nil {
				return res, err
			}
			if changed {
				res.Updated++
			}
		}
		// A message the read holds loses any absence the archive remembered.
		if _, ok := absent[m.DetailKey]; ok {
			delete(absent, m.DetailKey)
			if _, err := tx.ExecContext(ctx, `delete from mail_absent where account=? and detail_key=?`, b.Account, m.DetailKey); err != nil {
				return res, err
			}
		}
	}
	if err := writeMailCoverage(ctx, tx, b, at); err != nil {
		return res, err
	}
	if !b.Trusted {
		return res, nil
	}
	return judgeMissing(ctx, tx, b, at, byKey, absent, live, res)
}

// mailRootChanged records the account root of this read in meta and reports whether it differs
// from the one the previous read had. A changed root means the folder set moved, so the read says
// nothing about which messages are gone.
func mailRootChanged(ctx context.Context, tx *sql.Tx, b MailBatch) (bool, error) {
	key := mailRootKey + b.Account
	var stored string
	err := tx.QueryRowContext(ctx, `select value from meta where key=?`, key).Scan(&stored)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	now := strconv.FormatUint(uint64(b.Result.RootKey), 10)
	if _, err := tx.ExecContext(ctx, `insert into meta(key, value) values(?, ?) on conflict(key) do update set value=excluded.value`, key, now); err != nil {
		return false, err
	}
	return err == nil && stored != now, nil
}

func sortedMailKeys(m map[uint32]*mailExisting) []uint32 {
	out := make([]uint32, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func upsertMailFolders(ctx context.Context, tx *sql.Tx, b MailBatch) error {
	for _, f := range b.Result.Folders {
		if _, err := tx.ExecContext(ctx, `insert into mail_folders(account, folder_key, parent_key, name, kind) values(?,?,?,?,?)
on conflict(account, folder_key) do update set parent_key=excluded.parent_key, name=excluded.name, kind=excluded.kind`,
			b.Account, f.Key, f.Parent, f.Name, f.Kind); err != nil {
			return err
		}
	}
	return nil
}

func loadMailExisting(ctx context.Context, tx *sql.Tx, account string) (map[uint32]*mailExisting, error) {
	byKey := map[uint32]*mailExisting{}
	byRow := map[int64]*mailExisting{}
	err := eachRow(ctx, tx, `select m.rowid, m.detail_key, m.internet_message_id, m.folder_key, coalesce(f.kind,''), m.to_me, m.is_read, m.read_state, m.flag, m.importance,
  m.has_attachments, m.body_state, m.gone_at is not null, m.evicted_at is not null, m.received_at,
  exists(select 1 from mail_recipients r where r.message_rowid=m.rowid)
from mail_messages m left join mail_folders f on f.account=m.account and f.folder_key=m.folder_key where m.account=? order by m.rowid`, []any{account}, func(r *sql.Rows) error {
		ex := &mailExisting{atts: map[uint32]bool{}}
		if err := r.Scan(&ex.rowid, &ex.detailKey, &ex.msgID, &ex.folderKey, &ex.kind, &ex.toMe, &ex.isRead, &ex.readState, &ex.flag, &ex.imp,
			&ex.hasAtt, &ex.body, &ex.gone, &ex.evicted, &ex.received, &ex.hasRecips); err != nil {
			return err
		}
		byKey[ex.detailKey], byRow[ex.rowid] = ex, ex
		return nil
	})
	if err != nil {
		return nil, err
	}
	err = eachRow(ctx, tx, `select a.message_rowid, a.attachment_key, a.downloaded from mail_attachments a join mail_messages m on m.rowid=a.message_rowid where m.account=?`, []any{account}, func(r *sql.Rows) error {
		var row int64
		var key uint32
		var dl bool
		if err := r.Scan(&row, &key, &dl); err != nil {
			return err
		}
		byRow[row].atts[key] = dl
		return nil
	})
	return byKey, err
}

// rekeyMail moves a stored message to the detail key Outlook now gives it.
func rekeyMail(ctx context.Context, tx *sql.Tx, account string, ex *mailExisting, to uint32) error {
	if _, err := tx.ExecContext(ctx, `update mail_messages set detail_key=? where rowid=?`, to, ex.rowid); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `delete from mail_absent where account=? and detail_key=?`, account, ex.detailKey); err != nil {
		return err
	}
	ex.detailKey = to
	return nil
}

// mailDerived is what is computed from a message before it is stored.
type mailDerived struct {
	norm   string
	state  string
	gz     []byte // nil when the message has no HTML
	text   string
	hasAtt bool
}

func deriveMail(m outlookmail.Message) mailDerived {
	d := mailDerived{norm: NormalizeSubject(m.Subject), state: m.Body.State}
	for _, a := range m.Attachments {
		if !a.Inline {
			d.hasAtt = true
		}
	}
	switch {
	case len(m.Body.HTML) > 0:
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		_, _ = zw.Write(m.Body.HTML) // a bytes.Buffer cannot fail
		_ = zw.Close()
		d.gz, d.text = buf.Bytes(), outlookmail.HTMLText(m.Body.HTML)
	case d.state == outlookmail.BodyNotRead:
		d.state = bodyPending // the body file was not read
	}
	return d
}

func b2i(v bool) int {
	if v {
		return 1
	}
	return 0
}

func readFlag(m outlookmail.Message) any {
	if m.Unread == nil {
		return nil
	}
	return b2i(!*m.Unread)
}

func insertMail(ctx context.Context, tx *sql.Tx, account string, m outlookmail.Message, d mailDerived, at string) error {
	r, err := tx.ExecContext(ctx, `insert into mail_messages(account, detail_key, internet_message_id, folder_key, to_me, subject, subject_norm, sender_name, sender_address, preview,
  in_reply_to, received_at, sent_at, class, importance, ical_uid, is_read, read_state, flag, has_attachments, body_state, body_html_gz, body_text, body_text_version, first_seen_at, state_seen_at)
values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		account, m.DetailKey, m.MessageID, m.FolderKey, b2i(m.ToMe), m.Subject, d.norm, m.SenderName, m.SenderAddress, m.Preview,
		m.InReplyTo, fmtTime(m.Received), fmtTime(m.Sent), m.Class, m.Importance, strings.ToLower(m.ICalUID), readFlag(m), m.ReadState, m.Flag, b2i(d.hasAtt), d.state, d.gz, d.text, MailTextVersion, at, at)
	if err != nil {
		return err
	}
	id, err := r.LastInsertId()
	if err != nil {
		return err
	}
	if err := insertMailChildren(ctx, tx, id, m, nil, true); err != nil {
		return err
	}
	return indexMail(ctx, tx, id, m, d.text)
}

// replaceMail rewrites a stored message with the content of a different one that took its key.
func replaceMail(ctx context.Context, tx *sql.Tx, ex *mailExisting, m outlookmail.Message, d mailDerived, at string) error {
	if _, err := tx.ExecContext(ctx, `update mail_messages set internet_message_id=?, folder_key=?, to_me=?, subject=?, subject_norm=?, sender_name=?, sender_address=?, preview=?,
  in_reply_to=?, received_at=?, sent_at=?, class=?, importance=?, ical_uid=?, is_read=?, read_state=?, flag=?, has_attachments=?, body_state=?, body_html_gz=?, body_text=?, body_text_version=?,
  state_seen_at=?, gone_at=null, evicted_at=null where rowid=?`,
		m.MessageID, m.FolderKey, b2i(m.ToMe), m.Subject, d.norm, m.SenderName, m.SenderAddress, m.Preview,
		m.InReplyTo, fmtTime(m.Received), fmtTime(m.Sent), m.Class, m.Importance, strings.ToLower(m.ICalUID), readFlag(m), m.ReadState, m.Flag, b2i(d.hasAtt), d.state, d.gz, d.text, MailTextVersion,
		at, ex.rowid); err != nil {
		return err
	}
	for _, q := range []string{`delete from mail_recipients where message_rowid=?`, `delete from mail_attachments where message_rowid=?`} {
		if _, err := tx.ExecContext(ctx, q, ex.rowid); err != nil {
			return err
		}
	}
	if err := insertMailChildren(ctx, tx, ex.rowid, m, nil, true); err != nil {
		return err
	}
	return reindexMail(ctx, tx, ex.rowid, m, d.text)
}

// insertMailChildren stores the recipients and the attachments the row does not hold yet.
func insertMailChildren(ctx context.Context, tx *sql.Tx, rowid int64, m outlookmail.Message, have map[uint32]bool, recipients bool) error {
	if recipients { // a new or replaced row, or one that holds none yet
		for i, r := range m.Recipients {
			var addr any
			if r.Address != "" {
				addr = r.Address
			}
			if _, err := tx.ExecContext(ctx, `insert into mail_recipients(message_rowid, ord, name, address, kind_raw) values(?,?,?,?,?)`, rowid, i, r.Name, addr, r.KindRaw); err != nil {
				return err
			}
		}
	}
	for _, a := range m.Attachments {
		if _, held := have[a.Key]; held {
			continue
		}
		if _, err := tx.ExecContext(ctx, `insert into mail_attachments(message_rowid, attachment_key, name, size, content_type, inline, downloaded) values(?,?,?,?,?,?,?)`,
			rowid, a.Key, a.Name, a.Size, a.ContentType, b2i(a.Inline), b2i(a.Downloaded)); err != nil {
			return err
		}
	}
	return nil
}

func indexMail(ctx context.Context, tx *sql.Tx, rowid int64, m outlookmail.Message, text string) error {
	_, err := tx.ExecContext(ctx, `insert into mail_fts(rowid, subject, sender_name, sender_address, body_text) values(?,?,?,?,?)`, rowid, m.Subject, m.SenderName, m.SenderAddress, text)
	return err
}

func reindexMail(ctx context.Context, tx *sql.Tx, rowid int64, m outlookmail.Message, text string) error {
	if _, err := tx.ExecContext(ctx, `delete from mail_fts where rowid=?`, rowid); err != nil {
		return err
	}
	return indexMail(ctx, tx, rowid, m, text)
}

// updateMail applies what a later read may change on a stored message and reports whether it
// changed anything. moved says the row was just re-keyed, which counts as a change.
func updateMail(ctx context.Context, tx *sql.Tx, ex *mailExisting, m outlookmail.Message, d mailDerived, at string, moved bool) (bool, error) {
	var sets []string
	var args []any
	set := func(col string, v any) {
		sets = append(sets, col+"=?")
		args = append(args, v)
	}
	changed := moved
	newRead := readFlag(m)
	stateChanged := ex.folderKey != m.FolderKey || ex.toMe != m.ToMe || ex.readState != m.ReadState || ex.flag != m.Flag || ex.imp != m.Importance ||
		ex.isRead.Valid != (newRead != nil) || ex.isRead.Valid && int(ex.isRead.Int64) != newRead
	if stateChanged {
		set("folder_key", m.FolderKey)
		set("to_me", b2i(m.ToMe))
		set("is_read", newRead)
		set("read_state", m.ReadState)
		set("flag", m.Flag)
		set("importance", m.Importance)
		set("state_seen_at", at)
		changed = true
	}
	if ex.gone || ex.evicted {
		sets = append(sets, "gone_at=null", "evicted_at=null")
		set("state_seen_at", at)
		changed = true
	}
	if !ex.hasAtt && d.hasAtt { // raised by a later read, never cleared
		set("has_attachments", b2i(d.hasAtt))
		changed = true
	}
	fill := ex.body != outlookmail.BodyInline && ex.body != outlookmail.BodyFile
	switch {
	case fill && d.gz != nil:
		set("body_state", d.state)
		set("body_html_gz", d.gz)
		set("body_text", d.text)
		set("body_text_version", MailTextVersion)
		changed = true
	case fill && d.state != ex.body:
		set("body_state", d.state)
		changed = true
	}
	for _, a := range m.Attachments {
		if dl, held := ex.atts[a.Key]; held && dl != a.Downloaded {
			if _, err := tx.ExecContext(ctx, `update mail_attachments set downloaded=? where message_rowid=? and attachment_key=?`, b2i(a.Downloaded), ex.rowid, a.Key); err != nil {
				return false, err
			}
			changed = true
		}
	}
	if err := insertNewAttachments(ctx, tx, ex, m, &changed); err != nil {
		return false, err
	}
	if len(sets) == 0 {
		return changed, nil
	}
	if _, err := tx.ExecContext(ctx, `update mail_messages set `+strings.Join(sets, ", ")+` where rowid=?`, append(args, ex.rowid)...); err != nil { //nolint:gosec // G202: sets are fixed column fragments
		return false, err
	}
	if fill && d.gz != nil {
		if err := reindexMail(ctx, tx, ex.rowid, m, d.text); err != nil {
			return false, err
		}
	}
	return changed, nil
}

func insertNewAttachments(ctx context.Context, tx *sql.Tx, ex *mailExisting, m outlookmail.Message, changed *bool) error {
	for _, a := range m.Attachments {
		if _, held := ex.atts[a.Key]; !held {
			*changed = true
		}
	}
	if !ex.hasRecips && len(m.Recipients) > 0 {
		*changed = true
	}
	return insertMailChildren(ctx, tx, ex.rowid, m, ex.atts, !ex.hasRecips)
}

func writeMailCoverage(ctx context.Context, tx *sql.Tx, b MailBatch, at string) error {
	for _, c := range b.Result.Coverage {
		if _, err := tx.ExecContext(ctx, `insert into mail_coverage(account, folder_key, oldest_at, newest_at, count, read_at) values(?,?,?,?,?,?)
on conflict(account, folder_key) do update set oldest_at=excluded.oldest_at, newest_at=excluded.newest_at, count=excluded.count, read_at=excluded.read_at`,
			b.Account, c.FolderKey, fmtTime(c.Oldest), fmtTime(c.Newest), c.Count, at); err != nil {
			return err
		}
	}
	if !b.Trusted {
		return nil
	}
	// A trusted read that holds nothing of a folder says the folder is empty now.
	_, err := tx.ExecContext(ctx, `delete from mail_coverage where account=? and read_at<>?`, b.Account, at)
	return err
}

func loadMailAbsent(ctx context.Context, tx *sql.Tx, account string) (map[uint32]mailAbsent, error) {
	absent := map[uint32]mailAbsent{}
	err := eachRow(ctx, tx, `select detail_key, misses, fresh_at from mail_absent where account=?`, []any{account}, func(r *sql.Rows) error {
		var k uint32
		var a mailAbsent
		if err := r.Scan(&k, &a.misses, &a.fresh); err != nil {
			return err
		}
		absent[k] = a
		return nil
	})
	return absent, err
}

// mailAbsent is one remembered miss.
type mailAbsent struct {
	misses int
	fresh  string
}

// judgeMissing applies the two-read rule to the messages a trusted read did not hold.
func judgeMissing(ctx context.Context, tx *sql.Tx, b MailBatch, at string, byKey map[uint32]*mailExisting, absent map[uint32]mailAbsent, live int, res MailResult) (MailResult, error) {
	var missing []*mailExisting
	named := make(map[uint32]bool, len(b.Result.SeenDetailKeys))
	for _, k := range b.Result.SeenDetailKeys {
		named[k] = true
	}
	for _, k := range sortedMailKeys(byKey) {
		// A header still names the key (its detail or folder may be missing for now): not absent.
		if ex := byKey[k]; !ex.seen && !ex.gone && !ex.evicted && !named[k] {
			missing = append(missing, ex)
		}
	}
	rng, err := loadMailRanges(ctx, tx, b.Account)
	if err != nil {
		return res, err
	}
	// Only candidates for gone count toward withholding: a message older than its covered range
	// is evicted by the cache, which says nothing about a read that lost messages.
	goneCandidates := 0
	for _, ex := range missing {
		if rng.verdict(ex) != "evicted_at" {
			goneCandidates++
		}
	}
	if goneCandidates > MailGoneWithholdMin && goneCandidates*100 > live*MailGoneWithholdPercent {
		res.Withheld = goneCandidates
		return res, nil
	}
	fresh := ""
	if !b.FreshAt.IsZero() {
		fresh = b.FreshAt.UTC().Format(timeLayout)
	}
	for _, ex := range missing {
		prev, had := absent[ex.detailKey]
		if !had {
			if _, err := tx.ExecContext(ctx, `insert into mail_absent(account, detail_key, first_missed_at, misses, fresh_at) values(?,?,?,1,?)`, b.Account, ex.detailKey, at, fresh); err != nil {
				return res, err
			}
			continue
		}
		if fresh == "" || fresh <= prev.fresh {
			continue // the same store copy, or one with no age: not a second look
		}
		if _, err := tx.ExecContext(ctx, `update mail_absent set misses=misses+1, fresh_at=? where account=? and detail_key=?`, fresh, b.Account, ex.detailKey); err != nil {
			return res, err
		}
		col := rng.verdict(ex)
		if col == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `update mail_messages set `+col+`=? where rowid=?`, at, ex.rowid); err != nil { //nolint:gosec // G202: col is one of two column names
			return res, err
		}
		if col == "gone_at" {
			res.Gone++
		} else {
			res.Evicted++
		}
	}
	return res, nil
}

// mailRanges holds the oldest received time each folder's read covers.
type mailRanges struct {
	own      map[uint32]string // folder key to oldest_at, for inbox, to_me and sent folders
	combined string            // the earliest of them
}

func loadMailRanges(ctx context.Context, tx *sql.Tx, account string) (mailRanges, error) {
	r := mailRanges{own: map[uint32]string{}}
	err := eachRow(ctx, tx, `select c.folder_key, c.oldest_at from mail_coverage c join mail_folders f on f.account=c.account and f.folder_key=c.folder_key
where c.account=? and f.kind in ('inbox','to_me','sent') and c.oldest_at is not null`, []any{account}, func(rows *sql.Rows) error {
		var k uint32
		var oldest string
		if err := rows.Scan(&k, &oldest); err != nil {
			return err
		}
		r.own[k] = oldest
		if r.combined == "" || oldest < r.combined {
			r.combined = oldest
		}
		return nil
	})
	return r, err
}

// verdict says which mark an absent message gets: gone_at when it was received inside the range
// its folder's read covers, evicted_at when it is older, and none when the range or the time is
// unknown.
func (r mailRanges) verdict(ex *mailExisting) string {
	oldest := r.combined
	if own, ok := r.own[ex.folderKey]; ok {
		oldest = own
	}
	if oldest == "" || !ex.received.Valid {
		return ""
	}
	if ex.received.String >= oldest {
		return "gone_at"
	}
	return "evicted_at"
}

// MailHolds reports whether the archive holds any mail of the account: the collector refuses a
// store with no mail objects when it does (outlookmail.Options.ExpectMail).
func (s *Store) MailHolds(ctx context.Context, account string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `select exists(select 1 from mail_messages where account=?)`, account).Scan(&n)
	return n == 1, err
}

// MailNeedBody returns the NeedBody function for outlookmail.Options: a message needs its body
// read unless the archive already holds it (a stored body_state of inline or file).
func (s *Store) MailNeedBody(ctx context.Context, account string) (func(uint32) bool, error) {
	have := map[uint32]bool{}
	err := mailEach(ctx, s.db, `select detail_key from mail_messages where account=? and body_state in ('inline','file')`, []any{account}, func(r *sql.Rows) error {
		var k uint32
		if err := r.Scan(&k); err != nil {
			return err
		}
		have[k] = true
		return nil
	})
	if err != nil {
		return nil, err
	}
	return func(k uint32) bool { return !have[k] }, nil
}

// NormalizeSubject strips the reply and forward prefixes (re, fw, fwd, aw, wg, sv, in any case,
// followed by a colon), repeated, and trims the result.
func NormalizeSubject(s string) string {
	s = strings.TrimSpace(s)
	for {
		n := stripSubjectPrefix(s)
		if n == s {
			return s
		}
		s = n
	}
}

var subjectPrefixes = []string{"re", "fwd", "fw", "aw", "wg", "sv"}

func stripSubjectPrefix(s string) string {
	for _, p := range subjectPrefixes {
		if len(s) < len(p) || !strings.EqualFold(s[:len(p)], p) {
			continue
		}
		if rest := strings.TrimLeft(s[len(p):], " \t"); strings.HasPrefix(rest, ":") {
			return strings.TrimSpace(rest[1:])
		}
	}
	return s
}
