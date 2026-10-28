package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/ourostack/m365crawl/internal/engagecontent"
)

const (
	EngageKind                = "engage_home"
	EngageQualification       = "engage-native-home-v1/mapper-v1"
	EngageHomeURL             = "https://engage.cloud.microsoft/"
	EngageThreadKind          = "engage_thread"
	EngageThreadQualification = "engage-native-selected-v1/mapper-v1"
)

type EngageCapture struct {
	Result                 engagecontent.Result
	AcquiredAt, AdmittedAt time.Time
}
type EngageFailure struct {
	Status, Reason string
	At             time.Time
}
type EngageSession struct {
	ID, SourceID, CaptureID             int64
	RepresentationDigest, Qualification string
	Result                              engagecontent.Result
	AcquiredAt, CapturedAt              time.Time
}
type EngageSessionSummary struct {
	ID, SourceID, CaptureID                    int64
	RepresentationDigest, Qualification, State string
	ThreadsTotal, BlocksTotal                  int
	Losses                                     []engagecontent.Loss
	AcquiredAt, CapturedAt                     time.Time
}
type EngageRegistration struct {
	ID, LatestAttemptID                   int64
	Target, Outcome, Reason, AccountState string
	AttemptedAt, FinishedAt               *time.Time
	LastSuccessfulInstanceID              *int64
	Kind                                  string
}
type EngageSource struct {
	ID, RegistrationID, AdmittedAttemptID, CurrentSessionID int64
	Account                                                 engagecontent.SourceAccount
	Qualification, Coverage                                 string
	LastGoodSessionID                                       *int64
	AdmittedAt                                              *time.Time
	CurrentSession                                          EngageSessionSummary
	IsLastSuccessfulInstance                                bool
}
type EngageObservationRow struct {
	ID, SourceID, SessionID, CaptureID  int64
	RepresentationDigest, Qualification string
	Thread                              engagecontent.ThreadObservation
	SessionLosses                       []engagecontent.Loss
	Account                             engagecontent.SourceAccount
	AcquiredAt, CapturedAt              time.Time
	Retained                            bool
}
type EngageFilter struct {
	Query                        string
	SourceID                     int64
	ModifiedSince, ModifiedUntil *time.Time
	Limit                        int
}
type EngageSnapshot struct {
	Rows          []EngageObservationRow
	Registrations []EngageRegistration
	Sources       []EngageSource
	Truncated     bool
}
type EngageDetailResult struct {
	Observation  EngageObservationRow
	Source       EngageSource
	Registration EngageRegistration
}

var errEngageIntegrity = errors.New("engage: corrupt archive evidence")

func engageJSON(value any) string {
	b, _ := json.Marshal(value)
	return string(b)
}
func engageBoundary(account engagecontent.SourceAccount) string {
	return engageJSON([]string{account.Host, account.NetworkID, account.UserID})
}
func engageDigest(sourceID int64, encoded string) string {
	return engageDigestFor(sourceID, EngageQualification, encoded)
}
func engageDigestFor(sourceID int64, qualification, encoded string) string {
	return hashOf(engageJSON([]string{strconv.FormatInt(sourceID, 10), qualification, encoded}))
}
func engageCoverage(r engagecontent.Result) string {
	if r.Selection != nil {
		return "native_selected_thread_observations"
	}
	if r.State == "metadata_only" {
		return "metadata_only"
	}
	return "native_home_observations"
}
func engageLosses(r engagecontent.Result) map[string]int {
	out := map[string]int{}
	for _, loss := range r.Losses {
		out[loss.Code] = loss.Count
	}
	return out
}

// Nullable native clocks use pointer presence, never the Go-zero sentinel.
func engageNativeClock(at *time.Time) any {
	if at == nil {
		return nil
	}
	return at.UTC().Format("2006-01-02T15:04:05.000000000Z")
}
func engageIndex(session EngageSession, thread engagecontent.ThreadObservation) (string, string, string) {
	title := ""
	if thread.Title != nil {
		title = *thread.Title
	}
	meta := engageJSON(struct {
		Account                                 engagecontent.SourceAccount
		SessionID                               int64
		Digest                                  string
		Ordinal                                 int
		ThreadID, NetworkID, GroupID, StarterID string
		SenderID                                *string
		Selected                                *engagecontent.SelectedThread `json:",omitempty"`
	}{session.Result.Account, session.ID, session.RepresentationDigest, thread.Ordinal, thread.ID, thread.NetworkID, thread.GroupID, thread.StarterID, thread.SenderID, thread.Selected})
	body := strings.Join(thread.Blocks, "\n")
	if thread.Selected != nil {
		parts := []string{}
		for _, message := range thread.Selected.Messages {
			parts = append(parts, strings.Join(message.Blocks, "\n"))
		}
		body = strings.Join(parts, "\n")
	}
	return title, body, meta
}

