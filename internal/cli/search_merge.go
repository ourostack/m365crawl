package cli

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/store"
)

// Codes of search's own errors.
const (
	// CodeFlagSourceConflict is a flag that the chosen --source cannot use.
	CodeFlagSourceConflict  = "flag_source_conflict"
	searchCodeUnknownFolder = "unknown_folder"
)

// searchColumns are the columns of a text search result, whichever source an item came from. thread
// holds the arguments that read the item's thread: `m365crawl thread <conversation> <root>` for a
// chat message, `m365crawl mail thread <id>` (or mail show) for mail.
var searchColumns = []string{"at", "source", "where", "who", "thread", "text"}

const (
	searchChats       = "chats"
	searchMail        = "mail"
	searchTranscripts = "transcripts"
	searchNotes       = "notes"
	searchDocuments   = "documents"
	searchLoop        = "loop"
	searchContent     = "content"
)

type searchCloudItem struct {
	Source string `json:"source"`
	cloudItem
}
type searchLocalTextItem struct {
	Source string `json:"source"`
	LocalTextItem
}

type searchNoteItem struct {
	Source string `json:"source"`
	noteItem
}

type searchDocumentItem struct {
	Source string `json:"source"`
	documentItem
}

type searchLoopItem struct {
	Source string `json:"source"`
	loopChannelItem
}

// searchChatItem is a Teams message in a search result: the message as every Teams command prints
// it, plus the source it came from.
type searchChatItem struct {
	Source string `json:"source"`
	messageItem
}

// searchMailItem is a mail message in a search result: the item of mail list, plus the source.
type searchMailItem struct {
	Source              string     `json:"source"`
	Provenance          string     `json:"provenance"`
	ID                  string     `json:"id"`
	Account             string     `json:"account"`
	Folder              *string    `json:"folder"`
	FolderKind          *string    `json:"folder_kind"`
	ToMe                bool       `json:"to_me"`
	Subject             *string    `json:"subject"`
	FromName            *string    `json:"from_name"`
	FromAddress         *string    `json:"from_address"`
	RecipientCount      int        `json:"recipient_count"`
	RecipientsPreview   []string   `json:"recipients_preview"`
	InReplyTo           *string    `json:"in_reply_to"`
	ConversationID      *string    `json:"conversation_id"`
	ReceivedAt          *time.Time `json:"received_at"`
	SentAt              *time.Time `json:"sent_at"`
	DatedAt             *time.Time `json:"dated_at"`
	IsRead              *bool      `json:"is_read"`
	Flag                *string    `json:"flag"`
	Importance          *string    `json:"importance"`
	HasAttachments      bool       `json:"has_attachments"`
	Preview             *string    `json:"preview"`
	InternetMessageID   *string    `json:"internet_message_id"`
	ICalUID             *string    `json:"ical_uid"`
	GoneAt              *time.Time `json:"gone_at"`
	EvictedAt           *time.Time `json:"evicted_at"`
	DeletedAt           *time.Time `json:"deleted_at"`
	OlderCopy           bool       `json:"older_copy"`
	CopyOf              *string    `json:"copy_of"`
	ReceivedAtDisagrees bool       `json:"received_at_disagrees"`
	TextTruncated       bool       `json:"text_truncated,omitempty"`
}

// searchTranscriptItem is one part of a recorded meeting whose fetched transcript matched: the
// first matching entry, and how many of the part's entries matched. at is the entry's absolute
// time; fetched_at says when the part's text was fetched. Transcripts are read from the archive.
type searchTranscriptItem struct {
	Source        string     `json:"source"`
	CallID        string     `json:"call_id"`
	EventKey      *string    `json:"event_key"`
	Title         *string    `json:"title"`
	Ordinal       int        `json:"ordinal"`
	Speaker       *string    `json:"speaker"`
	At            *time.Time `json:"at"`
	Text          string     `json:"text"`
	Matches       int        `json:"matches"`
	FetchedAt     *time.Time `json:"fetched_at"`
	TextTruncated bool       `json:"text_truncated,omitempty"`
}

func searchTranscriptItemOf(h store.TranscriptHit, maxText int) searchTranscriptItem {
	it := searchTranscriptItem{Source: searchTranscripts, CallID: h.CallID, EventKey: searchNZ(h.EventKey), Title: searchNZ(h.Title), Ordinal: h.Ordinal,
		Speaker: searchNZ(h.Speaker), At: searchTime(h.At), Text: h.Text, Matches: h.Matches, FetchedAt: h.FetchedAt}
	if t, did := truncateRunes(it.Text, maxText); did {
		it.Text, it.TextTruncated = t, true
	}
	return it
}

