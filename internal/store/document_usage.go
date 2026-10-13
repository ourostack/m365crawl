package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/ourostack/m365crawl/internal/onedriveusage"
)

const (
	// UsageDocumentKind is both the knowledge kind of a OneDrive usage source and the variant of
	// its locator, observation and capture rows.
	UsageDocumentKind = "onedrive_usage"
	// UsageDocumentQualification names the reader and mapper a usage representation came from.
	UsageDocumentQualification = "onedrive-usage-v1/mapper-v1"
	// UsageDocumentSource is the fixed source label a usage row carries where an Office JSON row
	// carries its application.
	UsageDocumentSource = "onedrive"

	// The native record types of a usage source.
	UsageTypeDocument     = "document"
	UsageTypeRecent       = "recent"
	UsageTypeCollaborator = "collaborator"
)

// usageUnmappedLosses are the loss codes that each count one physical source row.
var usageUnmappedLosses = []string{"onedrive_usage_recent_unmapped", "onedrive_usage_document_unmapped",
	"onedrive_usage_document_orphan", "onedrive_usage_collaborator_unmapped"}

// UsageCaptureMetadata are the immutable clocks of one usage acquisition.
type UsageCaptureMetadata struct{ AcquiredAt, SourceObservedAt *time.Time }

// UsageNative is one complete native usage record. Exactly one payload is set, the one Type names:
// a formatted document variant (native ID plus Format), a recent row, or a collaborator.
type UsageNative struct {
	Type         string
	Document     *onedriveusage.DocumentObservation `json:",omitempty"`
	Recent       *onedriveusage.RecentObservation   `json:",omitempty"`
	Collaborator *onedriveusage.Collaborator        `json:",omitempty"`
}

// DocumentUsageRelation is one capture-owned edge between a recent row and a formatted document
// variant with the same native ID. It names the related observation, never a merged entity.
type DocumentUsageRelation struct {
	ObservationID    int64
	Type, ID, Format string
}

// identity is the record's native ID and Format, and whether exactly the payload Type names is set.
func (n UsageNative) identity() (id, format string, ok bool) {
	set := 0
	for _, present := range []bool{n.Document != nil, n.Recent != nil, n.Collaborator != nil} {
		if present {
			set++
		}
	}
	switch {
	case set != 1:
	case n.Type == UsageTypeDocument && n.Document != nil:
		return n.Document.ID, n.Document.Format, strings.TrimSpace(n.Document.ID) != "" && strings.TrimSpace(n.Document.Format) != ""
	case n.Type == UsageTypeRecent && n.Recent != nil:
		return n.Recent.ID, "", strings.TrimSpace(n.Recent.ID) != ""
	case n.Type == UsageTypeCollaborator && n.Collaborator != nil:
		return n.Collaborator.ID, "", strings.TrimSpace(n.Collaborator.ID) != ""
	}
	return "", "", false
}

func usageNativeID(native UsageNative) string {
	id, _, _ := native.identity()
	return id
}

// usageNativeKey is the full native tie key of a usage row: Type, ID and Format.
func usageNativeKey(native UsageNative) string {
	id, format, _ := native.identity()
	return native.Type + "\x1f" + id + "\x1f" + format
}

// UsageModifiedRaw is the record's own modification text: a document's file modification time or
// a recent row's file modification time. A collaborator states none. Access, sharing, saved and
// row-change clocks are never modification time.
func UsageModifiedRaw(native UsageNative) string {
	switch {
	case native.Document != nil:
		return native.Document.ModifiedRaw
	case native.Recent != nil:
		return native.Recent.FileModifiedRaw
	}
	return ""
}

// ParseUsageClock parses a raw usage clock to UTC, or returns nil. The grammar is the registry's:
// RFC 3339 with an explicit Z or numeric offset and at most nine fractional digits. A value
// outside it keeps its raw text and has no parsed time; nothing falls back to the host locale, a
// file time or a capture time.
func ParseUsageClock(raw string) *time.Time { return ParseRegistryOpened(raw) }

