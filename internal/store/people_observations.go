package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/ourostack/m365crawl/internal/outlookpeople"
)

const PeopleQualification = "hxstore-people-v1/mapper-v1"

func (s *Store) PeopleProfileLink(ctx context.Context, account string) (PeopleCaptureLink, error) {
	link, err := readPeopleLink(ctx, s.db, `select account_id,principal_id,method,linked_at from calendar_account_links
where source='outlook' and account_id=? and unlinked_at is null`, account)
	if link.ProfileAccount == "" {
		link.ProfileAccount = account
	}
	return link, err
}

type PeopleCaptureLink struct {
	ProfileAccount, Principal, Method string
	LinkedAt                          *time.Time
}

type PeopleCaptureMetadata struct {
	AcquiredAt, SourceObservedAt *time.Time
	Reused                       bool
	Link                         PeopleCaptureLink
}

type PeopleObservationRow struct {
	ID, SourceID, CaptureID          int64
	Person                           outlookpeople.Person
	CapturedAt, SourceObservedAt     *time.Time
	Retained, Ambiguous, Conflicting bool
}

type PeopleObservationDetail struct {
	PeopleObservationRow
	Copies, Conflicts         []PeopleObservationRow
	CapturedLink, CurrentLink PeopleCaptureLink
	IdentityState             string
}

func optionalPeopleTime(at *time.Time) any {
	if at == nil {
		return nil
	}
	return knowledgeTime(*at)
}

func (s *Store) CommitPeopleObservations(ctx context.Context, attempt KnowledgeAttempt, outcome KnowledgeOutcome, result outlookpeople.Result, capture PeopleCaptureMetadata) error {
	switch outcome.Status {
	case "complete", "partial", "unchanged":
	default:
		return errors.New("knowledge: invalid people outcome")
	}
	if outcome.ContentDigest == "" || outcome.Qualification != PeopleQualification ||
		(outcome.Status != "unchanged" && (len(result.Losses) > 0) != (outcome.Status == "partial")) {
		return errors.New("knowledge: unqualified people outcome")
	}
	return s.finishKnowledge(ctx, attempt, outcome, "outlook_people",
		func(ctx context.Context, tx *sql.Tx, attempt KnowledgeAttempt, outcome KnowledgeOutcome) (int64, string, map[string]int, error) {
			return admitPeopleObservations(ctx, tx, attempt, outcome, result, capture)
		})
}

