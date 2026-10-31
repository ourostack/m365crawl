package store

import (
	"cmp"
	"context"
	"database/sql"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/ourostack/m365crawl/internal/outlookmail"
)

// Folder membership (#270).
//
// Outlook for Mac keeps one detail object per message and one header object per folder copy: a
// message sent to oneself has copies in Sent Items, Inbox and To Me, and a sent message whose
// Inbox copy was deleted has copies in Sent Items and Deleted Items. mail_messages.folder_key is
// the copy the message is shown from (outlookmail's currentCopy). mail_message_folders records
// every folder of the read's folder set that holds a copy, with that copy's read state, for a
// message found in two or more folders; a message without rows is in its stored folder alone.
// The rows always include the stored folder.
//
// Only a native read writes them, from outlookmail.Message.FolderCopies. A Windows OWA item and
// a WorkIQ server item are one folder each and get no rows. A server write that changes the
// stored folder clears the rows: server folders have their own keys, and the server's folder
// stands until a newer native read gives the copies again.
//
// --folder, mail folders counts, the Sent Items clock (mail_clock.go) and the folder-copy rule
// (mail_copies.go) use the membership. Ids, folder and folder_kind do not change.

// mailFoldersReadyKey is the meta key prefix that marks an account whose native reads have
// written membership. The first native read without it fills the membership of the messages
// already archived without counting them as updated.
const mailFoldersReadyKey = "mail_folders_ready:"

// MailFolderRef is one folder a message is in, with the read state of its copy there (nil when
// unknown).
type MailFolderRef struct {
	Key        uint32
	Name, Kind string
	IsRead     *bool
}

// hasMailMembership reports whether the archive has the membership table (schema 24); a
// read-only open of an older archive does not.
func hasMailMembership(ctx context.Context, q mailQuerier) (bool, error) {
	var n int
	err := mailEach(ctx, q, `select count(*) from sqlite_master where type='table' and name='mail_message_folders'`, nil, func(r *sql.Rows) error {
		return r.Scan(&n)
	})
	return n > 0, err
}

// memberRowids is a select of the rowids of the messages of account acct (an SQL expression)
// in a folder of keys (an SQL list, such as "(1,2)" or "(f.folder_key)"), through the stored
// folder or the membership. unread keeps those whose copy there is unread: the membership row's
// read state, or the message's own when it has no rows. n is how many times acct appears.
func memberRowids(acct, keys string, unread, table bool) (q string, n int) {
	stored := `select x.rowid from mail_messages x where x.account=` + acct + ` and x.folder_key in ` + keys
	if !table {
		if unread {
			stored += ` and x.is_read=0`
		}
		return stored, 1
	}
	member := `select y.message_rowid from mail_message_folders y where y.account=` + acct + ` and y.folder_key in ` + keys
	if unread {
		member += ` and y.is_read=0`
		stored += ` and x.is_read=0 and not exists(select 1 from mail_message_folders z where z.message_rowid=x.rowid)`
	}
	return member + ` union all ` + stored, 2
}

// inFolders is a condition on m: the message is in one of the given folders of its account
// (keys by account), through its stored folder or its membership, with its arguments; "" when
// keys is empty. unread keeps those whose copy there is unread, as memberRowids says. Every term
// of the OR can use an index on its own: mail_messages_folder for the stored folder and
// mail_message_folders_folder for the membership, looked up by rowid.
func inFolders(keys map[string][]uint32, unread, table bool) (string, []any) {
	var conds []string
	var args []any
	for _, account := range slices.Sorted(maps.Keys(keys)) {
		ks := slices.Clone(keys[account])
		slices.Sort(ks)
		ks = slices.Compact(ks)
		list := make([]string, len(ks))
		for i, k := range ks {
			list[i] = strconv.FormatUint(uint64(k), 10)
		}
		in := `(` + strings.Join(list, ",") + `)`
		stored := `(m.account=? and m.folder_key in ` + in
		if unread {
			stored += ` and m.is_read=0`
			if table {
				stored += ` and not exists(select 1 from mail_message_folders z where z.message_rowid=m.rowid)`
			}
		}
		conds, args = append(conds, stored+`)`), append(args, account)
		if table {
			member := `m.rowid in (select y.message_rowid from mail_message_folders y where y.account=? and y.folder_key in ` + in
			if unread {
				member += ` and y.is_read=0`
			}
			conds, args = append(conds, member+`)`), append(args, account)
		}
	}
	if len(conds) == 0 {
		return "", nil
	}
	return `(` + strings.Join(conds, " or ") + `)`, args
}

// mailMember is one stored membership row: a folder and its copy's read state.
type mailMember struct {
	key    uint32
	isRead sql.NullInt64
}

