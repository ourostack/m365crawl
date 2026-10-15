package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/ourostack/m365crawl/internal/outlookattendees"
	"github.com/ourostack/m365crawl/internal/outlookcal"
	"github.com/ourostack/m365crawl/internal/outlookmeetingattendees"
)

const (
	AttendeeKind          = "outlook_attendees"
	AttendeeQualification = "hxstore-attendees-v1/mapper-v1"
	AttendeeScope         = "native_source_selected_detail_not_display_roster"
	attendeeCoverage      = "native_selected_detail_observations"
)

type AttendeeRequest struct {
	Root     string
	SourceID int64
	Discover bool
}
type AttendeeCapture struct {
	Evidence               outlookmeetingattendees.Evidence
	Census                 outlookmeetingattendees.Census
	InputDigest            string
	CalendarReadID         int64
	AcquiredAt, AdmittedAt time.Time
	SourceObservedAt       *time.Time
}
type AttendeeFilter struct {
	SourceID int64
	Limit    int
	History  bool
}
type AttendeeRow struct {
	ID, SourceID, SnapshotID, CaptureID int64
	Ordinal                             int
	RepresentationDigest, Qualification string
	Native                              outlookattendees.Attendee
	Losses                              []outlookattendees.Loss
	AcquiredAt                          time.Time
	SourceObservedAt                    *time.Time
	Retained, Ambiguous                 bool
	ScopeState                          string
}
type AttendeeBinding struct {
	ID, SourceID, SnapshotID                                              int64
	Selection                                                             outlookcal.SelectionReceipt
	Retained, MatchesRead, MatchesOrigin, MatchesLink, CalendarRowPresent bool
}
type AttendeeSourceSummary struct {
	RegistrationID                                              int64
	SourceID, AttemptID, AdmittedID, SnapshotID, CalendarReadID *int64
	LinkRevision, CurrentReadID, CurrentLinkRevision            *int64
	Locator, Account, Status, Reason                            string
	RepresentationDigest, Qualification                         string
	OriginState, CalendarState, LinkState, ScopeState           string
	CapturedLink, CurrentLink                                   PeopleCaptureLink
	AttendeesTotal, ParentsTotal, BindingsTotal                 int64
	Losses                                                      []outlookattendees.Loss
	AttemptedAt, AdmittedAt, AcquiredAt, SourceObservedAt       *time.Time
}
type AttendeeCoverage struct {
	Scope   string
	Sources []AttendeeSourceSummary
}
type AttendeeSnapshot struct {
	EventID                      string
	Rows                         []AttendeeRow
	Bindings                     []AttendeeBinding
	Coverage                     AttendeeCoverage
	Total, BindingsTotal         int64
	Truncated, BindingsTruncated bool
}
type AttendeeDetailResult struct {
	Row               AttendeeRow
	Bindings          []AttendeeBinding
	Coverage          AttendeeCoverage
	BindingsTotal     int64
	BindingsTruncated bool
}

type attendeeRequestRecord struct {
	Root      string `json:"root"`
	SourceID  int64  `json:"source_id,string"`
	Discover  bool   `json:"discover"`
	AttemptID int64  `json:"attempt_id,string"`
	LocatorID int64  `json:"locator_id,string"`
	Locator   string `json:"locator"`
	Platform  string `json:"platform"`
	Signature string `json:"signature"`
	StartedAt string `json:"started_at"`
	Digest    string `json:"digest"`
}

func attendeeCanonicalJSON(value any) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}

func attendeeClock(at *time.Time) any {
	if at == nil {
		return nil
	}
	return at.Format(time.RFC3339Nano)
}

func attendeeValidClock(at time.Time) bool {
	_, err := time.Parse(time.RFC3339Nano, at.Format(time.RFC3339Nano))
	return err == nil
}

func attendeeStrings(values ...string) bool {
	total := 0
	for _, value := range values {
		if !utf8.ValidString(value) || len(value) > 262144 {
			return false
		}
		total += len(value)
	}
	return total <= 1048576
}

func (r attendeeRequestRecord) digest() string {
	r.Digest = ""
	return attendeeDigest("outlook-attendee-request-v1", r)
}

