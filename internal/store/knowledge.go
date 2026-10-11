package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/ourostack/m365crawl/internal/onenote"
)

const NotesQualification = "onenote-index-v1/mapper-v1"

var (
	ErrStaleKnowledgeAttempt  = errors.New("knowledge: stale attempt")
	ErrKnowledgeSourceUnknown = errors.New("knowledge: source not found")
)

type KnowledgeInput struct {
	Kind, Platform, Locator, AcquisitionSignature string
	At                                            time.Time
}

type KnowledgeAttempt struct{ ID, LocatorID int64 }

type KnowledgeOutcome struct {
	Status, Reason, AcquisitionSignature, ContentDigest, Boundary, Qualification string
	SourceObservedAt                                                             time.Time
	Losses                                                                       map[string]int
	At                                                                           time.Time
}

type KnowledgeSource struct {
	ID, LocatorID, AttemptID, AdmittedID                       int64
	Kind, Platform, Locator, Boundary, Status, Coverage        string
	AcquisitionSignature, ContentDigest, Qualification, Reason string
	AttemptedAt, AdmittedAt, CompleteAt, SourceObservedAt      *time.Time
	Losses                                                     map[string]int
}

func nullableKnowledge(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func knowledgeTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value.UTC().Format("2006-01-02T15:04:05.000000000Z")
}

func knowledgeTimes(values ...sql.NullString) ([]*time.Time, error) {
	out := make([]*time.Time, len(values))
	for i, value := range values {
		if !value.Valid {
			continue
		}
		at, err := time.Parse(time.RFC3339Nano, value.String)
		if err != nil {
			return nil, errors.New("knowledge: invalid stored timestamp")
		}
		out[i] = &at
	}
	return out, nil
}

func (s *Store) hasKnowledgeSchema(ctx context.Context) (bool, error) {
	return hasKnowledgeSchema(ctx, s.db)
}

type knowledgeReader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func hasKnowledgeSchema(ctx context.Context, reader knowledgeReader) (bool, error) {
	var count int
	err := reader.QueryRowContext(ctx, `select count(*) from sqlite_master where type='table'
and name in ('knowledge_locators','knowledge_attempts','knowledge_sources','note_entities','note_elements','note_flags','note_fts')`).Scan(&count)
	return count == 7, err
}

func (s *Store) BeginKnowledgeAttempt(ctx context.Context, input KnowledgeInput) (KnowledgeAttempt, error) {
	if input.Kind == "" || input.Platform == "" || input.Locator == "" || input.At.IsZero() {
		return KnowledgeAttempt{}, errors.New("knowledge: invalid input")
	}
	var attempt KnowledgeAttempt
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `insert into knowledge_locators(kind,platform,locator)
values(?,?,?) on conflict(kind,platform,locator) do nothing`, input.Kind, input.Platform, input.Locator); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `select id from knowledge_locators where kind=? and platform=? and locator=?`,
			input.Kind, input.Platform, input.Locator).Scan(&attempt.LocatorID); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `insert into knowledge_attempts(locator_id,started_at,acquisition_signature)
values(?,?,?)`, attempt.LocatorID, knowledgeTime(input.At), nullableKnowledge(input.AcquisitionSignature))
		if err != nil {
			return err
		}
		attempt.ID, err = result.LastInsertId()
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `update knowledge_locators set latest_attempt=? where id=?`, attempt.ID, attempt.LocatorID)
		return err
	})
	if err != nil {
		return KnowledgeAttempt{}, err
	}
	return attempt, nil
}

func (s *Store) RecordKnowledgeFailure(ctx context.Context, attempt KnowledgeAttempt, outcome KnowledgeOutcome) error {
	switch outcome.Status {
	case "failed", "unavailable", "cancelled":
	default:
		return errors.New("knowledge: invalid failure outcome")
	}
	return s.finishNotes(ctx, attempt, outcome, nil)
}

func (s *Store) CommitNotes(ctx context.Context, attempt KnowledgeAttempt, outcome KnowledgeOutcome, result onenote.Result) error {
	switch outcome.Status {
	case "complete", "partial", "unchanged":
	default:
		return errors.New("knowledge: invalid notes outcome")
	}
	if outcome.ContentDigest == "" || (outcome.Qualification != "" && outcome.Qualification != NotesQualification) {
		return errors.New("knowledge: unqualified notes outcome")
	}
	if outcome.Status != "unchanged" && ((len(result.Losses) > 0) != (outcome.Status == "partial")) {
		return errors.New("knowledge: notes coverage mismatch")
	}
	outcome.Qualification = NotesQualification
	return s.finishNotes(ctx, attempt, outcome, &result)
}

func (s *Store) finishNotes(ctx context.Context, attempt KnowledgeAttempt, outcome KnowledgeOutcome, result *onenote.Result) error {
	var admit knowledgeAdmission
	kind := ""
	if result != nil {
		kind = "notes"
		admit = func(ctx context.Context, tx *sql.Tx, attempt KnowledgeAttempt, outcome KnowledgeOutcome) (int64, string, map[string]int, error) {
			return admitNotes(ctx, tx, attempt, outcome, *result)
		}
	}
	return s.finishKnowledge(ctx, attempt, outcome, kind, admit)
}