var usageBinding = documentBinding{Variant: UsageDocumentKind, Application: UsageDocumentSource}

func (s *Store) BeginUsageDocumentAttempt(ctx context.Context, input KnowledgeInput) (KnowledgeAttempt, error) {
	if input.Kind != UsageDocumentKind {
		return KnowledgeAttempt{}, errors.New("documents: invalid usage source binding")
	}
	return s.beginDocumentAttempt(ctx, input, usageBinding)
}

// usageRecords is a result as native records in publication order: recent rows, formatted
// document variants, collaborators.
func usageRecords(result onedriveusage.Result) []UsageNative {
	out := make([]UsageNative, 0, len(result.Recent)+len(result.Documents)+len(result.Collaborators))
	for i := range result.Recent {
		out = append(out, UsageNative{Type: UsageTypeRecent, Recent: &result.Recent[i]})
	}
	for i := range result.Documents {
		out = append(out, UsageNative{Type: UsageTypeDocument, Document: &result.Documents[i]})
	}
	for i := range result.Collaborators {
		out = append(out, UsageNative{Type: UsageTypeCollaborator, Collaborator: &result.Collaborators[i]})
	}
	return out
}

// CommitUsageDocumentObservations admits one usage capture through the same immutable
// representation, membership and frontier machinery as Office JSON and the registry. Every recent
// row, formatted variant and collaborator is its own representation: the complete native record
// plus its source binding. A repeated one keeps its original clocks. Each recent row is related,
// for this capture only, to every formatted variant of the same native ID.
func (s *Store) CommitUsageDocumentObservations(ctx context.Context, attempt KnowledgeAttempt, outcome KnowledgeOutcome, result onedriveusage.Result, capture UsageCaptureMetadata) error {
	records := usageRecords(result)
	if !slices.Contains([]string{"complete", "partial", "unchanged"}, outcome.Status) ||
		outcome.ContentDigest == "" || outcome.Qualification != UsageDocumentQualification ||
		(outcome.Status == "unchanged" && (len(records) != 0 || len(result.Losses) != 0)) ||
		(outcome.Status != "unchanged" && (len(result.Losses) > 0) != (outcome.Status == "partial")) {
		return errors.New("documents: unqualified usage outcome")
	}
	keys := map[string]bool{}
	for _, record := range records {
		if _, _, ok := record.identity(); !ok || keys[usageNativeKey(record)] {
			return errors.New("documents: invalid native usage representation")
		}
		keys[usageNativeKey(record)] = true
	}
	admission := documentAdmission{binding: usageBinding, qualification: UsageDocumentQualification,
		unmappedLosses: usageUnmappedLosses, documents: len(records), acquired: capture.AcquiredAt, observed: capture.SourceObservedAt,
		apply: func(ctx context.Context, tx *sql.Tx, sourceID, captureID int64) error {
			return applyUsageObservations(ctx, tx, attempt.LocatorID, sourceID, captureID, records)
		},
	}
	for _, loss := range result.Losses {
		if loss.Code == "" || loss.Count <= 0 {
			return errors.New("documents: invalid usage source loss")
		}
		admission.losses = append(admission.losses, documentLoss{loss.Code, loss.Count})
	}
	return s.finishKnowledge(ctx, attempt, outcome, UsageDocumentKind, admission.admit)
}

