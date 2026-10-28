package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"html"
	"strconv"
	"time"

	"github.com/ourostack/m365crawl/internal/outlookmail"
	"github.com/ourostack/m365crawl/internal/workiq"
)

type WorkIQMailResult struct {
	MailResult
	Gaps    map[string]int
	Pending bool
}

func (s *Store) CommitWorkIQMailGroup(ctx context.Context, attempt int64, normalized string) (WorkIQMailResult, error) {
	binding, err := s.validateWorkIQAttemptOwner(ctx, attempt)
	if err != nil {
		return WorkIQMailResult{}, err
	}
	if binding.Fact.Family != "mail" || workiq.NormalizeMessageID(normalized) != normalized {
		return WorkIQMailResult{}, errors.New("invalid WorkIQ mail identity group")
	}
	result := WorkIQMailResult{Gaps: map[string]int{}}
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		var census int64
		var complete bool
		if err := tx.QueryRowContext(ctx, `select id,complete from workiq_censuses where attempt_id=? and account_id=?`,
			attempt, binding.Fact.ID).Scan(&census, &complete); err != nil {
			return err
		}
		if !complete {
			result.Pending = true
			return nil
		}
		var copies int
		if err := tx.QueryRowContext(ctx, `select count(*) from workiq_census_members where census_id=? and normalized_id=? and removed=0`, census, normalized).Scan(&copies); err != nil {
			return err
		}
		type staged struct {
			id       string
			raw      json.RawMessage
			observed time.Time
		}
		var batch []staged
		if err := eachRow(ctx, tx, `select graph_id,data_json,observed_at from workiq_census_members
where census_id=? and normalized_id=? and admitted=0 order by graph_id limit 128`, []any{census, normalized}, func(row *sql.Rows) error {
			var item staged
			var data, at string
			if err := row.Scan(&item.id, &data, &at); err != nil {
				return err
			}
			var err error
			item.observed, err = time.Parse(time.RFC3339Nano, at)
			if err != nil {
				return errors.New("invalid WorkIQ mail observation clock")
			}
			item.raw = []byte(data)
			batch = append(batch, item)
			return nil
		}); err != nil {
			return err
		}
		for _, item := range batch {
			server, err := workiq.DecodeMail(ctx, item.raw)
			if err != nil {
				return err
			}
			if !server.Removed {
				if err := commitWorkIQMailItem(ctx, tx, binding.Fact.ID, server, item.observed, copies, &result); err != nil {
					return err
				}
			}
			if _, err := tx.ExecContext(ctx, `update workiq_census_members set admitted=1 where census_id=? and graph_id=?`, census, item.id); err != nil {
				return err
			}
		}
		for code, n := range result.Gaps {
			if _, err := tx.ExecContext(ctx, `insert into workiq_gaps(attempt_id,code,resource,n) values(?,?,?,?)
on conflict(attempt_id,code,resource) do update set n=workiq_gaps.n+excluded.n`,
				attempt, code, "mail-group:"+hashOf(normalized), n); err != nil {
				return err
			}
		}
		return tx.QueryRowContext(ctx, `select exists(select 1 from workiq_census_members where census_id=? and normalized_id=? and admitted=0)`, census, normalized).Scan(&result.Pending)
	})
	if err != nil {
		return WorkIQMailResult{}, err
	}
	return result, nil
}

// The prepared hash narrows the lookup, then the original header verifies exact
// normalization. Historical entries cannot turn a changed native row into a match.
func workIQNativeMailCandidates(ctx context.Context, tx *sql.Tx, account, normalized string) ([]int64, error) {
	var ids []int64
	var ready bool
	if err := tx.QueryRowContext(ctx, `select exists(select 1 from meta where key=?)`, workIQMailIndexPrefix+account).Scan(&ready); err != nil {
		return nil, err
	}
	if !ready {
		return nil, errors.New("WorkIQ native mail candidate preparation is incomplete")
	}
	err := eachRow(ctx, tx, `select m.rowid,m.internet_message_id from workiq_aliases a join mail_messages m on m.account=a.account_id and m.rowid=a.item_rowid
where a.kind='mail_identity_index' and a.account_id=? and a.namespace=?`, []any{account, hashOf(normalized)}, func(row *sql.Rows) error {
		var id int64
		var header string
		if err := row.Scan(&id, &header); err != nil {
			return err
		}
		if normalized != "" && workiq.NormalizeMessageID(header) == normalized {
			ids = append(ids, id)
		}
		return nil
	})
	return ids, err
}

