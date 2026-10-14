package store

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ourostack/m365crawl/internal/documenttext"
	"github.com/ourostack/m365crawl/internal/officecapture"
)

const LocalTextKind = "local_document_text"
const LocalTextQualification = "resident-ooxml-v1/parser-v1"
const localJSONLimit = 32 << 20

type LocalTextCapture struct {
	RequestedKind    string
	VerifiedKind     *string
	Evidence         officecapture.FileEvidence
	SourceByteDigest string
	Result           documenttext.Result
	AcquiredAt       time.Time
}
type LocalFileSnapshot struct {
	ID, SourceID, CaptureID         int64
	SourceByteDigest, Qualification string
	Evidence                        officecapture.FileEvidence
	AcquiredAt, CapturedAt          time.Time
}
type LocalTextRow struct {
	ID, SourceID, CaptureID int64
	RepresentationDigest    string
	Snapshot                LocalFileSnapshot
	Capture                 LocalTextCapture
	CapturedAt              time.Time
	Retained                bool
}
type LocalTextSource struct {
	Source                             KnowledgeSource
	RegistrationID                     int64
	Path, RequestedKind, SnapshotMatch string
	CurrentSnapshot                    *LocalFileSnapshot
	LastGoodObservationID              *int64
}
type LocalTextFilter struct {
	Query                        string
	SourceID                     int64
	ModifiedSince, ModifiedUntil time.Time
	Limit                        int
}
type LocalTextSnapshot struct {
	Rows      []LocalTextRow
	Sources   []LocalTextSource
	Truncated bool
}
type LocalTextReference struct {
	DocumentObservationID         int64
	OriginalURL, AssociationState string
}
type LocalTextCommit struct {
	Capture       LocalTextCapture
	RequestOrigin string
	Reference     *LocalTextReference
}
type LocalTextReferenceOccurrence struct {
	AttemptID, CaptureID, ObservationID int64
	Reference                           LocalTextReference
}
type LocalTextDetailResult struct {
	Observation         LocalTextRow
	Source              LocalTextSource
	References          []LocalTextReferenceOccurrence
	ReferencesTotal     int
	ReferencesTruncated bool
}

func LocalTextLocator(input officecapture.TextInput) string {
	b, _ := json.Marshal([2]string{input.Path, input.RequestedKind})
	return string(b)
}

func (s *Store) BeginLocalTextAttempt(ctx context.Context, input KnowledgeInput, request officecapture.TextInput) (KnowledgeAttempt, error) {
	if !officecapture.ValidTextRegistration(request, input.Platform) || input.Kind != LocalTextKind || input.Locator != LocalTextLocator(request) ||
		(input.Platform != "darwin" && input.Platform != "windows") {
		return KnowledgeAttempt{}, errors.New("local text: invalid registration")
	}
	return s.beginKnowledgeAttempt(ctx, input, func(tx *sql.Tx, id int64) error {
		_, err := tx.ExecContext(ctx, `insert into local_text_locator_metadata(locator_id,path,requested_kind) values(?,?,?)
on conflict(locator_id) do nothing`, id, request.Path, request.RequestedKind)
		if err != nil {
			return err
		}
		_, _, err = readLocalRequest(ctx, tx, id)
		if err != nil {
			return err
		}
		return nil
	}, nil)
}

func validLocalDigest(digest string) bool {
	b, err := hex.DecodeString(digest)
	return err == nil && len(b) == 32 && hex.EncodeToString(b) == digest
}

