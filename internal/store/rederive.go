package store

import (
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"errors"
	"io"
	"strconv"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/outlookmail"
	"github.com/ourostack/m365crawl/internal/teamsdesktop"
)

// Migration says an archive's derived fields were recomputed from their stored raw_json because a
// newer build derives them differently (see DerivationVersion). Rows counts the messages and
// conversations whose stored text, name or content hash changed. The recomputation is not a
// change of the archive's content: it counts as no update and no edit.
type Migration struct {
	From int `json:"from"`
	To   int `json:"to"`
	Rows int `json:"rows"`
	// MailRows counts the mail messages whose normalised subject or body text was recomputed
	// because a newer build derives them differently (see MailTextVersion).
	MailRows int `json:"mail_rows,omitempty"`
}

const (
	derivationKey  = "derivation_version"
	rederiveBatch  = 500
	alpha1Version  = 1 // an archive with no meta.derivation_version
	selectMessages = `select rowid, tenant_id, user_id, conversation_id, id, reply_chain_id, parent_message_id, client_message_id, sender_id, sender_name, sent_at, edited_at, deleted_at, message_type, content_type, content_html, content_text, version, mentions_json, mentions_me, reactions_json, files_json, links_json, subject, importance, pinned, link, raw_json, content_hash from messages where rowid>? order by rowid limit ?`
	selectConvs    = `select rowid, tenant_id, user_id, id, kind, title, topic, display_name, team_id, parent_id, members_json, last_message_at, read_horizon_at, read_horizon_client_message_id, favorite, raw_json, content_hash from conversations where rowid>? order by rowid limit ?`
)

// Rederive brings an archive written by an older build up to DerivationVersion: it recomputes the
// derived fields of every message and conversation from the stored raw_json, rewrites the text
// indexes of the rows that changed, and records the new version, all in one transaction. It
// returns an archive_newer error, writing nothing, when the archive carries a higher version than this build's. It returns nil when the archive is already current or has nothing to recompute (a new archive).
// The sync calls it before it reads the Teams cache, so rows the cache no longer holds are
// upgraded too.
func (s *Store) Rederive(ctx context.Context) (*Migration, error) {
	var m *Migration
	err := s.inTx(ctx, func(tx *sql.Tx) (e error) { m, e = rederive(ctx, tx); return })
	if err != nil {
		return nil, err
	}
	return m, nil
}

// DerivationVersion is the derivation version the archive was written with: the stored
// meta.derivation_version, or 1 (alpha.1) when there is none.
func (s *Store) DerivationVersion(ctx context.Context) (int, error) {
	return storedDerivation(ctx, s.db)
}

func storedDerivation(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) (int, error) {
	// An alpha.1 archive has no meta table at all, and a read-only open cannot create it.
	var tables int
	if err := q.QueryRowContext(ctx, `select count(*) from sqlite_master where type='table' and name='meta'`).Scan(&tables); err != nil {
		return 0, err
	}
	if tables == 0 {
		return alpha1Version, nil
	}
	var v string
	switch err := q.QueryRowContext(ctx, `select value from meta where key=?`, derivationKey).Scan(&v); {
	case errors.Is(err, sql.ErrNoRows):
		return alpha1Version, nil
	case err != nil:
		return 0, err
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, errors.New("archive meta.derivation_version is not a number: " + v)
	}
	return n, nil
}

func rederive(ctx context.Context, tx *sql.Tx) (*Migration, error) {
	from, err := storedDerivation(ctx, tx)
	if err != nil {
		return nil, err
	}
	if from > DerivationVersion {
		return nil, errs.ArchiveNewer(from, DerivationVersion)
	}
	mailRows, err := rederiveMail(ctx, tx)
	if err != nil {
		return nil, err
	}
	if from == DerivationVersion {
		if mailRows == 0 {
			return nil, nil
		}
		return &Migration{From: from, To: from, MailRows: mailRows}, nil
	}
	rows, err := rederiveMessages(ctx, tx)
	if err != nil {
		return nil, err
	}
	n, err := rederiveConversations(ctx, tx)
	if err != nil {
		return nil, err
	}
	rows += n
	if _, err := tx.ExecContext(ctx, `insert into meta(key, value) values(?, ?) on conflict(key) do update set value=excluded.value`, derivationKey, strconv.Itoa(DerivationVersion)); err != nil {
		return nil, err
	}
	if rows == 0 && mailRows == 0 {
		return nil, nil
	}
	return &Migration{From: from, To: DerivationVersion, Rows: rows, MailRows: mailRows}, nil
}

// storedMessage is one messages row as the rederivation reads it.
type storedMessage struct {
	rowid                                                    int64
	tenant, user, conv, id, chain, parent, client, senderID  string
	senderName, sent, mtype, ctype, html, text, subject, imp string
	link, hash                                               string
	edited, deleted, mentions, reactions, files, links, raw  sql.NullString
	version                                                  int64
	mentionsMe, pinned                                       bool
}

