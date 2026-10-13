package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/ourostack/m365crawl/internal/sharepointcontent"
)

const (
	CloudPageKind            = "sharepoint_page"
	CloudStreamKind          = "stream_transcript"
	CloudPageQualification   = "sharepoint-content-v1/page-supported-projection-v1"
	CloudStreamQualification = "sharepoint-content-v1/selected-transcript-v1"
	cloudJSONLimit           = 16 << 20
)

type CloudCapture struct {
	Request     sharepointcontent.CanonicalRequest
	Result      sharepointcontent.Result
	CompletedAt time.Time
}
type CloudObservationRow struct {
	ID, SourceID, CaptureID int64
	Result                  sharepointcontent.Result
	CapturedAt              time.Time
	Retained                bool
}
type CloudSource struct {
	Source                KnowledgeSource
	RegistrationID        int64
	RequestedTranscriptID *string
	CurrentTranscriptID   *string
	LastGoodObservationID *int64
	SelectionMatch        string
}
type CloudFilter struct {
	Query                        string
	SourceID                     int64
	ModifiedSince, ModifiedUntil time.Time
	Limit                        int
}
type CloudSnapshot struct {
	Rows      []CloudObservationRow
	Sources   []CloudSource
	Truncated bool
}

func cloudKind(request sharepointcontent.CanonicalRequest) (string, string) {
	if request.Request.Kind == "page" {
		return CloudPageKind, CloudPageQualification
	}
	return CloudStreamKind, CloudStreamQualification
}

func cloudBoundary(kind string, result sharepointcontent.Result) string {
	file, account := result.File, result.SourceAccount
	encoded, _ := json.Marshal([]string{kind, "browser", account.Host, file.SiteID, file.WebID, file.FileID, strconv.FormatInt(account.ID, 10), account.LoginName})
	return string(encoded)
}

func (s *Store) BeginCloudAttempt(ctx context.Context, input KnowledgeInput, request sharepointcontent.CanonicalRequest) (KnowledgeAttempt, error) {
	canonical, err := sharepointcontent.NormalizeRequest(request.Request)
	kind, _ := cloudKind(request)
	if err != nil || canonical != request || input.Kind != kind || input.Platform != "browser" || input.Locator != request.LocatorKey {
		return KnowledgeAttempt{}, errors.New("cloud: invalid registration")
	}
	encoded, _ := json.Marshal(request)
	return s.beginKnowledgeAttempt(ctx, input, func(tx *sql.Tx, id int64) error {
		_, err := tx.ExecContext(ctx, `insert into cloud_locator_metadata(locator_id,request_json) values(?,?)
on conflict(locator_id) do nothing`, id, string(encoded))
		return err
	}, nil)
}

func validateCloudCapture(ctx context.Context, capture CloudCapture) error {
	if capture.CompletedAt.IsZero() {
		return errors.New("cloud: missing completion clock")
	}
	_, err := sharepointcontent.ValidateResult(ctx, capture.Request, capture.Result)
	return err
}

