package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ourostack/teamscrawl/internal/teamsdesktop"
)

func hashOf(parts ...any) string {
	h := sha256.New()
	for _, p := range parts {
		_, _ = fmt.Fprint(h, p)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func nowText() string { return time.Now().UTC().Format(timeLayout) }

func (s *Store) inTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	return s.cs.WithTx(ctx, fn)
}

// ApplyAccount records the account, keeping first_seen_at and stamping last_synced_at.
func (s *Store) ApplyAccount(ctx context.Context, a teamsdesktop.Account) error {
	return applyAccount(ctx, s.db, a)
}

// execer is what a statement needs: a database or a transaction.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func applyAccount(ctx context.Context, db execer, a teamsdesktop.Account) error {
	if a.TenantID == "" || a.UserID == "" {
		return errors.New("account needs tenant and user ids")
	}
	now := nowText()
	_, err := db.ExecContext(ctx, `
insert into accounts(tenant_id, user_id, locale, first_seen_at, last_synced_at) values(?,?,?,?,?)
on conflict(tenant_id, user_id) do update set locale = case when excluded.locale <> '' then excluded.locale else accounts.locale end, last_synced_at = excluded.last_synced_at`,
		a.TenantID, a.UserID, a.Locale, now, now)
	return err
}

// Change kinds reported by ApplyMessagesChanges and ApplyActivityChanges.
const (
	ChangeNew     = "new"
	ChangeEdited  = "edited"
	ChangeDeleted = "deleted"
)

// Change is one row an Apply call inserted or updated. Key identifies the row:
// "<tenant>|<user>|<conversation>|<id>" for a message, "<tenant>|<user>|<id>" for an activity
// item. Rows that were unchanged are not reported.
type Change struct{ Change, Key string }

