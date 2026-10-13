package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/ourostack/m365crawl/internal/onedriveusage"
	"github.com/ourostack/m365crawl/internal/store"
)

// usagePreview is how many sharing instances, participants of one instance, collaborator
// addresses and related observations an item shows. Every bounded list states its full count.
const usagePreview = 100

// documentUsageHelp says what a OneDrive usage row is and what it does not prove.
const documentUsageHelp = "OneDrive usage rows (variant onedrive_usage, application onedrive, no surface) are records of OneDrive's local usage file, kept in usage_native exactly as that file states them. usage_native.Type is recent (one recently used file: native IDs, type, extension and clocks, no title), document (one formatted variant of a recent file, identified by its native ID plus Format: title, URL, owner, size and the sharing claims) or collaborator (one frequent-collaborator profile). One native ID can have a recent row and several document rows; none is preferred or merged. " +
	"Each clock is {Raw, Value}: Raw is the file's exact text and Value is UTC, or null when Raw is empty or is not an RFC 3339 time with an explicit offset. modified_at is the file modification time only (Document.Modified, or Recent.FileModified), so --since/--until never match access, sharing, saved or capture time, and a collaborator has none. In the OneDrive files examined so far every clock of a recent row is written without a zone (YYYY-MM-DD HH:MM:SS): m365crawl does not guess one, so those rows' Value clocks and modified_at are null and date windows exclude them. A recent row with a parsed explicit-offset FileModified clock can match a date window. " +
	"Document.History previews the first 100 sharing instances in native order (Position is the native index) and each instance previews its first 100 Participants; HistoryCaptured, ParticipantsCaptured and the *Truncated flags give the full captured counts, ParticipantsCount is the number the file itself states (or null), and PreviewTruncated is true when anything was cut. full_inspection is an archive-only `m365crawl --db PATH --max-age 0 sql` command that prints the row's complete stored record; run it as printed. " +
	"Names, addresses, thread IDs, meeting IDs and collaborator profiles are the file's claims: m365crawl links none of them to an archived person, chat, mail or account, so no thread or person command is offered for them. The documents and URLs are never opened, checked or resolved."

// usageClock is a native clock: its exact text, and its UTC value or null.
type usageClock struct {
	Raw   string
	Value *time.Time
}

func usageClockOf(raw string) usageClock {
	return usageClock{Raw: raw, Value: store.ParseUsageClock(raw)}
}

// usageShareItem is one sharing claim. Position is its index in the native history, or null for
// the document's latest-sharing claim. Participants previews the first usagePreview native rows.
type usageShareItem struct {
	Position                              *int
	DisplayName, SMTP, AadID              string
	At                                    usageClock
	ConversationID, Subject, AttachmentID string
	Participants                          []onedriveusage.Person
	ParticipantsCaptured                  int
	ParticipantsTruncated                 bool
	ParticipantsCount                     *int64
	MeetingStart                          usageClock
	ICalUID, MeetingSubject               string
	Recurring                             *bool
}

type usageDocumentItem struct {
	ID, Format, Title, URL, Extension, Owner string
	Created, Modified                        usageClock
	SiteID, WebID, ListID, UniqueID          string
	Size                                     *int64
	Latest                                   *usageShareItem
	History                                  []usageShareItem
	HistoryCaptured                          int
	HistoryTruncated                         bool
	// ParticipantsCaptured counts the participant rows of every captured history instance.
	ParticipantsCaptured int
}

type usageRecentItem struct {
	ID, ItemID, ListID, WebID, SiteID, Type, Source, Extension, WorkingSetID string
	LastModified, LastAccessed, FileModified, LastShared, Saved              usageClock
	HiddenFromShared, LocalPlaceholder                                       *int64
}

type usageCollaboratorItem struct {
	ID, UserID, DisplayName, Department, JobTitle, Office string
	Emails                                                []string
	EmailsCaptured                                        int
	EmailsTruncated                                       bool
}

// usageNativeItem is a usage row's native record as an item: exactly the payload Type names,
// with descriptive text clipped and long lists previewed. Native IDs, URLs, addresses and raw
// clocks are never clipped.
type usageNativeItem struct {
	Type             string
	Document         *usageDocumentItem     `json:",omitempty"`
	Recent           *usageRecentItem       `json:",omitempty"`
	Collaborator     *usageCollaboratorItem `json:",omitempty"`
	PreviewTruncated bool
}

// usageRelationItem names one related observation of the same capture and native ID.
type usageRelationItem struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	NativeID string `json:"native_id"`
	Format   string `json:"format"`
}

