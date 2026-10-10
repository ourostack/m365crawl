//go:build acceptance

package acceptance

import (
	"context"
	"reflect"

	"github.com/ourostack/m365crawl/internal/store"
)

func owaCompareAll(st *store.Store, observations []owaObservation) []string {
	ctx, cancel := context.WithTimeout(context.Background(), owaWorkTimeout)
	defer cancel()
	return owaCompareAllContext(ctx, st, observations)
}

func owaCompareAllContext(ctx context.Context, st *store.Store, observations []owaObservation) []string {
	bad := map[string]bool{}
	accounts := map[string]bool{}
	totals := map[string]int64{"mail_messages": 0, "mail_folders": 0, "mail_recipients": 0, "mail_attachments": 0, "mail_source_ids": 0, "mail_coverage": 0, "mail_absent": 0, "mail_fts": 0}
	for _, observation := range observations {
		if accounts[observation.account] {
			bad["observation_accounts"] = true
		}
		accounts[observation.account] = true
		c := observation.census
		identities := map[string]map[string]bool{"message": {}, "folder": {}, "attachment": {}}
		folders := map[string]bool{}
		for id, f := range c.folders {
			identities["folder"][id], identities["folder"][f.parent] = true, true
		}
		for id, m := range c.messages {
			identities["message"][id] = true
			folders[m.folder] = true
			totals["mail_recipients"] += int64(len(m.recipients))
			totals["mail_attachments"] += int64(len(m.attachments))
			for id := range m.attachments {
				identities["attachment"][id] = true
			}
		}
		for _, ids := range identities {
			totals["mail_source_ids"] += int64(len(ids))
		}
		totals["mail_messages"] += int64(len(c.messages))
		totals["mail_fts"] += int64(len(c.messages))
		totals["mail_folders"] += int64(len(c.folders))
		totals["mail_coverage"] += int64(len(folders))
		for _, field := range owaCompareContext(ctx, st, observation.account, c) {
			bad[field] = true
		}
	}
	actualAccounts := map[string]bool{}
	for _, table := range []string{"mail_messages", "mail_folders", "mail_source_ids", "mail_coverage", "mail_absent"} {
		rows, err := owaQueryContext(ctx, st, "select account from "+table+" group by account")
		if err != nil {
			bad["archive_query"] = true
		}
		for _, row := range rows {
			account, ok := row[0].(string)
			if !ok || account == "" {
				bad["archive_accounts"] = true
			}
			actualAccounts[account] = true
		}
	}
	roots, err := owaQueryContext(ctx, st, `select substr(key,length('outlook_mail_root:')+1) from meta where key like 'outlook_mail_root:%'`)
	if err != nil {
		bad["archive_query"] = true
	}
	for _, row := range roots {
		actualAccounts[asString(row[0])] = true
	}
	if !reflect.DeepEqual(accounts, actualAccounts) {
		bad["archive_accounts"] = true
	}
	for _, table := range []string{"mail_messages", "mail_folders", "mail_recipients", "mail_attachments", "mail_source_ids", "mail_coverage", "mail_absent", "mail_fts"} {
		rows, err := owaQueryContext(ctx, st, "select count(*) from "+table)
		if err != nil || len(rows) != 1 || len(rows[0]) != 1 || rows[0][0] != totals[table] {
			bad["aggregate_"+table] = true
		}
	}
	// All child rows must belong to an independently declared message, not merely resolve to some unexpected account.
	for _, q := range []string{
		`select m.account from mail_recipients r left join mail_messages m on m.rowid=r.message_rowid group by m.account`,
		`select m.account from mail_attachments a left join mail_messages m on m.rowid=a.message_rowid group by m.account`,
		`select m.account from mail_fts f left join mail_messages m on m.rowid=f.rowid group by m.account`,
	} {
		rows, err := owaQueryContext(ctx, st, q)
		if err != nil {
			bad["archive_query"] = true
		}
		for _, row := range rows {
			account, ok := row[0].(string)
			if !ok || !accounts[account] {
				bad["row_ownership"] = true
			}
		}
	}
	return owaSortedFields(bad)
}
