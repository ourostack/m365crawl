package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/ourostack/m365crawl/internal/outlookmail"
	"github.com/ourostack/m365crawl/internal/workiq"
)

type workIQMailCacheAlias struct {
	instance            string
	original, canonical uint32
}

func workIQNativeMailInstance(m outlookmail.Message) string {
	return hashOf(m.DetailKey, workiq.NormalizeMessageID(m.MessageID), m.SenderAddress, m.Sent.UTC().Format(time.RFC3339Nano), NormalizeSubject(m.Subject))
}

func (s *Store) validateWorkIQMailCacheOwner(ctx context.Context, account string) error {
	enabled, err := s.hasWorkIQ(ctx)
	if err != nil || !enabled {
		return err
	}
	var user, address string
	err = s.db.QueryRowContext(ctx, `select user_id,address from workiq_accounts where family='mail' and account_id=?`, account).Scan(&user, &address)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	facts, err := s.WorkIQAccounts(ctx)
	if err != nil {
		return err
	}
	_, err = workiq.SelectMailAccount(workiq.User{ID: user, Mail: address}, facts, account)
	return err
}

func prepareWorkIQCacheMail(ctx context.Context, tx *sql.Tx, b MailBatch) (MailBatch, []workIQMailCacheAlias, error) {
	have, err := workIQHasRows(ctx, tx, "mail")
	if err != nil || !have {
		return b, nil, err
	}
	counts := map[string]int{}
	var reservedHigh uint32
	for _, m := range b.Result.Messages {
		counts[workiq.NormalizeMessageID(m.MessageID)]++
		reservedHigh = max(reservedHigh, m.DetailKey)
	}
	b.Result.Messages = append([]outlookmail.Message(nil), b.Result.Messages...)
	b.Result.SeenDetailKeys = append([]uint32(nil), b.Result.SeenDetailKeys...)
	for _, key := range b.Result.SeenDetailKeys {
		if err := eachRow(ctx, tx, `select m.detail_key from workiq_aliases a join mail_messages m on m.account=a.account_id and m.rowid=a.item_rowid
where a.kind='mail_native_presence' and a.account_id=? and a.namespace=?`, []any{b.Account, strconv.FormatUint(uint64(key), 10)}, func(row *sql.Rows) error {
			var canonical uint32
			if err := row.Scan(&canonical); err != nil {
				return err
			}
			b.Result.SeenDetailKeys = append(b.Result.SeenDetailKeys, canonical)
			return nil
		}); err != nil {
			return b, nil, err
		}
	}
	var aliases []workIQMailCacheAlias
	for i, incoming := range b.Result.Messages {
		m := incoming
		instance := workIQNativeMailInstance(incoming)
		var rowid int64
		err := tx.QueryRowContext(ctx, `select item_rowid from workiq_aliases where kind='mail' and account_id=? and namespace='native_instance' and alias=?`, b.Account, instance).Scan(&rowid)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return b, nil, err
		}
		normalized := workiq.NormalizeMessageID(m.MessageID)
		if rowid == 0 && normalized != "" && counts[normalized] == 1 {
			ids, err := workIQNativeMailCandidates(ctx, tx, b.Account, normalized)
			if err != nil {
				return b, nil, err
			}
			if len(ids) == 1 {
				var raw string
				err := tx.QueryRowContext(ctx, `select server_projection from workiq_rows where kind='mail' and account_id=? and item_rowid=?`, b.Account, ids[0]).Scan(&raw)
				if err != nil && !errors.Is(err, sql.ErrNoRows) {
					return b, nil, err
				}
				if err == nil {
					var server workiq.MailMessage
					if json.Unmarshal([]byte(raw), &server) != nil {
						return b, nil, errors.New("invalid retained server mail projection")
					}
					local := workiq.MailMessage{InternetMessageID: m.MessageID, Subject: m.Subject, Sender: workiq.MailAddress{Address: m.SenderAddress}, SentAt: &m.Sent}
					if workiq.MailCorroborates(local, server) {
						rowid = ids[0]
					}
				}
			}
		}
		if rowid != 0 {
			if err := tx.QueryRowContext(ctx, `select detail_key from mail_messages where account=? and rowid=?`, b.Account, rowid).Scan(&m.DetailKey); err != nil {
				return b, nil, err
			}
			m, err = applyWorkIQCacheMail(ctx, tx, b.Account, rowid, m, b.ReadAt, b.FreshAt)
			if err != nil {
				return b, nil, err
			}
		} else {
			// A never-published native numeric key is not authority to replace
			// a server canonical key or an actually published alternate ID.
			var reserved bool
			if err := tx.QueryRowContext(ctx, `select exists(select 1 from mail_messages m join workiq_rows w on w.kind='mail' and w.account_id=m.account and w.item_rowid=m.rowid
where m.account=? and m.detail_key=?) or exists(select 1 from workiq_aliases where kind='mail' and account_id=? and namespace='public' and alias=?)`,
				b.Account, m.DetailKey, b.Account, strconv.FormatUint(uint64(m.DetailKey), 10)).Scan(&reserved); err != nil {
				return b, nil, err
			}
			if reserved {
				m.DetailKey, err = freeWorkIQMailCanonicalKey(ctx, tx, b.Account)
				if err != nil {
					return b, nil, err
				}
				if m.DetailKey <= reservedHigh {
					if int64(reservedHigh) >= maxMailSourceKey {
						return b, nil, errors.New("no canonical mail key is available")
					}
					m.DetailKey = reservedHigh + 1
				}
				reservedHigh = m.DetailKey
			}
		}
		b.Result.Messages[i] = m
		aliases = append(aliases, workIQMailCacheAlias{instance: instance, original: incoming.DetailKey, canonical: m.DetailKey})
	}
	return b, aliases, nil
}