// usageRelationsItem is a usage row's relations in the capture it is served from: for a recent
// row every document row with its native ID, for a document row its recent row.
type usageRelationsItem struct {
	CaptureID int64               `json:"capture_id"`
	Total     int                 `json:"total"`
	Truncated bool                `json:"truncated"`
	Items     []usageRelationItem `json:"items"`
}

// usageClip clips descriptive text and records whether anything was cut.
type usageClip struct {
	max       int
	truncated bool
}

func (c *usageClip) text(value string) string {
	out, cut := truncateRunes(value, c.max)
	c.truncated = c.truncated || cut
	return out
}

func (c *usageClip) share(share onedriveusage.Share, position *int) (usageShareItem, bool) {
	item := usageShareItem{Position: position, DisplayName: c.text(share.DisplayName), SMTP: share.SMTP, AadID: share.AadID,
		At: usageClockOf(share.AtRaw), ConversationID: share.ConversationID, Subject: c.text(share.Subject), AttachmentID: share.AttachmentID,
		Participants: []onedriveusage.Person{}, ParticipantsCaptured: len(share.Participants),
		ParticipantsTruncated: len(share.Participants) > usagePreview, ParticipantsCount: share.ParticipantsCount,
		MeetingStart: usageClockOf(share.MeetingStartRaw), ICalUID: share.ICalUID, MeetingSubject: c.text(share.MeetingSubject), Recurring: share.Recurring}
	for _, participant := range share.Participants[:min(len(share.Participants), usagePreview)] {
		item.Participants = append(item.Participants, onedriveusage.Person{DisplayName: c.text(participant.DisplayName), SMTP: participant.SMTP})
	}
	return item, item.ParticipantsTruncated
}

func (c *usageClip) document(d onedriveusage.DocumentObservation) (*usageDocumentItem, bool) {
	item := &usageDocumentItem{ID: d.ID, Format: d.Format, Title: c.text(d.Title), URL: d.URL, Extension: d.Extension, Owner: c.text(d.Owner),
		Created: usageClockOf(d.CreatedRaw), Modified: usageClockOf(d.ModifiedRaw), SiteID: d.SiteID, WebID: d.WebID, ListID: d.ListID,
		UniqueID: d.UniqueID, Size: d.Size, History: []usageShareItem{}, HistoryCaptured: len(d.History), HistoryTruncated: len(d.History) > usagePreview}
	preview := item.HistoryTruncated
	if d.Latest != nil {
		latest, cut := c.share(*d.Latest, nil)
		item.Latest, preview = &latest, preview || cut
	}
	for position, share := range d.History {
		item.ParticipantsCaptured += len(share.Participants)
		if position < usagePreview {
			shown, cut := c.share(share, &position)
			item.History, preview = append(item.History, shown), preview || cut
		}
	}
	return item, preview
}

// usageNativeOf builds the bounded, clipped item of one native record.
func usageNativeOf(native store.UsageNative, maxText int) (usageNativeItem, bool) {
	clip := usageClip{max: maxText}
	item := usageNativeItem{Type: native.Type}
	switch {
	case native.Document != nil:
		item.Document, item.PreviewTruncated = clip.document(*native.Document)
	case native.Recent != nil:
		r := native.Recent
		item.Recent = &usageRecentItem{ID: r.ID, ItemID: r.ItemID, ListID: r.ListID, WebID: r.WebID, SiteID: r.SiteID, Type: r.Type,
			Source: r.Source, Extension: r.Extension, WorkingSetID: r.WorkingSetID, LastModified: usageClockOf(r.LastModifiedRaw),
			LastAccessed: usageClockOf(r.LastAccessedRaw), FileModified: usageClockOf(r.FileModifiedRaw), LastShared: usageClockOf(r.LastSharedRaw),
			Saved: usageClockOf(r.SavedRaw), HiddenFromShared: r.HiddenFromShared, LocalPlaceholder: r.LocalPlaceholder}
	default:
		p := native.Collaborator
		item.Collaborator = &usageCollaboratorItem{ID: p.ID, UserID: p.UserID, DisplayName: clip.text(p.DisplayName), Department: clip.text(p.Department),
			JobTitle: clip.text(p.JobTitle), Office: clip.text(p.Office), Emails: append([]string{}, p.Emails[:min(len(p.Emails), usagePreview)]...),
			EmailsCaptured: len(p.Emails), EmailsTruncated: len(p.Emails) > usagePreview}
		item.PreviewTruncated = item.Collaborator.EmailsTruncated
	}
	return item, clip.truncated
}

// usageInspection is the archive-only command that prints a usage row's complete stored record.
// It is a fixed template around the positive observation row ID and the archive path db, which
// it names with --db so the command reads the archive the result came from; never source text.
func usageInspection(id int64, db string) string {
	return fmt.Sprintf(`m365crawl --db %s --max-age 0 sql "select usage_json from document_observations where rowid=%d" --limit 1`, quoteArg(db), id)
}

