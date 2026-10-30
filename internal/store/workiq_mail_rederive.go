package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"html"
	"strconv"
	"time"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/outlookmail"
	"github.com/ourostack/m365crawl/internal/workiq"
)

const WorkIQMailProjectionVersion = 1
const workIQMailProjectionKey = "workiq_mail_projection_version"

func (s *Store) EnsureWorkIQMailProjection(ctx context.Context) (int, error) {
	var key, value string
	err := s.db.QueryRowContext(ctx, `select key,value from meta where key=?`, workIQMailProjectionKey).Scan(&key, &value)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	version, err := strconv.Atoi(value)
	if err != nil || version < 0 {
		return 0, errors.New("invalid retained WorkIQ mail projection version")
	}
	if version > WorkIQMailProjectionVersion {
		return 0, errs.ArchiveNewer(version, WorkIQMailProjectionVersion)
	}
	if version == WorkIQMailProjectionVersion {
		return 0, nil
	}
	return s.RebuildWorkIQMail(ctx)
}

func workIQServerMailBody(server workiq.MailMessage) outlookmail.Body {
	body := outlookmail.Body{State: outlookmail.BodyNone}
	if server.BodyPresent {
		body.State = outlookmail.BodyInline
		switch server.BodyType {
		case "html":
			body.HTML = []byte(server.Body)
		case "text":
			body.HTML = []byte("<p>" + html.EscapeString(server.Body) + "</p>")
		}
	}
	return body
}

// RebuildWorkIQMail is source-specific maintenance, separate from the native
// mail text version. Its caller owns the ordinary archive writer role.
func (s *Store) RebuildWorkIQMail(ctx context.Context) (int, error) {
	var changed int
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var err error
		changed, err = rederiveWorkIQMail(ctx, tx)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `insert into meta(key,value) values(?,?) on conflict(key) do update set value=excluded.value`,
			workIQMailProjectionKey, strconv.Itoa(WorkIQMailProjectionVersion))
		return err
	})
	return changed, err
}

func rederiveWorkIQMail(ctx context.Context, tx *sql.Tx) (int, error) {
	last := int64(0)
	changed := 0
	for {
		type entry struct {
			id                                      int64
			message                                 outlookmail.Message
			graph, server, winner, oldText, oldNorm string
			cache                                   sql.NullString
		}
		var entries []entry
		if err := eachRow(ctx, tx, `select m.rowid,m.account,m.detail_key,m.subject,m.sender_name,m.sender_address,m.body_text,m.subject_norm,
w.graph_id,w.server_projection,w.cache_projection,w.winner from mail_messages m join workiq_rows w on w.kind='mail' and w.account_id=m.account and w.item_rowid=m.rowid
where m.rowid>? order by m.rowid limit ?`, []any{last, mailRederiveBatch}, func(row *sql.Rows) error {
			var e entry
			if err := row.Scan(&e.id, &e.message.Account, &e.message.DetailKey, &e.message.Subject, &e.message.SenderName, &e.message.SenderAddress,
				&e.oldText, &e.oldNorm, &e.graph, &e.server, &e.cache, &e.winner); err != nil {
				return err
			}
			entries = append(entries, e)
			return nil
		}); err != nil {
			return 0, err
		}
		if len(entries) == 0 {
			return changed, nil
		}
		for _, e := range entries {
			last = e.id
			var server workiq.MailMessage
			if json.Unmarshal([]byte(e.server), &server) != nil || server.ID != e.graph {
				return 0, errors.New("invalid retained server mail projection")
			}
			m := e.message
			switch e.winner {
			case "workiq_server":
				m.Body = workIQServerMailBody(server)
			case "desktop_cache":
				var cache outlookmail.Message
				if !e.cache.Valid || json.Unmarshal([]byte(e.cache.String), &cache) != nil || cache.Account != m.Account || cache.DetailKey != m.DetailKey {
					return 0, errors.New("invalid retained native mail projection")
				}
				m.Body = cache.Body
			default:
				return 0, errors.New("invalid retained mail source winner")
			}
			d := deriveMail(m)
			d.body(m)
			if _, err := tx.ExecContext(ctx, `update mail_messages set subject_norm=?,body_state=?,body_html_gz=?,body_text=?,body_text_version=?,modified_at=? where rowid=?`,
				d.norm, d.state, d.gz, d.text, MailTextVersion, fmtTime(time.Now()), e.id); err != nil {
				return 0, err
			}
			if err := reindexMail(ctx, tx, e.id, m, d.text); err != nil {
				return 0, err
			}
			if d.text != e.oldText || d.norm != e.oldNorm {
				changed++
			}
		}
	}
}