// sourceCount is what one source contributed to a search: the items of the result that came from
// it (returned; count is the same number under its older name), how many it matched before the
// limit, and whether it had more than the result holds. matched is exact. A source that stops
// reading at the limit cannot give an exact number: it reports matched_at_least instead, the hits
// it read, and never an invented total. A source that was not searched (no archive yet) reports
// neither.
type sourceCount struct {
	Count          int  `json:"count"`
	Returned       int  `json:"returned"`
	Matched        *int `json:"matched,omitempty"`
	MatchedAtLeast *int `json:"matched_at_least,omitempty"`
	Truncated      bool `json:"truncated"`
}

// newSourceCount builds one source's counts. matched is exact unless atLeast is set, and absent
// when the source was not searched.
func newSourceCount(returned, matched int, atLeast, truncated, searched bool) *sourceCount {
	out := &sourceCount{Count: returned, Returned: returned, Truncated: truncated}
	if !searched {
		return out
	}
	if atLeast {
		out.MatchedAtLeast = &matched
	} else {
		out.Matched = &matched
	}
	return out
}

// hints are the text lines that say which sources had hits the result does not show, in the order
// the sources are listed, and what shows them: --source for a source other than the one selected,
// and a higher --limit when the source alone matched more than the limit.
func (s *searchSources) hints() []string {
	if s == nil {
		return nil
	}
	var out []string
	for _, src := range []struct {
		count     *sourceCount
		one, many string
		flag      string
	}{
		{s.Chats, "chat hit", "chat hits", searchChats}, {s.Mail, "mail hit", "mail hits", searchMail},
		{s.Transcripts, "transcript hit", "transcript hits", searchTranscripts}, {s.Notes, "note hit", "note hits", searchNotes},
		{s.Documents, "document hit", "document hits", searchDocuments}, {s.Loop, "Loop hit", "Loop hits", searchLoop},
		{s.Content, "content hit", "content hits", searchContent},
	} {
		if src.count == nil {
			continue
		}
		lead, matched, beyond := "", src.count.Matched, false
		if matched == nil {
			lead, matched, beyond = "at least ", src.count.MatchedAtLeast, true // it stopped reading at the limit
		}
		if matched == nil { // not searched
			continue
		}
		hidden := *matched - src.count.Returned
		if hidden <= 0 {
			continue
		}
		advice := "use --source " + src.flag
		switch beyond = beyond || *matched > s.limit; {
		case s.selected == src.flag:
			advice = "raise --limit"
		case beyond:
			advice += " and raise --limit"
		}
		out = append(out, lead+plural(hidden, src.one, src.many)+" not shown; "+advice)
	}
	return out
}

// floorNote is the text line that names the sources whose match count is a floor because they
// stopped reading at the limit; empty when every count is exact.
func (s *searchSources) floorNote() string {
	if s == nil {
		return ""
	}
	var names []string
	for _, src := range []struct {
		count *sourceCount
		name  string
	}{{s.Chats, searchChats}, {s.Mail, searchMail}, {s.Transcripts, searchTranscripts}, {s.Notes, searchNotes}, {s.Documents, searchDocuments}, {s.Loop, searchLoop}, {s.Content, searchContent}} {
		if src.count != nil && src.count.MatchedAtLeast != nil {
			names = append(names, src.name)
		}
	}
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0] + " stopped reading at --limit, so its match count (matched_at_least in JSON) is a floor, not a total; raise --limit to read further"
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1] +
		" stopped reading at --limit, so their match counts (matched_at_least in JSON) are floors, not totals; raise --limit to read further"
}