func admitPeopleObservations(ctx context.Context, tx *sql.Tx, attempt KnowledgeAttempt, outcome KnowledgeOutcome, result outlookpeople.Result, capture PeopleCaptureMetadata) (int64, string, map[string]int, error) {
	var previous, previousSource int64
	var digest, qualification, coverage, encoded string
	err := tx.QueryRowContext(ctx, `select a.id,s.id,coalesce(a.content_digest,''),s.qualification,a.coverage,a.losses_json
from knowledge_locators l join knowledge_attempts a on a.id=l.last_admitted
join knowledge_sources s on s.id=a.source_id where l.id=?`, attempt.LocatorID).
		Scan(&previous, &previousSource, &digest, &qualification, &coverage, &encoded)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, "", nil, err
	}
	losses := map[string]int{}
	if outcome.Status == "unchanged" {
		if previous == 0 || digest != outcome.ContentDigest || qualification != PeopleQualification {
			return 0, "", nil, errors.New("knowledge: unchanged people not admitted")
		}
		if err := json.Unmarshal([]byte(encoded), &losses); err != nil {
			return 0, "", nil, err
		}
	} else {
		coverage = "observations"
		if outcome.Status == "partial" {
			coverage = "partial"
		}
		for _, loss := range result.Losses {
			losses[loss.Code] += loss.Count
		}
	}
	if _, err := tx.ExecContext(ctx, `insert into knowledge_sources(locator_id,boundary,qualification)
values(?,'unresolved',?) on conflict(locator_id,boundary,qualification) do nothing`, attempt.LocatorID, PeopleQualification); err != nil {
		return 0, "", nil, err
	}
	var sourceID int64
	if err := tx.QueryRowContext(ctx, `select id from knowledge_sources where locator_id=? and boundary='unresolved' and qualification=?`,
		attempt.LocatorID, PeopleQualification).Scan(&sourceID); err != nil {
		return 0, "", nil, err
	}
	var frontier int64
	if err := tx.QueryRowContext(ctx, "select coalesce(admitted_attempt,0) from knowledge_sources where id=?", sourceID).Scan(&frontier); err != nil {
		return 0, "", nil, err
	}
	if frontier >= attempt.ID {
		return 0, "", nil, ErrStaleKnowledgeAttempt
	}
	physical := len(result.People)
	if outcome.Status == "unchanged" {
		if previousSource != sourceID {
			return 0, "", nil, errors.New("knowledge: people source mismatch")
		}
		if err := tx.QueryRowContext(ctx, "select physical_observations from people_capture_metadata where capture_id=?", previous).Scan(&physical); err != nil {
			return 0, "", nil, err
		}
	}
	if _, err := tx.ExecContext(ctx, `insert into people_capture_metadata(capture_id,acquired_at,source_observed_at,reused,physical_observations)
values(?,?,?,?,?)`, attempt.ID, optionalPeopleTime(capture.AcquiredAt), optionalPeopleTime(capture.SourceObservedAt), outcome.Status == "unchanged", physical); err != nil {
		return 0, "", nil, err
	}
	if _, err := tx.ExecContext(ctx, `insert into people_capture_links(capture_id,profile_account,principal,method,linked_at) values(?,?,?,?,?)`,
		attempt.ID, capture.Link.ProfileAccount, capture.Link.Principal, capture.Link.Method, optionalPeopleTime(capture.Link.LinkedAt)); err != nil {
		return 0, "", nil, err
	}
	if outcome.Status == "unchanged" {
		if _, err := tx.ExecContext(ctx, `insert into people_capture_members(capture_id,observation_rowid)
select ?,observation_rowid from people_capture_members where capture_id=?`, attempt.ID, previous); err != nil {
			return 0, "", nil, err
		}
	} else if err := applyPeopleObservations(ctx, tx, sourceID, attempt.ID, result); err != nil {
		return 0, "", nil, err
	}
	if _, err := tx.ExecContext(ctx, `update knowledge_sources set admitted_attempt=?,
complete_attempt=case when ?='observations' then ? else complete_attempt end,source_observed_at=? where id=?`,
		attempt.ID, coverage, attempt.ID, knowledgeTime(outcome.SourceObservedAt), sourceID); err != nil {
		return 0, "", nil, err
	}
	if _, err := tx.ExecContext(ctx, "update knowledge_locators set last_admitted=? where id=?", attempt.ID, attempt.LocatorID); err != nil {
		return 0, "", nil, err
	}
	return sourceID, coverage, losses, nil
}

func applyPeopleObservations(ctx context.Context, tx *sql.Tx, sourceID, captureID int64, result outlookpeople.Result) error {
	for _, p := range result.People {
		encoded, err := json.Marshal(p)
		if err != nil {
			return err
		}
		digest := hashOf(string(encoded))
		if _, err := tx.ExecContext(ctx, `insert into people_observations(source_id,capture_id,observation_digest,native_class,native_key,
native_parent,version_raw,resynced,display_name,first_name,last_name,email,alternate_email,teams_id,refreshed_at)
values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) on conflict(source_id,observation_digest) do nothing`,
			sourceID, captureID, digest, p.Class, p.Key, p.Parent, strconv.FormatUint(p.VersionRaw, 10), p.Resynced,
			p.DisplayName, p.FirstName, p.LastName, p.Email, p.AlternateEmail, p.TeamsID, knowledgeTime(p.RefreshedAt)); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `insert into people_capture_members(capture_id,observation_rowid)
select ?,rowid from people_observations where source_id=? and observation_digest=?
on conflict(capture_id,observation_rowid) do nothing`, captureID, sourceID, digest); err != nil {
			return err
		}
		if p.Resynced {
			if err := markPeopleAmbiguous(ctx, tx, sourceID, captureID, outlookpeople.ObjectKey{Class: p.Class, Key: p.Key}); err != nil {
				return err
			}
		}
	}
	for _, key := range result.AmbiguousKeys {
		if err := markPeopleAmbiguous(ctx, tx, sourceID, captureID, key); err != nil {
			return err
		}
	}
	_, err := tx.ExecContext(ctx, `insert into people_group_ambiguities(source_id,native_class,native_key,first_capture_id)
select source_id,native_class,native_key,? from people_observations where source_id=?
group by source_id,native_class,native_key having count(*)>1
on conflict(source_id,native_class,native_key) do nothing`, captureID, sourceID)
	return err
}

