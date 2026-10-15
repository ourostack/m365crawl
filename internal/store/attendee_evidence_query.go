package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/ourostack/m365crawl/internal/outlookattendees"
	"github.com/ourostack/m365crawl/internal/outlookcal"
	"github.com/ourostack/m365crawl/internal/outlookmeetingattendees"
)

type attendeeOwner struct {
	Account   string     `json:"profile_account"`
	Principal string     `json:"principal"`
	Method    string     `json:"method"`
	LinkedAt  *time.Time `json:"linked_at"`
}

func attendeeOwnerLink(ctx context.Context, q knowledgeReader, account string) (attendeeOwner, error) {
	link, err := readPeopleLink(ctx, q, `select account_id,principal_id,method,linked_at from calendar_account_links where source='outlook' and account_id=? and unlinked_at is null`, account)
	if err != nil {
		return attendeeOwner{}, err
	}
	return attendeeOwner{account, link.Principal, link.Method, link.LinkedAt}, nil
}

func (o attendeeOwner) public() PeopleCaptureLink {
	return PeopleCaptureLink{ProfileAccount: o.Account, Principal: o.Principal, Method: o.Method, LinkedAt: o.LinkedAt}
}

type attendeeMember struct {
	AttemptID, SourceID, SnapshotID, ReadID, Revision                        int64
	Representation, Owner, Census, Input, Acquired, Admitted, Digest, Losses string
	Observed                                                                 any
	Signature, Started, RequestDigest, Index                                 string
}

func (m attendeeMember) digest(read OutlookReadReceipt) string {
	return attendeeDigest("outlook-attendee-membership-v1", strconv.FormatInt(m.SourceID, 10), strconv.FormatInt(m.AttemptID, 10),
		strconv.FormatInt(m.SnapshotID, 10), m.Representation, strconv.FormatInt(m.ReadID, 10), readReceiptDigest(read),
		strconv.FormatInt(m.Revision, 10), json.RawMessage(m.Owner), json.RawMessage(m.Census), m.Input, m.Acquired, m.Observed, m.Admitted,
		m.Signature, m.Started, m.RequestDigest, m.Index, json.RawMessage(m.Losses))
}

func attendeeReadByID(ctx context.Context, q knowledgeReader, id int64) (OutlookReadReceipt, error) {
	var r OutlookReadReceipt
	var origin sql.NullString
	var at, digest string
	err := q.QueryRowContext(ctx, `select rowid,account_id,origin,fingerprint,mapper_stamp,read_at,receipt_digest from outlook_calendar_reads where rowid=?`, id).
		Scan(&r.ID, &r.Account, &origin, &r.Fingerprint, &r.MapperStamp, &at, &digest)
	if err != nil {
		return r, err
	}
	r.Origin = origin.String
	r.At, err = time.Parse(time.RFC3339Nano, at)
	if err != nil || r.ID <= 0 || at != r.At.Format(time.RFC3339Nano) || digest != readReceiptDigest(r) {
		return OutlookReadReceipt{}, ErrAttendeeArchiveCorrupt
	}
	return r, nil
}

func attendeeBlob(ctx context.Context, q knowledgeReader, table, column, key string, id int64, limit int) ([]byte, error) {
	// Every identifier comes from a fixed family callsite, never user/native text.
	where := ` from ` + table + ` where ` + key + `=?`
	var length int64
	if err := q.QueryRowContext(ctx, `select length(cast(`+column+` as blob))`+where, id).Scan(&length); err != nil {
		return nil, err
	}
	if length < 0 || length > int64(limit) {
		return nil, ErrAttendeeArchiveCorrupt
	}
	var raw []byte
	if err := q.QueryRowContext(ctx, `select `+column+where, id).Scan(&raw); err != nil {
		return nil, err
	}
	if int64(len(raw)) != length {
		return nil, ErrAttendeeArchiveCorrupt
	}
	return raw, nil
}

