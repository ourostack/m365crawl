package syncer

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/hxstore"
	"github.com/ourostack/m365crawl/internal/outlookmail"
	"github.com/ourostack/m365crawl/internal/outlookowa"
	"github.com/ourostack/m365crawl/internal/store"
)

// A Windows profile's first mailbox keeps the profile's own account (outlook/<profile>) and source
// (outlook|<profile>|mail), so existing public IDs never move. Every further mailbox gets the next
// unused outlook/<profile>/mailbox-<n> (n from 2) and its own source, read state and absence. The
// number says nothing about the mailbox; only the archive's sealed binding links it to an identity.

func owaMailKey(account string) string {
	return mailKey(outlookKey(strings.TrimPrefix(account, "outlook/")))
}

func layoutGuard(detail string) error {
	return &hxstore.GuardError{Code: outlookmail.CodeMailLayoutUnsupported, Detail: detail}
}

type owaAssignment struct {
	account string
	// rebind replaces the account's recorded identity; only native-identity overlap proves it.
	rebind bool
}

// owaOverlap counts the mailbox's native folder and message identities the account holds.
func owaOverlap(native map[string]bool, mb outlookowa.Mailbox) int {
	n := 0
	count := func(kind, id string) {
		if native[store.NativeKey(kind, id)] {
			n++
		}
	}
	count("folder", mb.Result.RootID)
	for _, f := range mb.Result.Folders {
		count("folder", f.ID)
		count("folder", f.ParentID)
	}
	for _, id := range mb.Result.SeenIDs {
		count("message", id)
	}
	return n
}

// attributeOWAMailboxes assigns each populated mailbox to exactly one account of the profile, or
// refuses the whole read. A recorded identity binding decides first; an unbound mailbox takes the
// unclaimed account whose archived native identities it shares; a mailbox sharing none is new.
func attributeOWAMailboxes(view store.OWAMailboxView, scope string, mailboxes []outlookowa.Mailbox) ([]owaAssignment, error) {
	if len(mailboxes) == 1 && mailboxes[0].Identity == "" {
		// The single-mailbox read of earlier releases, kept only while nothing else is bound here
		// and, when the account holds mail with native identities, only for a mailbox sharing some.
		for account := range view.Bindings {
			if account != scope {
				return nil, layoutGuard("mailbox_identity_unresolved")
			}
		}
		if view.Holds[scope] && len(view.Native[scope]) > 0 && owaOverlap(view.Native[scope], mailboxes[0]) == 0 {
			return nil, layoutGuard("mailbox_binding_unproved")
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
		sealed := view.Sealed(mb.Identity)
		for account, identity := range view.Bindings {
			if identity == sealed {
				out[i].account = account
				claimed[account], bound[account] = true, true
			}
		}
	}
	candidates := []string{scope}
	for account := range view.Bindings {
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
			if !bound[c] && owaOverlap(view.Native[c], mb) > 0 {
				match = append(match, c)
			}
		}
		if len(match) > 1 || len(match) == 1 && claimed[match[0]] {
			return nil, layoutGuard("mailbox_binding_ambiguous")
		}
		if len(match) == 0 {
			fresh = append(fresh, i)
			continue
		}
		_, rebind := view.Bindings[match[0]]
		out[i] = owaAssignment{account: match[0], rebind: rebind}
		claimed[match[0]] = true
	}
	next := nextOWAMailboxNumber(view, scope)
	for _, i := range fresh {
		// A profile's only mailbox keeps the profile's account unless that account holds another mailbox.
		if _, scopeBound := view.Bindings[scope]; len(mailboxes) == 1 && !claimed[scope] && !scopeBound && !view.Holds[scope] {
			out[i].account = scope
			continue
		}
		out[i].account = scope + store.OWAMailboxInfix + strconv.Itoa(next)
		next++
	}
	return out, nil
}

// nextOWAMailboxNumber is one past the highest mailbox number any archived account of the
// profile uses, so a number is never reused for another mailbox.
func nextOWAMailboxNumber(view store.OWAMailboxView, scope string) int {
	next := 2
	use := func(account string) {
		if n, err := strconv.Atoi(strings.TrimPrefix(account, scope+store.OWAMailboxInfix)); err == nil && n >= next {
			next = n + 1
		}
	}
	for account := range view.Bindings {
		use(account)
	}
	for account := range view.Holds {
		use(account)
	}
	for account := range view.Native {
		use(account)
	}
	return next
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
	view, viewErr := r.st.OWAMailboxView(ctx, scope)
	var assigned []owaAssignment
	if err == nil && viewErr == nil {
		assigned, err = attributeOWAMailboxes(view, scope, mailboxes)
	}
	if err != nil {
		if mailboxes != nil && viewErr == nil && errors.Is(err, outlookowa.ErrReaderClose) {
			// The read finished before its release failed: keep its own refusal as the primary cause.
			err = errors.Join(owaRefusal(view, scope, mailboxes), err)
		}
		return failAll(err)
	}
	if viewErr != nil {
		return failAll(errs.DBError(viewErr))
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
		out[i].err = contained(func() (err error) {
			out[i].report, out[i].decoded, err = r.commitOWAMailbox(ctx, mb, a, view.Holds[a.account], scope, out[i].key, fp, begun, fresh, stable)
			return err
		})
	}
	slices.SortFunc(out, func(a, b outlookOutcome) int { return strings.Compare(a.key, b.key) })
	return out
}

// owaRefusal is the read-only attribution and expected-mail check of mailboxes whose reader
// release failed, or nil.
func owaRefusal(view store.OWAMailboxView, scope string, mailboxes []outlookowa.Mailbox) error {
	assigned, err := attributeOWAMailboxes(view, scope, mailboxes)
	for i := range assigned {
		err = errors.Join(err, outlookowa.CheckExpected(mailboxes[i].Result, view.Holds[assigned[i].account]))
	}
	return err
}
