package cli

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/ourostack/m365crawl/internal/store"
)

func (rt *runtime) noteSourceHealth(st *store.Store, label string, res result, needsSync bool) error {
	var families []string
	switch label {
	case "mail list", "mail show", "mail thread", "mail unread", "mail folders":
		families = []string{"outlook_mail"}
	case "messages", "thread", "conversations", "unread", "activity":
		families = []string{"teams"}
	case "calendar", "calendar actions":
		families = []string{"teams", "outlook_calendar"}
	case "calendar event", "sql":
		families = []string{"teams", "outlook_calendar", "outlook_mail"}
	case "search":
		families = rt.healthSources
	}
	if len(families) == 0 || st == nil {
		return nil
	}
	all, err := st.SourceHealth(rt.ctx)
	if err != nil {
		return err
	}
	health := []store.SourceHealth{}
	for _, h := range all {
		if !slices.Contains(families, h.Source) || h.Source != "teams" && !rt.outlookOn {
			continue
		}
		ok, err := rt.sourceHealthInScope(st, label, h)
		if err != nil {
			return err
		}
		if ok {
			health = append(health, h)
			res.addNotice(sourceHealthNotice(h))
		}
	}
	if len(health) > 0 || !needsSync {
		res.setSourceHealth(health)
	}
	return nil
}

func (rt *runtime) sourceHealthInScope(st *store.Store, label string, h store.SourceHealth) (bool, error) {
	account := ""
	if h.AccountID != nil {
		account = *h.AccountID
	}
	switch {
	case label == "sql":
		return true, nil // SQL has no automatic account predicate.
	case label == "calendar event" && h.Source == "outlook_mail":
		return true, nil // EventMail searches every mailbox, independently of the event's principal.
	case h.AccountID == nil:
		return true, nil // No observed account can prove this source irrelevant.
	case strings.HasPrefix(label, "mail "):
		return rt.healthAccount == "" || rt.healthAccount == account, nil
	case strings.HasPrefix(label, "calendar"):
		want := rt.healthAccount
		if want == "" && rt.account != nil {
			want = rt.account.TenantID + "/" + rt.account.UserID
		}
		if want == "" || want == account {
			return true, nil
		}
		if account == "" || h.Source == "teams" {
			return false, nil
		}
		principal, err := st.SourcePrincipal(rt.ctx, account)
		return principal == want, err
	case h.Source == "outlook_mail":
		want := mailAccountOf(rt.g.Account)
		return want == "" || want == account, nil
	default:
		if rt.account == nil {
			return true, nil
		}
		return account == rt.account.TenantID+"/"+rt.account.UserID, nil
	}
}

func sourceHealthNotice(h store.SourceHealth) string {
	name := map[string]string{"teams": "Teams", "outlook_mail": "Outlook mail", "outlook_calendar": "Outlook calendar"}[h.Source]
	if h.AccountID != nil {
		name += " for " + *h.AccountID
	}
	since := "has never been read successfully"
	if h.LastOKAt != nil {
		since = "has not been read successfully since " + h.LastOKAt.UTC().Format(time.RFC3339)
	}
	return fmt.Sprintf("%s %s (%s); newer items may be missing. Run m365crawl doctor.", name, since, h.LastErrorCode)
}