func (s *Store) BeginEngageAttempt(ctx context.Context, input KnowledgeInput) (KnowledgeAttempt, error) {
	if input.Kind != EngageKind || input.Platform != "browser" || input.Locator != EngageHomeURL || input.AcquisitionSignature != "" {
		return KnowledgeAttempt{}, errors.New("engage: invalid registration")
	}
	return s.beginEngage(ctx, input)
}
func (s *Store) BeginEngageThreadAttempt(ctx context.Context, input KnowledgeInput) (KnowledgeAttempt, error) {
	target, err := engagecontent.ParseThreadURL(input.Locator)
	if err != nil || target.URL != input.Locator || input.Kind != EngageThreadKind || input.Platform != "browser" || input.AcquisitionSignature != "" {
		return KnowledgeAttempt{}, errors.New("engage: invalid thread registration")
	}
	return s.beginEngage(ctx, input)
}
func (s *Store) beginEngage(ctx context.Context, input KnowledgeInput) (KnowledgeAttempt, error) {
	return s.beginKnowledgeAttempt(ctx, input, func(tx *sql.Tx, id int64) error {
		var target string
		var priorAttempts int
		if err := tx.QueryRowContext(ctx, `select count(*) from knowledge_attempts where locator_id=?`, id).Scan(&priorAttempts); err != nil {
			return err
		}
		if priorAttempts == 0 {
			if _, err := tx.ExecContext(ctx, `insert into engage_locator_metadata(locator_id,target_url) values(?,?)`, id, input.Locator); err != nil {
				return err
			}
		}
		if err := tx.QueryRowContext(ctx, `select target_url from engage_locator_metadata where locator_id=?`, id).Scan(&target); err != nil {
			return err
		}
		if target != input.Locator {
			return errEngageIntegrity
		}
		_, err := readEngage(ctx, tx, EngageFilter{}, 0, false, id)
		return err
	}, nil)
}

