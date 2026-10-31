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
	read := (*store.Store).SourceHealth
	if rt.outlookOn && slices.ContainsFunc(families, func(f string) bool { return strings.HasPrefix(f, "outlook_") }) {
		read = readSourceHealth // adds stale Outlook caches that WorkIQ has not covered
	}
	all, err := read(st, rt.ctx)
	if err != nil {
		return err
	}
	health := []store.SourceHealth{}
	for _, h := range all {
		if h.State == store.SourceStale && !rt.healthUntil.IsZero() && !rt.healthUntil.After(*h.CacheFreshAt) {
			continue // the read ends before the cache stopped being written
		}
		family := sourceHealthFamily(h)
		if !slices.Contains(families, family) || h.Source != "workiq" && h.Source != "teams" && !rt.outlookOn {
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
	if len(health) > 0 || !needsSync && !rt.healthUnknown {
		res.setSourceHealth(health)
	}
	return nil
}

func (rt *runtime) sourceHealthInScope(st *store.Store, label string, h store.SourceHealth) (bool, error) {
	if h.Source == "workiq" {
		h.Source = sourceHealthFamily(h)
	}
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
	name := map[string]string{"teams": "Teams", "outlook_mail": "Outlook mail", "outlook_calendar": "Outlook calendar", "workiq": "WorkIQ"}[h.Source]
	if h.AccountID != nil {
		name += " for " + *h.AccountID
	}
	if h.State == store.SourceStale {
		return name + ": " + outlookStaleSentence(h)
	}
	since := "has never been read successfully"
	if h.LastOKAt != nil {
		since = "has not been read successfully since " + h.LastOKAt.UTC().Format(time.RFC3339)
	}
	if h.State == "covered_by_server" && h.CoveredThrough != nil {
		return fmt.Sprintf("%s is still failing locally (%s); WorkIQ covers every mail folder through %s. Local reads have not recovered. Run m365crawl doctor.",
			name, h.LastErrorCode, h.CoveredThrough.UTC().Format(time.RFC3339))
	}
	if h.Source == "workiq" {
		return fmt.Sprintf("%s %s (%s); retry explicitly with m365crawl workiq status, then m365crawl workiq sync.", name, since, h.LastErrorCode)
	}
	if h.Source == "outlook_mail" && h.WorkIQEnabled {
		return fmt.Sprintf("%s %s (%s); newer items may be missing. Run m365crawl doctor, or explicitly try m365crawl workiq sync for server mail recovery.", name, since, h.LastErrorCode)
	}
	return fmt.Sprintf("%s %s (%s); newer items may be missing. Run m365crawl doctor.", name, since, h.LastErrorCode)
}

// outlookStaleSentence says what a stale Outlook cache means for a read and how to refresh it.
func outlookStaleSentence(h store.SourceHealth) string {
	return outlookStaleDetail(h) + ". " + outlookStaleFix
}

// outlookStaleDetail says since when Outlook has not written its cache and what that leaves out.
func outlookStaleDetail(h store.SourceHealth) string {
	what := "mail"
	if h.Source == "outlook_calendar" {
		what = "an event created or changed"
	}
	return fmt.Sprintf("Outlook has not updated its local cache since %s (checked %s, %s); %s after that is not in it",
		h.CacheFreshAt.UTC().Format(time.RFC3339), h.LastOKAt.UTC().Format(time.RFC3339), store.OutlookCacheStaleCode, what)
}

// readSourceHealth is the test seam of a read's failing and stale sources.
var readSourceHealth = (*store.Store).ReadSourceHealth

// outlookCacheHealth is the test seam of the stale Outlook sources.
var outlookCacheHealth = (*store.Store).OutlookCacheHealth

// outlookStale is the stale entry of one Outlook source family and account, or nil.
func (rt *runtime) outlookStale(st *store.Store, family, account string) (*store.SourceHealth, error) {
	stale, err := outlookCacheHealth(st, rt.ctx)
	return store.OutlookCacheStale(stale, family, account), err
}

// outlookStaleFix is the fix of a stale Outlook cache.
const outlookStaleFix = "Open Outlook and let it sync, then run `m365crawl sync`."

func sourceHealthFamily(h store.SourceHealth) string {
	if h.Source != "workiq" {
		return h.Source
	}
	switch h.WorkIQFamily {
	case "mail":
		return "outlook_mail"
	case "chats":
		return "teams"
	default:
		return ""
	}
}
