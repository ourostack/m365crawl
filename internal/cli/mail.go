package cli

import (
	"errors"
	"fmt"
	"reflect"
	"regexp"
	goruntime "runtime"
	"strconv"
	"strings"
	"time"

	"github.com/mattn/go-runewidth"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/outlookdesktop"
	"github.com/ourostack/m365crawl/internal/render"
	"github.com/ourostack/m365crawl/internal/store"
)

// Codes of the mail commands' own errors.
const (
	CodeMailUnsupportedPlatform = "mail_unsupported_platform"
	CodeUnknownFolder           = "unknown_folder"
	CodeBadMailID               = "bad_mail_id"
	CodeMailNotFound            = "mail_not_found"
	CodeAccountMismatch         = "account_mismatch"
)

// mailPlatform is the operating system the mail commands believe they run on; tests set it.
var mailPlatform = goruntime.GOOS

// mailGuard stops every mail command on a platform that cannot read mail.
func mailGuard() error {
	if mailPlatform != "windows" {
		return nil
	}
	return &errs.Coded{Code: CodeMailUnsupportedPlatform, Exit: errs.ExitEnvironment,
		Message: "mail is not yet read on Windows; Teams chats and the calendar work here — try `m365crawl calendar`",
		Fix:     "Run `m365crawl calendar` for the agenda, or a Teams command such as `m365crawl unread`."}
}

// mailGroup is the mail command group.
type mailGroup struct {
	List    mailListCmd    `cmd:"" help:"List archived Outlook mail, newest first."`
	Show    mailShowCmd    `cmd:"" help:"Show one message: headers, recipients, body text and each attachment's name, size and type."`
	Thread  mailThreadCmd  `cmd:"" help:"Show the conversation a message belongs to: its reply chain, or messages with the same subject and a shared participant when Outlook kept no reply link."`
	Folders mailFoldersCmd `cmd:"" help:"List mail folders with message and unread counts and how far back the cache reaches."`
	Unread  mailUnreadCmd  `cmd:"" help:"Unread mail by folder."`
}

const mailCommon = `Mail comes from Outlook for Mac's local cache, so it holds only what Outlook has cached. Every result says when mail was last read (synced_at) and, as a list, how far back the cache covers.
--account takes the mail account as printed in the ids, outlook/<profile>.
A field the archive does not hold is null: subject, sender, in_reply_to, ical_uid and internet_message_id when Outlook stored none, is_read when the read state is not known, sent_at when the store has no send time, address on a recipient with no address, gone_at and evicted_at while the message is present.
Recipients: To and Cc are not yet told apart, so recipients carries each person once with kind_raw, the store's own number. importance is likely right (low, normal, high) but is read from one store field that is not fully confirmed.
An id is account:detail_key, as printed by mail list.`

// Help is the long help of the mail group, shared by every mail command.
func (mailGroup) Help() string {
	return mailCommon
}

const mailSeeGroup = "What every mail command shares (null fields, ids, --account, recipients, importance) is in `m365crawl mail --help`."

// Help is the long help of mail list.
func (mailListCmd) Help() string {
	return mailSeeGroup + "\nmail list shows recipient_count and the first three recipient names (recipients_preview); mail show lists everyone. --since and --until take YYYY-MM-DD, RFC3339 or a relative age such as 7d; a date alone as --until includes that whole day, a time is exclusive. --unread also reports how many messages the cache holds in the folder, because Outlook may show more."
}

// Help is the long help of mail show.
func (mailShowCmd) Help() string {
	return mailSeeGroup + "\nmail show lists every recipient with its kind_raw and every attachment with its name, size and content type. body_text is read from the cached body; body_state says when it is missing or unreadable. --max-text cuts body_text and sets text_truncated."
}

// Help is the long help of mail thread.
func (mailThreadCmd) Help() string {
	return mailSeeGroup + "\nThe grouping key says how the thread was found: reply_chain follows In-Reply-To links in both directions; subject groups messages with the same subject (ignoring re:, fw: and the like) that share a sender or recipient address with the message. participants lists everyone in the thread once."
}

// Help is the long help of mail folders.
func (mailFoldersCmd) Help() string {
	return mailSeeGroup + "\nmessages and unread count what the archive holds in the folder, leaving out messages that are gone or evicted. oldest_at is the oldest message the cache held at the last read."
}

// Help is the long help of mail unread.
func (mailUnreadCmd) Help() string {
	return mailSeeGroup + "\nunread is the unread count of each folder against cached, the messages the archive holds there; the cache can hold fewer than Outlook shows, and the note says so. items are the newest unread messages (--limit changes how many, default 20)."
}

// ---- items ----

type mailRecipientOut struct {
	Name    *string `json:"name"`
	Address *string `json:"address"`
	KindRaw uint32  `json:"kind_raw"`
}

type mailAttachmentOut struct {
	Name        *string `json:"name"`
	Size        int64   `json:"size"`
	ContentType *string `json:"content_type"`
	Inline      bool    `json:"inline"`
	Downloaded  bool    `json:"downloaded"`
}

type mailPersonOut struct {
	Name    *string `json:"name"`
	Address *string `json:"address"`
}