func (s *Store) CommitCloudCapture(ctx context.Context, attempt KnowledgeAttempt, capture CloudCapture) error {
	if err := validateCloudCapture(ctx, capture); err != nil {
		return err
	}
	result := capture.Result
	kind, qualification := cloudKind(capture.Request)
	encoded, _ := json.Marshal(result)
	coverage, status := "partial", "partial"
	if result.Kind == "stream" && result.State == "transcript_observations" && !result.Partial {
		coverage, status = "selected_transcript", "complete"
	}
	losses := map[string]int{}
	for _, loss := range result.Losses {
		losses[loss.Code] += loss.Count
	}
	outcome := KnowledgeOutcome{Status: status, ContentDigest: hashOf(qualification + string(encoded)), Qualification: qualification, Losses: losses, At: capture.CompletedAt}
	return s.finishKnowledge(ctx, attempt, outcome, kind, func(ctx context.Context, tx *sql.Tx, a KnowledgeAttempt, o KnowledgeOutcome) (int64, string, map[string]int, error) {
		stored, err := readCloudRequest(ctx, tx, a.LocatorID)
		if err != nil {
			return 0, "", nil, err
		}
		if stored.LocatorKey != capture.Request.LocatorKey {
			return 0, "", nil, errors.New("cloud: request binding mismatch")
		}
		file, account := result.File, result.SourceAccount
		boundary := cloudBoundary(kind, result)
		if _, err := tx.ExecContext(ctx, `insert into knowledge_sources(locator_id,boundary,qualification) values(?,?,?)
on conflict(locator_id,boundary,qualification) do nothing`, a.LocatorID, boundary, qualification); err != nil {
			return 0, "", nil, err
		}
		var sourceID int64
		if err := tx.QueryRowContext(ctx, `select id from knowledge_sources where locator_id=? and boundary=? and qualification=?`, a.LocatorID, boundary, qualification).Scan(&sourceID); err != nil {
			return 0, "", nil, err
		}
		var title any
		if file.Title != nil {
			title = *file.Title
		}
		var modified any
		if file.ModifiedAt != nil {
			modified = knowledgeTime(*file.ModifiedAt)
		}
		if _, err := tx.ExecContext(ctx, `insert into cloud_observations(source_id,capture_id,representation_digest,result_json,kind,title,file_modified_at)
values(?,?,?,?,?,?,?) on conflict(source_id,representation_digest) do nothing`, sourceID, a.ID, o.ContentDigest, string(encoded), kind, title, modified); err != nil {
			return 0, "", nil, err
		}
		var rowID int64
		if err := tx.QueryRowContext(ctx, `select rowid from cloud_observations where source_id=? and representation_digest=?`, sourceID, o.ContentDigest).Scan(&rowID); err != nil {
			return 0, "", nil, err
		}
		if _, err := tx.ExecContext(ctx, `insert into cloud_capture_members(capture_id,observation_rowid) values(?,?)`, a.ID, rowID); err != nil {
			return 0, "", nil, err
		}
		var indexed int
		if err := tx.QueryRowContext(ctx, `select count(*) from cloud_fts where rowid=?`, rowID).Scan(&indexed); err != nil {
			return 0, "", nil, err
		}
		if indexed == 0 {
			metadata, _ := json.Marshal(struct {
				File    sharepointcontent.NativeFile
				Account sharepointcontent.SourceAccount
			}{file, account})
			if _, err := tx.ExecContext(ctx, `insert into cloud_fts(rowid,title,text,metadata) values(?,?,?,?)`, rowID, title, CloudText(result), string(metadata)); err != nil {
				return 0, "", nil, err
			}
		}
		var body any
		if result.State == "page_observations" || result.Transcript != nil {
			body = rowID
		}
		var selected any
		if result.Transcript != nil {
			selected = result.Transcript.ID
		}
		if _, err := tx.ExecContext(ctx, `insert into cloud_capture_metadata(capture_id,requested_transcript_id,current_transcript_id,body_observation_id,completed_at)
values(?,?,?,?,?)`, a.ID, nullableKnowledge(stored.Request.TranscriptID), selected, body, knowledgeTime(capture.CompletedAt)); err != nil {
			return 0, "", nil, err
		}
		if _, err := tx.ExecContext(ctx, `insert into cloud_source_body_state(source_id,last_good_observation_id,current_transcript_id) values(?,?,?)
on conflict(source_id) do update set last_good_observation_id=coalesce(excluded.last_good_observation_id,last_good_observation_id),
current_transcript_id=excluded.current_transcript_id`, sourceID, body, selected); err != nil {
			return 0, "", nil, err
		}
		if _, err := tx.ExecContext(ctx, `update knowledge_sources set admitted_attempt=?,
complete_attempt=case when ?='selected_transcript' then ? else complete_attempt end,source_observed_at=null where id=?`, a.ID, coverage, a.ID, sourceID); err != nil {
			return 0, "", nil, err
		}
		if _, err := tx.ExecContext(ctx, `update knowledge_locators set last_admitted=? where id=?`, a.ID, a.LocatorID); err != nil {
			return 0, "", nil, err
		}
		return sourceID, coverage, losses, nil
	})
}

// CloudText preserves every supported text occurrence in native order, including repeats.
func CloudText(result sharepointcontent.Result) string {
	var texts []string
	for _, control := range result.PageControls {
		texts = append(texts, control.Texts...)
	}
	if result.Transcript != nil {
		for _, entry := range result.Transcript.Entries {
			texts = append(texts, entry.Text)
		}
	}
	return strings.Join(texts, "\n")
}

func (s *Store) RecordCloudFailure(ctx context.Context, attempt KnowledgeAttempt, outcome KnowledgeOutcome) error {
	switch outcome.Status {
	case "failed", "unavailable", "cancelled":
	default:
		return errors.New("cloud: invalid failure status")
	}
	switch outcome.Reason {
	case "cloud_unreadable", "cloud_cancelled", "cloud_browser_failed", "cloud_signin_required", "cloud_identity_mismatch", "cloud_too_large", "cloud_malformed", "cloud_timeout", "cloud_no_access", "cloud_not_found":
	default:
		return errors.New("cloud: invalid failure diagnostic")
	}
	request, err := s.CloudRequest(ctx, attempt.LocatorID)
	if err != nil {
		return err
	}
	kind, _ := cloudKind(request)
	return s.finishKnowledge(ctx, attempt, outcome, kind, nil)
}