func (m storedMessage) hashWith(text, sender string) string {
	return hashOf(m.chain, m.parent, m.client, m.senderID, sender, m.sent, nullAny(m.edited), nullAny(m.deleted), m.mtype, m.ctype, m.html, text, m.version, nullAny(m.mentions), m.mentionsMe, nullAny(m.reactions), nullAny(m.files), nullAny(m.links), m.subject, m.imp, m.pinned, m.link, nullAny(m.raw))
}

// nullAny is the value the Apply functions hash for an optional column: nil for NULL, else the text.
func nullAny(s sql.NullString) any {
	if !s.Valid {
		return nil
	}
	return s.String
}

func rederiveMessages(ctx context.Context, tx *sql.Tx) (int, error) {
	upd, err := tx.PrepareContext(ctx, `update messages set sender_name=?, content_text=?, content_hash=? where rowid=?`)
	if err != nil {
		return 0, err
	}
	defer func() { _ = upd.Close() }()
	ftsDel, err := tx.PrepareContext(ctx, `delete from message_fts where rowid=?`)
	if err != nil {
		return 0, err
	}
	defer func() { _ = ftsDel.Close() }()
	ftsIns, err := tx.PrepareContext(ctx, `insert into message_fts(rowid, message_key, content) values(?,?,?)`)
	if err != nil {
		return 0, err
	}
	defer func() { _ = ftsIns.Close() }()
	changed, last := 0, int64(0)
	for {
		batch, err := readMessages(ctx, tx, last)
		if err != nil {
			return 0, err
		}
		if len(batch) == 0 {
			return changed, nil
		}
		last = batch[len(batch)-1].rowid
		var people []teamsdesktop.Person
		for _, m := range batch {
			if !m.raw.Valid {
				continue
			}
			text, sender, err := teamsdesktop.DeriveMessage([]byte(m.raw.String))
			if err != nil {
				continue // a record that cannot be read keeps what it has
			}
			hash := m.hashWith(text, sender)
			if text == m.text && sender == m.senderName && hash == m.hash {
				continue
			}
			if _, err := upd.ExecContext(ctx, sender, text, hash, m.rowid); err != nil {
				return 0, err
			}
			if text != m.text {
				if _, err := ftsDel.ExecContext(ctx, m.rowid); err != nil {
					return 0, err
				}
				if _, err := ftsIns.ExecContext(ctx, m.rowid, m.tenant+"|"+m.user+"|"+m.conv+"|"+m.id, text); err != nil {
					return 0, err
				}
			}
			if sender != "" && m.senderID != "" && m.senderName == "" {
				people = append(people, teamsdesktop.Person{TenantID: m.tenant, ID: m.senderID, DisplayName: sender, SeenAt: parseTime(sql.NullString{String: m.sent, Valid: true})})
			}
			changed++
		}
		if _, err := applyPeople(ctx, tx, people); err != nil {
			return 0, err
		}
	}
}

func readMessages(ctx context.Context, tx *sql.Tx, after int64) ([]storedMessage, error) {
	rows, err := tx.QueryContext(ctx, selectMessages, after, rederiveBatch)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []storedMessage
	for rows.Next() {
		var m storedMessage
		var mentionsMe, pinned int
		if err := rows.Scan(&m.rowid, &m.tenant, &m.user, &m.conv, &m.id, &m.chain, &m.parent, &m.client, &m.senderID, &m.senderName, &m.sent, &m.edited, &m.deleted, &m.mtype, &m.ctype, &m.html, &m.text, &m.version, &m.mentions, &mentionsMe, &m.reactions, &m.files, &m.links, &m.subject, &m.imp, &pinned, &m.link, &m.raw, &m.hash); err != nil {
			return nil, err
		}
		m.mentionsMe, m.pinned = mentionsMe != 0, pinned != 0
		out = append(out, m)
	}
	return out, rows.Err()
}

type storedConversation struct {
	rowid                                                    int64
	tenant, user, id, kind, title, topic, name, team, parent string
	clientID, hash                                           string
	members, last, horizon, raw                              sql.NullString
	favorite                                                 bool
}

func (c storedConversation) hashWith(name string) string {
	return hashOf(c.kind, c.title, c.topic, name, c.team, c.parent, nullAny(c.members), nullAny(c.last), nullAny(c.horizon), c.clientID, c.favorite, nullAny(c.raw))
}

func rederiveConversations(ctx context.Context, tx *sql.Tx) (int, error) {
	upd, err := tx.PrepareContext(ctx, `update conversations set display_name=?, content_hash=? where rowid=?`)
	if err != nil {
		return 0, err
	}
	defer func() { _ = upd.Close() }()
	var touched []teamsdesktop.Conversation
	last := int64(0)
	for {
		batch, err := readConversations(ctx, tx, last)
		if err != nil {
			return 0, err
		}
		if len(batch) == 0 {
			break
		}
		last = batch[len(batch)-1].rowid
		for _, c := range batch {
			if !c.raw.Valid {
				continue
			}
			name, err := teamsdesktop.DeriveConversationName(c.user, []byte(c.raw.String))
			if err != nil {
				continue
			}
			hash := c.hashWith(name)
			if name == c.name && hash == c.hash {
				continue
			}
			if _, err := upd.ExecContext(ctx, name, hash, c.rowid); err != nil {
				return 0, err
			}
			touched = append(touched, teamsdesktop.Conversation{TenantID: c.tenant, UserID: c.user, ID: c.id})
		}
	}
	if err := reindexTitles(ctx, tx, touched); err != nil {
		return 0, err
	}
	return len(touched), nil
}