func workIQMailProjection(ctx context.Context, tx *sql.Tx, account string, server workiq.MailMessage) (outlookmail.Message, error) {
	m := outlookmail.Message{Account: account, Copies: 1}
	var err error
	m.DetailKey, err = mailSourceKey(ctx, tx, account, "workiq:message", server.ID)
	if err != nil {
		return m, err
	}
	if err := tx.QueryRowContext(ctx, `select folder_key,well_known from workiq_folders where account_id=? and graph_id=?`,
		account, server.FolderID).Scan(&m.FolderKey, &m.Folder.Kind); err != nil {
		return m, err
	}
	m.Folder.Key = m.FolderKey
	m.MessageID, m.ConversationID, m.Subject, m.Preview = server.InternetMessageID, server.ConversationID, server.Subject, server.Preview
	m.SenderName, m.SenderAddress, m.InReplyTo, m.ICalUID = server.Sender.Name, server.Sender.Address, server.InReplyTo, server.ICalUID
	if server.SentAt != nil {
		m.Sent = *server.SentAt
	}
	if server.ReceivedAt != nil {
		m.Received = *server.ReceivedAt
	}
	if server.IsRead != nil {
		unread := !*server.IsRead
		m.Unread = &unread
	}
	m.ReadState = "unknown"
	if server.IsRead != nil {
		m.ReadState = "unread"
		if *server.IsRead {
			m.ReadState = "read"
		}
	}
	m.Flag, m.Importance, m.HasAttachments, m.Class = server.Flag, server.Importance, server.HasAttachments, "IPM.Note"
	m.Body.State = outlookmail.BodyNone
	if server.BodyPresent {
		m.Body.State = outlookmail.BodyInline
		if server.BodyType == "html" {
			m.Body.HTML = []byte(server.Body)
		} else if server.BodyType == "text" {
			m.Body.HTML = []byte("<p>" + html.EscapeString(server.Body) + "</p>")
		}
	}
	for _, group := range []struct {
		role      string
		addresses []workiq.MailAddress
	}{{"to", server.To}, {"cc", server.Cc}, {"bcc", server.Bcc}} {
		for _, a := range group.addresses {
			m.Recipients = append(m.Recipients, outlookmail.Recipient{Name: a.Name, Address: a.Address, Role: group.role})
		}
	}
	for _, a := range server.Attachments {
		key, err := mailSourceKey(ctx, tx, account, "workiq:attachment", a.ID)
		if err != nil {
			return m, err
		}
		m.Attachments = append(m.Attachments, outlookmail.Attachment{Key: key, MessageKey: m.DetailKey, Name: a.Name, Size: a.Size, ContentType: a.ContentType, Inline: a.Inline})
	}
	return m, nil
}

