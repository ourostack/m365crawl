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
	if mailSupported() {
		return nil
	}
	return &errs.Coded{Code: CodeMailUnsupportedPlatform, Exit: errs.ExitEnvironment,
		Message: "mail is not supported on this operating system",
		Fix:     "Read the archive on Windows, macOS or Linux."}
}

// mailGroup is the mail command group.
type mailGroup struct {
	List    mailListCmd    `cmd:"" help:"List archived Outlook mail, newest first."`
	Show    mailShowCmd    `cmd:"" help:"Show one message: headers, recipients, body text and each attachment's name, size and type."`
	Thread  mailThreadCmd  `cmd:"" help:"Show a message's conversation: conversation identity first, then reply chain, then shared subject and participant."`
	Folders mailFoldersCmd `cmd:"" help:"List mail folders with message and unread counts and how far back the cache reaches."`
	Unread  mailUnreadCmd  `cmd:"" help:"Unread mail by folder."`
}

const mailCommon = `Mail comes from new Outlook's local cache: HxStore on Mac, OWA IndexedDB on Windows. It holds only cached mail, not server totals. Explicit copied supported sources also work on Linux. Every result says when mail was last read (synced_at) and, as a list, how far back the cache covers and when Outlook last wrote it (cache_fresh_at): mail Outlook received after that is not in the archive, and source_health reports a cache Outlook stopped writing (state stale).
Items also retain their acquisition provenance: desktop_cache, workiq_server or both. This is not a content winner or a freshness claim. deleted_at is an effective explicit deletion mark, not inferred cache absence. For an account with retained server mail, coverage[].count counts canonical rows of all provenances, excluding inferred gone/evicted rows. count_basis is canonical_archive and cache_count retains the last native census count when known. The coverage range and synced_at still describe native cache reads; a server-only folder has null native range/read clocks. These counts are not server totals.
When --since reaches before the cached mail of a folder being read (each folder judged on its own), or a thread reply's parent is not archived, workiq_routes lists the WorkIQ commands that fetch older server mail; the read never starts WorkIQ.
--account takes the mail account as printed in the ids, outlook/<profile>.
A field the archive does not hold is null: subject, sender, in_reply_to, ical_uid and internet_message_id when Outlook stored none, is_read when the read state is not known, sent_at when the store has no send time, address on a recipient with no address, gone_at and evicted_at while the message is present.
Recipients: Mac To and Cc are not yet told apart: role is null and kind_raw is the store's own number. Windows preserves To/Cc/Bcc order with role to/cc/bcc and kind_raw=0; ReplyTo is not inferred. conversation_id is null when absent; threads use a shared conversation identity before reply_chain and subject fallback.
Windows: to_me is false, in_reply_to is null, attachments are metadata only (downloaded=false; missing size=0). Unknown HasAttachments stays unavailable in decoding and is not inferred from attachment metadata. Cached HTML can be truncated at source. Each content mailbox of a profile is read into its own account once its identity is proved; ambiguous or unsupported layouts are coded refusals. Cached OWA calendar events are read independently from mail; ` + "`calendar sources`" + ` reports their read state. Calendar absence does not establish deletion, and an unverified sign-in identity never creates an automatic link.
Mac importance is likely right (low, normal, high) but is read from one store field that is not fully confirmed.
Folder copies: Outlook for Mac can rebuild an account in its store with a new set of folders and new ids for the same messages; the archive keeps the older sets. A message from an older set that the current set holds in any folder (Sent Items only with Sent Items), or a newer older set in the same kind of folder (same Message-ID), is listed and counted once, from the newer copy; mail list --include-copies also lists the older copies, with copy_of naming the newer one. older_copy is true for a message from an older set, and current is false for an older set's folder in coverage, mail folders and mail unread: no sync reads it any more, so its range stopped at its last read.
Dates: every mail message has dated_at, the time --since and --until select it by and lists sort it by: sent_at in Sent Items (Outlook's stored received time on a sent message is often when the draft was started, or a later day), received_at in every other folder, Drafts included. When copies of a message in older folder sets disagree on received time by more than a minute, received_at_disagrees is true and dated_at uses the earliest copy's received time at or after sent_at (15 minutes of clock skew allowed), or the listed row's own when none is; received_at stays the row's own.
An id is account:detail_key, as printed by mail list.`