func markPeopleAmbiguous(ctx context.Context, tx *sql.Tx, sourceID, captureID int64, key outlookpeople.ObjectKey) error {
	_, err := tx.ExecContext(ctx, `insert into people_group_ambiguities(source_id,native_class,native_key,first_capture_id)
values(?,?,?,?) on conflict(source_id,native_class,native_key) do nothing`, sourceID, key.Class, key.Key, captureID)
	return err
}

const peopleObservationSelect = `select p.rowid,p.source_id,p.capture_id,p.native_class,p.native_key,p.native_parent,p.version_raw,p.resynced,
p.display_name,p.first_name,p.last_name,p.email,p.alternate_email,p.teams_id,p.refreshed_at,c.acquired_at,c.source_observed_at,
not exists(select 1 from people_capture_members m where m.capture_id=s.admitted_attempt and m.observation_rowid=p.rowid),
exists(select 1 from people_group_ambiguities g where g.source_id=p.source_id and g.native_class=p.native_class and g.native_key=p.native_key),
exists(select 1 from people_observations o where o.source_id=p.source_id and lower(trim(o.email))=lower(trim(p.email))
and trim(p.email)<>'' and o.teams_id<>'' and p.teams_id<>'' and o.teams_id<>p.teams_id)
from people_observations p join people_capture_metadata c on c.capture_id=p.capture_id join knowledge_sources s on s.id=p.source_id`

func readPeopleObservations(ctx context.Context, reader knowledgeReader, clause string, args ...any) (out []PeopleObservationRow, err error) {
	rows, err := reader.QueryContext(ctx, peopleObservationSelect+clause, args...)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	out = []PeopleObservationRow{}
	for rows.Next() {
		var row PeopleObservationRow
		var version string
		var refreshed, captured, observed sql.NullString
		p := &row.Person
		if err := rows.Scan(&row.ID, &row.SourceID, &row.CaptureID, &p.Class, &p.Key, &p.Parent, &version, &p.Resynced,
			&p.DisplayName, &p.FirstName, &p.LastName, &p.Email, &p.AlternateEmail, &p.TeamsID, &refreshed, &captured, &observed,
			&row.Retained, &row.Ambiguous, &row.Conflicting); err != nil {
			return nil, err
		}
		p.VersionRaw, err = strconv.ParseUint(version, 10, 64)
		if err != nil {
			return nil, errors.New("knowledge: invalid stored people version")
		}
		times, err := knowledgeTimes(refreshed, captured, observed)
		if err != nil {
			return nil, err
		}
		if times[0] != nil {
			p.RefreshedAt = *times[0]
		}
		row.CapturedAt, row.SourceObservedAt = times[1], times[2]
		out = append(out, row)
	}
	return out, rows.Err()
}

func (s *Store) hasPeopleObservations(ctx context.Context) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `select count(*) from sqlite_master where type='table' and name in
('people_observations','people_capture_members','people_group_ambiguities','people_capture_metadata','people_capture_links')`).Scan(&count)
	return count == 5, err
}