func freeWorkIQMailCanonicalKey(ctx context.Context, tx *sql.Tx, account string) (uint32, error) {
	var high int64
	err := tx.QueryRowContext(ctx, `select coalesce(max(k),0) from (
select detail_key k from mail_messages where account=? union all select detail_key from mail_absent where account=?
union all select key from mail_source_ids where account=? and kind='message'
union all select key from workiq_source_keys where account=? and kind='message'
union all select cast(alias as integer) from workiq_aliases where kind='mail' and account_id=? and namespace='public')`,
		account, account, account, account, account).Scan(&high)
	if err != nil {
		return 0, err
	}
	if high < 0 || high >= maxMailSourceKey {
		return 0, errors.New("no canonical mail key is available")
	}
	return uint32(high + 1), nil
}

func finishWorkIQCacheMailAliases(ctx context.Context, tx *sql.Tx, account string, aliases []workIQMailCacheAlias) error {
	for _, a := range aliases {
		var rowid int64
		if err := tx.QueryRowContext(ctx, `select rowid from mail_messages where account=? and detail_key=?`, account, a.canonical).Scan(&rowid); err != nil {
			return err
		}
		if err := bindWorkIQMailAlias(ctx, tx, account, "native_instance", a.instance, rowid); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `insert into workiq_aliases(kind,account_id,namespace,alias,item_rowid)
values('mail_native_presence',?,?,?,?) on conflict(kind,account_id,namespace,alias) do nothing`,
			account, strconv.FormatUint(uint64(a.original), 10), strconv.FormatInt(rowid, 10), rowid); err != nil {
			return err
		}
		var occupied bool
		if err := tx.QueryRowContext(ctx, `select exists(select 1 from mail_messages where account=? and detail_key=? and rowid<>?)
or exists(select 1 from workiq_aliases where kind='mail' and account_id=? and namespace='public' and alias=? and item_rowid<>?)`,
			account, a.original, rowid, account, strconv.FormatUint(uint64(a.original), 10), rowid).Scan(&occupied); err != nil {
			return err
		}
		if !occupied {
			if err := bindWorkIQMailAlias(ctx, tx, account, "public", strconv.FormatUint(uint64(a.original), 10), rowid); err != nil {
				return err
			}
		}
	}
	return nil
}

