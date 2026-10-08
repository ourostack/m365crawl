package cli

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"
	"time"

	goruntime "runtime"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/store"
)

// Codes of search's own errors.
const (
	// CodeFlagSourceConflict is a flag that the chosen --source cannot use.
	CodeFlagSourceConflict = "flag_source_conflict"
	// searchCodeMailUnsupported and searchCodeUnknownFolder are the codes the mail commands use for
	// the same two situations, so an agent reads one answer whichever command it ran.
	searchCodeMailUnsupported = "mail_unsupported_platform"
	searchCodeUnknownFolder   = "unknown_folder"
)

// searchMailPlatform is the operating system search believes it runs on when it decides whether
// mail can be read; tests set it.
var searchMailPlatform = goruntime.GOOS

// searchColumns are the columns of a text search result, whichever source an item came from. thread
// holds the arguments that read the item's thread: `m365crawl thread <conversation> <root>` for a
// chat message, `m365crawl mail thread <id>` (or mail show) for mail.
var searchColumns = []string{"at", "source", "where", "who", "thread", "text"}

const (
	searchChats = "chats"
	searchMail  = "mail"
)

// searchChatItem is a Teams message in a search result: the message as every Teams command prints
// it, plus the source it came from.
type searchChatItem struct {
	Source string `json:"source"`
	messageItem
}

// searchMailItem is a mail message in a search result: the item of mail list, plus the source.
type searchMailItem struct {
	Source            string     `json:"source"`
	ID                string     `json:"id"`
	Account           string     `json:"account"`
	Folder            *string    `json:"folder"`
	FolderKind        *string    `json:"folder_kind"`
	ToMe              bool       `json:"to_me"`
	Subject           *string    `json:"subject"`
	FromName          *string    `json:"from_name"`
	FromAddress       *string    `json:"from_address"`
	RecipientCount    int        `json:"recipient_count"`
	RecipientsPreview []string   `json:"recipients_preview"`
	InReplyTo         *string    `json:"in_reply_to"`
	ReceivedAt        *time.Time `json:"received_at"`
	SentAt            *time.Time `json:"sent_at"`
	IsRead            *bool      `json:"is_read"`
	Flag              *string    `json:"flag"`
	Importance        *string    `json:"importance"`
	HasAttachments    bool       `json:"has_attachments"`
	Preview           *string    `json:"preview"`
	InternetMessageID *string    `json:"internet_message_id"`
	ICalUID           *string    `json:"ical_uid"`
	GoneAt            *time.Time `json:"gone_at"`
	EvictedAt         *time.Time `json:"evicted_at"`
	TextTruncated     bool       `json:"text_truncated,omitempty"`
}

// sourceCount is what one source contributed to a search: the items of the result that came from
// it, and whether it had more than the result holds.
type sourceCount struct {
	Count     int  `json:"count"`
	Truncated bool `json:"truncated"`
}

// searchSources is the per-source part of a search result. A source that was not searched is absent.
type searchSources struct {
	Chats *sourceCount `json:"chats,omitempty"`
	Mail  *sourceCount `json:"mail,omitempty"`
}