func bool01(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ApplyConversations upserts conversations (one transaction) and refreshes their title index.
func (s *Store) ApplyConversations(ctx context.Context, cs []teamsdesktop.Conversation) (n Counts, err error) {
	err = s.inTx(ctx, func(tx *sql.Tx) (e error) { n, e = applyConversations(ctx, tx, cs); return })
	return n, err
}

func applyConversations(ctx context.Context, tx *sql.Tx, cs []teamsdesktop.Conversation) (Counts, error) {
	var n Counts
	err := func() error {
		sel, err := tx.PrepareContext(ctx, `select rowid, content_hash, read_horizon_at, read_horizon_client_message_id from conversations where tenant_id=? and user_id=? and id=?`)
		if err != nil {
			return err
		}
		defer func() { _ = sel.Close() }()
		ins, err := tx.PrepareContext(ctx, `insert into conversations(tenant_id,user_id,id,kind,title,topic,display_name,team_id,parent_id,members_json,last_message_at,read_horizon_at,read_horizon_client_message_id,favorite,raw_json,content_hash,updated_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
		if err != nil {
			return err
		}
		defer func() { _ = ins.Close() }()
		upd, err := tx.PrepareContext(ctx, `update conversations set kind=?,title=?,topic=?,display_name=?,team_id=?,parent_id=?,members_json=?,last_message_at=?,read_horizon_at=?,read_horizon_client_message_id=?,favorite=?,raw_json=?,content_hash=?,updated_at=? where rowid=?`)
		if err != nil {
			return err
		}
		defer func() { _ = upd.Close() }()
		now := nowText()
		var touched []teamsdesktop.Conversation
		for _, c := range cs {
			n.Seen++
			if c.TenantID == "" || c.UserID == "" || c.ID == "" {
				return errors.New("conversation needs tenant, user and id")
			}
			members, last, horizon := jsonOrNil(c.Members), fmtTime(c.LastMessageAt), fmtTime(c.ReadHorizonAt)
			raw := rawOrNil(c.Raw)
			var rowid int64
			var old string
			var oldHorizon sql.NullString
			var oldClientID string
			err := sel.QueryRowContext(ctx, c.TenantID, c.UserID, c.ID).Scan(&rowid, &old, &oldHorizon, &oldClientID)
			known := err == nil
			if known {
				// A later snapshot that lost the read marker must not forget a known one.
				if horizon == nil && oldHorizon.Valid {
					horizon = oldHorizon.String
				}
				if c.ReadHorizonClientMessageID == "" && horizon == oldHorizon.String {
					c.ReadHorizonClientMessageID = oldClientID
				}
			}
			hash := hashOf(c.Kind, c.Title, c.Topic, c.DisplayName, c.TeamID, c.ParentID, members, last, horizon, c.ReadHorizonClientMessageID, c.Favorite, raw)
			switch {
			case errors.Is(err, sql.ErrNoRows):
				res, err := ins.ExecContext(ctx, c.TenantID, c.UserID, c.ID, c.Kind, c.Title, c.Topic, c.DisplayName, c.TeamID, c.ParentID, members, last, horizon, c.ReadHorizonClientMessageID, bool01(c.Favorite), raw, hash, now)
				if err != nil {
					return err
				}
				if rowid, err = res.LastInsertId(); err != nil {
					return err
				}
				n.Inserted++
			case err != nil:
				return err
			case old == hash:
				n.Unchanged++
				continue
			default:
				if _, err := upd.ExecContext(ctx, c.Kind, c.Title, c.Topic, c.DisplayName, c.TeamID, c.ParentID, members, last, horizon, c.ReadHorizonClientMessageID, bool01(c.Favorite), raw, hash, now, rowid); err != nil {
					return err
				}
				n.Updated++
			}
			touched = append(touched, c)
		}
		return reindexTitles(ctx, tx, touched)
	}()
	return n, err
}

// reindexTitles refreshes the title index of the given conversations and of every channel whose
// team is among them. It runs after the whole batch is stored, so a team and its channels find
// each other whatever order they arrived in. A channel is indexed under its own names plus its
// team's name, the same name the display composes ("Team › Channel").
func reindexTitles(ctx context.Context, tx *sql.Tx, touched []teamsdesktop.Conversation) error {
	if len(touched) == 0 {
		return nil
	}
	own, err := tx.PrepareContext(ctx, `select rowid, id, title, topic, display_name, team_id from conversations where tenant_id=? and user_id=? and id=?`)
	if err != nil {
		return err
	}
	defer func() { _ = own.Close() }()
	children, err := tx.PrepareContext(ctx, `select id from conversations where tenant_id=? and user_id=? and team_id=? and id<>?`)
	if err != nil {
		return err
	}
	defer func() { _ = children.Close() }()
	ftsDel, err := tx.PrepareContext(ctx, `delete from conversation_fts where rowid=?`)
	if err != nil {
		return err
	}
	defer func() { _ = ftsDel.Close() }()
	ftsIns, err := tx.PrepareContext(ctx, `insert into conversation_fts(rowid, conversation_id, title) values(?,?,?)`)
	if err != nil {
		return err
	}
	defer func() { _ = ftsIns.Close() }()

	type key struct{ tenant, user, id string }
	done := map[key]bool{}
	index := func(k key) error {
		if done[k] {
			return nil
		}
		done[k] = true
		var rowid int64
		var c teamsdesktop.Conversation
		if err := own.QueryRowContext(ctx, k.tenant, k.user, k.id).Scan(&rowid, &c.ID, &c.Title, &c.Topic, &c.DisplayName, &c.TeamID); err != nil {
			return err
		}
		text := titleText(c)
		if c.TeamID != "" && c.TeamID != c.ID {
			var t teamsdesktop.Conversation
			var trow int64
			switch err := own.QueryRowContext(ctx, k.tenant, k.user, c.TeamID).Scan(&trow, &t.ID, &t.Title, &t.Topic, &t.DisplayName, &t.TeamID); {
			case errors.Is(err, sql.ErrNoRows):
			case err != nil:
				return err
			default:
				text = strings.TrimSpace(text + " " + firstNonEmpty(t.DisplayName, t.Topic, t.Title))
			}
		}
		if _, err := ftsDel.ExecContext(ctx, rowid); err != nil {
			return err
		}
		_, err := ftsIns.ExecContext(ctx, rowid, c.ID, text)
		return err
	}
	for _, c := range touched {
		if err := index(key{c.TenantID, c.UserID, c.ID}); err != nil {
			return err
		}
		rows, err := children.QueryContext(ctx, c.TenantID, c.UserID, c.ID, c.ID)
		if err != nil {
			return err
		}
		var kids []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				_ = rows.Close()
				return err
			}
			kids = append(kids, id)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if err := rows.Err(); err != nil {
			return err
		}
		for _, id := range kids {
			if err := index(key{c.TenantID, c.UserID, id}); err != nil {
				return err
			}
		}
	}
	return nil
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// titleText is what the title index holds: each distinct name the conversation goes by.
func titleText(c teamsdesktop.Conversation) string {
	var parts []string
	seen := map[string]bool{}
	for _, v := range []string{c.DisplayName, c.Title, c.Topic} {
		if v != "" && !seen[v] {
			seen[v] = true
			parts = append(parts, v)
		}
	}
	return strings.Join(parts, " ")
}

// ApplyMessages upserts messages (one transaction) and keeps the text index in step. A row
// changes only when the incoming version is newer, or the version is equal and the content
// differs; an older version is ignored. Messages absent from the batch are never touched.
func (s *Store) ApplyMessages(ctx context.Context, ms []teamsdesktop.Message) (Counts, error) {
	n, _, err := s.ApplyMessagesChanges(ctx, ms)
	return n, err
}

// ApplyMessagesChanges is ApplyMessages that also reports each inserted or updated row: "new" for
// an insert, "deleted" when the row gains its tombstone, "edited" for any other update. A
// deleted_at, once set, stays set unless a newer version arrives without it.
func (s *Store) ApplyMessagesChanges(ctx context.Context, ms []teamsdesktop.Message) (n Counts, changes []Change, err error) {
	err = s.inTx(ctx, func(tx *sql.Tx) (e error) { n, changes, e = applyMessages(ctx, tx, ms); return })
	if err != nil {
		return Counts{}, nil, err
	}
	return n, changes, nil
}

func applyMessages(ctx context.Context, tx *sql.Tx, ms []teamsdesktop.Message) (Counts, []Change, error) {
	var n Counts
	var changes []Change
	err := func() error {
		sel, err := tx.PrepareContext(ctx, `select rowid, version, content_hash, deleted_at from messages where tenant_id=? and user_id=? and conversation_id=? and id=?`)
		if err != nil {
			return err
		}
		defer func() { _ = sel.Close() }()
		ins, err := tx.PrepareContext(ctx, `insert into messages(tenant_id,user_id,conversation_id,id,reply_chain_id,parent_message_id,client_message_id,sender_id,sender_name,sent_at,edited_at,deleted_at,message_type,content_type,content_html,content_text,version,mentions_json,mentions_me,reactions_json,files_json,links_json,subject,importance,pinned,link,raw_json,content_hash,updated_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
		if err != nil {
			return err
		}
		defer func() { _ = ins.Close() }()
		upd, err := tx.PrepareContext(ctx, `update messages set reply_chain_id=?,parent_message_id=?,client_message_id=?,sender_id=?,sender_name=?,sent_at=?,edited_at=?,deleted_at=?,message_type=?,content_type=?,content_html=?,content_text=?,version=?,mentions_json=?,mentions_me=?,reactions_json=?,files_json=?,links_json=?,subject=?,importance=?,pinned=?,link=?,raw_json=?,content_hash=?,updated_at=? where rowid=?`)
		if err != nil {
			return err
		}
		defer func() { _ = upd.Close() }()
		ftsDel, err := tx.PrepareContext(ctx, `delete from message_fts where rowid=?`)
		if err != nil {
			return err
		}
		defer func() { _ = ftsDel.Close() }()
		ftsIns, err := tx.PrepareContext(ctx, `insert into message_fts(rowid, message_key, content) values(?,?,?)`)
		if err != nil {
			return err
		}
		defer func() { _ = ftsIns.Close() }()
		now := nowText()
		for _, m := range ms {
			n.Seen++
			if m.TenantID == "" || m.UserID == "" || m.ConversationID == "" || m.ID == "" {
				return errors.New("message needs tenant, user, conversation and id")
			}
			sent, edited, deleted := fmtTime(m.SentAt), fmtTime(m.EditedAt), fmtTime(m.DeletedAt)
			if sent == nil {
				sent = "" // sent_at is NOT NULL; a message with no time sorts first
			}
			mentions, reactions, files, links, raw := jsonOrNil(m.Mentions), jsonOrNil(m.Reactions), jsonOrNil(m.Files), jsonOrNil(m.Links), rawOrNil(m.Raw)
			var rowid, version int64
			var old string
			var oldDeleted sql.NullString
			err := sel.QueryRowContext(ctx, m.TenantID, m.UserID, m.ConversationID, m.ID).Scan(&rowid, &version, &old, &oldDeleted)
			if err == nil && deleted == nil && oldDeleted.Valid && m.Version <= version {
				deleted = oldDeleted.String // a known tombstone is sticky
			}
			hash := hashOf(m.ReplyChainID, m.ParentMessageID, m.ClientMessageID, m.SenderID, m.SenderName, sent, edited, deleted, m.MessageType, m.ContentType, m.ContentHTML, m.ContentText, m.Version, mentions, m.MentionsMe, reactions, files, links, m.Subject, m.Importance, m.Pinned, m.Link, raw)
			key := m.TenantID + "|" + m.UserID + "|" + m.ConversationID + "|" + m.ID
			switch {
			case errors.Is(err, sql.ErrNoRows):
				res, err := ins.ExecContext(ctx, m.TenantID, m.UserID, m.ConversationID, m.ID, m.ReplyChainID, m.ParentMessageID, m.ClientMessageID, m.SenderID, m.SenderName, sent, edited, deleted, m.MessageType, m.ContentType, m.ContentHTML, m.ContentText, m.Version, mentions, bool01(m.MentionsMe), reactions, files, links, m.Subject, m.Importance, bool01(m.Pinned), m.Link, raw, hash, now)
				if err != nil {
					return err
				}
				if rowid, err = res.LastInsertId(); err != nil {
					return err
				}
				n.Inserted++
				changes = append(changes, Change{Change: ChangeNew, Key: key})
			case err != nil:
				return err
			case m.Version < version, old == hash:
				n.Unchanged++
				continue
			default:
				if _, err := upd.ExecContext(ctx, m.ReplyChainID, m.ParentMessageID, m.ClientMessageID, m.SenderID, m.SenderName, sent, edited, deleted, m.MessageType, m.ContentType, m.ContentHTML, m.ContentText, m.Version, mentions, bool01(m.MentionsMe), reactions, files, links, m.Subject, m.Importance, bool01(m.Pinned), m.Link, raw, hash, now, rowid); err != nil {
					return err
				}
				if _, err := ftsDel.ExecContext(ctx, rowid); err != nil {
					return err
				}
				n.Updated++
				if deleted != nil && !oldDeleted.Valid {
					changes = append(changes, Change{Change: ChangeDeleted, Key: key})
				} else {
					changes = append(changes, Change{Change: ChangeEdited, Key: key})
				}
			}
			if _, err := ftsIns.ExecContext(ctx, rowid, key, m.ContentText); err != nil {
				return err
			}
		}
		return nil
	}()
	if err != nil {
		return Counts{}, nil, err
	}
	return n, changes, nil
}

// ApplyPeople upserts people: the display name follows the latest non-empty value, first_seen_at
// keeps the earliest sighting and last_seen_at the latest.
func (s *Store) ApplyPeople(ctx context.Context, ps []teamsdesktop.Person) (n Counts, err error) {
	err = s.inTx(ctx, func(tx *sql.Tx) (e error) { n, e = applyPeople(ctx, tx, ps); return })
	return n, err
}

func applyPeople(ctx context.Context, tx *sql.Tx, ps []teamsdesktop.Person) (Counts, error) {
	var n Counts
	err := func() error {
		sel, err := tx.PrepareContext(ctx, `select display_name, first_seen_at, last_seen_at from people where tenant_id=? and id=?`)
		if err != nil {
			return err
		}
		defer func() { _ = sel.Close() }()
		ins, err := tx.PrepareContext(ctx, `insert into people(tenant_id,id,display_name,first_seen_at,last_seen_at) values(?,?,?,?,?)`)
		if err != nil {
			return err
		}
		defer func() { _ = ins.Close() }()
		upd, err := tx.PrepareContext(ctx, `update people set display_name=?, first_seen_at=?, last_seen_at=? where tenant_id=? and id=?`)
		if err != nil {
			return err
		}
		defer func() { _ = upd.Close() }()
		for _, p := range ps {
			n.Seen++
			if p.TenantID == "" || p.ID == "" {
				return errors.New("person needs tenant and id")
			}
			seen := p.SeenAt
			var name string
			var first, last sql.NullString
			err := sel.QueryRowContext(ctx, p.TenantID, p.ID).Scan(&name, &first, &last)
			switch {
			case errors.Is(err, sql.ErrNoRows):
				if _, err := ins.ExecContext(ctx, p.TenantID, p.ID, p.DisplayName, fmtTime(seen), fmtTime(seen)); err != nil {
					return err
				}
				n.Inserted++
				continue
			case err != nil:
				return err
			}
			newName, newFirst, newLast := name, parseTime(first), parseTime(last)
			if p.DisplayName != "" {
				newName = p.DisplayName
			}
			if !seen.IsZero() {
				if newFirst.IsZero() || seen.Before(newFirst) {
					newFirst = seen
				}
				if seen.After(newLast) {
					newLast = seen
				}
			}
			if newName == name && newFirst.Equal(parseTime(first)) && newLast.Equal(parseTime(last)) {
				n.Unchanged++
				continue
			}
			if _, err := upd.ExecContext(ctx, newName, fmtTime(newFirst), fmtTime(newLast), p.TenantID, p.ID); err != nil {
				return err
			}
			n.Updated++
		}
		return nil
	}()
	return n, err
}

// ApplyActivity upserts activity-feed items; a changed item (read state, content) updates.
func (s *Store) ApplyActivity(ctx context.Context, as []teamsdesktop.Activity) (Counts, error) {
	n, _, err := s.ApplyActivityChanges(ctx, as)
	return n, err
}

// ApplyActivityChanges is ApplyActivity that also reports each inserted ("new") or updated
// ("edited") item.
func (s *Store) ApplyActivityChanges(ctx context.Context, as []teamsdesktop.Activity) (n Counts, changes []Change, err error) {
	err = s.inTx(ctx, func(tx *sql.Tx) (e error) { n, changes, e = applyActivity(ctx, tx, as); return })
	if err != nil {
		return Counts{}, nil, err
	}
	return n, changes, nil
}

func applyActivity(ctx context.Context, tx *sql.Tx, as []teamsdesktop.Activity) (Counts, []Change, error) {
	var n Counts
	var changes []Change
	err := func() error {
		sel, err := tx.PrepareContext(ctx, `select content_hash from activity where tenant_id=? and user_id=? and id=?`)
		if err != nil {
			return err
		}
		defer func() { _ = sel.Close() }()
		up, err := tx.PrepareContext(ctx, `insert into activity(tenant_id,user_id,id,type,subtype,is_read,at,conversation_id,message_id,reply_chain_id,app_id,raw_json,content_hash,updated_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?)
on conflict(tenant_id,user_id,id) do update set type=excluded.type,subtype=excluded.subtype,is_read=excluded.is_read,at=excluded.at,conversation_id=excluded.conversation_id,message_id=excluded.message_id,reply_chain_id=excluded.reply_chain_id,app_id=excluded.app_id,raw_json=excluded.raw_json,content_hash=excluded.content_hash,updated_at=excluded.updated_at`)
		if err != nil {
			return err
		}
		defer func() { _ = up.Close() }()
		now := nowText()
		for _, a := range as {
			n.Seen++
			if a.TenantID == "" || a.UserID == "" || a.ID == "" {
				return errors.New("activity needs tenant, user and id")
			}
			at := fmtTime(a.At)
			if at == nil {
				at = ""
			}
			raw := rawOrNil(a.Raw)
			hash := hashOf(a.Type, a.Subtype, a.IsRead, at, a.ConversationID, a.MessageID, a.ReplyChainID, a.AppID, raw)
			var old string
			err := sel.QueryRowContext(ctx, a.TenantID, a.UserID, a.ID).Scan(&old)
			key := a.TenantID + "|" + a.UserID + "|" + a.ID
			switch {
			case errors.Is(err, sql.ErrNoRows):
				n.Inserted++
				changes = append(changes, Change{Change: ChangeNew, Key: key})
			case err != nil:
				return err
			case old == hash:
				n.Unchanged++
				continue
			default:
				n.Updated++
				changes = append(changes, Change{Change: ChangeEdited, Key: key})
			}
			if _, err := up.ExecContext(ctx, a.TenantID, a.UserID, a.ID, a.Type, a.Subtype, bool01(a.IsRead), at, a.ConversationID, a.MessageID, a.ReplyChainID, a.AppID, raw, hash, now); err != nil {
				return err
			}
		}
		return nil
	}()
	if err != nil {
		return Counts{}, nil, err
	}
	return n, changes, nil
}
