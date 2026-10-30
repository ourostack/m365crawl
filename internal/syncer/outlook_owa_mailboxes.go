package syncer

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/hxstore"
	"github.com/ourostack/m365crawl/internal/outlookmail"
	"github.com/ourostack/m365crawl/internal/outlookowa"
	"github.com/ourostack/m365crawl/internal/store"
)

// A Windows profile's first mailbox keeps the profile's own account (outlook/<profile>) and source
// (outlook|<profile>|mail), so existing public IDs never move. Every further mailbox gets
// outlook/<profile>/mailbox-<identity prefix> and its own source, read state and absence.

func owaMailKey(account string) string {
	return mailKey(outlookKey(strings.TrimPrefix(account, "outlook/")))
}

func layoutGuard(detail string) error {
	return &hxstore.GuardError{Code: outlookmail.CodeMailLayoutUnsupported, Detail: detail}
}

// owaMailAccounts lists the accounts whose mailboxes the profile held at its last attributed read.
func (r *runner) owaMailAccounts(ctx context.Context, scope string) ([]string, error) {
	accounts, err := r.st.OWAMailboxes(ctx, scope)
	if err != nil {
		return nil, err
	}
	if len(accounts) == 0 {
		return []string{scope}, nil
	}
	return accounts, nil
}

type owaAssignment struct {
	account string
	// rebind replaces the account's recorded identity; only native-identity overlap proves it.
	rebind bool
}

// owaOverlap counts the mailbox's native folder, then message, identities the account holds.
func (r *runner) owaOverlap(ctx context.Context, account string, mb outlookowa.Mailbox) (int, error) {
	var folders []string
	if mb.Result.RootID != "" {
		folders = append(folders, mb.Result.RootID)
	}
	for _, f := range mb.Result.Folders {
		folders = append(folders, f.ID, f.ParentID)
	}
	n, err := r.st.OWAMailSourceOverlap(ctx, account, folders, nil)
	if err != nil || n > 0 {
		return n, err
	}
	return r.st.OWAMailSourceOverlap(ctx, account, nil, mb.Result.SeenIDs)
}

// attributeOWAMailboxes assigns each populated mailbox to exactly one account of the profile, or
// refuses the whole read. A recorded identity binding decides first; an unbound mailbox takes the
// unclaimed account whose archived native identities it shares; a mailbox sharing none is new.
func (r *runner) attributeOWAMailboxes(ctx context.Context, scope string, mailboxes []outlookowa.Mailbox) ([]owaAssignment, error) {
	bindings, err := r.st.OWAMailboxBindings(ctx, scope)
	if err != nil {
		return nil, errs.DBError(err)
	}
	holds := func(account string) (bool, error) { return r.st.MailHolds(ctx, account) }
	if len(mailboxes) == 1 && mailboxes[0].Identity == "" {
		// The single-mailbox read of earlier releases, kept only while nothing else is bound here.
		for account := range bindings {
			if account != scope {
				return nil, layoutGuard("mailbox_identity_unresolved")
			}
		}
		held, err := holds(scope)
		if err != nil {
			return nil, errs.DBError(err)
		}
		native := 0
		if held {
			if native, err = r.st.OWAMailSourceCount(ctx, scope); err != nil {
				return nil, errs.DBError(err)
			}
		}
		// Archived mail with native identities must share some with this mailbox.
		if native > 0 {
			n, err := r.owaOverlap(ctx, scope, mailboxes[0])
			if err != nil {
				return nil, errs.DBError(err)
			}
			if n == 0 {
				return nil, layoutGuard("mailbox_binding_unproved")
			}
		}
		return []owaAssignment{{account: scope}}, nil
	}
	seen := map[string]bool{}
	for _, mb := range mailboxes {
		if mb.Identity == "" {
			return nil, layoutGuard("multiple_mailboxes")
		}
		if seen[mb.Identity] {
			return nil, layoutGuard("mailbox_identity_duplicate")
		}
		seen[mb.Identity] = true
	}
	out := make([]owaAssignment, len(mailboxes))
	claimed, bound := map[string]bool{}, map[string]bool{}
	for i, mb := range mailboxes {
		for account, identity := range bindings {
			if identity == mb.Identity {
				out[i].account = account
				claimed[account], bound[account] = true, true
			}
		}
	}
	candidates := []string{scope}
	for account := range bindings {
		if account != scope {
			candidates = append(candidates, account)
		}
	}
	slices.Sort(candidates)
	var fresh []int
	for i, mb := range mailboxes {
		if out[i].account != "" {
			continue
		}
		var match []string
		for _, c := range candidates {
			if bound[c] {
				continue
			}
			n, err := r.owaOverlap(ctx, c, mb)
			if err != nil {
				return nil, errs.DBError(err)
			}
			if n > 0 {
				match = append(match, c)
			}
		}
		switch len(match) {
		case 0:
			fresh = append(fresh, i)
		case 1:
			if claimed[match[0]] {
				return nil, layoutGuard("mailbox_binding_ambiguous")
			}
			_, rebind := bindings[match[0]]
			out[i] = owaAssignment{account: match[0], rebind: rebind}
			claimed[match[0]] = true
		default:
			return nil, layoutGuard("mailbox_binding_ambiguous")
		}
	}
	for _, i := range fresh {
		account := scope + store.OWAMailboxInfix + mailboxes[i].Identity[:16]
		if len(mailboxes) == 1 && !claimed[scope] {
			// A profile's only mailbox keeps the profile's account unless that account holds another mailbox.
			if _, bound := bindings[scope]; !bound {
				held, err := holds(scope)
				if err != nil {
					return nil, errs.DBError(err)
				}
				if !held {
					account = scope
				}
			}
		}
		if _, bound := bindings[account]; bound || claimed[account] {
			return nil, layoutGuard("mailbox_binding_ambiguous")
		}
		if account != scope {
			held, err := holds(account)
			if err != nil {
				return nil, errs.DBError(err)
			}
			if held {
				return nil, layoutGuard("mailbox_binding_ambiguous")
			}
		}
		out[i].account = account
		claimed[account] = true
	}
	return out, nil
}