func (s *Store) PeopleObservations(ctx context.Context, query string, sourceID int64, limit int) ([]PeopleObservationRow, bool, error) {
	available, err := s.hasPeopleObservations(ctx)
	if err != nil || !available {
		return []PeopleObservationRow{}, false, err
	}
	if limit <= 0 {
		return nil, false, errors.New("knowledge: invalid people limit")
	}
	clause := " where 1=1"
	args := []any{}
	if sourceID > 0 {
		clause += " and p.source_id=?"
		args = append(args, sourceID)
	}
	if strings.HasPrefix(query, "person-observation:") {
		id, err := strconv.ParseInt(strings.TrimPrefix(query, "person-observation:"), 10, 64)
		if err != nil {
			return nil, false, errors.New("knowledge: invalid people observation id")
		}
		clause += " and p.rowid=?"
		args = append(args, id)
	} else if query != "" {
		clause += ` and (p.teams_id=? or lower(p.display_name) like ? escape '\' or lower(p.email) like ? escape '\')`
		pattern := "%" + strings.ToLower(escapeLike(query)) + "%"
		args = append(args, query, pattern, pattern)
	}
	clause += " order by p.display_name collate nocase,p.source_id,p.rowid limit ?"
	args = append(args, limit+1)
	rows, err := readPeopleObservations(ctx, s.db, clause, args...)
	if err != nil {
		return nil, false, err
	}
	truncated := len(rows) > limit
	if truncated {
		rows = rows[:limit]
	}
	return rows, truncated, nil
}

func readPeopleLink(ctx context.Context, reader knowledgeReader, clause string, args ...any) (PeopleCaptureLink, error) {
	var link PeopleCaptureLink
	var linked sql.NullString
	err := reader.QueryRowContext(ctx, clause, args...).Scan(&link.ProfileAccount, &link.Principal, &link.Method, &linked)
	if errors.Is(err, sql.ErrNoRows) {
		return link, nil
	}
	if err != nil {
		return link, err
	}
	times, err := knowledgeTimes(linked)
	if err != nil {
		return link, err
	}
	link.LinkedAt = times[0]
	return link, nil
}

func (s *Store) PeopleObservation(ctx context.Context, id int64) (detail PeopleObservationDetail, err error) {
	available, err := s.hasPeopleObservations(ctx)
	if err != nil {
		return detail, err
	}
	if !available {
		return detail, sql.ErrNoRows
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return detail, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := readPeopleObservations(ctx, tx, " where p.rowid=?", id)
	if err != nil {
		return detail, err
	}
	if len(rows) == 0 {
		return detail, sql.ErrNoRows
	}
	detail.PeopleObservationRow = rows[0]
	detail.IdentityState = "unresolved"
	p := detail.Person
	detail.Copies, err = readPeopleObservations(ctx, tx, " where p.source_id=? and p.native_class=? and p.native_key=? order by p.rowid",
		detail.SourceID, p.Class, p.Key)
	if err != nil {
		return detail, err
	}
	detail.Conflicts, err = readPeopleObservations(ctx, tx, " where p.source_id=? and lower(trim(p.email))=? and ?<>'' and ?<>'' and p.teams_id<>? and p.teams_id<>'' order by p.rowid",
		detail.SourceID, strings.ToLower(strings.TrimSpace(p.Email)), strings.TrimSpace(p.Email), p.TeamsID, p.TeamsID)
	if err != nil {
		return detail, err
	}
	detail.CapturedLink, err = readPeopleLink(ctx, tx, "select profile_account,principal,method,linked_at from people_capture_links where capture_id=?", detail.CaptureID)
	if err != nil {
		return detail, err
	}
	if detail.CapturedLink.ProfileAccount != "" {
		detail.CurrentLink, err = readPeopleLink(ctx, tx, `select account_id,principal_id,method,linked_at from calendar_account_links
where source='outlook' and account_id=? and unlinked_at is null`, detail.CapturedLink.ProfileAccount)
		if err != nil {
			return detail, err
		}
	}
	return detail, tx.Commit()
}