// searchSources is the per-source part of a search result. A source that was not searched is absent.
type searchSources struct {
	Chats       *sourceCount `json:"chats,omitempty"`
	Mail        *sourceCount `json:"mail,omitempty"`
	Transcripts *sourceCount `json:"transcripts,omitempty"`
	Notes       *sourceCount `json:"notes,omitempty"`
	Documents   *sourceCount `json:"documents,omitempty"`
	Loop        *sourceCount `json:"loop,omitempty"`
	Content     *sourceCount `json:"content,omitempty"`
	// selected is the --source of the search and limit its --limit: what a hint may advise.
	selected string
	limit    int
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
		Provenance: r.Provenance, DeletedAt: searchTime(r.DeletedAt),
		Source: searchMail, ID: r.ID(), Account: r.Account, Folder: searchNZ(r.Folder), FolderKind: searchNZ(r.FolderKind), ToMe: r.ToMe,
		Subject: searchNZ(r.Subject), FromName: searchNZ(r.SenderName), FromAddress: searchNZ(r.SenderAddress),
		RecipientCount: len(r.Recipients), RecipientsPreview: []string{}, InReplyTo: searchNZ(r.InReplyTo), ConversationID: searchNZ(r.ConversationID),
		ReceivedAt: searchTime(r.ReceivedAt), SentAt: searchTime(r.SentAt), DatedAt: searchTime(r.DatedAt), IsRead: r.IsRead, Flag: searchNZ(r.Flag), Importance: searchNZ(r.Importance),
		HasAttachments: r.HasAttachments, InternetMessageID: searchNZ(r.InternetMessageID), ICalUID: searchNZ(r.ICalUID),
		GoneAt: searchTime(r.GoneAt), EvictedAt: searchTime(r.EvictedAt), OlderCopy: r.OlderCopy, CopyOf: searchNZ(r.CopyOf), ReceivedAtDisagrees: r.ReceivedAtDisagrees,
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

// searchKeys are the --fields keys a search accepts: source, then the keys of a Teams message, of a
// mail message and of a transcript hit. An item carries only the keys its own source has.
func searchKeys() []string {
	keys := []string{"source"}
	all := append(jsonKeys(reflect.TypeFor[messageItem]()), jsonKeys(reflect.TypeFor[searchMailItem]())...)
	all = append(all, jsonKeys(reflect.TypeFor[searchTranscriptItem]())...)
	for _, k := range append(all, typeKeys[noteItem]()...) {
		if !slices.Contains(keys, k) {
			keys = append(keys, k)
		}
		for _, key := range typeKeys[documentItem]() {
			if !slices.Contains(keys, key) {
				keys = append(keys, key)
			}
		}
	}
	for _, key := range typeKeys[loopChannelItem]() {
		if !slices.Contains(keys, key) {
			keys = append(keys, key)
		}
		for _, key := range typeKeys[cloudItem]() {
			if !slices.Contains(keys, key) {
				keys = append(keys, key)
			}
			for _, key := range typeKeys[LocalTextItem]() {
				if !slices.Contains(keys, key) {
					keys = append(keys, key)
				}
			}
		}
	}
	return keys
}

// searchPlan says which sources a search reads and what it tells the reader about the ones it skips.
type searchPlan struct {
	chats, mail, transcripts bool
	indexedNotes             bool
	indexedDocuments         bool
	loop                     bool
	content                  bool
	notes                    []string
	noteExclusion            string
	documentExclusion        string
	loopExclusion            string
	contentExclusion         string
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

// searchesTranscripts is the end of a conflict message about --source transcripts.
const searchesTranscripts = "; --source transcripts searches meeting transcripts"

func sourceConflict(msg string) error {
	return &errs.Coded{Code: CodeFlagSourceConflict, Exit: errs.ExitUsage, Message: msg,
		Fix: "Drop the flag, or pick the --source it belongs to (chats, mail or all)."}
}

// plan decides which sources to read. A flag that one source cannot use is a usage error when
// --source names the other source, and narrows the search to its own source, with a note, when
// --source is left at all. Mail is skipped, with a note, on a platform that cannot read it.
func (c *searchCmd) plan(rt *runtime) (searchPlan, error) {
	p := searchPlan{chats: true, mail: true, transcripts: true}
	chatFlags := c.chatOnlyFlags()
	loopScoped := len(chatFlags) > 0 || c.Folder != "" || c.From != "" || c.Since != "" || c.Until != "" ||
		rt.g.Account != "" || rt.g.OutlookAccount != "" || rt.g.OutlookProfile != ""
	contentScoped := len(chatFlags) > 0 || c.Folder != "" || c.From != "" || rt.g.Account != "" || rt.g.OutlookAccount != "" || rt.g.OutlookProfile != ""
	if c.Source == searchContent {
		if contentScoped {
			return p, sourceConflict("--source content cannot use account/author, chat/mail-only or profile filters")
		}
		return searchPlan{content: true}, nil
	}
	if c.Source == searchLoop {
		if loopScoped {
			return p, sourceConflict("--source loop cannot use date, account/author, chat-only, mail-only or profile filters: cached Loop channels have no native date or resolved owner")
		}
		return searchPlan{loop: true}, nil
	}
	if c.Source == searchDocuments {
		if len(chatFlags) > 0 || c.Folder != "" || c.From != "" || rt.g.Account != "" ||
			rt.g.OutlookAccount != "" || rt.g.OutlookProfile != "" {
			return p, sourceConflict("--source documents cannot use account/author, chat-only or mail-only filters")
		}
		return searchPlan{indexedDocuments: true}, nil
	}
	if c.Source == searchNotes {
		if len(chatFlags) > 0 || c.Folder != "" || c.From != "" || rt.g.Account != "" {
			return p, sourceConflict("--source notes cannot use account, author, chat-only or mail-only filters")
		}
		return searchPlan{indexedNotes: true}, nil
	}
	mailAccount := isSearchMailAccount(rt.g.Account)
	switch {
	case mailAccount && c.Source == searchChats:
		return p, sourceConflict("--account " + rt.g.Account + " names a mail account; --source chats searches Teams chats")
	case mailAccount && c.Source == searchTranscripts:
		return p, sourceConflict("--account " + rt.g.Account + " names a mail account" + searchesTranscripts)
	case mailAccount && len(chatFlags) > 0:
		return p, sourceConflict("--account " + rt.g.Account + " names a mail account; " + strings.Join(chatFlags, ", ") + " applies to Teams chats only")
	case rt.g.Account != "" && !mailAccount && c.Source == searchMail:
		return p, sourceConflict("--account " + rt.g.Account + " names a Teams account; --source mail searches mail")
	case rt.g.Account != "" && !mailAccount && c.Folder != "":
		return p, sourceConflict("--account " + rt.g.Account + " names a Teams account; --folder applies to mail only")
	case mailAccount && c.Source == "all":
		p.chats, p.transcripts = false, false
		p.notes = append(p.notes, "--account names a mail account; Teams chats and meeting transcripts were not searched")
	case c.Source == searchChats && c.Folder != "":
		return p, sourceConflict("--folder applies to mail only; --source chats searches Teams chats")
	case c.Source == searchMail && len(chatFlags) > 0:
		return p, sourceConflict(strings.Join(chatFlags, ", ") + " applies to Teams chats only; --source mail searches mail")
	case c.Source == searchTranscripts && (len(chatFlags) > 0 || c.Folder != ""):
		var why []string
		if len(chatFlags) > 0 {
			why = append(why, appliesTo(chatFlags, "Teams chats"))
		}
		if c.Folder != "" {
			why = append(why, "--folder applies to mail only")
		}
		return p, sourceConflict(strings.Join(why, " and ") + searchesTranscripts)
	case c.Source == searchChats:
		p.mail, p.transcripts = false, false
	case c.Source == searchMail:
		p.chats, p.transcripts = false, false
	case c.Source == searchTranscripts:
		p.chats, p.mail = false, false
	case len(chatFlags) > 0 && c.Folder != "":
		return p, sourceConflict("--folder applies to mail only and " + strings.Join(chatFlags, ", ") + " applies to Teams chats only; no source can use both")
	case len(chatFlags) > 0:
		p.mail, p.transcripts = false, false
		p.notes = append(p.notes, appliesTo(chatFlags, "Teams chats")+"; mail and meeting transcripts were not searched")
	case c.Folder != "":
		p.chats, p.transcripts = false, false
		p.notes = append(p.notes, "--folder applies to mail only; Teams chats and meeting transcripts were not searched")
	}
	if p.mail && !mailSupported() {
		if !p.chats && !p.transcripts {
			return p, mailGuard()
		}
		p.mail = false
		p.notes = append(p.notes, "mail is not supported on this operating system; searched Teams chats and meeting transcripts only")
	}
	if c.Source == "all" {
		if contentScoped {
			p.contentExclusion = "cloud content was not searched: account/author, chat/mail-only or profile filters cannot qualify unresolved native service identities; local Office content was not searched because these filters cannot qualify unresolved file ownership"
		} else {
			p.content = true
		}
		if len(chatFlags) == 0 && c.Folder == "" && rt.g.Account == "" && c.From == "" {
			p.indexedNotes = true
		} else {
			p.noteExclusion = "indexed notes were not searched: account/author or chat/mail-only filters do not apply to unresolved note ownership"
		}
		if len(chatFlags) == 0 && c.Folder == "" && rt.g.Account == "" && c.From == "" &&
			rt.g.OutlookAccount == "" && rt.g.OutlookProfile == "" {
			p.indexedDocuments = true
		} else {
			p.documentExclusion = "document metadata was not searched: account/author or chat/mail-only filters cannot qualify unresolved documents"
		}
		if loopScoped {
			p.loopExclusion = "Loop channels were not searched: date, account/author, chat/mail-only and profile filters cannot qualify cached Loop channels, which have no native date or resolved owner"
		} else {
			p.loop = true
		}
	}
	return p, nil
}

// isSearchMailAccount says whether an --account value names an Outlook mail account (outlook/<profile>)
// rather than a Teams account (<tenantId>/<userId>).
func isSearchMailAccount(account string) bool { return strings.HasPrefix(account, "outlook/") }

// searchHit is one result item with the time it is ordered by.
type searchHit struct {
	at                                time.Time
	item                              any
	source                            string
	app                               store.MessageApplicationEnvelope // a chat hit's evidence, counted only if the hit survives the limit
	cloudLabel                        string
	cloudSourceID, cloudObservationID int64
	localText                         bool
	engage, engageKnown               bool
	engageSessionID                   int64
	engageOrdinal                     int
}

func sortSearchHits(hits []searchHit) {
	sort.SliceStable(hits, func(i, j int) bool {
		a, b := hits[i], hits[j]
		// Knownness is one global ordering tier, not an operand-family exception.
		// Engage has explicit native presence, including an observed Go-zero.
		// Legacy hits retain their zero-means-absent convention.
		ak, bk := !a.at.IsZero(), !b.at.IsZero()
		if a.engage {
			ak = a.engageKnown
		}
		if b.engage {
			bk = b.engageKnown
		}
		if ak != bk {
			return ak
		}
		if !a.at.Equal(b.at) {
			return a.at.After(b.at)
		}
		if a.engage != b.engage {
			return !a.engage
		}
		if a.source != searchContent && b.source != searchContent {
			return false
		}
		if a.source != searchContent {
			return true
		}
		if b.source != searchContent {
			return false
		}
		if a.localText != b.localText {
			return !a.localText
		}
		if a.cloudLabel != b.cloudLabel {
			return a.cloudLabel < b.cloudLabel
		}
		if a.cloudSourceID != b.cloudSourceID {
			return a.cloudSourceID < b.cloudSourceID
		}
		if a.engage && a.engageSessionID != b.engageSessionID {
			return a.engageSessionID < b.engageSessionID
		}
		if a.engage && a.engageOrdinal != b.engageOrdinal {
			return a.engageOrdinal < b.engageOrdinal
		}
		return a.cloudObservationID < b.cloudObservationID
	})
}

// search runs the plan against the archive and merges what each source found, newest first.
func (c *searchCmd) search(rt *runtime, p searchPlan, f store.Filter, st *store.Store) (result, error) {
	notes := slices.Clone(p.notes)
	searchMailNow := p.mail
	if p.mail {
		b, err := rt.mailStatus(st)
		if err != nil {
			return nil, err
		}
		has := false
		if st != nil {
			var err error
			if has, err = st.HasMail(rt.ctx); err != nil {
				return nil, err
			}
		}
		if !has {
			searchMailNow = false
			const syncForMail = "mail is not in the archive yet; run m365crawl sync"
			why := syncForMail
			if _, n := rt.whyNoMail(b); n != noMailYet {
				why = n // a sync would read no mail: say why instead
			}
			// With no archive and chats searched, the empty note already says to sync.
			if st != nil || !p.chats || why != syncForMail {
				notes = append(notes, why)
			}
		} else if b.SourceError != nil {
			notes = append(notes, mailSourceNote(b.SourceError))
		}
	}
	var hits []searchHit
	var chatTrunc, mailTrunc bool
	// matched is what each source matched before any limit; atLeast marks a source that stopped
	// reading at the limit, so its number is only a lower bound.
	matched, atLeast := map[string]int{}, map[string]bool{}
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
		if matched[searchChats] = len(rows); trunc {
			matched[searchChats] = *f.Total // the store counts exactly when it cuts
		}
		for i, it := range rt.withAppSummary(messageItems(rows, rt.g.MaxText, html, rt.archiveRef())) {
			hits = append(hits, searchHit{at: rows[i].SentAt, item: searchChatItem{Source: searchChats, messageItem: it}, source: searchChats, app: rows[i].Application})
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
		matched[searchMail], atLeast[searchMail] = len(rows), trunc
		if rt.g.Account != "" && !isSearchMailAccount(rt.g.Account) {
			notes = append(notes, "--account names a Teams account; mail was searched across all Outlook accounts")
		}
		for _, r := range rows {
			hits = append(hits, searchHit{at: r.DatedAt, item: searchMailItemOf(r, rt.g.MaxText), source: searchMail})
		}
	}
	tr, err := c.searchTranscripts(rt, p, f, st)
	if err != nil {
		return nil, err
	}
	hits = append(hits, tr.hits...)
	matched[searchTranscripts], atLeast[searchTranscripts] = len(tr.hits), tr.truncated
	if tr.note != "" {
		notes = append(notes, tr.note)
	}
	noteTruncated := false
	if p.indexedNotes && st != nil {
		rows, truncated, err := st.Notes(rt.ctx, store.NoteFilter{Query: c.Query, Since: f.Since, Until: f.Until, Limit: f.Limit})
		if err != nil {
			return nil, err
		}
		noteTruncated = truncated
		matched[searchNotes], atLeast[searchNotes] = len(rows), truncated
		for _, row := range rows {
			var at time.Time
			if row.ModifiedAt != nil {
				at = *row.ModifiedAt
			}
			hits = append(hits, searchHit{at: at, item: searchNoteItem{Source: searchNotes, noteItem: noteItemOf(row, rt.g.MaxText)}, source: searchNotes})
		}
	}
	documentTruncated, undated := false, ""
	if p.indexedDocuments && st != nil {
		snapshot, err := st.DocumentSnapshot(rt.ctx, store.DocumentFilter{Query: c.Query,
			ModifiedSince: f.Since, ModifiedUntil: f.Until, Limit: f.Limit})
		if err != nil {
			return nil, err
		}
		documentTruncated = snapshot.Truncated
		matched[searchDocuments], atLeast[searchDocuments] = len(snapshot.Rows), snapshot.Truncated
		for _, row := range snapshot.Rows {
			at := row.Document.Modified.Value
			if row.ModifiedAt != nil { // a usage row's modification time is its own column
				at = *row.ModifiedAt
			}
			hits = append(hits, searchHit{at: at, item: searchDocumentItem{
				Source: searchDocuments, documentItem: documentItemOf(row, rt.g.MaxText, rt.archiveRef())}, source: searchDocuments})
		}
		rt.documentCoverage = documentSnapshotCoverage(rt, snapshot.Sources)
		undated = undatedNotice(snapshot.Undated)
	}
	loopTruncated := false
	if p.loop && st != nil {
		snapshot, err := st.LoopSnapshot(rt.ctx, store.LoopFilter{Query: c.Query, Limit: f.Limit})
		if err != nil {
			return nil, err
		}
		loopTruncated = snapshot.Truncated
		matched[searchLoop] = snapshot.Matched
		for _, row := range snapshot.Rows {
			// No native time: Loop hits follow every dated hit, in the snapshot's path order.
			hits = append(hits, searchHit{item: searchLoopItem{Source: searchLoop, loopChannelItem: loopChannelItemOf(row, rt.g.MaxText)}, source: searchLoop})
		}
		rt.loopCoverage = loopSnapshotCoverage(rt, snapshot.Sources)
	}
	cloudTruncated := false
	cloudCoverage := rt.cloudCoverageOf(nil)
	localCoverage := rt.localCoverageOf(nil)
	engageCoverage := rt.engageCoverageOf(store.EngageSnapshot{})
	if p.content && st != nil {
		snapshot, err := st.CloudSnapshot(rt.ctx, store.CloudFilter{Query: c.Query, ModifiedSince: f.Since, ModifiedUntil: f.Until, Limit: f.Limit})
		if err != nil {
			return nil, rt.contentError(err)
		}
		cloudTruncated = snapshot.Truncated
		matched[searchContent] = len(snapshot.Rows)
		atLeast[searchContent] = snapshot.Truncated
		cloudCoverage = rt.cloudCoverageOf(snapshot.Sources)
		for _, row := range snapshot.Rows {
			at := time.Time{}
			if row.Result.File.ModifiedAt != nil {
				at = *row.Result.File.ModifiedAt
			}
			label := ""
			if row.Result.File.Title != nil {
				label = *row.Result.File.Title
			}
			hits = append(hits, searchHit{at: at, source: searchContent, cloudLabel: label, cloudSourceID: row.SourceID, cloudObservationID: row.ID,
				item: searchCloudItem{Source: searchContent, cloudItem: cloudItemOf(row, cloudSourceFor(snapshot.Sources, row.SourceID), rt.g.MaxText)}})
		}
		local, err := st.LocalTextSnapshot(rt.ctx, store.LocalTextFilter{Query: c.Query, ModifiedSince: f.Since, ModifiedUntil: f.Until, Limit: f.Limit})
		if err != nil {
			return nil, rt.contentError(err)
		}
		cloudTruncated = cloudTruncated || local.Truncated
		matched[searchContent] += len(local.Rows)
		atLeast[searchContent] = atLeast[searchContent] || local.Truncated
		localCoverage = rt.localCoverageOf(local.Sources)
		for _, row := range local.Rows {
			at := time.Time{}
			if row.Snapshot.Evidence.ModifiedAt != nil {
				at = *row.Snapshot.Evidence.ModifiedAt
			}
			src := localSourceFor(local.Sources, row.SourceID)
			hits = append(hits, searchHit{at: at, source: searchContent, localText: true, cloudLabel: src.Path, cloudSourceID: row.SourceID, cloudObservationID: row.ID,
				item: searchLocalTextItem{Source: searchContent, LocalTextItem: localItemOf(row, src, rt.g.MaxText)}})
		}
		engage, err := st.EngageSnapshot(rt.ctx, store.EngageFilter{Query: c.Query, ModifiedSince: engageBounds(c.Since, f.Since), ModifiedUntil: engageBounds(c.Until, f.Until), Limit: f.Limit})
		if err != nil {
			return nil, rt.contentError(err)
		}
		engageCoverage = rt.engageCoverageOf(engage)
		cloudTruncated = cloudTruncated || engage.Truncated
		matched[searchContent] += len(engage.Rows)
		atLeast[searchContent] = atLeast[searchContent] || engage.Truncated
		for _, row := range engage.Rows {
			hits = append(hits, engageHit(rt, row, engage.Sources, true))
		}
	}
	sortSearchHits(hits)
	trunc := map[string]bool{searchChats: chatTrunc, searchMail: mailTrunc, searchTranscripts: tr.truncated, searchNotes: noteTruncated, searchDocuments: documentTruncated, searchLoop: loopTruncated}
	// The chats' total is the result's only while no other source gave or held back an item.
	trunc[searchContent] = cloudTruncated
	chatTotalIsAll := !p.indexedNotes && !p.indexedDocuments && !p.loop && matched[searchContent] == 0 && !cloudTruncated && !searchMailNow && len(tr.hits) == 0 && !tr.truncated
	truncated := chatTrunc || mailTrunc || tr.truncated || noteTruncated || documentTruncated || loopTruncated || cloudTruncated
	if len(hits) > f.Limit {
		truncated = true
		for _, h := range hits[f.Limit:] {
			trunc[h.source] = true
		}
		hits = hits[:f.Limit]
	}
	count := map[string]int{}
	out := make([]any, len(hits))
	envs := []store.MessageApplicationEnvelope{}
	for i, h := range hits {
		out[i] = h.item
		count[h.source]++
		if h.source == searchChats {
			envs = append(envs, h.app)
		}
	}
	res := newList(shape(rt, out), truncated)
	if p.chats && st != nil {
		res.ApplicationEvidenceCoverage = applicationCoverageOf(envs)
		if res.WorkIQRoutes, err = rt.chatRoutes(st, f.Since); err != nil {
			return nil, err
		}
	}
	if searchMailNow && !f.Since.IsZero() {
		coverage, err := mailCoverageOf(st, rt.ctx)
		if err != nil {
			return nil, err
		}
		var own []store.CoverageRow
		for _, c := range coverage {
			if account := mailAccountOf(rt.g.Account); account == "" || c.Account == account {
				own = append(own, c)
			}
		}
		if c.Folder != "" {
			own = matchCoverage(own, c.Folder)
		}
		routes, err := rt.sinceRoutes(st, own, f.Since)
		if err != nil {
			return nil, err
		}
		res.WorkIQRoutes = append(res.WorkIQRoutes, routes...)
	}
	if st != nil && (p.chats || p.mail) && (!f.Since.IsZero() || !f.Until.IsZero()) {
		if err := rt.searchRangeCoverage(st, p, f, c.Folder, &res.meta); err != nil {
			return nil, err
		}
	}
	if p.noteExclusion != "" {
		res.addNotice(p.noteExclusion)
	}
	if p.documentExclusion != "" {
		res.addNotice(p.documentExclusion)
	}
	if p.loopExclusion != "" {
		res.addNotice(p.loopExclusion)
	}
	if p.contentExclusion != "" {
		res.addNotice(p.contentExclusion)
		res.addNotice("Engage Home-session observations were not searched: these filters do not qualify native service accounts or sender identities")
	}
	if undated != "" {
		res.addNotice(undated)
	}
	res.Sources = &searchSources{selected: c.Source, limit: f.Limit}
	sourceOf := func(name string) *sourceCount {
		return newSourceCount(count[name], matched[name], atLeast[name], trunc[name], st != nil)
	}
	if p.chats {
		res.Sources.Chats = sourceOf(searchChats)
	}
	if searchMailNow {
		res.Sources.Mail = sourceOf(searchMail)
	}
	if tr.searched {
		res.Sources.Transcripts = sourceOf(searchTranscripts)
	}
	if p.indexedNotes {
		res.Sources.Notes = sourceOf(searchNotes)
		res.SourceCoverage = rt.noteCoverage
	}
	if p.indexedDocuments {
		res.Sources.Documents = sourceOf(searchDocuments)
		res.SourceCoverage = mergedOptionalCoverage(res.SourceCoverage, rt.documentCoverage)
	}
	if p.loop {
		res.Sources.Loop = sourceOf(searchLoop)
		res.LoopCoverage = rt.loopCoverage.compact(rt)
	}
	if p.content {
		res.Sources.Content = sourceOf(searchContent)
		if c.Source == searchContent || len(cloudCoverage.Sources) > 0 {
			res.ContentCoverage = cloudCoverage
		}
		if c.Source == searchContent || len(localCoverage.Sources) > 0 {
			res.LocalContentCoverage = localCoverage
		}
		if c.Source == searchContent || len(engageCoverage.Registrations) > 0 {
			res.EngageContentCoverage = engageCoverage
		}
	}
	if chatTotalIsAll {
		res.withTotal(*f.Total)
	}
	if len(hits) == 0 {
		cause, err := rt.searchEmptyNote(st, p.chats, searchMailNow, tr.hasText)
		if err != nil {
			return nil, err
		}
		if cause != "" {
			notes = append(notes, cause)
		}
		if p.indexedNotes && rt.noteCoverage != nil && len(rt.noteCoverage.Sources) > 0 {
			notes = append(notes, "no indexed note matched; source_coverage describes captured input")
		}
		if p.indexedDocuments && (c.Source == searchDocuments ||
			(rt.documentCoverage != nil && len(rt.documentCoverage.Sources) > 0)) {
			notes = append(notes, "no document metadata matched; inspect docs sources or register with docs sync")
		}
		if p.loop && (c.Source == searchLoop || (rt.loopCoverage != nil && len(rt.loopCoverage.items) > 0)) {
			notes = append(notes, "no Loop channel matched; inspect loop sources or register with loop sync")
		}
	}
	res.Note = strings.Join(notes, "; ")
	return res, nil
}

// transcriptSearch is what the transcripts gave a search: their hits, whether more matched,
// whether they were searched at all (the archive has the transcript tables), whether the archive
// holds the text of any transcript, and a note when --source transcripts found nothing to search.
type transcriptSearch struct {
	hits                         []searchHit
	truncated, searched, hasText bool
	note                         string
}

// noTranscriptText is the note of a transcript search in an archive with no fetched transcript.
const noTranscriptText = "no transcripts are fetched yet; run m365crawl transcripts to see what can be fetched"

// searchTranscripts searches the fetched transcripts when the plan includes them: --from matches the
// speaker, --since and --until bound each entry's time.
func (c *searchCmd) searchTranscripts(rt *runtime, p searchPlan, f store.Filter, st *store.Store) (transcriptSearch, error) {
	var out transcriptSearch
	if !p.transcripts || st == nil {
		return out, nil
	}
	only := !p.chats && !p.mail
	ok, err := transcriptTablesOf(st, rt.ctx)
	if err != nil {
		return out, err
	}
	if !ok {
		if only {
			out.note = noTranscriptTables
		}
		return out, nil
	}
	out.searched = true
	if out.hasText, err = transcriptTextOf(st, rt.ctx); err != nil {
		return out, err
	}
	rows, trunc, err := st.TranscriptSearch(rt.ctx, c.Query, store.TranscriptFilter{Account: rt.account, Speaker: f.From, Since: f.Since, Until: f.Until, Limit: f.Limit})
	if err != nil {
		return out, err
	}
	out.truncated = trunc
	for _, h := range rows {
		out.hits = append(out.hits, searchHit{at: h.At, item: searchTranscriptItemOf(h, rt.g.MaxText), source: searchTranscripts})
	}
	if only && !out.hasText {
		out.note = noTranscriptText
	}
	return out, nil
}

// searchNoMatch is the note of a search that read its sources and found nothing.
const searchNoMatch = "no message matched the search words and filters"

// searchEmptyNote says why a search found nothing, naming only the sources it read: the Teams
// cause (no archive, no Teams data, an unknown account, a range outside the archived window, or no
// match), and for mail and for transcripts that nothing matched. Transcripts are named only when
// the archive holds the text of one: otherwise there was nothing of theirs to match, and a search
// of transcripts alone already has a note saying so. Empty when no source was read: the plan's
// notes already say why.
func (rt *runtime) searchEmptyNote(st *store.Store, chats, mail, transcripts bool) (string, error) {
	const matched = " matched the search words and filters"
	var others []string
	if mail {
		others = append(others, "mail")
	}
	if transcripts {
		others = append(others, "transcript")
	}
	noOther := ""
	if len(others) > 0 {
		noOther = "no " + orList(others) + matched
	}
	if !chats {
		return noOther, nil
	}
	cause, err := rt.emptyListNote(st)
	switch {
	case err != nil || noOther == "":
		return cause, err
	case cause == searchNoMatch:
		return "no " + orList(append([]string{"chat message"}, others...)) + matched, nil
	}
	return "Teams chats: " + cause + "; " + noOther, nil
}

// orList writes words as prose: "a", "a or b", "a, b or c".
func orList(words []string) string {
	if len(words) < 2 {
		return strings.Join(words, "")
	}
	return strings.Join(words[:len(words)-1], ", ") + " or " + words[len(words)-1]
}

// mailAccountOf is the mail account an --account value names, or "" for a Teams account or none.
func mailAccountOf(account string) string {
	if isSearchMailAccount(account) {
		return account
	}
	return ""
}