func validateLocalTextCommit(ctx context.Context, commit LocalTextCommit) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c := commit.Capture
	if !slices.Contains([]string{"explicit", "extension"}, commit.RequestOrigin) || c.AcquiredAt.IsZero() ||
		!validLocalDigest(c.SourceByteDigest) || !slices.Contains([]string{"docx", "xlsx", "pptx"}, c.RequestedKind) || c.Result.Kind != c.RequestedKind {
		return errors.New("local text: invalid capture")
	}
	if err := officecapture.ValidateFileEvidence(c.Evidence); err != nil {
		return err
	}
	r := c.Result
	encodedResult, _ := json.Marshal(r)
	if len(encodedResult) > localJSONLimit {
		return errors.New("local text: representation bound")
	}
	switch r.State {
	case "compound_unreadable":
		if c.VerifiedKind != nil || r.Partial || len(r.Losses) != 0 || len(r.Paragraphs) != 0 || len(r.Cells) != 0 {
			return errors.New("local text: compound format is unverified")
		}
	case "text_observations":
		if c.VerifiedKind == nil || *c.VerifiedKind != c.RequestedKind || r.Partial != (len(r.Losses) > 0) {
			return errors.New("local text: invalid format qualification")
		}
	default:
		return errors.New("local text: invalid parser state")
	}
	limits := documenttext.DefaultLimits()
	if len(r.Paragraphs) > limits.Paragraphs || len(r.Cells) > limits.Cells || (c.RequestedKind == "xlsx" && len(r.Paragraphs) > 0) ||
		(c.RequestedKind != "xlsx" && len(r.Cells) > 0) {
		return errors.New("local text: invalid collections")
	}
	total := int64(0)
	parts := map[string]bool{}
	for _, p := range r.Paragraphs {
		if p.Index < 0 || !validLocalPart(p.Part) || !utf8.ValidString(p.Text) {
			return errors.New("local text: invalid paragraph")
		}
		parts[p.Part] = true
		total += int64(len(p.Text))
	}
	for _, c := range r.Cells {
		if !validLocalPart(c.Part) || !utf8.ValidString(c.Value) || len(c.Reference) > 64 || c.Reference == "" ||
			!slices.Contains([]string{"", "n", "b", "e", "str", "s", "inlineStr", "d"}, c.Type) {
			return errors.New("local text: invalid cell")
		}
		parts[c.Part] = true
		total += int64(len(c.Value))
	}
	if total > limits.TextBytes || len(parts) > limits.Parts {
		return errors.New("local text: text bound")
	}
	previous := ""
	for _, loss := range r.Losses {
		if loss.Count <= 0 || loss.Count > limits.XMLTokens || loss.Code <= previous ||
			!slices.Contains([]string{"external_part_unmapped", "embedded_content_unmapped", "revision_or_instruction_unmapped", "text_without_paragraph_unmapped", "formatting_or_external_values_unmapped", "phonetic_text_unmapped", "cell_unmapped", "formula_without_cache"}, loss.Code) {
			return errors.New("local text: invalid losses")
		}
		previous = loss.Code
	}
	if commit.Reference != nil && (commit.Reference.DocumentObservationID <= 0 || commit.Reference.OriginalURL == "" ||
		commit.Reference.AssociationState != "reference_locator_match") {
		return errors.New("local text: invalid reference")
	}
	return nil
}