func readAttendeeRequest(ctx context.Context, q knowledgeReader, id int64) (attendeeRequestRecord, error) {
	var r attendeeRequestRecord
	raw, err := attendeeBlob(ctx, q, "outlook_attendee_requests", "request_json", "attempt_id", id, 6*1048576+4096)
	if err != nil {
		return r, err
	}
	if json.Unmarshal(raw, &r) != nil || attendeeCanonicalJSON(r) != string(raw) || r.Digest != r.digest() || r.AttemptID != id || r.LocatorID <= 0 ||
		!attendeeStrings(r.Root, r.Locator, r.Platform, r.Signature) {
		return r, ErrAttendeeArchiveCorrupt
	}
	var locator, platform, signature, started, kind string
	var locatorID int64
	if err := q.QueryRowContext(ctx, `select a.locator_id,l.locator,l.platform,coalesce(a.acquisition_signature,''),a.started_at,l.kind
from knowledge_attempts a join knowledge_locators l on l.id=a.locator_id where a.id=?`, id).
		Scan(&locatorID, &locator, &platform, &signature, &started, &kind); err != nil {
		return r, err
	}
	if locatorID != r.LocatorID || locator != r.Locator || platform != r.Platform || signature != r.Signature || started != r.StartedAt || kind != AttendeeKind {
		return r, ErrAttendeeArchiveCorrupt
	}
	return r, nil
}

func (s *Store) AttendeeRequestForLocator(ctx context.Context, id int64) (AttendeeRequest, error) {
	var attempt int64
	if err := s.db.QueryRowContext(ctx, `select latest_attempt from knowledge_locators where id=? and kind=?`, id, AttendeeKind).Scan(&attempt); err != nil {
		return AttendeeRequest{}, err
	}
	r, err := readAttendeeRequest(ctx, s.db, attempt)
	return AttendeeRequest{Root: r.Root, SourceID: r.SourceID, Discover: r.Discover}, err
}

func attendeeIndexDigest(ctx context.Context, q knowledgeReader, snapshotID, sourceID int64) (string, error) {
	h := sha256.New()
	_, _ = h.Write([]byte("[" + attendeeCanonicalJSON("outlook-attendee-index-v1") + "," + attendeeCanonicalJSON(strconv.FormatInt(sourceID, 10)) + "," + attendeeCanonicalJSON(strconv.FormatInt(snapshotID, 10)) + ",["))
	for n, query := range []string{
		`select rowid,source_id,ordinal,native_key,detail_key from outlook_attendee_occurrences where snapshot_id=? order by ordinal`,
		`select rowid,source_id,ordinal,native_event_id,detail_key from outlook_attendee_bindings where snapshot_id=? order by ordinal`,
	} {
		rows, err := q.QueryContext(ctx, query, snapshotID)
		if err != nil {
			return "", err
		}
		ordinal := 0
		for rows.Next() {
			var id, source int64
			var index int
			var key uint32
			var native any
			if n == 0 {
				var value uint32
				err = rows.Scan(&id, &source, &index, &value, &key)
				native = value
			} else {
				var value string
				err = rows.Scan(&id, &source, &index, &value, &key)
				native = value
			}
			if err != nil {
				_ = rows.Close()
				return "", err
			}
			if id <= 0 || source != sourceID || index != ordinal || ordinal >= 262144 || (n == 1 && ordinal >= 65536) {
				_ = rows.Close()
				return "", ErrAttendeeArchiveCorrupt
			}
			if ordinal > 0 {
				_, _ = h.Write([]byte(","))
			}
			_, _ = h.Write([]byte(attendeeCanonicalJSON([]any{strconv.FormatInt(id, 10), index, native, key})))
			ordinal++
		}
		err = errors.Join(rows.Err(), rows.Close())
		if err != nil {
			return "", err
		}
		if n == 0 {
			_, _ = h.Write([]byte("],["))
		}
	}
	_, _ = h.Write([]byte("]]"))
	return hex.EncodeToString(h.Sum(nil)), nil
}