func commitWorkIQMailItem(ctx context.Context, tx *sql.Tx, account string, server workiq.MailMessage, observed time.Time, copies int, result *WorkIQMailResult) error {
	var rowid int64
	err := tx.QueryRowContext(ctx, `select item_rowid from workiq_aliases where kind='mail' and account_id=? and namespace='graph' and alias=?`, account, server.ID).Scan(&rowid)
	bound := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	cacheFirst := false
	cacheSeen := false
	var oldProjection string
	var oldWinner string
	var cachedProjection sql.NullString
	if bound {
		if err := tx.QueryRowContext(ctx, `select cache_first,cache_seen,server_projection,winner,cache_projection from workiq_rows where kind='mail' and account_id=? and item_rowid=?`,
			account, rowid).Scan(&cacheFirst, &cacheSeen, &oldProjection, &oldWinner, &cachedProjection); err != nil {
			return err
		}
		var previous workiq.MailMessage
		if json.Unmarshal([]byte(oldProjection), &previous) != nil || previous.ID != server.ID {
			return errors.New("invalid retained server mail projection")
		}
		if server.ModifiedAt != nil && previous.ModifiedAt != nil && server.ModifiedAt.Before(*previous.ModifiedAt) {
			return nil
		}
		if server.Body == "" && previous.Body != "" {
			server.Body, server.BodyType, server.BodyPresent = previous.Body, previous.BodyType, previous.BodyPresent
		}
	}
	if !bound && workiq.NormalizeMessageID(server.InternetMessageID) != "" {
		candidates, err := workIQNativeMailCandidates(ctx, tx, account, workiq.NormalizeMessageID(server.InternetMessageID))
		if err != nil {
			return err
		}
		if copies == 1 && len(candidates) == 1 {
			var header, subject, sender string
			var sent sql.NullString
			if err := tx.QueryRowContext(ctx, `select internet_message_id,subject,sender_address,sent_at from mail_messages where rowid=?`, candidates[0]).Scan(&header, &subject, &sender, &sent); err != nil {
				return err
			}
			at, err := workIQStoredTime(sent)
			if err != nil {
				return err
			}
			local := workiq.MailMessage{InternetMessageID: header, Subject: subject, Sender: workiq.MailAddress{Address: sender}, SentAt: at}
			if workiq.MailCorroborates(local, server) {
				rowid = candidates[0]
				var existing bool
				if err := tx.QueryRowContext(ctx, `select exists(select 1 from workiq_rows where kind='mail' and account_id=? and item_rowid=?)`, account, rowid).Scan(&existing); err != nil {
					return err
				}
				if existing {
					if err := tx.QueryRowContext(ctx, `select cache_first,cache_seen from workiq_rows where kind='mail' and account_id=? and item_rowid=?`, account, rowid).Scan(&cacheFirst, &cacheSeen); err != nil {
						return err
					}
				} else {
					cacheFirst, cacheSeen = true, true
				}
			} else {
				result.Gaps["workiq_mail_identity_uncorroborated"]++
			}
		} else if copies > 1 || len(candidates) > 1 {
			result.Gaps["workiq_mail_identity_ambiguous"]++
		}
	}
	m, err := workIQMailProjection(ctx, tx, account, server)
	if err != nil {
		return err
	}
	if workiq.NormalizeMessageID(server.InternetMessageID) == "" {
		result.Gaps["workiq_mail_message_id_missing"]++
	}
	winner := "workiq_server"
	if rowid == 0 {
		if err := insertMail(ctx, tx, account, m, deriveMail(m), observed.UTC().Format(timeLayout)); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `select rowid from mail_messages where account=? and detail_key=?`, account, m.DetailKey).Scan(&rowid); err != nil {
			return err
		}
		result.Added++
	} else {
		var canonical uint32
		var currentText string
		var nativeRead sql.NullInt64
		var readState, flag, importance string
		if err := tx.QueryRowContext(ctx, `select detail_key,body_text,subject,sender_name,sender_address,is_read,read_state,flag,importance from mail_messages where rowid=?`, rowid).
			Scan(&canonical, &currentText, &m.Subject, &m.SenderName, &m.SenderAddress, &nativeRead, &readState, &flag, &importance); err != nil {
			return err
		}
		m.DetailKey = canonical
		cacheHasBody := cacheSeen && currentText != "" && oldWinner != "workiq_server"
		if cacheSeen && cachedProjection.Valid {
			var cached outlookmail.Message
			if json.Unmarshal([]byte(cachedProjection.String), &cached) != nil || cached.Account != account || cached.DetailKey != canonical {
				return errors.New("invalid retained native mail projection")
			}
			cacheHasBody = len(cached.Body.HTML) > 0
		}
		if cacheHasBody {
			winner = "desktop_cache"
		} else {
			if cacheSeen {
				if nativeRead.Valid {
					unread := nativeRead.Int64 == 0
					m.Unread = &unread
					m.ReadState = readState
				}
				if flag != "" && flag != "unknown" {
					m.Flag = flag
				}
				if importance != "" && importance != "unknown" {
					m.Importance = importance
				}
			}
			d := deriveMail(m)
			d.body(m)
			if _, err := tx.ExecContext(ctx, `update mail_messages set folder_key=?,is_read=?,read_state=?,flag=?,importance=?,
body_state=?,body_html_gz=?,body_text=?,body_text_version=?,state_seen_at=?,modified_at=? where rowid=?`,
				m.FolderKey, readFlag(m), m.ReadState, m.Flag, m.Importance, d.state, d.gz, d.text, MailTextVersion,
				fmtTime(observed), fmtTime(observed), rowid); err != nil {
				return err
			}
			if err := reindexMail(ctx, tx, rowid, m, d.text); err != nil {
				return err
			}
		}
		result.Updated++
	}
	if err := bindWorkIQMailAlias(ctx, tx, account, "graph", server.ID, rowid); err != nil {
		return err
	}
	var key uint32
	if err := tx.QueryRowContext(ctx, `select detail_key from mail_messages where rowid=?`, rowid).Scan(&key); err != nil {
		return err
	}
	if err := bindWorkIQMailAlias(ctx, tx, account, "public", strconv.FormatUint(uint64(key), 10), rowid); err != nil {
		return err
	}
	var liveVersion, deleteMark, deleteVersion, liveSeen sql.NullString
	var retainedRowID int64
	err = tx.QueryRowContext(ctx, `select item_rowid,server_version,server_deleted_at,server_deleted_version,server_seen_at from workiq_rows
where kind='mail' and account_id=? and item_rowid=?`, account, rowid).Scan(&retainedRowID, &liveVersion, &deleteMark, &deleteVersion, &liveSeen)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if server.ModifiedAt == nil {
		result.Gaps["workiq_clock_unknown"]++
	}
	if server.DeletedAt == nil {
		liveSeen = sql.NullString{String: observed.UTC().Format(time.RFC3339Nano), Valid: true}
		if server.ModifiedAt != nil {
			liveVersion = sql.NullString{String: server.ModifiedAt.UTC().Format(time.RFC3339Nano), Valid: true}
		}
	} else {
		previousDelete, err := workIQStoredTime(deleteVersion)
		if err != nil {
			return err
		}
		if !deleteMark.Valid || server.ModifiedAt != nil && previousDelete != nil && server.ModifiedAt.After(*previousDelete) {
			deleteMark = sql.NullString{String: server.DeletedAt.UTC().Format(time.RFC3339Nano), Valid: true}
			deleteVersion = sql.NullString{}
			if server.ModifiedAt != nil {
				deleteVersion = sql.NullString{String: server.ModifiedAt.UTC().Format(time.RFC3339Nano), Valid: true}
			}
		}
	}
	projected, _ := json.Marshal(server)
	_, err = tx.ExecContext(ctx, `insert into workiq_rows(kind,account_id,item_rowid,graph_id,server_json,server_projection,cache_first,cache_seen,server_seen_at,winner,modified_at,
server_version,server_deleted_at,server_deleted_version)
values('mail',?,?,?,?,?,?,?,?,?,?,?,?,?) on conflict(kind,account_id,item_rowid) do update set graph_id=excluded.graph_id,
server_json=excluded.server_json,server_projection=excluded.server_projection,server_seen_at=excluded.server_seen_at,winner=excluded.winner,modified_at=excluded.modified_at,
server_version=excluded.server_version,server_deleted_at=excluded.server_deleted_at,server_deleted_version=excluded.server_deleted_version`,
		account, rowid, server.ID, string(server.Raw), string(projected), b2i(cacheFirst), b2i(cacheSeen), nullAny(liveSeen), winner, workIQTimeValue(&observed),
		nullAny(liveVersion), nullAny(deleteMark), nullAny(deleteVersion))
	if err != nil {
		return err
	}
	if server.DeletedAt == nil {
		if _, err := tx.ExecContext(ctx, `update workiq_rows set server_gone_at=null,server_first_missing_census=null where kind='mail' and account_id=? and item_rowid=?`,
			account, rowid); err != nil {
			return err
		}
	}
	if err := retainWorkIQMailCacheMarks(ctx, tx, account); err != nil {
		return err
	}
	return reduceWorkIQMailMarks(ctx, tx, account)
}