func validLocalPart(part string) bool {
	return utf8.ValidString(part) && len(part) <= 32768 && part != "" && !strings.Contains(part, `\`) &&
		!strings.HasPrefix(part, "/") && path.Clean(part) == part && part != ".." && !strings.HasPrefix(part, "../")
}

func localBoundary(input officecapture.TextInput, platform string, e officecapture.FileEvidence, attempt int64) string {
	generation := ""
	switch e.GenerationKind {
	case "native_generation":
		generation = *e.NativeGenerationRaw
	case "native_creation":
		generation = *e.CreationRaw
	default:
		generation = strconv.FormatInt(attempt, 10)
	}
	b, _ := json.Marshal([]string{LocalTextLocator(input), platform, e.GenerationPolicy, e.VolumeID, e.FileID, e.GenerationKind, generation})
	return string(b)
}

func localRepresentation(input officecapture.TextInput, platform, boundary string, c LocalTextCapture) string {
	b, _ := json.Marshal(struct {
		Locator, Platform, Boundary, Qualification string
		RequestedKind                              string
		VerifiedKind                               *string
		Evidence                                   officecapture.FileEvidence
		SourceByteDigest                           string
		Result                                     documenttext.Result
	}{LocalTextLocator(input), platform, boundary, LocalTextQualification, c.RequestedKind, c.VerifiedKind, c.Evidence, c.SourceByteDigest, c.Result})
	return hashOf(string(b))
}

func LocalTextValues(r documenttext.Result) string {
	var text []string
	for _, p := range r.Paragraphs {
		text = append(text, p.Part, p.Text)
	}
	for _, c := range r.Cells {
		text = append(text, c.Part, c.Reference, c.Value)
	}
	return strings.Join(text, "\n")
}

func OriginalDocumentURL(row DocumentObservationRow) string {
	switch row.Variant {
	case RegistryDocumentKind:
		if row.Registry != nil {
			return row.Registry.URL
		}
	case UsageDocumentKind:
		if row.Usage != nil && row.Usage.Document != nil {
			return row.Usage.Document.URL
		}
	default:
		return row.Document.URL
	}
	return ""
}

func validateLocalReference(ctx context.Context, reader knowledgeReader, ref LocalTextReference) error {
	rows, _, err := readDocumentObservations(ctx, reader, DocumentFilter{Limit: 1}, ref.DocumentObservationID)
	if err != nil {
		return err
	}
	if len(rows) != 1 || OriginalDocumentURL(rows[0]) != ref.OriginalURL || ref.AssociationState != "reference_locator_match" {
		return errors.New("local text: selected reference mismatch")
	}
	return nil
}

func (s *Store) CommitLocalText(ctx context.Context, attempt KnowledgeAttempt, commit LocalTextCommit) error {
	if err := validateLocalTextCommit(ctx, commit); err != nil {
		return err
	}
	input, platform, err := readLocalRequest(ctx, s.db, attempt.LocatorID)
	if err != nil {
		return err
	}
	c := commit.Capture
	if err := officecapture.ValidatePlatformEvidence(platform, c.Evidence); err != nil {
		return err
	}
	boundary := localBoundary(input, platform, c.Evidence, attempt.ID)
	digest := localRepresentation(input, platform, boundary, c)
	losses := map[string]int{}
	for _, loss := range c.Result.Losses {
		losses[loss.Code] = loss.Count
	}
	status, coverage := "complete", "supported_text"
	if c.Result.Partial {
		status, coverage = "partial", "partial"
	}
	if c.Result.State == "compound_unreadable" {
		status, coverage = "partial", "compound_unreadable"
	}
	o := KnowledgeOutcome{Status: status, ContentDigest: digest, Qualification: LocalTextQualification, At: time.Now().UTC()}
	return s.finishKnowledge(ctx, attempt, o, LocalTextKind, func(ctx context.Context, tx *sql.Tx, a KnowledgeAttempt, o KnowledgeOutcome) (int64, string, map[string]int, error) {
		stored, p, err := readLocalRequest(ctx, tx, a.LocatorID)
		if err != nil {
			return 0, "", nil, err
		}
		if stored != input || p != platform || stored.RequestedKind != c.RequestedKind {
			return 0, "", nil, errors.New("local text: request binding mismatch")
		}
		if commit.Reference != nil {
			if err := validateLocalReference(ctx, tx, *commit.Reference); err != nil {
				return 0, "", nil, err
			}
		}
		if _, err := tx.ExecContext(ctx, `insert into knowledge_sources(locator_id,boundary,qualification) values(?,?,?)
on conflict(locator_id,boundary,qualification) do nothing`, a.LocatorID, boundary, LocalTextQualification); err != nil {
			return 0, "", nil, err
		}
		var sourceID int64
		if err := tx.QueryRowContext(ctx, `select id from knowledge_sources where locator_id=? and boundary=? and qualification=?`, a.LocatorID, boundary, LocalTextQualification).Scan(&sourceID); err != nil {
			return 0, "", nil, err
		}
		evidence, _ := json.Marshal(c.Evidence)
		encoded, _ := json.Marshal(c.Result)
		if _, err := tx.ExecContext(ctx, `insert into local_file_snapshots(source_id,capture_id,source_byte_digest,evidence_json,qualification,acquired_at,captured_at) values(?,?,?,?,?,?,?)
on conflict(source_id,source_byte_digest,evidence_json,qualification) do nothing`, sourceID, a.ID, c.SourceByteDigest, string(evidence), LocalTextQualification, knowledgeTime(c.AcquiredAt), knowledgeTime(o.At)); err != nil {
			return 0, "", nil, err
		}
		var snapshotID int64
		if err := tx.QueryRowContext(ctx, `select rowid from local_file_snapshots where source_id=? and source_byte_digest=? and evidence_json=? and qualification=?`, sourceID, c.SourceByteDigest, string(evidence), LocalTextQualification).Scan(&snapshotID); err != nil {
			return 0, "", nil, err
		}
		var verified, modified any
		if c.VerifiedKind != nil {
			verified = *c.VerifiedKind
		}
		if c.Evidence.ModifiedAt != nil {
			modified = knowledgeTime(*c.Evidence.ModifiedAt)
		}
		if _, err := tx.ExecContext(ctx, `insert into local_text_observations(source_id,capture_id,snapshot_id,representation_digest,requested_kind,verified_kind,result_json,file_modified_at) values(?,?,?,?,?,?,?,?)
on conflict(source_id,representation_digest) do nothing`, sourceID, a.ID, snapshotID, digest, c.RequestedKind, verified, string(encoded), modified); err != nil {
			return 0, "", nil, err
		}
		var rowID int64
		if err := tx.QueryRowContext(ctx, `select rowid from local_text_observations where source_id=? and representation_digest=?`, sourceID, digest).Scan(&rowID); err != nil {
			return 0, "", nil, err
		}
		if _, err := tx.ExecContext(ctx, `insert into local_text_capture_members(capture_id,observation_rowid) values(?,?)`, a.ID, rowID); err != nil {
			return 0, "", nil, err
		}
		if _, err := tx.ExecContext(ctx, `insert into local_text_attempt_metadata(attempt_id,request_origin) values(?,?)`, a.ID, commit.RequestOrigin); err != nil {
			return 0, "", nil, err
		}
		if commit.Reference != nil {
			ref := commit.Reference
			if _, err := tx.ExecContext(ctx, `insert into local_text_references(attempt_id,capture_id,observation_rowid,document_observation_id,original_url,association_state) values(?,?,?,?,?,?)`, a.ID, a.ID, rowID, ref.DocumentObservationID, ref.OriginalURL, ref.AssociationState); err != nil {
				return 0, "", nil, err
			}
		}
		var indexed int
		if err := tx.QueryRowContext(ctx, `select count(*) from local_text_fts where rowid=?`, rowID).Scan(&indexed); err != nil {
			return 0, "", nil, err
		}
		if indexed == 0 {
			if _, err := tx.ExecContext(ctx, `insert into local_text_fts(rowid,label,text,metadata) values(?,?,?,?)`, rowID, filepath.Base(input.Path), LocalTextValues(c.Result), input.Path); err != nil {
				return 0, "", nil, err
			}
		}
		var body any
		if c.Result.State == "text_observations" {
			body = rowID
		}
		if _, err := tx.ExecContext(ctx, `insert into local_text_source_state(source_id,current_snapshot_id,last_good_observation_id) values(?,?,?)
on conflict(source_id) do update set current_snapshot_id=excluded.current_snapshot_id,last_good_observation_id=coalesce(excluded.last_good_observation_id,last_good_observation_id)`, sourceID, snapshotID, body); err != nil {
			return 0, "", nil, err
		}
		if _, err := tx.ExecContext(ctx, `update knowledge_sources set admitted_attempt=?,complete_attempt=case when ?='complete' then ? else complete_attempt end,source_observed_at=null where id=?`, a.ID, status, a.ID, sourceID); err != nil {
			return 0, "", nil, err
		}
		if _, err := tx.ExecContext(ctx, `update knowledge_locators set last_admitted=? where id=?`, a.ID, a.LocatorID); err != nil {
			return 0, "", nil, err
		}
		return sourceID, coverage, losses, nil
	})
}

func (s *Store) RecordLocalTextFailure(ctx context.Context, a KnowledgeAttempt, o KnowledgeOutcome) error {
	if !slices.Contains([]string{"failed", "unavailable", "cancelled"}, o.Status) || !slices.Contains([]string{
		"local_text_input_unqualified", "local_text_capture_failed", "local_text_cleanup_failed", "local_text_policy_restore_failed",
		"local_text_unsupported", "local_text_too_large", "local_text_malformed", "local_text_read_failed", "local_text_cancelled"}, o.Reason) {
		return errors.New("local text: invalid failure")
	}
	if _, _, err := readLocalRequest(ctx, s.db, a.LocatorID); err != nil {
		return err
	}
	return s.finishKnowledge(ctx, a, o, LocalTextKind, nil)
}