func readConversations(ctx context.Context, tx *sql.Tx, after int64) ([]storedConversation, error) {
	rows, err := tx.QueryContext(ctx, selectConvs, after, rederiveBatch)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []storedConversation
	for rows.Next() {
		var c storedConversation
		var fav int
		if err := rows.Scan(&c.rowid, &c.tenant, &c.user, &c.id, &c.kind, &c.title, &c.topic, &c.name, &c.team, &c.parent, &c.members, &c.last, &c.horizon, &c.clientID, &fav, &c.raw, &c.hash); err != nil {
			return nil, err
		}
		c.favorite = fav != 0
		out = append(out, c)
	}
	return out, rows.Err()
}

const (
	mailTextKey         = "mail_text_version"
	mailRederiveBatch   = 100
	selectMailRederive  = `select rowid, subject, subject_norm, sender_name, sender_address, body_text, body_html_gz from mail_messages where body_text_version<? and rowid>? order by rowid limit ?`
	updateMailRederived = `update mail_messages set subject_norm=?, body_text=?, body_text_version=? where rowid=?`
)

// rederiveMail recomputes subject_norm, body_text and the full-text entry of every mail message
// whose body_text_version is below MailTextVersion, from its subject and its stored HTML, then
// records the version in meta. A message with no stored HTML (or HTML that cannot be inflated)
// keeps its body text; its subject_norm is still recomputed. It returns how many messages changed,
// and an archive_newer error when the archive's mail text version is higher than this build's.
func rederiveMail(ctx context.Context, tx *sql.Tx) (int, error) {
	var stored string
	switch err := tx.QueryRowContext(ctx, `select value from meta where key=?`, mailTextKey).Scan(&stored); {
	case errors.Is(err, sql.ErrNoRows):
		stored = "0"
	case err != nil:
		return 0, err
	}
	from, err := strconv.Atoi(stored)
	if err != nil {
		return 0, errors.New("archive meta.mail_text_version is not a number: " + stored)
	}
	if from > MailTextVersion {
		return 0, errs.ArchiveNewer(from, MailTextVersion)
	}
	if from == MailTextVersion {
		return 0, nil
	}
	changed, last := 0, int64(0)
	for {
		type row struct {
			rowid                           int64
			subject, norm, name, addr, text string
			gz                              []byte
		}
		var batch []row
		err := eachRow(ctx, tx, selectMailRederive, []any{MailTextVersion, last, mailRederiveBatch}, func(r *sql.Rows) error {
			var x row
			if err := r.Scan(&x.rowid, &x.subject, &x.norm, &x.name, &x.addr, &x.text, &x.gz); err != nil {
				return err
			}
			batch = append(batch, x)
			return nil
		})
		if err != nil {
			return 0, err
		}
		if len(batch) == 0 {
			break
		}
		last = batch[len(batch)-1].rowid
		for _, x := range batch {
			norm, text := NormalizeSubject(x.subject), x.text
			if html, ok := inflateMailHTML(x.gz); ok {
				text = outlookmail.HTMLText(html)
			}
			if _, err := tx.ExecContext(ctx, updateMailRederived, norm, text, MailTextVersion, x.rowid); err != nil {
				return 0, err
			}
			if text != x.text {
				if _, err := tx.ExecContext(ctx, `delete from mail_fts where rowid=?`, x.rowid); err != nil {
					return 0, err
				}
				if _, err := tx.ExecContext(ctx, `insert into mail_fts(rowid, subject, sender_name, sender_address, body_text) values(?,?,?,?,?)`, x.rowid, x.subject, x.name, x.addr, text); err != nil {
					return 0, err
				}
			}
			if text != x.text || norm != x.norm {
				changed++
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `insert into meta(key, value) values(?, ?) on conflict(key) do update set value=excluded.value`, mailTextKey, strconv.Itoa(MailTextVersion)); err != nil {
		return 0, err
	}
	return changed, nil
}

// inflateMailHTML reads a stored body_html_gz. It reports false for no HTML or HTML that does not
// inflate within the body cap.
func inflateMailHTML(gz []byte) ([]byte, bool) {
	if len(gz) == 0 {
		return nil, false
	}
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		return nil, false
	}
	html, err := io.ReadAll(io.LimitReader(zr, outlookmail.MaxBodyBytes))
	if err != nil {
		return nil, false
	}
	return html, true
}
