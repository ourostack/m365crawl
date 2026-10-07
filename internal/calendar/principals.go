package calendar

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"time"

	"github.com/ourostack/m365crawl/internal/errs"
)

// linkUsage is a usage-class error that says how to fix the link.
func linkUsage(msg, fix string) error {
	err := errs.Usage(msg)
	err.Fix = fix
	return err
}

// LinkAccount records that accountID, an account of a non-Teams source, belongs to principalID, a
// Teams account the archive has synced. Linking again updates the row and clears an unlink. Rules:
// only a non-Teams source is linked; the principal must be a Teams account in calendar_sources, so
// principals never chain; and a principal has at most one active account per source. A rule
// violation is a usage-class error (*errs.Coded).
func LinkAccount(ctx context.Context, tx *sql.Tx, source Source, accountID, principalID, method string, at time.Time) error {
	switch {
	case source == SourceTeams || source == "":
		return linkUsage("only an account of a source other than teams can be linked", "Name the Outlook account to link to a Teams account.")
	case accountID == "" || principalID == "" || method == "":
		return linkUsage("a link needs an account, a principal and a method", "Give the account, the Teams account to link it to, and the method.")
	}
	var known, teams int
	if err := tx.QueryRowContext(ctx, `SELECT count(*), coalesce(sum(account_id=?),0) FROM calendar_sources WHERE source=?`,
		principalID, string(SourceTeams)).Scan(&teams, &known); err != nil {
		return err
	}
	switch {
	case teams == 0:
		return linkUsage("no Teams account is in the archive yet", "Sync Teams first, then link the account.")
	case known == 0:
		return linkUsage("unknown principal "+principalID+": it is not a synced Teams account", "Name a Teams account in the form <tenantId>/<userId>.")
	}
	var other string
	err := tx.QueryRowContext(ctx, `SELECT account_id FROM calendar_account_links
	  WHERE source=? AND principal_id=? AND unlinked_at IS NULL AND account_id<>?`, string(source), principalID, accountID).Scan(&other)
	if err == nil {
		return linkUsage("principal "+principalID+" already has the "+string(source)+" account "+other,
			"Unlink that account first, or keep it.")
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO calendar_account_links (source, account_id, principal_id, method, linked_at, unlinked_at)
	  VALUES (?,?,?,?,?,NULL)
	  ON CONFLICT(source, account_id) DO UPDATE SET principal_id=excluded.principal_id, method=excluded.method,
	  linked_at=excluded.linked_at, unlinked_at=NULL`,
		string(source), accountID, principalID, method, formatTime(at))
	return err
}

// UnlinkAccount ends the account's link. The row stays, with unlinked_at set (nothing is deleted).
// An account with no active link is a usage-class error.
func UnlinkAccount(ctx context.Context, tx *sql.Tx, source Source, accountID string, at time.Time) error {
	res, err := tx.ExecContext(ctx, `UPDATE calendar_account_links SET unlinked_at=? WHERE source=? AND account_id=? AND unlinked_at IS NULL`,
		formatTime(at), string(source), accountID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return linkUsage("the "+string(source)+" account "+accountID+" is not linked", "Link it first, or check the account id.")
	}
	return nil
}

// Principals maps accounts to the person they belong to. A Teams account is its own principal; an
// account of another source is grouped under the Teams account it is linked to. Load it once per
// read. The zero value has no links, so every account is its own principal.
type Principals struct {
	of       map[string]string   // linked account -> principal
	accounts map[string][]string // principal -> linked accounts, sorted
}

// rowQuerier is what LoadPrincipals reads through: a *sql.DB or a *sql.Tx.
type rowQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// LoadPrincipals reads the active links.
func LoadPrincipals(ctx context.Context, q rowQuerier) (Principals, error) {
	rows, err := q.QueryContext(ctx, `SELECT account_id, principal_id FROM calendar_account_links
	  WHERE unlinked_at IS NULL AND source<>? ORDER BY principal_id, account_id`, string(SourceTeams))
	if err != nil {
		return Principals{}, err
	}
	defer func() { _ = rows.Close() }()
	p := Principals{of: map[string]string{}, accounts: map[string][]string{}}
	for rows.Next() {
		var account, principal string
		if err := scanRow(rows, &account, &principal); err != nil {
			return Principals{}, err
		}
		p.of[account] = principal
		p.accounts[principal] = append(p.accounts[principal], account)
	}
	return p, rowsErr(rows)
}

// Of is the principal of an account: the account it is actively linked to, otherwise itself.
func (p Principals) Of(account string) string {
	if principal, ok := p.of[account]; ok {
		return principal
	}
	return account
}

// Accounts lists the accounts of a principal: the principal first, then its linked accounts,
// sorted.
func (p Principals) Accounts(principal string) []string {
	linked := append([]string(nil), p.accounts[principal]...)
	sort.Strings(linked)
	return append([]string{principal}, linked...)
}

// Resolve lists the accounts an AgendaQuery.AccountID selects: every account of the filter's
// principal. An empty filter selects every account and returns nil.
func (p Principals) Resolve(filter string) []string {
	if filter == "" {
		return nil
	}
	return p.Accounts(p.Of(filter))
}
