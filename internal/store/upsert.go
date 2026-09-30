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
	if a.TenantID == "" || a.UserID == "" {
		return errors.New("account needs tenant and user ids")
	}
	now := nowText()
	_, err := s.db.ExecContext(ctx, `
insert into accounts(tenant_id, user_id, locale, first_seen_at, last_synced_at) values(?,?,?,?,?)
on conflict(tenant_id, user_id) do update set locale = case when excluded.locale <> '' then excluded.locale else accounts.locale end, last_synced_at = excluded.last_synced_at`,
		a.TenantID, a.UserID, a.Locale, now, now)
	return err
}

func bool01(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ApplyConversations upserts conversations (one transaction) and refreshes their title index.
func (s *Store) ApplyConversations(ctx context.Context, cs []teamsdesktop.Conversation) (Counts, error) {
	var n Counts
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		sel, err := tx.PrepareContext(ctx, `select rowid, content_hash from conversations where tenant_id=? and user_id=? and id=?`)
		if err != nil {
			return err
		}
		defer func() { _ = sel.Close() }()
		ins, err := tx.PrepareContext(ctx, `insert into conversations(tenant_id,user_id,id,kind,title,topic,display_name,team_id,parent_id,members_json,last_message_at,read_horizon_at,read_horizon_message_id,favorite,raw_json,content_hash,updated_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
		if err != nil {
			return err
		}
		defer func() { _ = ins.Close() }()
		upd, err := tx.PrepareContext(ctx, `update conversations set kind=?,title=?,topic=?,display_name=?,team_id=?,parent_id=?,members_json=?,last_message_at=?,read_horizon_at=?,read_horizon_message_id=?,favorite=?,raw_json=?,content_hash=?,updated_at=? where rowid=?`)
		if err != nil {
			return err
		}
		defer func() { _ = upd.Close() }()
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
		now := nowText()
		for _, c := range cs {
			n.Seen++
			if c.TenantID == "" || c.UserID == "" || c.ID == "" {
				return errors.New("conversation needs tenant, user and id")
			}
			members, last, horizon := jsonOrNil(c.Members), fmtTime(c.LastMessageAt), fmtTime(c.ReadHorizonAt)
			raw := rawOrNil(c.Raw)
			hash := hashOf(c.Kind, c.Title, c.Topic, c.DisplayName, c.TeamID, c.ParentID, members, last, horizon, c.ReadHorizonMessageID, c.Favorite, raw)
			var rowid int64
			var old string
			err := sel.QueryRowContext(ctx, c.TenantID, c.UserID, c.ID).Scan(&rowid, &old)
			switch {
			case errors.Is(err, sql.ErrNoRows):
				res, err := ins.ExecContext(ctx, c.TenantID, c.UserID, c.ID, c.Kind, c.Title, c.Topic, c.DisplayName, c.TeamID, c.ParentID, members, last, horizon, c.ReadHorizonMessageID, bool01(c.Favorite), raw, hash, now)
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
				if _, err := upd.ExecContext(ctx, c.Kind, c.Title, c.Topic, c.DisplayName, c.TeamID, c.ParentID, members, last, horizon, c.ReadHorizonMessageID, bool01(c.Favorite), raw, hash, now, rowid); err != nil {
					return err
				}
				if _, err := ftsDel.ExecContext(ctx, rowid); err != nil {
					return err
				}
				n.Updated++
			}
			if _, err := ftsIns.ExecContext(ctx, rowid, c.ID, titleText(c)); err != nil {
				return err
			}
		}
		return nil
	})
	return n, err
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
	var n Counts
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		sel, err := tx.PrepareContext(ctx, `select rowid, version, content_hash from messages where tenant_id=? and user_id=? and conversation_id=? and id=?`)
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
			hash := hashOf(m.ReplyChainID, m.ParentMessageID, m.ClientMessageID, m.SenderID, m.SenderName, sent, edited, deleted, m.MessageType, m.ContentType, m.ContentHTML, m.ContentText, m.Version, mentions, m.MentionsMe, reactions, files, links, m.Subject, m.Importance, m.Pinned, m.Link, raw)
			var rowid, version int64
			var old string
			err := sel.QueryRowContext(ctx, m.TenantID, m.UserID, m.ConversationID, m.ID).Scan(&rowid, &version, &old)
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
			}
			key := m.TenantID + "|" + m.UserID + "|" + m.ConversationID + "|" + m.ID
			if _, err := ftsIns.ExecContext(ctx, rowid, key, m.ContentText); err != nil {
				return err
			}
		}
		return nil
	})
	return n, err
}

// ApplyPeople upserts people: the display name follows the latest non-empty value, first_seen_at
// keeps the earliest sighting and last_seen_at the latest.
func (s *Store) ApplyPeople(ctx context.Context, ps []teamsdesktop.Person) (Counts, error) {
	var n Counts
	err := s.inTx(ctx, func(tx *sql.Tx) error {
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
	})
	return n, err
}

// ApplyActivity upserts activity-feed items; a changed item (read state, content) updates.
func (s *Store) ApplyActivity(ctx context.Context, as []teamsdesktop.Activity) (Counts, error) {
	var n Counts
	err := s.inTx(ctx, func(tx *sql.Tx) error {
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
			switch {
			case errors.Is(err, sql.ErrNoRows):
				n.Inserted++
			case err != nil:
				return err
			case old == hash:
				n.Unchanged++
				continue
			default:
				n.Updated++
			}
			if _, err := up.ExecContext(ctx, a.TenantID, a.UserID, a.ID, a.Type, a.Subtype, bool01(a.IsRead), at, a.ConversationID, a.MessageID, a.ReplyChainID, a.AppID, raw, hash, now); err != nil {
				return err
			}
		}
		return nil
	})
	return n, err
}
