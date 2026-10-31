package store

import (
	"context"
	"crypto/hmac"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/ourostack/m365crawl/internal/outlookowa"
	"github.com/ourostack/m365crawl/internal/teamsdesktop"
	"github.com/ourostack/m365crawl/internal/workiq"
)

// WorkIQAccounts reads archived facts only, including named failed source attempts.
func (s *Store) WorkIQAccounts(ctx context.Context) ([]workiq.AccountFact, error) {
	ok, err := s.hasWorkIQ(ctx)
	if err != nil || !ok {
		return nil, err
	}
	facts := map[string]*workiq.AccountFact{}
	outlook := func(id string) *workiq.AccountFact {
		if facts[id] == nil {
			facts[id] = &workiq.AccountFact{ID: id, Family: "mail"}
		}
		return facts[id]
	}
	err = s.each(ctx, `select tenant_id,user_id from accounts order by tenant_id,user_id`, func(rs *sql.Rows) error {
		var tenant, user string
		if err := rs.Scan(&tenant, &user); err != nil {
			return err
		}
		id := tenant + "/" + user
		facts[id] = &workiq.AccountFact{ID: id, Family: "chats", UserID: user, TenantID: tenant, IdentityPresent: true}
		return nil
	})
	if err != nil {
		return nil, err
	}
	err = s.each(ctx, `select account from mail_messages
union select account from mail_folders
union select account_id from calendar_sources where source='outlook'
union select account_id from calendar_account_links where source='outlook'`, func(rs *sql.Rows) error {
		var id string
		if err := rs.Scan(&id); err != nil {
			return err
		}
		outlook(id)
		return nil
	})
	if err != nil {
		return nil, err
	}
	err = s.each(ctx, `select source,source_accounts_json from sync_runs where source like 'outlook|%' or source in ('outlook','outlook_mail')`, func(rs *sql.Rows) error {
		var source string
		var raw sql.NullString
		if err := rs.Scan(&source, &raw); err != nil {
			return err
		}
		var accounts []string
		if raw.Valid {
			if err := json.Unmarshal([]byte(raw.String), &accounts); err != nil {
				return errors.New("invalid archived Outlook source scope")
			}
		}
		if len(accounts) == 0 {
			_, id := SourceFamily(source)
			if id != "" {
				accounts = []string{id}
			}
		}
		for _, id := range accounts {
			if strings.HasPrefix(id, "outlook/") && len(id) > len("outlook/") {
				outlook(id)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	seal, err := owaSeal(ctx, s.db)
	if err != nil {
		return nil, err
	}
	err = s.each(ctx, `select key,value from meta where key like 'outlook\_identity:%' escape '\'
or key like 'outlook\_mail\_failure:%' escape '\' or key like 'outlook\_failure:%' escape '\'
or key like 'outlook\_mailbox\_identity:%' escape '\'`, func(rs *sql.Rows) error {
		var key, value string
		if err := rs.Scan(&key, &value); err != nil {
			return err
		}
		if id, ok := strings.CutPrefix(key, owaMailboxIdentityKey); ok {
			// A Windows mailbox's identity is sealed: it identifies a mailbox only to a caller that
			// seals the same mailbox and signed-in addresses with this archive's key.
			fact := outlook(id)
			fact.IdentityPresent = true
			fact.MatchMailbox = func(mailboxAddress, userIdentity string) bool {
				return seal != nil && hmac.Equal([]byte(sealIdentity(seal, outlookowa.MailboxIdentity(mailboxAddress, userIdentity))), []byte(value))
			}
			return nil
		}
		if id, ok := strings.CutPrefix(key, outlookIdentityKey); ok {
			fact := outlook(id)
			fact.IdentityPresent = true
			for _, address := range strings.Split(joinAddresses(strings.Split(value, "\n")), "\n") {
				if address != "" {
					fact.Addresses = append(fact.Addresses, address)
				}
			}
			return nil
		}
		id, mail := strings.CutPrefix(key, mailFailureKey)
		if !mail {
			id = strings.TrimPrefix(key, outlookFailureKey)
		}
		var failure OutlookFailure
		if json.Unmarshal([]byte(value), &failure) != nil {
			return errors.New("invalid archived Outlook refusal")
		}
		fact := outlook(id)
		fact.MultipleMailboxes = fact.MultipleMailboxes || strings.Contains(failure.Message, "multiple_mailboxes")
		return nil
	})
	if err != nil {
		return nil, err
	}
	type profileRef struct{ id, source, database, store, key string }
	var profiles []profileRef
	err = s.each(ctx, `select tenant_id||'/'||user_id,source,database,store,key_json from records
where store='profiles' and removed_at is null and key_json='"8:orgid:'||user_id||'"'`, func(rs *sql.Rows) error {
		var ref profileRef
		if err := rs.Scan(&ref.id, &ref.source, &ref.database, &ref.store, &ref.key); err != nil {
			return err
		}
		if fact := facts[ref.id]; fact != nil && fact.Family == "chats" &&
			!teamsdesktop.Denied(ref.database) && !teamsdesktop.Denied(ref.store) {
			profiles = append(profiles, ref)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, ref := range profiles {
		var raw string
		if err := s.db.QueryRowContext(ctx, `select coalesce(value_json,'') from records
where source=? and database=? and store=? and key_json=? and removed_at is null`,
			ref.source, ref.database, ref.store, ref.key).Scan(&raw); err != nil {
			return nil, err
		}
		if raw != "" {
			fact := facts[ref.id]
			var profile struct{ Mail, UserPrincipalName, Email string }
			if json.Unmarshal([]byte(raw), &profile) == nil {
				for _, address := range strings.Split(joinAddresses([]string{profile.Mail, profile.UserPrincipalName, profile.Email}), "\n") {
					if address != "" {
						fact.Addresses = append(fact.Addresses, address)
					}
				}
			}
		}
	}
	err = s.each(ctx, `select family,account_id,address from workiq_accounts`, func(rs *sql.Rows) error {
		var family, id, address string
		if err := rs.Scan(&family, &id, &address); err != nil {
			return err
		}
		if fact := facts[id]; fact != nil && fact.Family == family && len(fact.Addresses) == 0 &&
			(family == "chats" || !fact.IdentityPresent) {
			fact.Addresses = append(fact.Addresses, address)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	var result []workiq.AccountFact
	for _, fact := range facts {
		result = append(result, *fact)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}

// WorkIQMailBindable says whether WorkIQ could bind the Outlook mail account at all: a further
// mailbox of a Windows profile binds only through its recorded mailbox identity, never by bootstrap.
func (s *Store) WorkIQMailBindable(ctx context.Context, account string) (bool, error) {
	var bound bool
	err := s.db.QueryRowContext(ctx, `select exists(select 1 from meta where key=?)`, owaMailboxIdentityKey+account).Scan(&bound)
	return bound || !strings.Contains(account, OWAMailboxInfix), err
}