func (s *Store) CommitEngageCapture(ctx context.Context, a KnowledgeAttempt, c EngageCapture) error {
	if c.AcquiredAt.IsZero() || c.AdmittedAt.IsZero() {
		return errors.New("engage: missing acquisition/admission clock")
	}
	if err := engagecontent.ValidateResult(ctx, c.Result); err != nil {
		return err
	}
	encoded := engageJSON(c.Result)
	kind, qualification, target := EngageKind, EngageQualification, EngageHomeURL
	if c.Result.Selection != nil {
		kind, qualification, target = EngageThreadKind, EngageThreadQualification, c.Result.Selection.URL
	}
	outcome := KnowledgeOutcome{Status: "partial", Qualification: qualification, At: c.AdmittedAt}
	return s.finishKnowledge(ctx, a, outcome, kind, func(ctx context.Context, tx *sql.Tx, a KnowledgeAttempt, _ KnowledgeOutcome) (int64, string, map[string]int, error) {
		// Reuse is not a repair path. Audit the complete prior namespace first.
		if _, err := readEngage(ctx, tx, EngageFilter{}, 0, false, 0); err != nil {
			return 0, "", nil, err
		}
		boundary := engageBoundary(c.Result.Account)
		if _, err := tx.ExecContext(ctx, `insert into knowledge_sources(locator_id,boundary,qualification) select ?,?,? from knowledge_locators where id=? and locator=? on conflict(locator_id,boundary,qualification) do nothing`, a.LocatorID, boundary, qualification, a.LocatorID, target); err != nil {
			return 0, "", nil, err
		}
		var id int64
		if err := tx.QueryRowContext(ctx, `select s.id from knowledge_sources s join knowledge_locators l on l.id=s.locator_id where s.locator_id=? and s.boundary=? and s.qualification=? and l.locator=?`, a.LocatorID, boundary, qualification, target).Scan(&id); err != nil {
			return 0, "", nil, err
		}
		if _, err := tx.ExecContext(ctx, `insert into engage_source_metadata(source_id,account_json) values(?,?) on conflict(source_id) do nothing`, id, engageJSON(c.Result.Account)); err != nil {
			return 0, "", nil, err
		}
		digest := engageDigestFor(id, qualification, encoded)
		// finishKnowledge's outcome is value-captured before admission. Bind its
		// content digest here in the same transaction, and retain it below.
		if _, err := tx.ExecContext(ctx, `update knowledge_attempts set content_digest=? where id=?`, digest, a.ID); err != nil {
			return 0, "", nil, err
		}
		var sessionID int64
		err := tx.QueryRowContext(ctx, `select rowid from engage_sessions where source_id=? and representation_digest=?`, id, digest).Scan(&sessionID)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			res, err := tx.ExecContext(ctx, `insert into engage_sessions(source_id,capture_id,representation_digest,qualification,result_json,acquired_at,captured_at) values(?,?,?,?,?,?,?)`, id, a.ID, digest, qualification, encoded, knowledgeTime(c.AcquiredAt), knowledgeTime(c.AdmittedAt))
			if err != nil {
				return 0, "", nil, err
			}
			sessionID, err = res.LastInsertId()
			if err != nil {
				return 0, "", nil, err
			}
			session := EngageSession{ID: sessionID, SourceID: id, RepresentationDigest: digest, Result: c.Result}
			for _, thread := range c.Result.Threads {
				res, err := tx.ExecContext(ctx, `insert into engage_occurrences(source_id,session_id,ordinal,native_thread_id,native_starter_id,starter_modified_at) values(?,?,?,?,?,?)`, id, sessionID, thread.Ordinal, thread.ID, thread.StarterID, engageNativeClock(thread.StarterUpdatedAt))
				if err != nil {
					return 0, "", nil, err
				}
				rowID, err := res.LastInsertId()
				if err != nil {
					return 0, "", nil, err
				}
				title, text, meta := engageIndex(session, thread)
				if _, err := tx.ExecContext(ctx, `insert into engage_fts(rowid,title,text,metadata) values(?,?,?,?)`, rowID, title, text, meta); err != nil {
					return 0, "", nil, err
				}
			}
		case err != nil:
			return 0, "", nil, err
		}
		if _, err := tx.ExecContext(ctx, `insert into engage_capture_members(capture_id,session_id) values(?,?)`, a.ID, sessionID); err != nil {
			return 0, "", nil, err
		}
		var good any
		if len(c.Result.Threads) > 0 {
			good = sessionID
		}
		if _, err := tx.ExecContext(ctx, `insert into engage_source_state(source_id,current_session_id,last_good_session_id) values(?,?,?) on conflict(source_id) do update set current_session_id=excluded.current_session_id,last_good_session_id=coalesce(excluded.last_good_session_id,last_good_session_id)`, id, sessionID, good); err != nil {
			return 0, "", nil, err
		}
		if _, err := tx.ExecContext(ctx, `update knowledge_sources set admitted_attempt=?,source_observed_at=null where id=?`, a.ID, id); err != nil {
			return 0, "", nil, err
		}
		if _, err := tx.ExecContext(ctx, `update knowledge_locators set last_admitted=? where id=?`, a.ID, a.LocatorID); err != nil {
			return 0, "", nil, err
		}
		return id, engageCoverage(c.Result), engageLosses(c.Result), nil
	})
}

func validEngageFailure(status, reason string) bool {
	if status != "failed" && status != "unavailable" && status != "cancelled" {
		return false
	}
	switch reason {
	case "engage_unavailable", "engage_cancelled", "engage_timeout", "engage_browser_failed", "engage_early_observer_unsupported", "engage_cleanup_failed", "engage_identity_unverified", "engage_too_large", "engage_malformed", "engage_capture_failed":
		return true
	}
	return false
}
func (s *Store) RecordEngageFailure(ctx context.Context, a KnowledgeAttempt, f EngageFailure) error {
	if !validEngageFailure(f.Status, f.Reason) {
		return errors.New("engage: invalid failure")
	}
	return s.finishKnowledge(ctx, a, KnowledgeOutcome{Status: f.Status, Reason: f.Reason, At: f.At}, EngageKind, nil)
}
func (s *Store) RecordEngageThreadFailure(ctx context.Context, a KnowledgeAttempt, f EngageFailure) error {
	if !validEngageFailure(f.Status, f.Reason) {
		return errors.New("engage: invalid thread failure")
	}
	return s.finishKnowledge(ctx, a, KnowledgeOutcome{Status: f.Status, Reason: f.Reason, At: f.At}, EngageThreadKind, nil)
}