type knowledgeAdmission func(context.Context, *sql.Tx, KnowledgeAttempt, KnowledgeOutcome) (int64, string, map[string]int, error)

func (s *Store) finishKnowledge(ctx context.Context, attempt KnowledgeAttempt, outcome KnowledgeOutcome, kind string, admit knowledgeAdmission) error {
	if attempt.ID <= 0 || attempt.LocatorID <= 0 || outcome.At.IsZero() {
		return errors.New("knowledge: invalid attempt")
	}
	stale := false
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var latest, frontier int64
		var actualKind string
		var terminal sql.NullString
		if err := tx.QueryRowContext(ctx, `select l.latest_attempt,coalesce(l.last_admitted,0),a.finished_at,l.kind
from knowledge_locators l join knowledge_attempts a on a.locator_id=l.id where a.id=? and l.id=?`,
			attempt.ID, attempt.LocatorID).Scan(&latest, &frontier, &terminal, &actualKind); err != nil {
			return err
		}
		if kind != "" && actualKind != kind {
			return errors.New("knowledge: source kind mismatch")
		}
		if terminal.Valid {
			stale = true
			return nil
		}
		var sourceID any
		coverage := "unknown"
		losses := map[string]int{}
		if latest != attempt.ID || frontier >= attempt.ID {
			stale = true
			coverage = "not_admitted"
			outcome.Reason = "stale_attempt"
		} else if admit != nil {
			id, c, l, err := admit(ctx, tx, attempt, outcome)
			switch {
			case errors.Is(err, ErrStaleKnowledgeAttempt):
				stale = true
				coverage = "not_admitted"
				outcome.Reason = "stale_attempt"
			case err != nil:
				return err
			default:
				sourceID, coverage, losses = id, c, l
			}
		}
		encoded, _ := json.Marshal(losses) // map[string]int has no unsupported or cyclic JSON values
		_, err := tx.ExecContext(ctx, `update knowledge_attempts set source_id=?,finished_at=?,outcome=?,
acquisition_signature=coalesce(?,acquisition_signature),content_digest=?,coverage=?,losses_json=?,reason=? where id=?`,
			sourceID, knowledgeTime(outcome.At), outcome.Status, nullableKnowledge(outcome.AcquisitionSignature),
			nullableKnowledge(outcome.ContentDigest), coverage, string(encoded), outcome.Reason, attempt.ID)
		return err
	})
	if err != nil {
		return err
	}
	if stale {
		return ErrStaleKnowledgeAttempt
	}
	return nil
}

func notesBoundary(result onenote.Result) string {
	var ids []string
	for _, entity := range result.Entities {
		if entity.Type == 4 {
			ids = append(ids, entity.GOID)
		}
	}
	if len(ids) == 0 {
		return "unresolved"
	}
	sort.Strings(ids)
	encoded, _ := json.Marshal(ids)
	return "notebooks:" + hashOf(string(encoded))
}

func admitNotes(ctx context.Context, tx *sql.Tx, attempt KnowledgeAttempt, outcome KnowledgeOutcome, result onenote.Result) (int64, string, map[string]int, error) {
	var previousID int64
	var previousBoundary, previousQualification, previousDigest, previousCoverage, previousLosses string
	err := tx.QueryRowContext(ctx, `select s.id,s.boundary,s.qualification,coalesce(a.content_digest,''),a.coverage,a.losses_json
from knowledge_locators l join knowledge_attempts a on a.id=l.last_admitted
join knowledge_sources s on s.id=a.source_id where l.id=?`, attempt.LocatorID).
		Scan(&previousID, &previousBoundary, &previousQualification, &previousDigest, &previousCoverage, &previousLosses)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, "", nil, err
	}
	boundary := notesBoundary(result)
	coverage := "indexed"
	losses := map[string]int{}
	if outcome.Status == "partial" {
		boundary = "unresolved"
		if previousID != 0 {
			boundary = previousBoundary
		}
		coverage = "partial"
		for _, loss := range result.Losses {
			losses[loss.Code] += loss.Count
		}
	}
	if outcome.Status == "unchanged" {
		if previousID == 0 || previousDigest != outcome.ContentDigest || previousQualification != outcome.Qualification ||
			(outcome.Boundary != "" && outcome.Boundary != previousBoundary) {
			return 0, "", nil, errors.New("knowledge: unchanged input not admitted")
		}
		boundary, coverage = previousBoundary, previousCoverage
		if err := json.Unmarshal([]byte(previousLosses), &losses); err != nil {
			return 0, "", nil, err
		}
	}
	if outcome.Boundary != "" && outcome.Boundary != boundary {
		return 0, "", nil, errors.New("knowledge: notebook boundary mismatch")
	}
	if _, err := tx.ExecContext(ctx, `insert into knowledge_sources(locator_id,boundary,qualification) values(?,?,?)
on conflict(locator_id,boundary,qualification) do nothing`, attempt.LocatorID, boundary, outcome.Qualification); err != nil {
		return 0, "", nil, err
	}
	var id int64
	if err := tx.QueryRowContext(ctx, `select id from knowledge_sources where locator_id=? and boundary=? and qualification=?`,
		attempt.LocatorID, boundary, outcome.Qualification).Scan(&id); err != nil {
		return 0, "", nil, err
	}
	var admitted int64
	if err := tx.QueryRowContext(ctx, `select coalesce(admitted_attempt,0) from knowledge_sources where id=?`, id).Scan(&admitted); err != nil {
		return 0, "", nil, err
	}
	if admitted >= attempt.ID {
		return 0, "", nil, ErrStaleKnowledgeAttempt
	}
	if outcome.Status != "unchanged" {
		if err := applyNotes(ctx, tx, id, attempt.ID, result, outcome.Status == "partial"); err != nil {
			return 0, "", nil, err
		}
	}
	if _, err := tx.ExecContext(ctx, `update knowledge_sources set admitted_attempt=?,
complete_attempt=case when ?='indexed' then ? else complete_attempt end,source_observed_at=? where id=?`,
		attempt.ID, coverage, attempt.ID, knowledgeTime(outcome.SourceObservedAt), id); err != nil {
		return 0, "", nil, err
	}
	if _, err := tx.ExecContext(ctx, `update knowledge_locators set last_admitted=? where id=?`, attempt.ID, attempt.LocatorID); err != nil {
		return 0, "", nil, err
	}
	return id, coverage, losses, nil
}

