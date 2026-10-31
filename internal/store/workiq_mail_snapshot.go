package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"html"
	"time"

	"github.com/ourostack/m365crawl/internal/outlookmail"
)

const workIQMailFreshPrefix = "workiq_mail_native_fresh:"

// This is an own-copy replay token, never a cache/server content-clock bridge.
func workIQNativeMailCopyVersion(fresh time.Time) (int64, error) {
	value := fresh.UnixNano()
	if !time.Unix(0, value).Equal(fresh) {
		return 0, errors.New("native mail copy clock cannot be represented")
	}
	return value, nil
}

func loadWorkIQNativeMailFresh(ctx context.Context, tx *sql.Tx, account string) (*time.Time, error) {
	var key, value string
	err := tx.QueryRowContext(ctx, `select key,value from meta where key=?`, workIQMailFreshPrefix+account).Scan(&key, &value)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	at, err := workIQStoredTime(sql.NullString{String: value, Valid: true})
	if err != nil {
		return nil, err
	}
	if _, err := workIQNativeMailCopyVersion(*at); err != nil {
		return nil, err
	}
	return at, nil
}

func retainWorkIQNativeMailFresh(ctx context.Context, tx *sql.Tx, account string, fresh time.Time) error {
	if fresh.IsZero() {
		return nil
	}
	if _, err := workIQNativeMailCopyVersion(fresh); err != nil {
		return err
	}
	previous, err := loadWorkIQNativeMailFresh(ctx, tx, account)
	if err != nil {
		return err
	}
	if previous != nil && !fresh.After(*previous) {
		return nil
	}
	_, err = tx.ExecContext(ctx, `insert into meta(key,value) values(?,?) on conflict(key) do update set value=excluded.value`,
		workIQMailFreshPrefix+account, workIQTimeValue(&fresh))
	return err
}

func cleanWorkIQNativeMail(cache outlookmail.Message) (outlookmail.Message, []byte, error) {
	var body string
	encodedBody, _ := json.Marshal(string(cache.Body.HTML))
	safeBody, _ := scrubWorkIQProjection(encodedBody)
	if json.Unmarshal(safeBody, &body) != nil {
		return cache, nil, errors.New("invalid scrubbed cache mail body")
	}
	cache.Body.HTML = []byte(body)
	encoded, err := json.Marshal(cache)
	if err != nil {
		return cache, nil, errors.New("invalid native mail projection")
	}
	safe, _ := scrubWorkIQProjection(encoded)
	var cleaned outlookmail.Message
	if json.Unmarshal(safe, &cleaned) != nil || cleaned.Account != cache.Account || cleaned.DetailKey != cache.DetailKey {
		return cache, nil, errors.New("native mail identity could not be retained")
	}
	return cleaned, safe, nil
}

// snapshotWorkIQNativeMail materializes only the archive's existing native
// evidence before the first server reduction. It does not read a cache or stamp
// migration/current time as a new native sighting.
func snapshotWorkIQNativeMail(ctx context.Context, tx *sql.Tx, account string, rowid int64) ([]byte, *time.Time, error) {
	m := outlookmail.Message{Account: account, Copies: 1}
	var sent, received, observed sql.NullString
	var read sql.NullBool
	var has bool
	var gz []byte
	var text string
	if err := tx.QueryRowContext(ctx, `select m.detail_key,m.internet_message_id,m.conversation_id,m.folder_key,m.to_me,m.subject,
m.sender_name,m.sender_address,m.preview,m.in_reply_to,m.received_at,m.sent_at,m.class,m.importance,m.ical_uid,
m.is_read,m.read_state,m.flag,m.has_attachments,m.body_state,m.body_html_gz,m.body_text,m.state_seen_at,
coalesce(f.name,''),coalesce(f.kind,'other') from mail_messages m left join mail_folders f on f.account=m.account and f.folder_key=m.folder_key
where m.account=? and m.rowid=?`, account, rowid).Scan(&m.DetailKey, &m.MessageID, &m.ConversationID, &m.FolderKey, &m.ToMe, &m.Subject,
		&m.SenderName, &m.SenderAddress, &m.Preview, &m.InReplyTo, &received, &sent, &m.Class, &m.Importance, &m.ICalUID,
		&read, &m.ReadState, &m.Flag, &has, &m.Body.State, &gz, &text, &observed, &m.Folder.Name, &m.Folder.Kind); err != nil {
		return nil, nil, err
	}
	m.Folder.Key = m.FolderKey
	m.HasAttachments = &has
	if read.Valid {
		unread := !read.Bool
		m.Unread = &unread
	}
	for _, clock := range []struct {
		raw  sql.NullString
		into *time.Time
	}{{sent, &m.Sent}, {received, &m.Received}} {
		at, err := workIQStoredTime(clock.raw)
		if err != nil {
			return nil, nil, err
		}
		if at != nil {
			*clock.into = *at
		}
	}
	at, err := workIQStoredTime(observed)
	if err != nil {
		return nil, nil, err
	}
	if body, ok := inflateMailHTML(gz); ok {
		m.Body.HTML = body
	} else if text != "" {
		// Old archives may retain plaintext without recoverable HTML. Preserve
		// that known text as a private materialization, not newly fetched HTML.
		m.Body.HTML = []byte("<p>" + html.EscapeString(text) + "</p>")
	}
	if err := eachRow(ctx, tx, `select name,coalesce(address,''),kind_raw,coalesce(role,'') from mail_recipients where message_rowid=? order by ord`, []any{rowid}, func(row *sql.Rows) error {
		var r outlookmail.Recipient
		if err := row.Scan(&r.Name, &r.Address, &r.KindRaw, &r.Role); err != nil {
			return err
		}
		m.Recipients = append(m.Recipients, r)
		return nil
	}); err != nil {
		return nil, nil, err
	}
	if err := eachRow(ctx, tx, `select attachment_key,name,size,content_type,inline,downloaded from mail_attachments where message_rowid=? order by attachment_key`, []any{rowid}, func(row *sql.Rows) error {
		var a outlookmail.Attachment
		a.MessageKey = m.DetailKey
		if err := row.Scan(&a.Key, &a.Name, &a.Size, &a.ContentType, &a.Inline, &a.Downloaded); err != nil {
			return err
		}
		m.Attachments = append(m.Attachments, a)
		return nil
	}); err != nil {
		return nil, nil, err
	}
	_, safe, err := cleanWorkIQNativeMail(m)
	return safe, at, err
}