func auditAttendeeMetadata(ctx context.Context, q knowledgeReader) error {
	if err := auditAttendeeNamespace(ctx, q); err != nil {
		return err
	}
	var bad int
	if err := q.QueryRowContext(ctx, `select
(select count(*) from knowledge_attempts a join knowledge_locators l on l.id=a.locator_id where l.kind='outlook_attendees' and not exists(select 1 from outlook_attendee_requests r where r.attempt_id=a.id))
+(select count(*) from outlook_attendee_sources n join knowledge_sources s on s.id=n.source_id join knowledge_locators l on l.id=s.locator_id where l.kind<>'outlook_attendees' or s.boundary<>'unresolved' or s.qualification<>'hxstore-attendees-v1/mapper-v1' or s.complete_attempt is not null or n.origin<>l.locator)
+(select count(*) from knowledge_sources s join knowledge_locators l on l.id=s.locator_id where l.kind='outlook_attendees' and not exists(select 1 from outlook_attendee_sources n where n.source_id=s.id))
+(select count(*) from outlook_attendee_members m join outlook_attendee_snapshots p on p.rowid=m.snapshot_id join knowledge_attempts a on a.id=m.attempt_id where a.source_id is not p.source_id or a.outcome<>'partial' or a.coverage<>'native_selected_detail_observations')
+(select count(*) from outlook_attendee_snapshots p where p.first_attempt_id is not (select min(m.attempt_id) from outlook_attendee_members m where m.snapshot_id=p.rowid))
+(select count(*) from knowledge_sources s join outlook_attendee_sources n on n.source_id=s.id where s.admitted_attempt is null or s.admitted_attempt is not (select max(m.attempt_id) from outlook_attendee_members m join outlook_attendee_snapshots p on p.rowid=m.snapshot_id where p.source_id=s.id))
+(select count(*) from knowledge_locators l where l.kind='outlook_attendees' and
 (l.latest_attempt is not (select max(a.id) from knowledge_attempts a where a.locator_id=l.id)
 or l.last_admitted is not (select max(m.attempt_id) from outlook_attendee_members m join knowledge_attempts a on a.id=m.attempt_id where a.locator_id=l.id)))
+(select count(*) from knowledge_sources s join outlook_attendee_sources n on n.source_id=s.id
 left join outlook_attendee_members m on m.attempt_id=s.admitted_attempt where s.source_observed_at is not m.source_observed_at)
+(select count(*) from knowledge_attempts a join knowledge_locators l on l.id=a.locator_id
 where l.kind='outlook_attendees' and (a.reader_version<>1 or a.mapper_version<>1))`).Scan(&bad); err != nil {
		return err
	}
	if bad != 0 {
		return ErrAttendeeArchiveCorrupt
	}
	attempts, err := q.QueryContext(ctx, `select a.id from knowledge_attempts a join knowledge_locators l on l.id=a.locator_id where l.kind=? order by a.id`, AttendeeKind)
	if err != nil {
		return err
	}
	for attempts.Next() {
		var id int64
		if err := attempts.Scan(&id); err != nil {
			_ = attempts.Close()
			return err
		}
		if _, err := readAttendeeRequest(ctx, q, id); err != nil {
			_ = attempts.Close()
			return err
		}
	}
	if err := errors.Join(attempts.Err(), attempts.Close()); err != nil {
		return err
	}
	members, err := q.QueryContext(ctx, `select m.attempt_id,m.snapshot_id,p.source_id from outlook_attendee_members m
join outlook_attendee_snapshots p on p.rowid=m.snapshot_id order by m.snapshot_id,m.attempt_id`)
	if err != nil {
		return err
	}
	var lastSnapshot int64
	var index string
	for members.Next() {
		var id, snapshot, source int64
		if err := members.Scan(&id, &snapshot, &source); err != nil {
			_ = members.Close()
			return err
		}
		if snapshot != lastSnapshot {
			index, err = attendeeIndexDigest(ctx, q, snapshot, source)
			if err != nil {
				_ = members.Close()
				return err
			}
			lastSnapshot = snapshot
		}
		if _, err := readAttendeeMember(ctx, q, id, index); err != nil {
			_ = members.Close()
			return err
		}
	}
	return errors.Join(members.Err(), members.Close())
}

