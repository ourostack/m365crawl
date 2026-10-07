package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/ourostack/teamscrawl/internal/calendar"
	"github.com/ourostack/teamscrawl/internal/errs"
)

// joinAddresses is the stored form of a profile's addresses: lower case, distinct, sorted, one per line.
func joinAddresses(addrs []string) string {
	seen := map[string]bool{}
	var out []string
	for _, a := range addrs {
		if a = strings.ToLower(strings.TrimSpace(a)); a != "" && !seen[a] {
			seen[a] = true
			out = append(out, a)
		}
	}
	sort.Strings(out)
	return strings.Join(out, "\n")
}

// AutoLinkResult counts what AutoLinkOutlook changed.
type AutoLinkResult struct {
	Linked   int
	Unlinked int
}

// teamsAddresses maps each Teams account in the archive ("<tenantId>/<userId>") to the addresses of
// its own profile: the userPrincipalName, mail and email of the record the account keeps of itself
// (store profiles of its own database, key "8:orgid:<userId>"). An address is lower case. An account
// whose record is missing, removed or holds none is not in the map.
func teamsAddresses(ctx context.Context, tx *sql.Tx) (map[string]map[string]bool, error) {
	rows, err := tx.QueryContext(ctx, `select r.tenant_id, r.user_id, r.value_json from records r
	  where r.store='profiles' and r.removed_at is null and r.tenant_id<>'' and r.user_id<>''
	    and r.key_json='"8:orgid:'||r.user_id||'"'
	    and exists (select 1 from calendar_sources c where c.source=? and c.account_id=r.tenant_id||'/'||r.user_id)`, string(calendar.SourceTeams))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]map[string]bool{}
	for rows.Next() {
		var tenant, user string
		var raw sql.NullString
		_ = rows.Scan(&tenant, &user, &raw) // text columns that cannot be NULL, and a NullString
		var v struct {
			UPN   string `json:"userPrincipalName"`
			Mail  string `json:"mail"`
			Email string `json:"email"`
		}
		if json.Unmarshal([]byte(raw.String), &v) != nil {
			continue // a value that is not a profile has no address
		}
		for _, a := range []string{v.UPN, v.Mail, v.Email} {
			if a = strings.ToLower(strings.TrimSpace(a)); strings.Contains(a, "@") {
				if out[tenant+"/"+user] == nil {
					out[tenant+"/"+user] = map[string]bool{}
				}
				out[tenant+"/"+user][a] = true
			}
		}
	}
	return out, rows.Err()
}

type outlookLinkRow struct {
	method, principal string
	active            bool
}

// AutoLinkOutlook links each Outlook account to the Teams account that has the same address, and
// records the link with method OutlookLinkAddress. It is an identity match and not a guess: an
// address of an account signed in to the profile (see OutlookBatch.Addresses) must equal, compared
// without regard to case, an address of exactly one Teams account's own profile. An address that
// another Outlook profile also holds is not used, and a Teams account that matches more than one
// profile is not linked. Anything else leaves the account as it is, and the unlinked notice stays.
//
// A row the operator wrote (method config, linked or ended) is never changed. An automatic link
// whose evidence is gone, because the addresses changed or no longer match exactly one account, is
// ended.
func (s *Store) AutoLinkOutlook(ctx context.Context, at time.Time) (res AutoLinkResult, err error) {
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		teams, err := teamsAddresses(ctx, tx)
		if err != nil {
			return err
		}
		identities, err := outlookIdentities(ctx, tx)
		if err != nil {
			return err
		}
		links, err := outlookLinkRows(ctx, tx)
		if err != nil {
			return err
		}
		owners := map[string]int{}
		for _, addrs := range identities {
			for _, addr := range addrs {
				owners[addr]++
			}
		}
		// The Teams account each Outlook account matches, when it is exactly one.
		match := map[string]string{}
		claims := map[string]int{}
		for account, addrs := range identities {
			found := map[string]bool{}
			for _, addr := range addrs {
				if owners[addr] != 1 {
					continue
				}
				for principal, set := range teams {
					if set[addr] {
						found[principal] = true
					}
				}
			}
			if len(found) == 1 {
				for principal := range found {
					match[account] = principal
					claims[principal]++
				}
			}
		}
		accounts := make([]string, 0, len(identities))
		for account := range identities {
			accounts = append(accounts, account)
		}
		sort.Strings(accounts)
		for _, account := range accounts {
			row, held := links[account]
			if held && row.method != OutlookLinkAddress {
				continue // the operator's row, linked or ended
			}
			principal := match[account]
			if principal != "" && claims[principal] != 1 {
				principal = ""
			}
			if held && row.active && row.principal == principal {
				continue
			}
			if held && row.active {
				if err := calendar.UnlinkAccount(ctx, tx, calendar.SourceOutlook, account, at); err != nil {
					return err
				}
				res.Unlinked++
			}
			if principal == "" {
				continue
			}
			if err := calendar.LinkAccount(ctx, tx, calendar.SourceOutlook, account, principal, OutlookLinkAddress, at); err != nil {
				var coded *errs.Coded
				if errors.As(err, &coded) {
					continue // the core refuses this link (the principal has another Outlook account): the notice stays
				}
				return err
			}
			res.Linked++
		}
		return nil
	})
	return res, err
}

// outlookIdentities maps each Outlook account a read has looked at to the addresses of the accounts
// signed in to it, none when the read found none.
func outlookIdentities(ctx context.Context, tx *sql.Tx) (map[string][]string, error) {
	rows, err := tx.QueryContext(ctx, `select substr(key, ?), value from meta where key like 'outlook\_identity:%' escape '\'`, len(outlookIdentityKey)+1)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string][]string{}
	for rows.Next() {
		var account, addrs string
		_ = rows.Scan(&account, &addrs) // a key suffix and a text value
		out[account] = nil
		for _, a := range strings.Split(strings.ToLower(addrs), "\n") {
			if a != "" {
				out[account] = append(out[account], a)
			}
		}
	}
	return out, rows.Err()
}

func outlookLinkRows(ctx context.Context, tx *sql.Tx) (map[string]outlookLinkRow, error) {
	rows, err := tx.QueryContext(ctx, `select account_id, method, principal_id, unlinked_at is null from calendar_account_links where source=?`, string(calendar.SourceOutlook))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]outlookLinkRow{}
	for rows.Next() {
		var account string
		var r outlookLinkRow
		_ = rows.Scan(&account, &r.method, &r.principal, &r.active) // NOT NULL text columns and a boolean
		out[account] = r
	}
	return out, rows.Err()
}