func (s *Store) BeginAttendeeAttempt(ctx context.Context, input KnowledgeInput, request AttendeeRequest) (KnowledgeAttempt, error) {
	if input.Kind != AttendeeKind || !attendeeValidClock(input.At) || !filepath.IsAbs(input.Locator) || filepath.Clean(input.Locator) != input.Locator ||
		filepath.Base(input.Locator) != "HxStore.hxd" || request.SourceID < 0 ||
		(request.Root != "" && request.SourceID != 0) || !attendeeStrings(input.Kind, input.Platform, input.Locator, input.AcquisitionSignature, request.Root) {
		return KnowledgeAttempt{}, outlookmeetingattendees.ErrInvalid
	}
	return s.beginKnowledgeAttempt(ctx, input, func(tx *sql.Tx, _ int64) error { return auditAttendeeNamespace(ctx, tx) },
		func(tx *sql.Tx, a KnowledgeAttempt) error {
			r := attendeeRequestRecord{Root: request.Root, SourceID: request.SourceID, Discover: request.Discover, AttemptID: a.ID, LocatorID: a.LocatorID,
				Locator: input.Locator, Platform: input.Platform, Signature: input.AcquisitionSignature, StartedAt: knowledgeTime(input.At).(string)}
			r.Digest = r.digest()
			_, err := tx.ExecContext(ctx, `insert into outlook_attendee_requests(attempt_id,request_json) values(?,?)`, a.ID, attendeeCanonicalJSON(r))
			return err
		})
}

func (s *Store) RecordAttendeeFailure(ctx context.Context, a KnowledgeAttempt, outcome KnowledgeOutcome) error {
	switch outcome.Status {
	case "failed", "unavailable", "cancelled":
	default:
		return outlookmeetingattendees.ErrInvalid
	}
	return s.finishKnowledge(ctx, a, outcome, AttendeeKind, nil)
}

func attendeeRepresentation(sourceID int64, raw []byte) string {
	h := sha256.New()
	_, _ = h.Write([]byte("[" + attendeeCanonicalJSON(AttendeeQualification) + "," + attendeeCanonicalJSON(strconv.FormatInt(sourceID, 10)) + ","))
	_, _ = h.Write(raw)
	_, _ = h.Write([]byte("]"))
	return hex.EncodeToString(h.Sum(nil))
}