func readAttendeeMember(ctx context.Context, q knowledgeReader, id int64, auditedIndex ...string) (attendeeMember, error) {
	var m attendeeMember
	var observed sql.NullString
	var account, origin, finished, content, coverage, status string
	err := q.QueryRowContext(ctx, `select m.attempt_id,p.source_id,m.snapshot_id,m.calendar_read_id,m.link_revision,p.representation_digest,
m.input_digest,m.acquired_at,m.source_observed_at,m.admitted_at,m.membership_digest,n.calendar_account,n.origin,
coalesce(a.finished_at,''),coalesce(a.content_digest,''),a.coverage,a.outcome,a.losses_json
from outlook_attendee_members m join outlook_attendee_snapshots p on p.rowid=m.snapshot_id
join outlook_attendee_sources n on n.source_id=p.source_id join knowledge_attempts a on a.id=m.attempt_id where m.attempt_id=?`, id).
		Scan(&m.AttemptID, &m.SourceID, &m.SnapshotID, &m.ReadID, &m.Revision, &m.Representation, &m.Input, &m.Acquired, &observed, &m.Admitted, &m.Digest,
			&account, &origin, &finished, &content, &coverage, &status, &m.Losses)
	if err != nil {
		return m, err
	}
	if observed.Valid {
		m.Observed = observed.String
	}
	request, err := readAttendeeRequest(ctx, q, id)
	if err != nil {
		return m, err
	}
	m.Signature, m.Started, m.RequestDigest = request.Signature, request.StartedAt, request.Digest
	read, err := attendeeReadByID(ctx, q, m.ReadID)
	if err != nil {
		return m, err
	}
	if read.Account != account || read.Origin != origin || origin != request.Locator || m.Revision < 0 ||
		status != "partial" || coverage != attendeeCoverage || content != m.Input {
		return m, ErrAttendeeArchiveCorrupt
	}
	if m.Revision > 0 {
		var count int
		if err := q.QueryRowContext(ctx, `select count(*) from outlook_link_events where rowid=? and account_id=?`, m.Revision, account).Scan(&count); err != nil {
			return m, err
		}
		if count != 1 {
			return m, ErrAttendeeArchiveCorrupt
		}
	}
	owner, err := attendeeBlob(ctx, q, "outlook_attendee_members", "owner_link_json", "attempt_id", id, 6*1048576+4096)
	if err != nil {
		return m, err
	}
	m.Owner = string(owner)
	var link attendeeOwner
	if json.Unmarshal(owner, &link) != nil || attendeeCanonicalJSON(link) != m.Owner || link.Account != account ||
		!attendeeStrings(origin, account, link.Principal, link.Method, request.Root, request.Signature, request.Platform) {
		return m, ErrAttendeeArchiveCorrupt
	}
	census, err := attendeeBlob(ctx, q, "outlook_attendee_members", "census_json", "attempt_id", id, outlookmeetingattendees.MaxCensusBytes)
	if err != nil {
		return m, err
	}
	if _, err := outlookmeetingattendees.DecodeCensus(ctx, census); err != nil {
		return m, ErrAttendeeArchiveCorrupt
	}
	m.Census = string(census)
	acquired, err := time.Parse(time.RFC3339Nano, m.Acquired)
	if err != nil || acquired.IsZero() || acquired.Format(time.RFC3339Nano) != m.Acquired {
		return m, ErrAttendeeArchiveCorrupt
	}
	admitted, err := time.Parse(time.RFC3339Nano, m.Admitted)
	if err != nil || admitted.IsZero() || admitted.Format(time.RFC3339Nano) != m.Admitted || knowledgeTime(admitted) != finished {
		return m, ErrAttendeeArchiveCorrupt
	}
	if _, err := knowledgeTimes(observed); err != nil {
		return m, ErrAttendeeArchiveCorrupt
	}
	p, err := attendeeSnapshotHeader(ctx, q, m.SnapshotID)
	if err != nil {
		return m, err
	}
	if p.FirstAttempt == m.AttemptID && (p.Acquired != m.Acquired || p.Observed != observed) {
		return m, ErrAttendeeArchiveCorrupt
	}
	if len(auditedIndex) > 0 {
		m.Index = auditedIndex[0]
	} else {
		m.Index, err = attendeeIndexDigest(ctx, q, m.SnapshotID, m.SourceID)
		if err != nil {
			return m, err
		}
	}
	if m.digest(read) != m.Digest {
		return m, ErrAttendeeArchiveCorrupt
	}
	return m, nil
}

type attendeeStoredSnapshot struct {
	ID, SourceID, FirstAttempt      int64
	Digest, Qualification, Acquired string
	Observed                        sql.NullString
}

func attendeeSnapshotHeader(ctx context.Context, q knowledgeReader, id int64) (attendeeStoredSnapshot, error) {
	var p attendeeStoredSnapshot
	err := q.QueryRowContext(ctx, `select rowid,source_id,first_attempt_id,representation_digest,qualification,acquired_at,source_observed_at from outlook_attendee_snapshots where rowid=?`, id).
		Scan(&p.ID, &p.SourceID, &p.FirstAttempt, &p.Digest, &p.Qualification, &p.Acquired, &p.Observed)
	return p, err
}