// usageItemOf is the item of a OneDrive usage row. Title and URL are the document's own; a recent
// row and a collaborator state neither, so theirs are empty rather than invented.
func usageItemOf(row store.DocumentObservationRow, maxText int, db string) documentItem {
	native, truncated := usageNativeOf(*row.Usage, maxText)
	item := documentItem{ID: fmt.Sprintf("document-observation:%d", row.ID), SourceID: row.SourceID,
		CaptureID: row.CaptureID, Application: row.Application, ModifiedAt: row.ModifiedAt,
		CapturedAt: row.CapturedAt, SourceObservedAt: row.SourceObservedAt, Retained: row.Retained,
		AssociationState: "unresolved", TextState: "not_captured", TextTruncated: truncated,
		Variant: row.Variant, UsageNative: &native, FullInspection: usageInspection(row.ID, db)}
	if native.Document != nil {
		item.Title, item.URL = native.Document.Title, native.Document.URL
	}
	return item
}

func usageRelationsOf(detail store.DocumentObservationDetail) *usageRelationsItem {
	if detail.Observation.Usage == nil {
		return nil
	}
	out := &usageRelationsItem{CaptureID: detail.UsageRelationsCaptureID, Total: detail.UsageRelationsTotal,
		Truncated: detail.UsageRelationsTruncated, Items: []usageRelationItem{}}
	for _, relation := range detail.UsageRelations {
		out.Items = append(out.Items, usageRelationItem{ID: fmt.Sprintf("document-observation:%d", relation.ObservationID),
			Type: relation.Type, NativeID: relation.ID, Format: relation.Format})
	}
	return out
}

// usageLabel is a usage row's title column in a text table: what the file states for the row. A
// recent row and a collaborator state no title, so it is the native ID or the stated name; the
// row's type has its own column, so a clipped title never hides it.
func usageLabel(item documentItem) string {
	native := item.UsageNative
	switch {
	case native.Recent != nil:
		return native.Recent.ID
	case native.Collaborator != nil:
		return firstOf(native.Collaborator.DisplayName, native.Collaborator.ID)
	}
	return firstOf(item.Title, native.Document.ID+" (untitled)")
}

// usageKind names a usage row's native type in a sentence.
func usageKind(native string) string {
	if native == store.UsageTypeRecent {
		return "recent row"
	}
	return native + " row"
}

func usageStamp(at *time.Time) string {
	if at == nil {
		return "unknown"
	}
	return at.UTC().Format(time.RFC3339Nano)
}

// usageDetailLines are the words of a usage detail: what the row is, its modification time, its
// preview counts, its relations and how to read the complete record.
func usageDetailLines(detail documentDetailItem) []string {
	native := detail.UsageNative
	what, modified := "OneDrive usage "+native.Type, "modified_at: "+usageStamp(detail.ModifiedAt)
	lines := []string{}
	switch {
	case native.Document != nil:
		if detail.ModifiedAt == nil {
			modified += " (the file gave none, or Document.Modified.Raw is not an RFC 3339 time with an offset)"
		}
		shown := 0
		for _, share := range native.Document.History {
			shown += len(share.Participants)
		}
		lines = append(lines, fmt.Sprintf("sharing history: %d captured, %d shown; participants: %d captured, %d shown",
			native.Document.HistoryCaptured, len(native.Document.History), native.Document.ParticipantsCaptured, shown))
		if native.Document.Latest != nil {
			lines = append(lines, fmt.Sprintf("latest sharing claim participants: %d captured, %d shown",
				native.Document.Latest.ParticipantsCaptured, len(native.Document.Latest.Participants)))
		}
	case native.Recent != nil:
		what = "OneDrive usage recent row"
		if detail.ModifiedAt == nil {
			modified += " (the file gave none, or Recent.FileModified.Raw is not an RFC 3339 time with an offset)"
		}
	default:
		modified += " (a collaborator profile states no modification time)"
	}
	out := []string{what, modified, "access, sharing, saved and row-change clocks are separate native claims under usage native claims; none is a modification time"}
	out = append(out, lines...)
	relations := detail.UsageRelations
	related := ""
	for _, relation := range relations.Items {
		related += "; " + strings.TrimSpace(relation.ID+" "+relation.Type+" "+relation.Format)
	}
	out = append(out, fmt.Sprintf("related observations in capture %d (same native ID, not merged): %d (returned %d; truncated=%t)%s",
		relations.CaptureID, relations.Total, len(relations.Items), relations.Truncated, related))
	return append(out, "full archived record: "+detail.FullInspection)
}