// Help is the long help of the mail group, shared by every mail command.
func (mailGroup) Help() string {
	return mailCommon
}

const mailSeeGroup = "What every mail command shares (null fields, ids, --account, recipients, importance) is in `m365crawl mail --help`."

// Help is the long help of mail list.
func (mailListCmd) Help() string {
	return mailSeeGroup + "\nmail list shows recipient_count and the first three recipient names (recipients_preview); mail show lists everyone. Messages are newest first by dated_at: when they were sent in Sent Items, when they were received elsewhere. --since and --until select on dated_at and take YYYY-MM-DD, RFC3339 or a relative age such as 7d; a date alone as --until includes that whole day, a time is exclusive. --unread also reports how many messages the cache holds in the folder, because Outlook may show more. With --since or --until, folder_copies compares, for Inbox and Sent Items when they have older copies, the current copy with the older ones over that range, counting each message once: state partial means an older copy holds messages dated inside the current cache's range that the current set does not hold anywhere, older_copy_only means only an older copy reaches the range, and consistent means neither; dated_by names the clock that selected its messages (sent_at for sent, received_at for inbox), while the current cache's range (current_oldest_at) is a received time. older_copies_marked counts older-copy messages the list shows only with --include-gone --include-evicted."
}

// Help is the long help of mail show.
func (mailShowCmd) Help() string {
	return mailSeeGroup + "\nmail show lists every recipient with role (null on Mac) and kind_raw, and every attachment with its name, size and content type. body_text is read from the cached body; body_state says when it is missing or unreadable. --max-text cuts body_text and sets text_truncated."
}

// Help is the long help of mail thread.
func (mailThreadCmd) Help() string {
	return mailSeeGroup + "\nThe grouping key says how the thread was found: conversation groups at least two messages with the same account-scoped conversation_id; otherwise reply_chain follows In-Reply-To links in both directions; subject groups messages with the same subject (ignoring re:, fw: and the like) that share a sender or recipient address with the message. participants lists everyone in the thread once."
}

// Help is the long help of mail folders.
func (mailFoldersCmd) Help() string {
	return mailSeeGroup + "\nmessages and unread count what the archive holds in the folder, leaving out messages that are gone or evicted. oldest_at is the oldest message the cache held at the last read. current is false for a folder of an older folder set; its counts leave out messages a newer copy holds."
}

// Help is the long help of mail unread.
func (mailUnreadCmd) Help() string {
	return mailSeeGroup + "\nunread is the unread count of each folder against cached, the messages the archive holds there; the cache can hold fewer than Outlook shows, and the note says so. items are the newest unread messages (--limit changes how many, default 20)."
}

// ---- items ----