func searchNZ(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func searchVal(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func searchTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	u := t.UTC()
	return &u
}

func searchMailItemOf(r store.MailRow, maxText int) searchMailItem {
	it := searchMailItem{
		Source: searchMail, ID: r.ID(), Account: r.Account, Folder: searchNZ(r.Folder), FolderKind: searchNZ(r.FolderKind), ToMe: r.ToMe,
		Subject: searchNZ(r.Subject), FromName: searchNZ(r.SenderName), FromAddress: searchNZ(r.SenderAddress),
		RecipientCount: len(r.Recipients), RecipientsPreview: []string{}, InReplyTo: searchNZ(r.InReplyTo),
		ReceivedAt: searchTime(r.ReceivedAt), SentAt: searchTime(r.SentAt), IsRead: r.IsRead, Flag: searchNZ(r.Flag), Importance: searchNZ(r.Importance),
		HasAttachments: r.HasAttachments, InternetMessageID: searchNZ(r.InternetMessageID), ICalUID: searchNZ(r.ICalUID),
		GoneAt: searchTime(r.GoneAt), EvictedAt: searchTime(r.EvictedAt),
	}
	for i, rc := range r.Recipients {
		if i == 3 {
			break
		}
		label := rc.Name
		if label == "" {
			label = rc.Address
		}
		it.RecipientsPreview = append(it.RecipientsPreview, label)
	}
	preview := r.Preview
	if t, did := truncateRunes(preview, maxText); did {
		preview, it.TextTruncated = t, true
	}
	it.Preview = searchNZ(preview)
	return it
}

// searchKeys are the --fields keys a search accepts: source, then the keys of a Teams message and
// of a mail message. An item carries only the keys its own source has.
func searchKeys() []string {
	keys := []string{"source"}
	for _, k := range append(jsonKeys(reflect.TypeFor[messageItem]()), jsonKeys(reflect.TypeFor[searchMailItem]())...) {
		if !slices.Contains(keys, k) {
			keys = append(keys, k)
		}
	}
	return keys
}

// searchPlan says which sources a search reads and what it tells the reader about the ones it skips.
type searchPlan struct {
	chats, mail bool
	notes       []string
}

// chatOnlyFlags names the flags that only Teams chats understand, in the order the help lists them.
func (c *searchCmd) chatOnlyFlags() []string {
	var out []string
	for _, f := range []struct {
		on   bool
		name string
	}{
		{c.MentionsMe, "--mentions-me"}, {c.DirectMentions, "--direct-mentions"}, {c.Conversation != "", "--conversation"},
		{c.Team != "", "--team"}, {c.IncludeSystem, "--include-system"}, {c.IncludeDeleted, "--include-deleted"}, {c.HTML, "--html"},
	} {
		if f.on {
			out = append(out, f.name)
		}
	}
	return out
}

// flagList writes flag names as prose: "a", "a and b", "a, b and c".
func flagList(flags []string) string {
	if len(flags) < 2 {
		return strings.Join(flags, "")
	}
	return strings.Join(flags[:len(flags)-1], ", ") + " and " + flags[len(flags)-1]
}

func appliesTo(flags []string, what string) string {
	verb := "applies"
	if len(flags) > 1 {
		verb = "apply"
	}
	return flagList(flags) + " " + verb + " to " + what + " only"
}

func sourceConflict(msg string) error {
	return &errs.Coded{Code: CodeFlagSourceConflict, Exit: errs.ExitUsage, Message: msg,
		Fix: "Drop the flag, or pick the --source it belongs to (chats, mail or all)."}
}

// plan decides which sources to read. A flag that one source cannot use is a usage error when
// --source names the other source, and narrows the search to its own source, with a note, when
// --source is left at all. Mail is skipped, with a note, on a platform that cannot read it.
func (c *searchCmd) plan(rt *runtime) (searchPlan, error) {
	p := searchPlan{chats: true, mail: true}
	chatFlags := c.chatOnlyFlags()
	mailAccount := isSearchMailAccount(rt.g.Account)
	switch {
	case mailAccount && c.Source == searchChats:
		return p, sourceConflict("--account " + rt.g.Account + " names a mail account; --source chats searches Teams chats")
	case mailAccount && len(chatFlags) > 0:
		return p, sourceConflict("--account " + rt.g.Account + " names a mail account; " + strings.Join(chatFlags, ", ") + " applies to Teams chats only")
	case rt.g.Account != "" && !mailAccount && c.Source == searchMail:
		return p, sourceConflict("--account " + rt.g.Account + " names a Teams account; --source mail searches mail")
	case rt.g.Account != "" && !mailAccount && c.Folder != "":
		return p, sourceConflict("--account " + rt.g.Account + " names a Teams account; --folder applies to mail only")
	case mailAccount && c.Source == "all":
		p.chats = false
		p.notes = append(p.notes, "--account names a mail account; Teams chats were not searched")
	case c.Source == searchChats && c.Folder != "":
		return p, sourceConflict("--folder applies to mail only; --source chats searches Teams chats")
	case c.Source == searchMail && len(chatFlags) > 0:
		return p, sourceConflict(strings.Join(chatFlags, ", ") + " applies to Teams chats only; --source mail searches mail")
	case c.Source == searchChats:
		p.mail = false
	case c.Source == searchMail:
		p.chats = false
	case len(chatFlags) > 0 && c.Folder != "":
		return p, sourceConflict("--folder applies to mail only and " + strings.Join(chatFlags, ", ") + " applies to Teams chats only; no source can use both")
	case len(chatFlags) > 0:
		p.mail = false
		p.notes = append(p.notes, appliesTo(chatFlags, "Teams chats")+"; mail was not searched")
	case c.Folder != "":
		p.chats = false
		p.notes = append(p.notes, "--folder applies to mail only; Teams chats were not searched")
	}
	if p.mail && searchMailPlatform == "windows" {
		if !p.chats {
			return p, &errs.Coded{Code: searchCodeMailUnsupported, Exit: errs.ExitEnvironment,
				Message: "mail is not yet read on Windows; Teams chats and the calendar work here — try `m365crawl calendar`",
				Fix:     "Run `m365crawl calendar` for the agenda, or a Teams command such as `m365crawl unread`."}
		}
		p.mail = false
		p.notes = append(p.notes, "mail is not yet read on Windows; searched Teams chats only")
	}
	return p, nil
}

// isSearchMailAccount says whether an --account value names an Outlook mail account (outlook/<profile>)
// rather than a Teams account (<tenantId>/<userId>).
func isSearchMailAccount(account string) bool { return strings.HasPrefix(account, "outlook/") }

// searchHit is one result item with the time it is ordered by.
type searchHit struct {
	at   time.Time
	item any
	mail bool
}

// search runs the plan against the archive and merges what each source found, newest first.
func (c *searchCmd) search(rt *runtime, p searchPlan, f store.Filter, st *store.Store) (result, error) {
	notes := slices.Clone(p.notes)
	searchMailNow := p.mail
	if p.mail {
		has := false
		if st != nil {
			var err error
			if has, err = st.HasMail(rt.ctx); err != nil {
				return nil, err
			}
		}
		if !has {
			searchMailNow = false
			b, err := rt.mailStatus(st)
			if err != nil {
				return nil, err
			}
			const syncForMail = "mail is not in the archive yet; run m365crawl sync"
			why := syncForMail
			if _, n := rt.whyNoMail(b); n != noMailYet {
				why = n // a sync would read no mail: say why instead
			}
			// With no archive and chats searched, the empty note already says to sync.
			if st != nil || !p.chats || why != syncForMail {
				notes = append(notes, why)
			}
		}
	}
	var hits []searchHit
	var chatTrunc, mailTrunc bool
	if p.chats && st != nil {
		rows, trunc, err := st.Search(rt.ctx, c.Query, f)
		if err != nil {
			return nil, err
		}
		html, err := rt.chatHTML(st, rows, c.HTML)
		if err != nil {
			return nil, err
		}
		chatTrunc = trunc
		for i, it := range messageItems(rows, rt.g.MaxText, html) {
			hits = append(hits, searchHit{at: rows[i].SentAt, item: searchChatItem{Source: searchChats, messageItem: it}})
		}
	}
	if searchMailNow {
		rows, trunc, err := st.MailSearch(rt.ctx, c.Query, store.MailFilter{Account: mailAccountOf(rt.g.Account), Folder: c.Folder, From: f.From, Since: f.Since, Until: f.Until, Limit: f.Limit})
		if errors.Is(err, store.ErrUnknownMailFolder) {
			u := errs.Usage(fmt.Sprintf("no mail folder matches %q", c.Folder))
			u.Code, u.Fix = searchCodeUnknownFolder, "`m365crawl mail folders` lists the folders."
			return nil, u
		}
		if err != nil {
			return nil, err
		}
		mailTrunc = trunc
		if rt.g.Account != "" && !isSearchMailAccount(rt.g.Account) {
			notes = append(notes, "--account names a Teams account; mail was searched across all Outlook accounts")
		}
		for _, r := range rows {
			hits = append(hits, searchHit{at: r.ReceivedAt, item: searchMailItemOf(r, rt.g.MaxText), mail: true})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].at.After(hits[j].at) })
	truncated := chatTrunc || mailTrunc
	if len(hits) > f.Limit {
		truncated = true
		for _, h := range hits[f.Limit:] {
			if h.mail {
				mailTrunc = true
			} else {
				chatTrunc = true
			}
		}
		hits = hits[:f.Limit]
	}
	var chats, mail int
	out := make([]any, len(hits))
	for i, h := range hits {
		out[i] = h.item
		if h.mail {
			mail++
		} else {
			chats++
		}
	}
	res := newList(shape(rt, out), truncated)
	res.Sources = &searchSources{}
	if p.chats {
		res.Sources.Chats = &sourceCount{Count: chats, Truncated: chatTrunc}
	}
	if searchMailNow {
		res.Sources.Mail = &sourceCount{Count: mail, Truncated: mailTrunc}
	} else {
		res.withTotal(*f.Total)
	}
	if len(hits) == 0 {
		cause, err := rt.searchEmptyNote(st, p.chats, searchMailNow)
		if err != nil {
			return nil, err
		}
		if cause != "" {
			notes = append(notes, cause)
		}
	}
	res.Note = strings.Join(notes, "; ")
	return res, nil
}

// searchNoMatch is the note of a search that read its sources and found nothing.
const searchNoMatch = "no message matched the search words and filters"

// searchEmptyNote says why a search found nothing, naming only the sources it read: the Teams
// cause (no archive, no Teams data, an unknown account, a range outside the archived window, or no match), and for mail
// that nothing matched. Empty when neither source was read: the plan's notes already say why.
func (rt *runtime) searchEmptyNote(st *store.Store, chats, mail bool) (string, error) {
	const noMail = "no mail matched the search words and filters"
	if !chats {
		if mail {
			return noMail, nil
		}
		return "", nil
	}
	cause, err := rt.emptyListNote(st)
	switch {
	case err != nil || !mail:
		return cause, err
	case cause == searchNoMatch:
		return "no chat message or mail matched the search words and filters", nil
	}
	return "Teams chats: " + cause + "; " + noMail, nil
}

// mailAccountOf is the mail account an --account value names, or "" for a Teams account or none.
func mailAccountOf(account string) string {
	if isSearchMailAccount(account) {
		return account
	}
	return ""
}