func applyUsageObservations(ctx context.Context, tx *sql.Tx, locatorID, sourceID, captureID int64, records []UsageNative) error {
	var platform, locator string
	if err := tx.QueryRowContext(ctx, `select platform,locator from knowledge_locators where id=?`, locatorID).Scan(&platform, &locator); err != nil {
		return err
	}
	recent := map[string]int64{}
	for ordinal, record := range records {
		encoded, _ := json.Marshal(record) // strings, integers, booleans and nullable scalars always encode
		publication := documentPublication{sourceID: sourceID, captureID: captureID,
			digest:  hashOf(strings.Join([]string{UsageDocumentKind, UsageDocumentQualification, platform, locator, string(encoded)}, "\x00")),
			ordinal: ordinal, variant: UsageDocumentKind, application: UsageDocumentSource,
			usageJSON: string(encoded), nativeKey: usageNativeKey(record), metadata: usageMetadataText(record)}
		if at := ParseUsageClock(UsageModifiedRaw(record)); at != nil {
			publication.modified = knowledgeTime(*at)
		}
		if record.Document != nil {
			publication.title, publication.url = record.Document.Title, record.Document.URL
		}
		rowID, err := publishDocumentObservation(ctx, tx, publication)
		if err != nil {
			return err
		}
		switch {
		case record.Recent != nil:
			recent[record.Recent.ID] = rowID
		case record.Document != nil && recent[record.Document.ID] != 0:
			if _, err := tx.ExecContext(ctx, `insert into document_usage_relations(capture_id,recent_rowid,document_rowid) values(?,?,?)`,
				captureID, recent[record.Document.ID], rowID); err != nil {
				return err
			}
		}
	}
	return nil
}

// usageMetadataText is what the document index holds beside the title: the selected native IDs,
// URL, extension and owner of a document or recent row, the names, addresses, subjects and thread
// IDs its sharing claims state, and a collaborator's stated profile. Nothing else in the usage
// file is indexed.
func usageMetadataText(native UsageNative) string {
	var values []string
	switch {
	case native.Document != nil:
		d := native.Document
		values = []string{d.URL, d.Extension, d.Owner, d.ID, d.Format, d.SiteID, d.WebID, d.ListID, d.UniqueID}
		shares := d.History
		if d.Latest != nil {
			shares = append(slices.Clone(shares), *d.Latest)
		}
		seen := map[string]bool{"": true}
		for _, share := range shares {
			claims := []string{share.DisplayName, share.SMTP, share.Subject, share.ConversationID}
			for _, participant := range share.Participants {
				claims = append(claims, participant.DisplayName, participant.SMTP)
			}
			for _, claim := range claims {
				if !seen[claim] {
					seen[claim] = true
					values = append(values, claim)
				}
			}
		}
	case native.Recent != nil:
		r := native.Recent
		values = []string{r.ID, r.ItemID, r.ListID, r.WebID, r.SiteID, r.Extension}
	default:
		c := native.Collaborator
		values = append([]string{c.ID, c.UserID, c.DisplayName, c.Department, c.JobTitle, c.Office}, c.Emails...)
	}
	return strings.Join(values, "\n")
}

// decodeUsageRow validates a stored usage row's discriminator, label, payload, native key and
// modification time together and attaches the native record.
func decodeUsageRow(row *DocumentObservationRow, stored documentStored) error {
	if !stored.usage.Valid || stored.registry.Valid || stored.opened.Valid || stored.document != "" || stored.surface != "" ||
		row.Application != UsageDocumentSource || stored.kind != UsageDocumentKind {
		return errors.New("documents: invalid stored usage representation")
	}
	decoder := json.NewDecoder(strings.NewReader(stored.usage.String))
	decoder.DisallowUnknownFields()
	var native UsageNative
	if err := decoder.Decode(&native); err != nil {
		return errors.New("documents: invalid stored usage payload")
	}
	if _, _, ok := native.identity(); !ok || usageNativeKey(native) != stored.nativeKey {
		return errors.New("documents: stored usage payload does not match its native key")
	}
	times, err := knowledgeTimes(stored.modified)
	if err != nil {
		return err
	}
	want := ParseUsageClock(UsageModifiedRaw(native))
	if (want == nil) != (times[0] == nil) || (want != nil && !want.Equal(*times[0])) {
		return errors.New("documents: stored usage modified time does not match its payload")
	}
	row.Usage, row.ModifiedAt = &native, want
	return nil
}