type mailRecipientOut struct {
	Name    *string `json:"name"`
	Address *string `json:"address"`
	Role    *string `json:"role"`
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

// mailFolderRefOut is one folder a message is in.
type mailFolderRefOut struct {
	Folder     *string `json:"folder"`
	FolderKind string  `json:"folder_kind"`
}

// foldersOut is every folder a message has a copy in (store.MailRow.Folders).
func foldersOut(r store.MailRow) []mailFolderRefOut {
	out := make([]mailFolderRefOut, len(r.Folders))
	for i, f := range r.Folders {
		out[i] = mailFolderRefOut{Folder: nz(f.Name), FolderKind: f.Kind}
	}
	return out
}

// mailListItem is one message in a list.
type mailListItem struct {
	Provenance        string             `json:"provenance"`
	ID                string             `json:"id"`
	Account           string             `json:"account"`
	Folder            *string            `json:"folder"`
	FolderKind        *string            `json:"folder_kind"`
	Folders           []mailFolderRefOut `json:"folders"`
	ToMe              bool               `json:"to_me"`
	Subject           *string            `json:"subject"`
	FromName          *string            `json:"from_name"`
	FromAddress       *string            `json:"from_address"`
	RecipientCount    int                `json:"recipient_count"`
	RecipientsPreview []string           `json:"recipients_preview"`
	InReplyTo         *string            `json:"in_reply_to"`
	ConversationID    *string            `json:"conversation_id"`
	ReceivedAt        *time.Time         `json:"received_at"`
	SentAt            *time.Time         `json:"sent_at"`
	DatedAt           *time.Time         `json:"dated_at"`
	IsRead            *bool              `json:"is_read"`
	Flag              *string            `json:"flag"`
	Importance        *string            `json:"importance"`
	HasAttachments    bool               `json:"has_attachments"`
	Preview           *string            `json:"preview"`
	InternetMessageID *string            `json:"internet_message_id"`
	ICalUID           *string            `json:"ical_uid"`
	GoneAt            *time.Time         `json:"gone_at"`
	EvictedAt         *time.Time         `json:"evicted_at"`
	DeletedAt         *time.Time         `json:"deleted_at"`
	OlderCopy         bool               `json:"older_copy"`
	CopyOf            *string            `json:"copy_of"`
	// ReceivedAtDisagrees is true when copies of the message in other folder sets hold received
	// times more than a minute apart; dated_at then uses the one chosen (store.MailRow).
	ReceivedAtDisagrees bool `json:"received_at_disagrees"`
	TextTruncated       bool `json:"text_truncated,omitempty"`
}

// mailShowItem is one message in full.
type mailShowItem struct {
	Provenance          string              `json:"provenance"`
	ID                  string              `json:"id"`
	Account             string              `json:"account"`
	Folder              *string             `json:"folder"`
	FolderKind          *string             `json:"folder_kind"`
	Folders             []mailFolderRefOut  `json:"folders"`
	ToMe                bool                `json:"to_me"`
	Subject             *string             `json:"subject"`
	FromName            *string             `json:"from_name"`
	FromAddress         *string             `json:"from_address"`
	Recipients          []mailRecipientOut  `json:"recipients"`
	InReplyTo           *string             `json:"in_reply_to"`
	ConversationID      *string             `json:"conversation_id"`
	ReceivedAt          *time.Time          `json:"received_at"`
	SentAt              *time.Time          `json:"sent_at"`
	DatedAt             *time.Time          `json:"dated_at"`
	IsRead              *bool               `json:"is_read"`
	ReadState           *string             `json:"read_state"`
	Flag                *string             `json:"flag"`
	Importance          *string             `json:"importance"`
	HasAttachments      bool                `json:"has_attachments"`
	Attachments         []mailAttachmentOut `json:"attachments"`
	Preview             *string             `json:"preview"`
	BodyText            *string             `json:"body_text"`
	BodyState           *string             `json:"body_state"`
	InternetMessageID   *string             `json:"internet_message_id"`
	ICalUID             *string             `json:"ical_uid"`
	GoneAt              *time.Time          `json:"gone_at"`
	EvictedAt           *time.Time          `json:"evicted_at"`
	DeletedAt           *time.Time          `json:"deleted_at"`
	OlderCopy           bool                `json:"older_copy"`
	CopyOf              *string             `json:"copy_of"`
	ReceivedAtDisagrees bool                `json:"received_at_disagrees"`
	TextTruncated       bool                `json:"text_truncated,omitempty"`
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
	Current  bool       `json:"current"`
}

// mailUnreadFolder is one folder's line of mail unread.
type mailUnreadFolder struct {
	Account string `json:"account"`
	Folder  string `json:"folder"`
	Kind    string `json:"kind"`
	Unread  int    `json:"unread"`
	Cached  int    `json:"cached"`
	Current bool   `json:"current"`
}

type mailCoverageOut struct {
	Account    string     `json:"account"`
	Folder     string     `json:"folder"`
	Kind       string     `json:"kind"`
	OldestAt   *time.Time `json:"oldest_at"`
	NewestAt   *time.Time `json:"newest_at"`
	Count      int        `json:"count"`
	CountBasis string     `json:"count_basis,omitempty"`
	CacheCount *int       `json:"cache_count,omitempty"`
	Current    bool       `json:"current"`
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
		Provenance: r.Provenance, DeletedAt: tp(r.DeletedAt),
		ID: r.ID(), Account: r.Account, Folder: nz(r.Folder), FolderKind: nz(r.FolderKind), Folders: foldersOut(r), ToMe: r.ToMe,
		Subject: nz(r.Subject), FromName: nz(r.SenderName), FromAddress: nz(r.SenderAddress),
		RecipientCount: len(r.Recipients), RecipientsPreview: []string{}, InReplyTo: nz(r.InReplyTo), ConversationID: nz(r.ConversationID),
		ReceivedAt: tp(r.ReceivedAt), SentAt: tp(r.SentAt), DatedAt: tp(r.DatedAt), IsRead: r.IsRead, Flag: nz(r.Flag), Importance: nz(r.Importance),
		HasAttachments: r.HasAttachments, InternetMessageID: nz(r.InternetMessageID), ICalUID: nz(r.ICalUID),
		GoneAt: tp(r.GoneAt), EvictedAt: tp(r.EvictedAt), OlderCopy: r.OlderCopy, CopyOf: nz(r.CopyOf), ReceivedAtDisagrees: r.ReceivedAtDisagrees,
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
		Provenance: r.Provenance, DeletedAt: tp(r.DeletedAt),
		ID: r.ID(), Account: r.Account, Folder: nz(r.Folder), FolderKind: nz(r.FolderKind), Folders: foldersOut(r), ToMe: r.ToMe,
		Subject: nz(r.Subject), FromName: nz(r.SenderName), FromAddress: nz(r.SenderAddress),
		Recipients: []mailRecipientOut{}, InReplyTo: nz(r.InReplyTo), ConversationID: nz(r.ConversationID), ReceivedAt: tp(r.ReceivedAt), SentAt: tp(r.SentAt),
		DatedAt: tp(r.DatedAt), IsRead: r.IsRead, ReadState: nz(r.ReadState), Flag: nz(r.Flag), Importance: nz(r.Importance),
		HasAttachments: r.HasAttachments, Attachments: []mailAttachmentOut{}, BodyState: nz(r.BodyState),
		InternetMessageID: nz(r.InternetMessageID), ICalUID: nz(r.ICalUID), GoneAt: tp(r.GoneAt), EvictedAt: tp(r.EvictedAt),
		OlderCopy: r.OlderCopy, CopyOf: nz(r.CopyOf), ReceivedAtDisagrees: r.ReceivedAtDisagrees,
	}
	for _, rc := range r.Recipients {
		it.Recipients = append(it.Recipients, mailRecipientOut{Name: nz(rc.Name), Address: nz(rc.Address), Role: nz(rc.Role), KindRaw: rc.KindRaw})
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

// ---- scope: folders and coverage ----

type mailScope struct {
	all      []store.MailFolderRow // every folder of the account filter
	folders  []store.MailFolderRow // the folders --folder names (all when it is empty)
	coverage []store.CoverageRow   // the coverage of those folders
	syncedAt time.Time             // when mail was last read, over every folder of the account filter
	fresh    time.Time             // the earliest store write time behind the last reads of the account filter
	marked   bool                  // a sync has read the mail of the account filter (the outlook_mail_read marker)
}

// since is how far back the current cache reaches: the oldest message of the selected folders of
// the current folder sets, or of every selected folder when none of them is current.
func (sc mailScope) since() time.Time {
	current, older := sc.reach()
	if current.IsZero() {
		return older
	}
	return current
}

// reach is the oldest message of the selected current folders and of the selected older copies.
func (sc mailScope) reach() (current, older time.Time) {
	for _, c := range sc.coverage {
		switch {
		case c.OldestAt.IsZero():
		case c.Current && (current.IsZero() || c.OldestAt.Before(current)):
			current = c.OldestAt
		case !c.Current && (older.IsZero() || c.OldestAt.Before(older)):
			older = c.OldestAt
		}
	}
	return current, older
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
	if strings.EqualFold(name, "junk") {
		c.Fix = "Junk mail folders are not detected by kind, so no folder has kind junk. Pass the junk folder's name as `m365crawl mail folders` lists it, for example `--folder \"<name>\"`. The folders are: " + strings.Join(names, ", ") + "."
	}
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
	if sc.fresh, err = mailCacheFresh(st, rt.ctx, rt.g.Account); err != nil {
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
		out[i] = mailCoverageOut{Account: c.Account, Folder: c.Folder, Kind: c.Kind, OldestAt: tp(c.OldestAt), NewestAt: tp(c.NewestAt), Count: c.Count,
			CountBasis: c.CountBasis, CacheCount: c.CacheCount, Current: c.Current}
	}
	return out
}

// ---- notes ----

// mailReadAccounts is the test seam of the read markers.
var mailReadAccounts = (*store.Store).MailReadAccounts

// mailCacheFresh is the test seam of the mail's cache write time.
var mailCacheFresh = (*store.Store).MailCacheFresh

// mailProfileCount counts the Outlook profiles under root (the default root when empty); a test seam.
var mailProfileCount = func(rt *runtime) (int, *mailSourceError) {
	profiles, problem := rt.outlookProfiles()
	return len(profiles), problem
}

// emptyNote explains an empty result: which of the causes it is. filtered says the command had
// a filter beyond the account; since is --since.
func (rt *runtime) emptyNote(st *store.Store, sc mailScope, filtered bool, since time.Time) string {
	profileCount := 0
	if rt.outlookOn {
		var problem *mailSourceError
		profileCount, problem = mailProfileCount(rt)
		if problem != nil {
			return mailSourceNote(problem)
		}
	}
	switch {
	case st == nil:
		return "no archive yet: run m365crawl sync"
	case rt.g.Account != "" && len(sc.all) == 0:
		return "no mail is archived for account " + rt.g.Account + ": the ids that `m365crawl mail list` prints name the accounts that have mail"
	case len(sc.all) == 0 && !rt.outlookOn:
		return "mail is not read in this run: the Outlook source is off (--outlook-root none, or --teams-root without --outlook-root)"
	case len(sc.all) == 0 && profileCount == 0:
		return "no supported Outlook profile was found on this machine, so there is no mail to read"
	case len(sc.all) == 0 && sc.marked:
		return "the Outlook source is on and the last read found no mail folders: mail is turned off in Outlook for this account, or none is set up"
	case len(sc.all) == 0 && rt.synced != nil:
		return "no mail has been read, although this run synced: m365crawl status shows the mail state and m365crawl doctor says why"
	case len(sc.all) == 0:
		return "no mail has been read yet: run m365crawl sync"
	}
	if cut := sc.since(); filtered && !since.IsZero() && !cut.IsZero() && since.Before(cut) {
		return beforeCacheNote(since, cut, sc)
	}
	if filtered {
		return "no message matched the filters"
	}
	return "the mailbox is empty: the cache holds no messages"
}

// beforeCacheNote explains an empty result that reaches before the current cache. An older copy
// of a folder (from a folder set Outlook replaced, which no read uses any more) may reach further
// back; the note says how far, and whether that reaches the requested start.
func beforeCacheNote(since, cut time.Time, sc mailScope) string {
	day := func(t time.Time) string { return t.In(displayZone).Format("2006-01-02") }
	current, older := sc.reach()
	switch {
	case current.IsZero() || older.IsZero() || !older.Before(cut):
		return "no message matched, and the cache covers only since " + day(cut) + ": older mail is not in it"
	case !since.Before(older):
		return "no message matched; the current cache covers only since " + day(cut) + ", and only an older copy of the folder, from a folder set Outlook replaced and no sync reads any more, reaches back to " + day(older) + ": mail it did not hold from then is not in the archive"
	}
	return "no message matched; the current cache covers only since " + day(cut) + ", and an older copy of the folder, from a folder set Outlook replaced, reached back only to " + day(older) + ": older mail is not in it"
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
	Items     []any      `json:"items"`
	Count     int        `json:"count"`
	Truncated bool       `json:"truncated"`
	SyncedAt  *time.Time `json:"synced_at"`
	// CacheFreshAt is when Outlook last wrote the local cache behind synced_at (the earliest across
	// accounts): mail Outlook received after it cannot be in the archive.
	CacheFreshAt *time.Time          `json:"cache_fresh_at,omitempty"`
	Coverage     []mailCoverageOut   `json:"coverage"`
	FolderCopies []mailFolderCopyOut `json:"folder_copies,omitempty"`
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
	res := &mailListResult{Items: shape(rt, items), Truncated: truncated, SyncedAt: tp(sc.syncedAt), CacheFreshAt: tp(sc.fresh), Coverage: coverageOut(sc.coverage),
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
		rows[i] = []string{m.ID(), stamp(m.DatedAt), mailState(m), firstOf(m.SenderName, m.SenderAddress), oneLine(m.Subject), m.Folder}
	}
	rt.fitTable([]string{"id", "date", "state", "from", "subject", "folder"}, rows, 4, 0)
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
	if m.OlderCopy {
		s = append(s, "older copy")
	}
	return strings.Join(s, ",")
}

func (rt *runtime) readMail(label, account string, fn func(*store.Store) (result, error)) error {
	rt.healthAccount = account
	return rt.read(label, func(st *store.Store) (result, error) {
		res, err := fn(st)
		if err != nil {
			return nil, err
		}
		return res, rt.noteMailFailures(st, res, account)
	})
}

func (rt *runtime) noteMailFailures(st *store.Store, res result, account string) error {
	if st == nil {
		return nil
	}
	failures, err := st.MailFailures(rt.ctx, account)
	if err != nil {
		return err
	}
	for _, f := range failures {
		res.addNotice("Outlook mail for " + f.Account + " may be stale: " + f.Code + ": " + f.Message + ". " + f.Fix)
	}
	return nil
}

// ---- mail list ----

type mailListCmd struct {
	Folder         string `help:"Only this folder, by name (checked first) or kind: inbox, sent, drafts, archive, deleted, to_me, other." placeholder:"NAME|KIND"`
	From           string `help:"Only messages whose sender name or address contains this text, ignoring case." placeholder:"TEXT"`
	Since          string `help:"Only messages dated at or after this time (YYYY-MM-DD, RFC3339 or an age such as 7d): Sent Items by when they were sent, other folders by when they were received." placeholder:"DATE"`
	Until          string `help:"Only messages dated before this time (sent for Sent Items, received otherwise); a date alone includes that whole local day, a time is exclusive." placeholder:"DATE"`
	Unread         bool   `help:"Only unread messages."`
	Flagged        bool   `help:"Only flagged messages."`
	HasAttachments bool   `name:"has-attachments" help:"Only messages with attachments."`
	IncludeGone    bool   `name:"include-gone" help:"Also list messages a sync saw disappear."`
	IncludeEvicted bool   `name:"include-evicted" help:"Also list messages that fell out of the cache's covered range."`
	IncludeCopies  bool   `name:"include-copies" help:"Also list older copies of a message that a newer folder set holds too; copy_of names the newer one."`
	Limit          int    `default:"50" help:"Maximum messages to return; truncated says whether more exist." placeholder:"N"`
}

func (c *mailListCmd) Run(rt *runtime) error {
	if err := mailGuard(); err != nil {
		return err
	}
	if err := checkCommandFields(rt, "mail list", ""); err != nil {
		return err
	}
	if err := checkMailLimit(c.Limit); err != nil {
		return err
	}
	since, until, err := rt.window("--since", c.Since, "--until", c.Until)
	if err != nil {
		return err
	}
	return rt.readMail("mail list", rt.g.Account, func(st *store.Store) (result, error) {
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
			IncludeGone: c.IncludeGone, IncludeEvicted: c.IncludeEvicted, IncludeCopies: c.IncludeCopies, Limit: c.Limit + 1}
		rows, err := st.MailList(rt.ctx, f)
		if err != nil {
			return nil, err
		}
		truncated := len(rows) > c.Limit
		if truncated {
			rows = rows[:c.Limit]
		}
		res := rt.newMailList(rows, truncated, sc)
		if !since.IsZero() || !until.IsZero() {
			if err := rt.checkFolderCopies(st, res, sc, store.MailCopyRange{Since: since, Until: until, IncludeGone: c.IncludeGone, IncludeEvicted: c.IncludeEvicted}); err != nil {
				return nil, err
			}
		}
		if res.WorkIQRoutes, err = rt.sinceRoutes(st, sc.coverage, since); err != nil {
			return nil, err
		}
		if !since.IsZero() || !until.IsZero() {
			if err := rt.mailRangeCoverage(st, sc, since, until, res); err != nil {
				return nil, err
			}
		}
		var note []string
		if len(rows) == 0 {
			filtered := c.From != "" || !since.IsZero() || !until.IsZero() || c.Unread || c.Flagged || c.HasAttachments || c.Folder != ""
			note = append(note, rt.emptyNote(st, sc, filtered, since))
		}
		if c.Unread {
			var selected []store.MailFolderRow
			if c.Folder != "" {
				selected = sc.folders
			}
			n := unreadNote(unreadFolders(sc.all, selected, rows))
			if n == "" {
				n = "no unread mail in the cache; Outlook may show more"
			}
			note = append(note, n)
		}
		res.Note = strings.Join(note, "; ")
		return res, nil
	})
}

// unreadFolders is, for each folder the rows sit in, its unread and cached counts. With a
// --folder selection, a row sits in each selected folder it has a copy in (store.MailRow.Folders);
// without one, in the folder it is shown from.
func unreadFolders(all, selected []store.MailFolderRow, rows []store.MailRow) []mailUnreadFolder {
	var out []mailUnreadFolder
	seen := map[string]bool{}
	for _, r := range rows {
		keys := []uint32{r.FolderKey}
		if selected != nil {
			keys = nil
			for _, f := range r.Folders {
				for _, sf := range selected {
					if sf.Account == r.Account && sf.FolderKey == f.Key {
						keys = append(keys, f.Key)
					}
				}
			}
		}
		for _, k := range keys {
			key := r.Account + "\x00" + strconv.Itoa(int(k))
			if seen[key] {
				continue
			}
			seen[key] = true
			for _, f := range all {
				if f.Account == r.Account && f.FolderKey == k {
					out = append(out, mailUnreadFolder{Account: f.Account, Folder: f.Name, Kind: f.Kind, Unread: f.Unread, Cached: f.Messages, Current: f.Current})
				}
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
	if err := checkCommandFields(rt, "mail show", ""); err != nil {
		return err
	}
	account, key, err := parseMailID(c.ID)
	if err != nil {
		return err
	}
	if err := rt.checkIDAccount(account); err != nil {
		return err
	}
	return rt.readMail("mail show", account, func(st *store.Store) (result, error) {
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
	if it.ReceivedAtDisagrees {
		head["received"] = head["received"].(string) + " (copies disagree; dated " + stamp(derefTime(it.DatedAt)) + ")"
	}
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
			rows[i] = []string{sv(rc.Name), sv(rc.Address), sv(rc.Role), strconv.FormatUint(uint64(rc.KindRaw), 10)}
		}
		render.Table(w, []string{"recipient", "address", "role (unknown on Mac)", "kind_raw"}, rows, color)
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
	if err := checkCommandFields(rt, "mail thread", ""); err != nil {
		return err
	}
	account, key, err := parseMailID(c.ID)
	if err != nil {
		return err
	}
	if err := rt.checkIDAccount(account); err != nil {
		return err
	}
	return rt.readMail("mail thread", account, func(st *store.Store) (result, error) {
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
		if !truncated && threadReachesBack(rows) {
			r, ok, err := rt.mailRoute(st, account, routeParentNotArchived, "A reply in this thread answers a message this archive does not hold.")
			if err != nil {
				return nil, err
			}
			if ok {
				res.WorkIQRoutes = []workIQRoute{r}
			}
		}
		for _, p := range store.MailParticipants(rows) {
			res.Participants = append(res.Participants, mailPersonOut{Name: nz(p.Name), Address: nz(p.Address)})
		}
		return res, nil
	})
}

func (r *mailThreadResult) renderMail(rt *runtime) {
	how := "same subject and a shared participant"
	switch r.Grouping {
	case "reply_chain":
		how = "reply chain"
	case "conversation":
		how = "conversation identity"
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
	if err := checkCommandFields(rt, "mail folders", ""); err != nil {
		return err
	}
	return rt.readMail("mail folders", rt.g.Account, func(st *store.Store) (result, error) {
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
				OldestAt: tp(f.OldestAt), NewestAt: tp(f.NewestAt), ReadAt: tp(f.ReadAt), Current: f.Current}
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
	if err := checkCommandFields(rt, "mail unread", ""); err != nil {
		return err
	}
	if err := checkMailLimit(c.Limit); err != nil {
		return err
	}
	return rt.readMail("mail unread", rt.g.Account, func(st *store.Store) (result, error) {
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
		// Each unread message counts once, however many of the folders it is in.
		if res.UnreadTotal, err = st.MailCount(rt.ctx, store.MailFilter{Account: rt.g.Account, Folder: c.Folder, Unread: true}); err != nil {
			return nil, err
		}
		var withUnread []mailUnreadFolder
		for _, f := range sc.folders {
			if f.Messages == 0 && f.Unread == 0 {
				continue
			}
			line := mailUnreadFolder{Account: f.Account, Folder: f.Name, Kind: f.Kind, Unread: f.Unread, Cached: f.Messages, Current: f.Current}
			res.Folders = append(res.Folders, line)
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