func (s *Store) KnowledgeSources(ctx context.Context, kind string) ([]KnowledgeSource, error) {
	available, err := s.hasKnowledgeSchema(ctx)
	if err != nil || !available {
		return []KnowledgeSource{}, err
	}
	rows, err := s.db.QueryContext(ctx, `select coalesce(s.id,0),l.id,coalesce(a.id,0),coalesce(s.admitted_attempt,0),
l.kind,l.platform,l.locator,coalesce(s.boundary,''),coalesce(s.qualification,''),
case when s.id<>current.source_id then 'retained' else coalesce(a.outcome,'unknown') end,
coalesce(g.coverage,'unknown'),coalesce(a.acquisition_signature,''),coalesce(g.content_digest,''),
case when s.id<>current.source_id then 'source_incarnation_retained' else coalesce(a.reason,'') end,
a.started_at,g.finished_at,c.finished_at,s.source_observed_at,coalesce(g.losses_json,'{}')
from knowledge_locators l left join knowledge_sources s on s.locator_id=l.id
left join knowledge_attempts a on a.id=l.latest_attempt
left join knowledge_attempts current on current.id=l.last_admitted
left join knowledge_attempts g on g.id=s.admitted_attempt
left join knowledge_attempts c on c.id=s.complete_attempt
where l.kind=? order by l.id,s.id`, kind)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []KnowledgeSource{}
	for rows.Next() {
		var source KnowledgeSource
		var attempted, admitted, complete, observed sql.NullString
		var losses string
		if err := rows.Scan(&source.ID, &source.LocatorID, &source.AttemptID, &source.AdmittedID,
			&source.Kind, &source.Platform, &source.Locator, &source.Boundary, &source.Qualification,
			&source.Status, &source.Coverage, &source.AcquisitionSignature, &source.ContentDigest,
			&source.Reason, &attempted, &admitted, &complete, &observed, &losses); err != nil {
			return nil, err
		}
		times, err := knowledgeTimes(attempted, admitted, complete, observed)
		if err != nil {
			return nil, err
		}
		source.AttemptedAt, source.AdmittedAt, source.CompleteAt, source.SourceObservedAt = times[0], times[1], times[2], times[3]
		if err := json.Unmarshal([]byte(losses), &source.Losses); err != nil {
			return nil, err
		}
		out = append(out, source)
	}
	return out, rows.Err()
}

func (s *Store) KnowledgeLocators(ctx context.Context, sourceID int64) ([]KnowledgeSource, error) {
	return s.KnowledgeKindLocators(ctx, "notes", sourceID)
}

func (s *Store) KnowledgeKindLocators(ctx context.Context, kind string, sourceID int64) ([]KnowledgeSource, error) {
	sources, err := s.KnowledgeSources(ctx, kind)
	if err != nil {
		return nil, err
	}
	selected := []KnowledgeSource{}
	for _, source := range sources {
		if sourceID > 0 {
			if source.ID == sourceID {
				for _, current := range sources {
					if current.LocatorID == source.LocatorID && current.Status != "retained" {
						return []KnowledgeSource{current}, nil
					}
				}
				return nil, ErrKnowledgeSourceUnknown
			}
		} else if source.Status != "retained" {
			selected = append(selected, source)
		}
	}
	if sourceID != 0 {
		return nil, ErrKnowledgeSourceUnknown
	}
	return selected, nil
}