func (s *Store) CommitAttendeeCapture(ctx context.Context, a KnowledgeAttempt, c AttendeeCapture) error {
	if c.AcquiredAt.IsZero() || c.AdmittedAt.IsZero() || c.CalendarReadID <= 0 ||
		!attendeeValidClock(c.AcquiredAt) || !attendeeValidClock(c.AdmittedAt) || (c.SourceObservedAt != nil && !attendeeValidClock(*c.SourceObservedAt)) {
		return outlookmeetingattendees.ErrInvalid
	}
	digest, err := hex.DecodeString(c.InputDigest)
	if err != nil || len(digest) != 32 || hex.EncodeToString(digest) != c.InputDigest {
		return outlookmeetingattendees.ErrInvalid
	}
	raw, err := outlookmeetingattendees.Encode(ctx, c.Evidence)
	if err != nil {
		return err
	}
	census, err := outlookmeetingattendees.EncodeCensus(ctx, c.Census)
	if err != nil {
		return err
	}
	outcome := KnowledgeOutcome{Status: "partial", ContentDigest: c.InputDigest, Qualification: AttendeeQualification, At: c.AdmittedAt}
	return s.finishKnowledge(ctx, a, outcome, AttendeeKind, func(ctx context.Context, tx *sql.Tx, a KnowledgeAttempt, _ KnowledgeOutcome) (int64, string, map[string]int, error) {
		if err := auditAttendeeMetadata(ctx, tx); err != nil {
			return 0, "", nil, err
		}
		request, err := readAttendeeRequest(ctx, tx, a.ID)
		if err != nil {
			return 0, "", nil, err
		}
		var previous int64
		err = tx.QueryRowContext(ctx, `select m.snapshot_id from knowledge_sources s join outlook_attendee_members m on m.attempt_id=s.admitted_attempt where s.locator_id=? and s.qualification=?`,
			a.LocatorID, AttendeeQualification).Scan(&previous)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return 0, "", nil, err
		}
		if err == nil {
			if _, _, _, err := auditAttendeeBody(ctx, tx, previous, nil, nil, 0); err != nil {
				return 0, "", nil, err
			}
		}
		read, err := attendeeReadByID(ctx, tx, c.CalendarReadID)
		if err != nil {
			return 0, "", nil, err
		}
		current, err := outlookReadReceipt(ctx, tx, read.Account)
		if err != nil {
			return 0, "", nil, err
		}
		if read.ID == 0 || read.ID != current.ID || read.Origin == "" || read.Origin != request.Locator {
			return 0, "", nil, errors.New("outlook_attendee_calendar_unassociated")
		}
		if _, err := tx.ExecContext(ctx, `insert into knowledge_sources(locator_id,boundary,qualification) values(?,'unresolved',?) on conflict(locator_id,boundary,qualification) do nothing`, a.LocatorID, AttendeeQualification); err != nil {
			return 0, "", nil, err
		}
		var sourceID int64
		if err := tx.QueryRowContext(ctx, `select id from knowledge_sources where locator_id=? and boundary='unresolved' and qualification=?`, a.LocatorID, AttendeeQualification).Scan(&sourceID); err != nil {
			return 0, "", nil, err
		}
		if _, err := tx.ExecContext(ctx, `insert into outlook_attendee_sources(source_id,calendar_account,origin) values(?,?,?) on conflict(source_id) do nothing`, sourceID, read.Account, read.Origin); err != nil {
			return 0, "", nil, err
		}
		var account, origin string
		if err := tx.QueryRowContext(ctx, `select calendar_account,origin from outlook_attendee_sources where source_id=?`, sourceID).Scan(&account, &origin); err != nil {
			return 0, "", nil, err
		}
		if account != read.Account || origin != read.Origin {
			return 0, "", nil, ErrAttendeeArchiveCorrupt
		}
		representation := attendeeRepresentation(sourceID, raw)
		var snapshotID int64
		err = tx.QueryRowContext(ctx, `select rowid from outlook_attendee_snapshots where source_id=? and representation_digest=?`, sourceID, representation).Scan(&snapshotID)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			result, err := tx.ExecContext(ctx, `insert into outlook_attendee_snapshots(source_id,first_attempt_id,representation_digest,qualification,result_json,acquired_at,source_observed_at) values(?,?,?,?,?,?,?)`,
				sourceID, a.ID, representation, AttendeeQualification, string(raw), c.AcquiredAt.Format(time.RFC3339Nano), attendeeClock(c.SourceObservedAt))
			if err != nil {
				return 0, "", nil, err
			}
			snapshotID, err = result.LastInsertId()
			if err != nil {
				return 0, "", nil, err
			}
			for i, row := range c.Evidence.Attendees {
				if _, err := tx.ExecContext(ctx, `insert into outlook_attendee_occurrences(source_id,snapshot_id,ordinal,native_key,detail_key) values(?,?,?,?,?)`, sourceID, snapshotID, i, row.Key, row.DetailKey); err != nil {
					return 0, "", nil, err
				}
			}
			for i, row := range c.Evidence.Selections {
				if _, err := tx.ExecContext(ctx, `insert into outlook_attendee_bindings(source_id,snapshot_id,ordinal,native_event_id,detail_key) values(?,?,?,?,?)`, sourceID, snapshotID, i, row.SourceID, row.DetailKey); err != nil {
					return 0, "", nil, err
				}
			}
		case err != nil:
			return 0, "", nil, err
		default:
			if _, _, _, err := auditAttendeeBody(ctx, tx, snapshotID, nil, nil, 0); err != nil {
				return 0, "", nil, err
			}
		}
		link, err := attendeeOwnerLink(ctx, tx, account)
		if err != nil {
			return 0, "", nil, err
		}
		if !attendeeStrings(read.Origin, read.Account, link.Principal, link.Method, request.Root, request.Signature, request.Platform) {
			return 0, "", nil, outlookmeetingattendees.ErrTooLarge
		}
		revision, err := attendeeLinkRevision(ctx, tx, account)
		if err != nil {
			return 0, "", nil, err
		}
		index, err := attendeeIndexDigest(ctx, tx, snapshotID, sourceID)
		if err != nil {
			return 0, "", nil, err
		}
		losses := map[string]int{}
		for _, loss := range c.Evidence.Losses {
			losses[loss.Code] = loss.Count
		}
		member := attendeeMember{AttemptID: a.ID, SourceID: sourceID, SnapshotID: snapshotID, ReadID: read.ID, Revision: revision, Representation: representation, Losses: attendeeCanonicalJSON(losses),
			Owner: attendeeCanonicalJSON(link), Census: string(census), Input: c.InputDigest, Acquired: c.AcquiredAt.Format(time.RFC3339Nano), Observed: attendeeClock(c.SourceObservedAt),
			Admitted: c.AdmittedAt.Format(time.RFC3339Nano), Signature: request.Signature, Started: request.StartedAt, RequestDigest: request.Digest, Index: index}
		member.Digest = member.digest(read)
		if _, err := tx.ExecContext(ctx, `insert into outlook_attendee_members(attempt_id,snapshot_id,calendar_read_id,link_revision,owner_link_json,census_json,input_digest,acquired_at,source_observed_at,admitted_at,membership_digest) values(?,?,?,?,?,?,?,?,?,?,?)`,
			a.ID, snapshotID, read.ID, revision, member.Owner, member.Census, member.Input, member.Acquired, member.Observed, member.Admitted, member.Digest); err != nil {
			return 0, "", nil, err
		}
		if _, err := tx.ExecContext(ctx, `update knowledge_sources set admitted_attempt=?,complete_attempt=null,source_observed_at=? where id=?`, a.ID, member.Observed, sourceID); err != nil {
			return 0, "", nil, err
		}
		if _, err := tx.ExecContext(ctx, `update knowledge_locators set last_admitted=? where id=?`, a.ID, a.LocatorID); err != nil {
			return 0, "", nil, err
		}
		return sourceID, attendeeCoverage, losses, nil
	})
}
