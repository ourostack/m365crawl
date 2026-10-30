package cli

import (
	"fmt"
	"time"

	"github.com/ourostack/m365crawl/internal/store"
)

// mailFolderCopyOut is one folder_copies entry: for an account and folder kind with older copies,
// how the current copy and the older ones compare over the listed range (store.MailCopyCheck).
type mailFolderCopyOut struct {
	Account               string     `json:"account"`
	Kind                  string     `json:"kind"`
	Copies                int        `json:"copies"`
	State                 string     `json:"state"`
	Messages              int        `json:"messages"`
	InCurrentCopy         int        `json:"in_current_copy"`
	OnlyInOlderCopies     int        `json:"only_in_older_copies"`
	LackingInCurrentRange int        `json:"lacking_in_current_range"`
	CurrentOldestAt       *time.Time `json:"current_oldest_at"`
}

// mailCopyChecks is the test seam of the copy check.
var mailCopyChecks = (*store.Store).MailCopyChecks

// checkFolderCopies adds folder_copies to a bounded mail list, one entry per account and folder
// kind among the selected folders that has older copies, and a notice for each range the current
// copy does not cover alone.
func (rt *runtime) checkFolderCopies(st *store.Store, res *mailListResult, sc mailScope, rng store.MailCopyRange) error {
	scopes := make([]store.MailCopyScope, len(sc.folders))
	for i, f := range sc.folders {
		scopes[i] = store.MailCopyScope{Account: f.Account, Kind: f.Kind}
	}
	checks, err := mailCopyChecks(st, rt.ctx, scopes, rng)
	if err != nil {
		return err
	}
	for _, c := range checks {
		res.FolderCopies = append(res.FolderCopies, mailFolderCopyOut{Account: c.Account, Kind: c.Kind, Copies: c.Copies, State: c.State, Messages: c.Messages,
			InCurrentCopy: c.InCurrentCopy, OnlyInOlderCopies: c.OnlyInOlderCopies, LackingInCurrentRange: c.LackingInCurrentRange, CurrentOldestAt: tp(c.CurrentOldestAt)})
		switch c.State {
		case "partial":
			res.addNotice(fmt.Sprintf("%s mail of %s is partial in this range: an older copy of the folder, from a folder set Outlook replaced, holds %d of %s that the current cache does not hold, although its range covers their dates",
				c.Kind, c.Account, c.LackingInCurrentRange, plural(c.Messages, "message", "messages")))
		case "older_copy_only":
			res.addNotice(fmt.Sprintf("%s mail of %s in this range comes only from an older copy of the folder, from a folder set Outlook replaced and no sync reads any more: the current cache does not reach these dates", c.Kind, c.Account))
		}
	}
	return nil
}