func applyWorkIQCacheMail(ctx context.Context, tx *sql.Tx, account string, rowid int64, m outlookmail.Message, observed, fresh time.Time) (outlookmail.Message, error) {
	var raw, originalHeader, oldWinner, retainedGraphID string
	var oldCache sql.NullString
	err := tx.QueryRowContext(ctx, `select w.server_projection,m.internet_message_id,w.cache_projection,w.winner,w.graph_id from workiq_rows w join mail_messages m on m.rowid=w.item_rowid and m.account=w.account_id
where w.kind='mail' and w.account_id=? and w.item_rowid=?`, account, rowid).Scan(&raw, &originalHeader, &oldCache, &oldWinner, &retainedGraphID)
	if errors.Is(err, sql.ErrNoRows) {
		return m, nil
	}
	if err != nil {
		return m, err
	}
	var server workiq.MailMessage
	if json.Unmarshal([]byte(raw), &server) != nil || server.ID != retainedGraphID {
		return m, errors.New("invalid retained server mail projection")
	}
	var previous outlookmail.Message
	if oldCache.Valid {
		if json.Unmarshal([]byte(oldCache.String), &previous) != nil || previous.Account != account || previous.DetailKey != m.DetailKey {
			return m, errors.New("invalid retained native mail projection")
		}
	}
	mapped, err := workIQMailProjection(ctx, tx, account, server)
	if err != nil {
		return m, err
	}
	cache := m
	if len(cache.Body.HTML) == 0 && len(previous.Body.HTML) > 0 {
		cache.Body = previous.Body
	}
	cleaned, safe, err := cleanWorkIQNativeMail(cache)
	if err != nil {
		return m, err
	}
	winner := "desktop_cache"
	effective := cleaned
	if len(cleaned.Body.HTML) == 0 && len(mapped.Body.HTML) > 0 {
		effective.Body = mapped.Body
		winner = "workiq_server"
	}
	if len(mapped.Attachments) > 0 || mapped.HasAttachments != nil && *mapped.HasAttachments {
		yes := true
		effective.HasAttachments = &yes
	}
	effective.MessageID = originalHeader
	if !fresh.IsZero() {
		copyVersion, err := workIQNativeMailCopyVersion(fresh)
		if err != nil {
			return m, err
		}
		if _, err := tx.ExecContext(ctx, `update workiq_rows set cache_seen=1,cache_seen_at=?,cache_version=?,
cache_first_miss_at=null,cache_gone_at=null,cache_evicted_at=null where kind='mail' and account_id=? and item_rowid=?
and (cache_version is null or cache_version<?)`, workIQTimeValue(&observed), copyVersion, account, rowid, copyVersion); err != nil {
			return m, err
		}
	}
	if oldCache.Valid && oldCache.String == string(safe) && oldWinner == winner {
		return effective, nil
	}
	d := deriveMail(effective)
	d.body(effective)
	if _, err := tx.ExecContext(ctx, `update mail_messages set body_state=?,body_html_gz=?,body_text=?,body_text_version=?,modified_at=? where rowid=?
and (body_state<>? or body_html_gz is not ? or body_text<>? or body_text_version<>?)`,
		d.state, d.gz, d.text, MailTextVersion, fmtTime(observed), rowid, d.state, d.gz, d.text, MailTextVersion); err != nil {
		return m, err
	}
	if err := reindexMail(ctx, tx, rowid, effective, d.text); err != nil {
		return m, err
	}
	if _, err := tx.ExecContext(ctx, `update workiq_rows set cache_seen=1,cache_projection=?,winner=? where kind='mail' and account_id=? and item_rowid=?`,
		string(safe), winner, account, rowid); err != nil {
		return m, err
	}
	return effective, nil
}