// mailListItem is one message in a list.
type mailListItem struct {
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

// mailShowItem is one message in full.
type mailShowItem struct {
	ID                string              `json:"id"`
	Account           string              `json:"account"`
	Folder            *string             `json:"folder"`
	FolderKind        *string             `json:"folder_kind"`
	ToMe              bool                `json:"to_me"`
	Subject           *string             `json:"subject"`
	FromName          *string             `json:"from_name"`
	FromAddress       *string             `json:"from_address"`
	Recipients        []mailRecipientOut  `json:"recipients"`
	InReplyTo         *string             `json:"in_reply_to"`
	ReceivedAt        *time.Time          `json:"received_at"`
	SentAt            *time.Time          `json:"sent_at"`
	IsRead            *bool               `json:"is_read"`
	ReadState         *string             `json:"read_state"`
	Flag              *string             `json:"flag"`
	Importance        *string             `json:"importance"`
	HasAttachments    bool                `json:"has_attachments"`
	Attachments       []mailAttachmentOut `json:"attachments"`
	Preview           *string             `json:"preview"`
	BodyText          *string             `json:"body_text"`
	BodyState         *string             `json:"body_state"`
	InternetMessageID *string             `json:"internet_message_id"`
	ICalUID           *string             `json:"ical_uid"`
	GoneAt            *time.Time          `json:"gone_at"`
	EvictedAt         *time.Time          `json:"evicted_at"`
	TextTruncated     bool                `json:"text_truncated,omitempty"`
}

// mailFolderItem is one folder with its counts.
type mailFolderItem struct {
	Account  string     `json:"account"`
	Folder   string     `json:"folder"`
	Kind     string     `json:"kind"`
	Messages int        `json:"messages"`
	Unread   int        `json:"unread"`
	OldestAt *time.Time `json:"oldest_at"`
	NewestAt *time.Time `json:"newest_at"`
	ReadAt   *time.Time `json:"read_at"`
}

// mailUnreadFolder is one folder's line of mail unread.
type mailUnreadFolder struct {
	Account string `json:"account"`
	Folder  string `json:"folder"`
	Kind    string `json:"kind"`
	Unread  int    `json:"unread"`
	Cached  int    `json:"cached"`
}

type mailCoverageOut struct {
	Account  string     `json:"account"`
	Folder   string     `json:"folder"`
	Kind     string     `json:"kind"`
	OldestAt *time.Time `json:"oldest_at"`
	NewestAt *time.Time `json:"newest_at"`
	Count    int        `json:"count"`
}

func nz(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func sv(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func tp(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	u := t.UTC()
	return &u
}

func recipientLabel(r store.MailRecipient) string {
	if r.Name != "" {
		return r.Name
	}
	return r.Address
}

func listItemOf(r store.MailRow, max int) mailListItem {
	it := mailListItem{
		ID: r.ID(), Account: r.Account, Folder: nz(r.Folder), FolderKind: nz(r.FolderKind), ToMe: r.ToMe,
		Subject: nz(r.Subject), FromName: nz(r.SenderName), FromAddress: nz(r.SenderAddress),
		RecipientCount: len(r.Recipients), RecipientsPreview: []string{}, InReplyTo: nz(r.InReplyTo),
		ReceivedAt: tp(r.ReceivedAt), SentAt: tp(r.SentAt), IsRead: r.IsRead, Flag: nz(r.Flag), Importance: nz(r.Importance),
		HasAttachments: r.HasAttachments, InternetMessageID: nz(r.InternetMessageID), ICalUID: nz(r.ICalUID),
		GoneAt: tp(r.GoneAt), EvictedAt: tp(r.EvictedAt),
	}
	for i, rc := range r.Recipients {
		if i == 3 {
			break
		}
		it.RecipientsPreview = append(it.RecipientsPreview, recipientLabel(rc))
	}
	prev := r.Preview
	if t, did := truncateRunes(prev, max); did {
		prev, it.TextTruncated = t, true
	}
	it.Preview = nz(prev)
	return it
}

func showItemOf(r store.MailRow, max int) mailShowItem {
	it := mailShowItem{
		ID: r.ID(), Account: r.Account, Folder: nz(r.Folder), FolderKind: nz(r.FolderKind), ToMe: r.ToMe,
		Subject: nz(r.Subject), FromName: nz(r.SenderName), FromAddress: nz(r.SenderAddress),
		Recipients: []mailRecipientOut{}, InReplyTo: nz(r.InReplyTo), ReceivedAt: tp(r.ReceivedAt), SentAt: tp(r.SentAt),
		IsRead: r.IsRead, ReadState: nz(r.ReadState), Flag: nz(r.Flag), Importance: nz(r.Importance),
		HasAttachments: r.HasAttachments, Attachments: []mailAttachmentOut{}, BodyState: nz(r.BodyState),
		InternetMessageID: nz(r.InternetMessageID), ICalUID: nz(r.ICalUID), GoneAt: tp(r.GoneAt), EvictedAt: tp(r.EvictedAt),
	}
	for _, rc := range r.Recipients {
		it.Recipients = append(it.Recipients, mailRecipientOut{Name: nz(rc.Name), Address: nz(rc.Address), KindRaw: rc.KindRaw})
	}
	for _, a := range r.Attachments {
		it.Attachments = append(it.Attachments, mailAttachmentOut{Name: nz(a.Name), Size: a.Size, ContentType: nz(a.ContentType), Inline: a.Inline, Downloaded: a.Downloaded})
	}
	prev, body := r.Preview, r.BodyText
	if t, did := truncateRunes(prev, max); did {
		prev, it.TextTruncated = t, true
	}
	if t, did := truncateRunes(body, max); did {
		body, it.TextTruncated = t, true
	}
	it.Preview, it.BodyText = nz(prev), nz(body)
	return it
}

func listKeys() []string   { return jsonKeys(reflect.TypeFor[mailListItem]()) }
func showKeys() []string   { return jsonKeys(reflect.TypeFor[mailShowItem]()) }
func folderKeys() []string { return jsonKeys(reflect.TypeFor[mailFolderItem]()) }

// checkMailFields rejects a --fields key the command's items do not have.
func (rt *runtime) checkMailFields(valid []string) error {
	for _, f := range rt.fields {
		if !contains(valid, f) {
			c := errs.Usage(fmt.Sprintf("unknown --fields key %q; valid keys: %s", f, strings.Join(valid, ", ")))
			c.Fix = "Pick keys from the list in the message."
			return c
		}
	}
	return nil
}

// ---- scope: folders and coverage ----

type mailScope struct {
	all      []store.MailFolderRow // every folder of the account filter
	folders  []store.MailFolderRow // the folders --folder names (all when it is empty)
	coverage []store.CoverageRow   // the coverage of those folders
	syncedAt time.Time             // when mail was last read, over every folder of the account filter
	marked   bool                  // a sync has read the mail of the account filter (the outlook_mail_read marker)
}

func (sc mailScope) since() time.Time {
	var out time.Time
	for _, c := range sc.coverage {
		if !c.OldestAt.IsZero() && (out.IsZero() || c.OldestAt.Before(out)) {
			out = c.OldestAt
		}
	}
	return out
}

func sameFolder(f store.MailFolderRow, c store.CoverageRow) bool {
	return f.Account == c.Account && f.FolderKey == c.FolderKey
}

// matchFolders is the folders a --folder value names: by name first, then by kind, ignoring case.
func matchFolders(all []store.MailFolderRow, name string) []store.MailFolderRow {
	var byName, byKind []store.MailFolderRow
	for _, f := range all {
		if strings.EqualFold(f.Name, name) {
			byName = append(byName, f)
		}
		if strings.EqualFold(f.Kind, name) {
			byKind = append(byKind, f)
		}
	}
	if len(byName) > 0 {
		return byName
	}
	return byKind
}

func unknownFolder(name string, all []store.MailFolderRow) *errs.Coded {
	var names []string
	seen := map[string]bool{}
	for _, f := range all {
		if !seen[f.Name] {
			seen[f.Name] = true
			names = append(names, f.Name)
		}
	}
	c := errs.Usage(fmt.Sprintf("no mail folder matches %q", name))
	c.Code = CodeUnknownFolder
	if len(names) == 0 {
		c.Fix = "No mail folders are archived yet: run `m365crawl sync`."
		return c
	}
	c.Fix = "Pick a folder name or kind that `m365crawl mail folders` lists. The folders are: " + strings.Join(names, ", ") + "."
	return c
}

// mailScope loads the folders and coverage of the account filter and narrows them to --folder.
func (rt *runtime) mailScope(st *store.Store, folder string) (mailScope, error) {
	var sc mailScope
	cov, err := st.MailCoverage(rt.ctx)
	if err != nil {
		return sc, err
	}
	all, err := st.MailFolders(rt.ctx)
	if err != nil {
		return sc, err
	}
	marked, err := mailReadAccounts(st, rt.ctx)
	if err != nil {
		return sc, err
	}
	for _, a := range marked {
		sc.marked = sc.marked || rt.g.Account == "" || a == rt.g.Account
	}
	for _, f := range all {
		if rt.g.Account == "" || f.Account == rt.g.Account {
			sc.all = append(sc.all, f)
		}
	}
	var covAll []store.CoverageRow
	for _, c := range cov {
		if rt.g.Account == "" || c.Account == rt.g.Account {
			covAll = append(covAll, c)
			if c.ReadAt.After(sc.syncedAt) {
				sc.syncedAt = c.ReadAt
			}
		}
	}
	sc.folders, sc.coverage = sc.all, covAll
	if folder == "" {
		return sc, nil
	}
	sc.folders = matchFolders(sc.all, folder)
	if len(sc.folders) == 0 {
		return sc, unknownFolder(folder, sc.all)
	}
	sc.coverage = nil
	for _, c := range covAll {
		for _, f := range sc.folders {
			if sameFolder(f, c) {
				sc.coverage = append(sc.coverage, c)
				break
			}
		}
	}
	return sc, nil
}

func coverageOut(rows []store.CoverageRow) []mailCoverageOut {
	out := make([]mailCoverageOut, len(rows))
	for i, c := range rows {
		out[i] = mailCoverageOut{Account: c.Account, Folder: c.Folder, Kind: c.Kind, OldestAt: tp(c.OldestAt), NewestAt: tp(c.NewestAt), Count: c.Count}
	}
	return out
}

// ---- notes ----

// mailReadAccounts is the test seam of the read markers.
var mailReadAccounts = (*store.Store).MailReadAccounts

// mailProfileCount counts the Outlook profiles under root (the default root when empty); a test seam.
var mailProfileCount = func(root string) int {
	if root == "" {
		root, _ = outlookdesktop.DefaultRoot() // an unknown home gives "", which Discover refuses
	}
	profiles, _, _, err := outlookdesktop.Discover(root)
	if err != nil {
		return 0
	}
	return len(profiles)
}

// emptyNote explains an empty result: which of the causes it is. filtered says the command had
// a filter beyond the account; since is --since.
func (rt *runtime) emptyNote(st *store.Store, sc mailScope, filtered bool, since time.Time) string {
	switch {
	case st == nil:
		return "no archive yet: run m365crawl sync"
	case rt.g.Account != "" && len(sc.all) == 0:
		return "no mail is archived for account " + rt.g.Account + ": the ids that `m365crawl mail list` prints name the accounts that have mail"
	case len(sc.all) == 0 && !rt.outlookOn:
		return "mail is not read in this run: the Outlook source is off (--outlook-root none, or --teams-root without --outlook-root)"
	case len(sc.all) == 0 && mailProfileCount(rt.outlookRoot) == 0:
		return "no Outlook for Mac profile was found on this machine, so there is no mail to read"
	case len(sc.all) == 0 && sc.marked:
		return "the Outlook source is on and the last read found no mail folders: mail is turned off in Outlook for this account, or none is set up"
	case len(sc.all) == 0 && rt.synced != nil:
		return "no mail has been read, although this run synced: m365crawl status shows the mail state and m365crawl doctor says why"
	case len(sc.all) == 0:
		return "no mail has been read yet: run m365crawl sync"
	}
	if cut := sc.since(); filtered && !since.IsZero() && !cut.IsZero() && since.Before(cut) {
		return "no message matched, and the cache covers only since " + cut.In(displayZone).Format("2006-01-02") + ": older mail is not in it"
	}
	if filtered {
		return "no message matched the filters"
	}
	return "the mailbox is empty: the cache holds no messages"
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

// cacheNote is the sentence every unread result carries for a folder.
func cacheNote(cached int) string {
	return fmt.Sprintf("the local cache holds %s in this folder; Outlook may show more", plural(cached, "message", "messages"))
}

// unreadNote is the one sentence about what the cache holds: with several folders it names each.
func unreadNote(rows []mailUnreadFolder) string {
	switch len(rows) {
	case 0:
		return ""
	case 1:
		return cacheNote(rows[0].Cached)
	}
	parts := make([]string, len(rows))
	for i, r := range rows {
		parts[i] = plural(r.Cached, "message", "messages") + " in " + r.Folder
	}
	return "the local cache holds " + strings.Join(parts, ", ") + "; Outlook may show more"
}

// ---- results ----

// mailListResult is the document of every mail list command.
type mailListResult struct {
	Items     []any             `json:"items"`
	Count     int               `json:"count"`
	Truncated bool              `json:"truncated"`
	SyncedAt  *time.Time        `json:"synced_at"`
	Coverage  []mailCoverageOut `json:"coverage"`
	meta
	rows   []store.MailRow // the items, for the text output
	typed  bool            // the items are mailListItem (no --fields)
	covers time.Time       // how far back the cache covers, for the text footer
}

func (rt *runtime) newMailList(rows []store.MailRow, truncated bool, sc mailScope) *mailListResult {
	items := make([]mailListItem, len(rows))
	for i, r := range rows {
		items[i] = listItemOf(r, rt.g.MaxText)
	}
	res := &mailListResult{Items: shape(rt, items), Truncated: truncated, SyncedAt: tp(sc.syncedAt), Coverage: coverageOut(sc.coverage),
		rows: rows, typed: len(rt.fields) == 0, covers: sc.since()}
	res.Count = len(res.Items)
	return res
}

// mailRenderer is a result that prints itself as text.
type mailRenderer interface {
	renderMail(rt *runtime)
}

// footer is the last line of every human mail list.
func (r *mailListResult) footer(rt *runtime) { syncFooter(rt, r.SyncedAt, r.covers) }

// syncFooter prints when mail was last read and how far back the cache covers.
func syncFooter(rt *runtime, syncedAt *time.Time, since time.Time) {
	synced, covers := "never", "-"
	if syncedAt != nil {
		synced = stamp(*syncedAt)
	}
	if !since.IsZero() {
		covers = since.In(displayZone).Format("2006-01-02")
	}
	_, _ = fmt.Fprintf(rt.stdout, "%s\n", render.Dim("synced "+synced+" · cache covers since "+covers, rt.color))
}

func (r *mailListResult) tail(rt *runtime) {
	w := rt.stdout
	_, _ = fmt.Fprintln(w)
	more := ""
	if r.Truncated {
		more = " (more exist; raise --limit)"
	}
	_, _ = fmt.Fprintf(w, "%s\n", render.Dim(fmt.Sprintf("%d items%s", r.Count, more), rt.color))
	metaLines(w, r.meta, rt.color)
	r.footer(rt)
}

func (r *mailListResult) renderMail(rt *runtime) {
	r.messages(rt)
	r.tail(rt)
}

// messages prints the item table.
func (r *mailListResult) messages(rt *runtime) {
	if !r.typed || len(r.rows) == 0 {
		rt.listTable(&listResult{Items: r.Items})
		return
	}
	rows := make([][]string, len(r.rows))
	for i, m := range r.rows {
		rows[i] = []string{m.ID(), stamp(m.ReceivedAt), mailState(m), firstOf(m.SenderName, m.SenderAddress), oneLine(m.Subject), m.Folder}
	}
	rt.fitTable([]string{"id", "received", "state", "from", "subject", "folder"}, rows, 4, 0)
}

// fitTable prints a table whose text column is clipped to the terminal; keep names a column that
// is never clipped.
func (rt *runtime) fitTable(cols []string, rows [][]string, textCol, keep int) {
	for _, row := range rows {
		for i := range row {
			if i != textCol && i != keep {
				row[i] = render.Truncate(row[i], 40)
			}
		}
	}
	used := 2 * (len(cols) - 1)
	for i := range cols {
		if i == textCol {
			continue
		}
		w := runewidth.StringWidth(cols[i])
		for _, row := range rows {
			w = max(w, runewidth.StringWidth(row[i]))
		}
		used += w
	}
	room := max(rt.termWidth()-used, 20)
	for _, row := range rows {
		row[textCol] = render.Truncate(row[textCol], room)
	}
	render.Table(rt.stdout, cols, rows, rt.color)
}

func mailState(m store.MailRow) string {
	var s []string
	switch {
	case m.IsRead == nil:
		s = append(s, "?")
	case !*m.IsRead:
		s = append(s, "unread")
	}
	if m.Flag == "flagged" {
		s = append(s, "flagged")
	}
	if m.HasAttachments {
		s = append(s, "files")
	}
	if !m.GoneAt.IsZero() {
		s = append(s, "gone")
	}
	if !m.EvictedAt.IsZero() {
		s = append(s, "evicted")
	}
	return strings.Join(s, ",")
}

// ---- mail list ----

type mailListCmd struct {
	Folder         string `help:"Only this folder, by name (checked first) or kind: inbox, sent, drafts, archive, deleted, to_me, other." placeholder:"NAME|KIND"`
	From           string `help:"Only messages whose sender name or address contains this text, ignoring case." placeholder:"TEXT"`
	Since          string `help:"Only messages received at or after this time (YYYY-MM-DD, RFC3339 or an age such as 7d)." placeholder:"DATE"`
	Until          string `help:"Only messages received before this time; a date alone (YYYY-MM-DD) includes that whole day, a time is exclusive." placeholder:"DATE"`
	Unread         bool   `help:"Only unread messages."`
	Flagged        bool   `help:"Only flagged messages."`
	HasAttachments bool   `name:"has-attachments" help:"Only messages with attachments."`
	IncludeGone    bool   `name:"include-gone" help:"Also list messages a sync saw disappear."`
	IncludeEvicted bool   `name:"include-evicted" help:"Also list messages that fell out of the cache's covered range."`
	Limit          int    `default:"50" help:"Maximum messages to return; truncated says whether more exist." placeholder:"N"`
}

func (c *mailListCmd) Run(rt *runtime) error {
	if err := mailGuard(); err != nil {
		return err
	}
	if err := rt.checkMailFields(listKeys()); err != nil {
		return err
	}
	if err := checkMailLimit(c.Limit); err != nil {
		return err
	}
	since, err := rt.when("--since", c.Since)
	if err != nil {
		return err
	}
	until, err := rt.when("--until", c.Until)
	if err != nil {
		return err
	}
	if dateOnly.MatchString(c.Until) {
		until = until.In(time.Local).AddDate(0, 0, 1) // a date alone names the whole day
	}
	return rt.read("mail list", func(st *store.Store) (result, error) {
		if st == nil {
			res := rt.newMailList(nil, false, mailScope{})
			res.Note = rt.emptyNote(nil, mailScope{}, false, since)
			return res, nil
		}
		sc, err := rt.mailScope(st, c.Folder)
		if err != nil {
			return nil, err
		}
		f := store.MailFilter{Account: rt.g.Account, Folder: c.Folder, From: c.From, Since: since, Until: until,
			Unread: c.Unread, Flagged: c.Flagged, HasAttachments: c.HasAttachments,
			IncludeGone: c.IncludeGone, IncludeEvicted: c.IncludeEvicted, Limit: c.Limit + 1}
		rows, err := st.MailList(rt.ctx, f)
		if err != nil {
			return nil, err
		}
		truncated := len(rows) > c.Limit
		if truncated {
			rows = rows[:c.Limit]
		}
		res := rt.newMailList(rows, truncated, sc)
		var note []string
		if len(rows) == 0 {
			filtered := c.From != "" || !since.IsZero() || !until.IsZero() || c.Unread || c.Flagged || c.HasAttachments || c.Folder != ""
			note = append(note, rt.emptyNote(st, sc, filtered, since))
		}
		if c.Unread {
			n := unreadNote(unreadFolders(sc.all, rows))
			if n == "" {
				n = "no unread mail in the cache; Outlook may show more"
			}
			note = append(note, n)
		}
		res.Note = strings.Join(note, "; ")
		return res, nil
	})
}

// unreadFolders is, for each folder the rows sit in, its unread and cached counts.
func unreadFolders(all []store.MailFolderRow, rows []store.MailRow) []mailUnreadFolder {
	var out []mailUnreadFolder
	seen := map[string]bool{}
	for _, r := range rows {
		key := r.Account + "\x00" + strconv.Itoa(int(r.FolderKey))
		if seen[key] {
			continue
		}
		seen[key] = true
		for _, f := range all {
			if f.Account == r.Account && f.FolderKey == r.FolderKey {
				out = append(out, mailUnreadFolder{Account: f.Account, Folder: f.Name, Kind: f.Kind, Unread: f.Unread, Cached: f.Messages})
			}
		}
	}
	return out
}

// ---- mail show ----

type mailShowCmd struct {
	ID string `arg:"" name:"id" help:"A message id, account:detail_key, as printed by mail list."`
}

// parseMailID splits account:detail_key at the last colon.
func parseMailID(id string) (string, uint32, error) {
	i := strings.LastIndex(id, ":")
	if i > 0 {
		if n, err := strconv.ParseUint(id[i+1:], 10, 32); err == nil {
			return id[:i], uint32(n), nil
		}
	}
	c := errs.Usage(fmt.Sprintf("%q is not a mail id", id))
	c.Code = CodeBadMailID
	c.Fix = "A mail id is account:detail_key, for example outlook/Main:12345, as printed by `m365crawl mail list`."
	return "", 0, c
}

// checkIDAccount refuses an --account that names another account than the id does.
func (rt *runtime) checkIDAccount(account string) error {
	if rt.g.Account == "" || rt.g.Account == account {
		return nil
	}
	c := errs.Usage(fmt.Sprintf("--account %s does not match the id's account %s", rt.g.Account, account))
	c.Code = CodeAccountMismatch
	c.Fix = "Drop --account (the id already names the account), or pass --account " + account + "."
	return c
}

func mailNotFound(id string) *errs.Coded {
	c := errs.Usage(fmt.Sprintf("no mail message %q in the archive", id))
	c.Code = CodeMailNotFound
	c.Fix = "List messages with `m365crawl mail list`; a message the cache dropped is not archived. Run `m365crawl sync` if the mail is new."
	return c
}

func (c *mailShowCmd) Run(rt *runtime) error {
	if err := mailGuard(); err != nil {
		return err
	}
	if err := rt.checkMailFields(showKeys()); err != nil {
		return err
	}
	account, key, err := parseMailID(c.ID)
	if err != nil {
		return err
	}
	if err := rt.checkIDAccount(account); err != nil {
		return err
	}
	return rt.read("mail show", func(st *store.Store) (result, error) {
		if st == nil {
			return nil, mailNotFound(c.ID)
		}
		row, err := st.MailGet(rt.ctx, account, key)
		if errors.Is(err, store.ErrMailNotFound) {
			return nil, mailNotFound(c.ID)
		}
		if err != nil {
			return nil, err
		}
		sc, err := rt.mailScope(st, "")
		if err != nil {
			return nil, err
		}
		return &mailShowResult{item: showItemOf(row, rt.g.MaxText), keys: rt.fields, syncedAt: tp(sc.syncedAt), covers: sc.since()}, nil
	})
}

// mailShowResult is mail show's document: the message, or the keys --fields kept, then the meta.
type mailShowResult struct {
	item     mailShowItem
	keys     []string
	syncedAt *time.Time
	covers   time.Time
	meta
}

// mailShowTail is what follows the message: when mail was last read, then the meta.
type mailShowTail struct {
	SyncedAt *time.Time `json:"synced_at"`
	meta
}

func (r *mailShowResult) MarshalJSON() ([]byte, error) {
	var body any = r.item
	if len(r.keys) > 0 {
		body, _ = project(r.item, append(append([]string(nil), r.keys...), "text_truncated")) // an item always encodes
	}
	return joinJSON(body, mailShowTail{SyncedAt: r.syncedAt, meta: r.meta})
}

func (r *mailShowResult) renderMail(rt *runtime) {
	w, color, it := rt.stdout, rt.color, r.item
	if len(r.keys) > 0 {
		p, _ := project(it, append(append([]string(nil), r.keys...), "text_truncated"))
		rt.listTable(&listResult{Items: []any{p}})
		metaLines(w, r.meta, color)
		syncFooter(rt, r.syncedAt, r.covers)
		return
	}
	head := map[string]any{"id": it.ID, "folder": sv(it.Folder), "received": stamp(derefTime(it.ReceivedAt))}
	set := func(k, v string) {
		if v != "" {
			head[k] = v
		}
	}
	set("from", personText(sv(it.FromName), sv(it.FromAddress)))
	if it.SentAt != nil {
		head["sent"] = stamp(*it.SentAt)
	}
	state := "unknown"
	if it.IsRead != nil {
		state = map[bool]string{true: "read", false: "unread"}[*it.IsRead]
	}
	head["state"] = state
	set("flag", sv(it.Flag))
	set("importance (likely)", sv(it.Importance))
	set("in reply to", sv(it.InReplyTo))
	set("body", sv(it.BodyState))
	if it.GoneAt != nil {
		head["gone"] = stamp(*it.GoneAt)
	}
	if it.EvictedAt != nil {
		head["evicted"] = stamp(*it.EvictedAt)
	}
	render.Block(w, oneLine(sv(it.Subject)), head, color)
	if len(it.Recipients) > 0 {
		_, _ = fmt.Fprintln(w)
		rows := make([][]string, len(it.Recipients))
		for i, rc := range it.Recipients {
			rows[i] = []string{sv(rc.Name), sv(rc.Address), strconv.FormatUint(uint64(rc.KindRaw), 10)}
		}
		render.Table(w, []string{"recipient (To and Cc not yet told apart)", "address", "kind_raw"}, rows, color)
	}
	if len(it.Attachments) > 0 {
		_, _ = fmt.Fprintln(w)
		rows := make([][]string, len(it.Attachments))
		for i, a := range it.Attachments {
			kind := sv(a.ContentType)
			if a.Inline {
				kind += " (inline)"
			}
			rows[i] = []string{sv(a.Name), strconv.FormatInt(a.Size, 10), kind}
		}
		render.Table(w, []string{"attachment", "bytes", "type"}, rows, color)
	}
	if body := sv(it.BodyText); body != "" {
		_, _ = fmt.Fprintf(w, "\n%s\n", body)
		if it.TextTruncated {
			_, _ = fmt.Fprintf(w, "%s\n", render.Dim("(text cut by --max-text)", color))
		}
	}
	metaLines(w, r.meta, color)
	syncFooter(rt, r.syncedAt, r.covers)
}

func derefTime(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

func personText(name, addr string) string {
	switch {
	case name != "" && addr != "":
		return name + " <" + addr + ">"
	case name != "":
		return name
	}
	return addr
}

// ---- mail thread ----

type mailThreadCmd struct {
	ID string `arg:"" name:"id" help:"A message id, account:detail_key, as printed by mail list."`
}

// mailThreadResult is mail thread's document.
type mailThreadResult struct {
	Grouping     string          `json:"grouping"`
	Participants []mailPersonOut `json:"participants"`
	mailListResult
}

func (c *mailThreadCmd) Run(rt *runtime) error {
	if err := mailGuard(); err != nil {
		return err
	}
	if err := rt.checkMailFields(listKeys()); err != nil {
		return err
	}
	account, key, err := parseMailID(c.ID)
	if err != nil {
		return err
	}
	if err := rt.checkIDAccount(account); err != nil {
		return err
	}
	return rt.read("mail thread", func(st *store.Store) (result, error) {
		if st == nil {
			return nil, mailNotFound(c.ID)
		}
		rows, grouping, truncated, err := st.MailThread(rt.ctx, account, key)
		if errors.Is(err, store.ErrMailNotFound) {
			return nil, mailNotFound(c.ID)
		}
		if err != nil {
			return nil, err
		}
		sc, err := rt.mailScope(st, "")
		if err != nil {
			return nil, err
		}
		res := &mailThreadResult{Grouping: grouping, Participants: []mailPersonOut{}, mailListResult: *rt.newMailList(rows, truncated, sc)}
		if len(rows) == 1 {
			res.Note = "no other message shares its subject and a participant"
		}
		for _, p := range store.MailParticipants(rows) {
			res.Participants = append(res.Participants, mailPersonOut{Name: nz(p.Name), Address: nz(p.Address)})
		}
		return res, nil
	})
}

func (r *mailThreadResult) renderMail(rt *runtime) {
	how := "same subject and a shared participant"
	if r.Grouping == "reply_chain" {
		how = "reply chain"
	}
	_, _ = fmt.Fprintf(rt.stdout, "%s\n\n", render.Dim("thread: "+how, rt.color))
	r.messages(rt)
	if len(r.Participants) > 0 {
		names := make([]string, len(r.Participants))
		for i, p := range r.Participants {
			names[i] = personText(sv(p.Name), sv(p.Address))
		}
		_, _ = fmt.Fprintf(rt.stdout, "\nparticipants: %s\n", strings.Join(names, ", "))
	}
	r.tail(rt)
}

// ---- mail folders ----

type mailFoldersCmd struct{}

// mailFoldersResult is mail folders' document.
type mailFoldersResult struct {
	mailListResult
	folders []store.MailFolderRow
}

func (c *mailFoldersCmd) Run(rt *runtime) error {
	if err := mailGuard(); err != nil {
		return err
	}
	if err := rt.checkMailFields(folderKeys()); err != nil {
		return err
	}
	return rt.read("mail folders", func(st *store.Store) (result, error) {
		var sc mailScope
		if st != nil {
			var err error
			if sc, err = rt.mailScope(st, ""); err != nil {
				return nil, err
			}
		}
		items := make([]mailFolderItem, len(sc.folders))
		for i, f := range sc.folders {
			items[i] = mailFolderItem{Account: f.Account, Folder: f.Name, Kind: f.Kind, Messages: f.Messages, Unread: f.Unread,
				OldestAt: tp(f.OldestAt), NewestAt: tp(f.NewestAt), ReadAt: tp(f.ReadAt)}
		}
		res := &mailFoldersResult{mailListResult: *rt.newMailList(nil, false, sc)}
		res.Items, res.typed = shape(rt, items), len(rt.fields) == 0
		res.Count = len(res.Items)
		res.folders = sc.folders
		if len(items) == 0 {
			res.Note = rt.emptyNote(st, sc, false, time.Time{})
		}
		return res, nil
	})
}

func (r *mailFoldersResult) renderMail(rt *runtime) {
	if !r.typed || len(r.folders) == 0 {
		rt.listTable(&listResult{Items: r.Items})
	} else {
		rows := make([][]string, len(r.folders))
		for i, f := range r.folders {
			rows[i] = []string{f.Name, f.Kind, strconv.Itoa(f.Messages), strconv.Itoa(f.Unread), stamp(f.OldestAt), stamp(f.NewestAt)}
		}
		render.Table(rt.stdout, []string{"folder", "kind", "messages", "unread", "oldest cached", "newest"}, rows, rt.color)
	}
	r.tail(rt)
}

// ---- mail unread ----

type mailUnreadCmd struct {
	Folder string `help:"Only this folder, by name (checked first) or kind." placeholder:"NAME|KIND"`
	Limit  int    `default:"20" help:"How many of the newest unread messages to list." placeholder:"N"`
}

// mailUnreadResult is mail unread's document: the folders, their unread total, then the newest
// unread messages.
type mailUnreadResult struct {
	Folders     []mailUnreadFolder `json:"folders"`
	UnreadTotal int                `json:"unread_total"`
	mailListResult
}

func (c *mailUnreadCmd) Run(rt *runtime) error {
	if err := mailGuard(); err != nil {
		return err
	}
	if err := rt.checkMailFields(listKeys()); err != nil {
		return err
	}
	if err := checkMailLimit(c.Limit); err != nil {
		return err
	}
	return rt.read("mail unread", func(st *store.Store) (result, error) {
		if st == nil {
			res := &mailUnreadResult{Folders: []mailUnreadFolder{}, mailListResult: *rt.newMailList(nil, false, mailScope{})}
			res.Note = rt.emptyNote(nil, mailScope{}, false, time.Time{})
			return res, nil
		}
		sc, err := rt.mailScope(st, c.Folder)
		if err != nil {
			return nil, err
		}
		rows, err := st.MailList(rt.ctx, store.MailFilter{Account: rt.g.Account, Folder: c.Folder, Unread: true, Limit: c.Limit + 1})
		if err != nil {
			return nil, err
		}
		truncated := len(rows) > c.Limit
		if truncated {
			rows = rows[:c.Limit]
		}
		res := &mailUnreadResult{Folders: []mailUnreadFolder{}, mailListResult: *rt.newMailList(rows, truncated, sc)}
		var withUnread []mailUnreadFolder
		for _, f := range sc.folders {
			if f.Messages == 0 && f.Unread == 0 {
				continue
			}
			line := mailUnreadFolder{Account: f.Account, Folder: f.Name, Kind: f.Kind, Unread: f.Unread, Cached: f.Messages}
			res.Folders = append(res.Folders, line)
			res.UnreadTotal += f.Unread
			if f.Unread > 0 {
				withUnread = append(withUnread, line)
			}
		}
		switch {
		case len(sc.coverage) == 0:
			res.Note = rt.emptyNote(st, sc, c.Folder != "", time.Time{})
		case len(withUnread) == 0:
			res.Note = "no unread mail in the cache; Outlook may show more"
		default:
			res.Note = unreadNote(withUnread)
		}
		return res, nil
	})
}

func (r *mailUnreadResult) renderMail(rt *runtime) {
	if len(r.Folders) > 0 {
		rows := make([][]string, len(r.Folders))
		for i, f := range r.Folders {
			rows[i] = []string{f.Folder, f.Kind, strconv.Itoa(f.Unread), strconv.Itoa(f.Cached)}
		}
		render.Table(rt.stdout, []string{"folder", "kind", "unread", "cached"}, rows, rt.color)
		if len(r.rows) > 0 {
			_, _ = fmt.Fprintln(rt.stdout)
		}
	}
	if len(r.rows) > 0 || len(r.Folders) == 0 {
		r.messages(rt)
	}
	r.tail(rt)
}

var dateOnly = regexp.MustCompile(`^\d{4}-\d\d-\d\d$`)

// maxMailLimit is the most messages any mail command returns.
const maxMailLimit = 1000

// checkMailLimit is checkLimit plus the cap every mail command has.
func checkMailLimit(n int) error {
	if err := checkLimit(n); err != nil {
		return err
	}
	if n > maxMailLimit {
		c := errs.Usage(fmt.Sprintf("--limit %d is more than mail returns at once", n))
		c.Fix = fmt.Sprintf("Use --limit %d or less, and narrow the list with --since, --until, --from or --folder.", maxMailLimit)
		return c
	}
	return nil
}