// refuseOWAMailboxes is the read-only part of attribution and the expected-mail check.
func (r *runner) refuseOWAMailboxes(ctx context.Context, scope string, mailboxes []outlookowa.Mailbox) error {
	assigned, err := r.attributeOWAMailboxes(ctx, scope, mailboxes)
	if err != nil {
		return err
	}
	for i, a := range assigned {
		expect, err := r.st.MailHolds(ctx, a.account)
		if err != nil {
			return errs.DBError(err)
		}
		if err := outlookowa.CheckExpected(mailboxes[i].Result, expect); err != nil {
			return err
		}
	}
	return nil
}

// commitOWAMailboxes reads every mailbox of the snapshot and commits each to its own account. An
// attribution refusal fails every account the profile held, applies nothing and leaves rows as
// they were.
func (r *runner) commitOWAMailboxes(ctx context.Context, snap, scope string, known []outlookOutcome, fp string, begun, fresh time.Time, stable bool, closed *bool) []outlookOutcome {
	failAll := func(err error) []outlookOutcome {
		for i := range known {
			known[i].err = err
		}
		return known
	}
	mailboxes, err := collectOWAMailboxes(ctx, snap, closed)
	if err != nil {
		if mailboxes != nil && errors.Is(err, outlookowa.ErrReaderClose) && ctx.Err() == nil {
			// The read finished before its release failed: keep its own refusal as the primary cause.
			if primary := r.refuseOWAMailboxes(ctx, scope, mailboxes); primary != nil {
				err = errors.Join(primary, err)
			}
		}
		return failAll(err)
	}
	if err := ctx.Err(); err != nil {
		return failAll(err)
	}
	assigned, err := r.attributeOWAMailboxes(ctx, scope, mailboxes)
	if err != nil {
		return failAll(err)
	}
	accounts := make([]string, len(assigned))
	for i, a := range assigned {
		accounts[i] = a.account
	}
	if err := r.st.SetOWAMailboxes(ctx, scope, accounts); err != nil {
		return failAll(errs.DBError(err))
	}
	out := make([]outlookOutcome, len(mailboxes))
	for i, mb := range mailboxes {
		a := assigned[i]
		out[i].key = owaMailKey(a.account)
		if err := ctx.Err(); err != nil {
			out[i].err = err
			continue
		}
		out[i].err = contained(func() (err error) {
			out[i].report, out[i].decoded, err = r.commitOWAMailbox(ctx, mb, a, scope, out[i].key, fp, begun, fresh, stable)
			return err
		})
	}
	slices.SortFunc(out, func(a, b outlookOutcome) int { return strings.Compare(a.key, b.key) })
	return out
}