// loadMailMembers reads the account's membership rows by message rowid.
func loadMailMembers(ctx context.Context, q mailQuerier, account string) (map[int64][]mailMember, error) {
	out := map[int64][]mailMember{}
	err := mailEach(ctx, q, `select message_rowid, folder_key, is_read from mail_message_folders where account=? order by message_rowid, folder_key`, []any{account}, func(r *sql.Rows) error {
		var rowid int64
		var m mailMember
		if err := r.Scan(&rowid, &m.key, &m.isRead); err != nil {
			return err
		}
		out[rowid] = append(out[rowid], m)
		return nil
	})
	return out, err
}

// wantMailMembers is the membership a native read gives a message: one row per folder of its
// copies, the stored folder always among them (with the message's own read state when the copies
// do not name it), sorted by key; none when the message is in fewer than two folders.
func wantMailMembers(m outlookmail.Message) []mailMember {
	by := map[uint32]sql.NullInt64{}
	for _, c := range m.FolderCopies {
		by[c.Key] = readValue(c.Unread)
	}
	if _, ok := by[m.FolderKey]; !ok {
		by[m.FolderKey] = readValue(m.Unread)
	}
	if len(by) < 2 {
		return nil
	}
	out := make([]mailMember, 0, len(by))
	for _, k := range slices.Sorted(maps.Keys(by)) {
		out = append(out, mailMember{k, by[k]})
	}
	return out
}

// readValue is a copy's is_read: 1 read, 0 unread, NULL unknown.
func readValue(unread *bool) sql.NullInt64 {
	if unread == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: int64(b2i(!*unread)), Valid: true}
}

// syncMailMembers writes a message's membership when it differs from the stored rows (have) and
// reports whether it did.
func syncMailMembers(ctx context.Context, tx *sql.Tx, account string, rowid int64, have, want []mailMember) (bool, error) {
	if slices.Equal(have, want) {
		return false, nil
	}
	if _, err := tx.ExecContext(ctx, `delete from mail_message_folders where message_rowid=?`, rowid); err != nil {
		return false, err
	}
	for _, w := range want {
		if _, err := tx.ExecContext(ctx, `insert into mail_message_folders(message_rowid, account, folder_key, is_read) values(?,?,?,?)`, rowid, account, w.key, w.isRead); err != nil {
			return false, err
		}
	}
	return true, nil
}

// clearMailMembers drops a message's membership: a server write moved it to a folder the native
// copies do not name.
func clearMailMembers(ctx context.Context, tx *sql.Tx, rowid int64) error {
	_, err := tx.ExecContext(ctx, `delete from mail_message_folders where message_rowid=?`, rowid)
	return err
}

// loadMailFolderRefs fills Folders on rows: the membership in the order mail folders lists
// folders, or the stored folder alone.
func loadMailFolderRefs(ctx context.Context, q mailQuerier, rows []MailRow) error {
	at := make(map[int64]int, len(rows))
	ids := make([]int64, len(rows))
	for i, r := range rows {
		at[r.Rowid] = i
		ids[i] = r.Rowid
	}
	if len(rows) > 0 {
		table, err := hasMailMembership(ctx, q)
		if err != nil {
			return err
		}
		if table {
			err = mailEach(ctx, q, `select f.message_rowid, f.folder_key, coalesce(d.name,''), coalesce(d.kind,'unknown'), f.is_read from mail_message_folders f
left join mail_folders d on d.account=f.account and d.folder_key=f.folder_key where f.message_rowid in (`+joinIDs(ids)+`)`, nil, func(r *sql.Rows) error { //nolint:gosec // G202: row ids are integers
				var rowid int64
				var f MailFolderRef
				var isRead sql.NullInt64
				if err := r.Scan(&rowid, &f.Key, &f.Name, &f.Kind, &isRead); err != nil {
					return err
				}
				if isRead.Valid {
					v := isRead.Int64 == 1
					f.IsRead = &v
				}
				rows[at[rowid]].Folders = append(rows[at[rowid]].Folders, f)
				return nil
			})
			if err != nil {
				return err
			}
		}
	}
	for i := range rows {
		r := &rows[i]
		if len(r.Folders) == 0 {
			r.Folders = []MailFolderRef{{Key: r.FolderKey, Name: r.Folder, Kind: r.FolderKind, IsRead: r.IsRead}}
		}
		slices.SortStableFunc(r.Folders, func(a, b MailFolderRef) int {
			if ra, rb := mailKindRank(a.Kind), mailKindRank(b.Kind); ra != rb {
				return ra - rb
			}
			if na, nb := strings.ToLower(a.Name), strings.ToLower(b.Name); na != nb {
				return strings.Compare(na, nb)
			}
			return cmp.Compare(a.Key, b.Key)
		})
	}
	return nil
}

// inSent reports whether a row is in a Sent Items folder, through its stored folder or its
// membership: such a message is dated by when it was sent (mail_clock.go).
func (r MailRow) inSent() bool {
	if r.FolderKind == "sent" {
		return true
	}
	for _, f := range r.Folders {
		if f.Kind == "sent" {
			return true
		}
	}
	return false
}