func auditAttendeeBody(ctx context.Context, q knowledgeReader, id int64, native func(AttendeeRow) error, binding func(AttendeeBinding) error, limit int) (outlookmeetingattendees.Summary, attendeeStoredSnapshot, []byte, error) {
	p, err := attendeeSnapshotHeader(ctx, q, id)
	if err != nil {
		return outlookmeetingattendees.Summary{}, p, nil, err
	}
	if limit == 0 {
		limit = outlookmeetingattendees.MaxEnvelopeBytes
	}
	raw, err := attendeeBlob(ctx, q, "outlook_attendee_snapshots", "result_json", "rowid", id, limit)
	if err != nil {
		return outlookmeetingattendees.Summary{}, p, nil, err
	}
	if attendeeRepresentation(p.SourceID, raw) != p.Digest || p.Qualification != AttendeeQualification {
		return outlookmeetingattendees.Summary{}, p, nil, ErrAttendeeArchiveCorrupt
	}
	acquired, err := time.Parse(time.RFC3339Nano, p.Acquired)
	if err != nil || acquired.IsZero() {
		return outlookmeetingattendees.Summary{}, p, nil, ErrAttendeeArchiveCorrupt
	}
	clocks, err := knowledgeTimes(p.Observed)
	if err != nil {
		return outlookmeetingattendees.Summary{}, p, nil, ErrAttendeeArchiveCorrupt
	}
	rows, err := q.QueryContext(ctx, `select rowid,ordinal,native_key,detail_key from outlook_attendee_occurrences where snapshot_id=? order by ordinal`, id)
	if err != nil {
		return outlookmeetingattendees.Summary{}, p, nil, err
	}
	defer func() { _ = rows.Close() }()
	bindings, err := q.QueryContext(ctx, `select rowid,ordinal,native_event_id,detail_key from outlook_attendee_bindings where snapshot_id=? order by ordinal`, id)
	if err != nil {
		return outlookmeetingattendees.Summary{}, p, nil, err
	}
	defer func() { _ = bindings.Close() }()
	summary, err := outlookmeetingattendees.AuditVisitors(ctx, raw, func(i int, a outlookattendees.Attendee) error {
		if !rows.Next() {
			return errors.Join(ErrAttendeeArchiveCorrupt, rows.Err())
		}
		var rowID int64
		var ordinal int
		var key, parent uint32
		if err := rows.Scan(&rowID, &ordinal, &key, &parent); err != nil {
			return err
		}
		if i != ordinal || key != a.Key || parent != a.DetailKey {
			return ErrAttendeeArchiveCorrupt
		}
		if native != nil {
			return native(AttendeeRow{ID: rowID, SourceID: p.SourceID, SnapshotID: p.ID, CaptureID: p.FirstAttempt, Ordinal: i,
				RepresentationDigest: p.Digest, Qualification: p.Qualification, Native: a, AcquiredAt: acquired, SourceObservedAt: clocks[0]})
		}
		return nil
	}, func(i int, s outlookcal.SelectionReceipt) error {
		if !bindings.Next() {
			return errors.Join(ErrAttendeeArchiveCorrupt, bindings.Err())
		}
		var rowID int64
		var ordinal int
		var event string
		var parent uint32
		if err := bindings.Scan(&rowID, &ordinal, &event, &parent); err != nil {
			return err
		}
		if ordinal != i || event != s.SourceID || parent != s.DetailKey {
			return ErrAttendeeArchiveCorrupt
		}
		if binding != nil {
			return binding(AttendeeBinding{ID: rowID, SourceID: p.SourceID, SnapshotID: p.ID, Selection: s})
		}
		return nil
	})
	if err != nil {
		return outlookmeetingattendees.Summary{}, p, nil, errors.Join(ErrAttendeeArchiveCorrupt, err)
	}
	if rows.Next() || bindings.Next() {
		return outlookmeetingattendees.Summary{}, p, nil, ErrAttendeeArchiveCorrupt
	}
	if err := errors.Join(rows.Err(), bindings.Err(), rows.Close(), bindings.Close()); err != nil {
		return outlookmeetingattendees.Summary{}, p, nil, err
	}
	return summary, p, raw, nil
}
