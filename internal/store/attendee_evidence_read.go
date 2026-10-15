package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"time"

	"github.com/ourostack/m365crawl/internal/calendar"
	"github.com/ourostack/m365crawl/internal/outlookattendees"
	"github.com/ourostack/m365crawl/internal/outlookmeetingattendees"
	"github.com/ourostack/m365crawl/internal/teamsdesktop"
)

func emptyAttendeeSnapshot() AttendeeSnapshot {
	return AttendeeSnapshot{Rows: []AttendeeRow{}, Bindings: []AttendeeBinding{}, Coverage: AttendeeCoverage{Scope: AttendeeScope, Sources: []AttendeeSourceSummary{}}}
}

func (s *Store) attendeeView(ctx context.Context, read func(knowledgeReader, bool) error) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		ok, err := hasAttendeeSchema(ctx, tx)
		if err != nil {
			return err
		}
		if ok {
			if err := auditAttendeeMetadata(ctx, tx); err != nil {
				return err
			}
		}
		return read(tx, ok)
	})
}

func attendeePointer(id int64) *int64 { return &id }

func attendeeScope(ctx context.Context, q knowledgeReader, m attendeeMember) (AttendeeSourceSummary, error) {
	summary := AttendeeSourceSummary{SourceID: attendeePointer(m.SourceID), AdmittedID: attendeePointer(m.AttemptID), SnapshotID: attendeePointer(m.SnapshotID),
		CalendarReadID: attendeePointer(m.ReadID), LinkRevision: attendeePointer(m.Revision), RepresentationDigest: m.Representation, Qualification: AttendeeQualification,
		OriginState: "unknown", CalendarState: "missing_read", LinkState: "unknown", ScopeState: "unassociated"}
	original, err := attendeeReadByID(ctx, q, m.ReadID)
	if err != nil {
		return summary, err
	}
	summary.Account = original.Account
	current, err := outlookReadReceipt(ctx, q, original.Account)
	if err != nil {
		return summary, err
	}
	if current.ID > 0 {
		summary.CurrentReadID = attendeePointer(current.ID)
	}
	summary.OriginState = "conflict"
	switch current.Origin {
	case "":
		summary.OriginState = "unknown"
	case original.Origin:
		summary.OriginState = "matched"
	}
	summary.CalendarState = "different_read"
	switch {
	case current.ID == 0:
		summary.CalendarState = "missing_read"
	case current.Origin != original.Origin:
		summary.CalendarState = "different_origin"
	case current.ID == m.ReadID:
		summary.CalendarState = "matched"
	}
	owner, err := attendeeOwnerLink(ctx, q, original.Account)
	if err != nil {
		return summary, err
	}
	var captured attendeeOwner
	if json.Unmarshal([]byte(m.Owner), &captured) != nil {
		return summary, ErrAttendeeArchiveCorrupt
	}
	summary.CapturedLink, summary.CurrentLink = captured.public(), owner.public()
	revision, err := attendeeLinkRevision(ctx, q, original.Account)
	if err != nil {
		return summary, err
	}
	summary.CurrentLinkRevision = attendeePointer(revision)
	summary.LinkState = "drifted"
	if revision == m.Revision && attendeeCanonicalJSON(owner) == m.Owner {
		switch {
		case owner.Principal != "":
			summary.LinkState = "linked"
		case owner.Method == "" && owner.LinkedAt == nil:
			summary.LinkState = "source_local_unlinked"
		default:
			summary.LinkState = "unknown"
		}
	}
	if summary.OriginState == "matched" && summary.CalendarState == "matched" && (summary.LinkState == "linked" || summary.LinkState == "source_local_unlinked") {
		summary.ScopeState = summary.LinkState
	}
	acquired, err := time.Parse(time.RFC3339Nano, m.Acquired)
	if err != nil {
		return summary, ErrAttendeeArchiveCorrupt
	}
	admitted, err := time.Parse(time.RFC3339Nano, m.Admitted)
	if err != nil {
		return summary, ErrAttendeeArchiveCorrupt
	}
	summary.AcquiredAt, summary.AdmittedAt = &acquired, &admitted
	if observed, ok := m.Observed.(string); ok {
		at, err := time.Parse(time.RFC3339Nano, observed)
		if err != nil {
			return summary, ErrAttendeeArchiveCorrupt
		}
		summary.SourceObservedAt = &at
	}
	return summary, nil
}

func attendeeSourcesInView(ctx context.Context, q knowledgeReader, accounts map[string]bool, sourceID int64) ([]AttendeeSourceSummary, error) {
	rows, err := q.QueryContext(ctx, `select l.id,l.locator,l.latest_attempt,l.last_admitted,a.started_at,a.outcome,a.reason,s.id,n.calendar_account
from knowledge_locators l join knowledge_attempts a on a.id=l.latest_attempt
left join knowledge_sources s on s.locator_id=l.id and s.qualification=?
left join outlook_attendee_sources n on n.source_id=s.id where l.kind=? order by l.id`, AttendeeQualification, AttendeeKind)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []AttendeeSourceSummary{}
	for rows.Next() {
		var registration, attempt int64
		var admitted, source sql.NullInt64
		var locator, started, status, reason string
		var account sql.NullString
		if err := rows.Scan(&registration, &locator, &attempt, &admitted, &started, &status, &reason, &source, &account); err != nil {
			return nil, err
		}
		if accounts != nil && !accounts[account.String] {
			matchesOrigin := false
			if !account.Valid {
				for candidate := range accounts {
					read, err := outlookReadReceipt(ctx, q, candidate)
					if err != nil {
						return nil, err
					}
					matchesOrigin = matchesOrigin || (read.Origin != "" && read.Origin == locator)
				}
			}
			if !matchesOrigin {
				continue
			}
		}
		if sourceID > 0 && source.Int64 != sourceID {
			continue
		}
		summary := AttendeeSourceSummary{OriginState: "unknown", CalendarState: "missing_read", LinkState: "unknown", ScopeState: "unavailable", Losses: []outlookattendees.Loss{}}
		if admitted.Valid {
			m, err := readAttendeeMember(ctx, q, admitted.Int64)
			if err != nil {
				return nil, err
			}
			summary, err = attendeeScope(ctx, q, m)
			if err != nil {
				return nil, err
			}
			body, _, _, err := auditAttendeeBody(ctx, q, m.SnapshotID, nil, nil, 0)
			if err != nil {
				return nil, err
			}
			summary.AttendeesTotal, summary.ParentsTotal, summary.BindingsTotal = int64(body.AttendeesTotal), int64(body.ParentsTotal), int64(body.SelectionsTotal)
			summary.Losses = body.Losses
		}
		at, err := time.Parse(time.RFC3339Nano, started)
		if err != nil {
			return nil, ErrAttendeeArchiveCorrupt
		}
		summary.RegistrationID, summary.Locator, summary.AttemptID, summary.Status, summary.Reason, summary.AttemptedAt = registration, locator, attendeePointer(attempt), status, reason, &at
		request, err := readAttendeeRequest(ctx, q, attempt)
		if err != nil {
			return nil, err
		}
		summary.Request = AttendeeRequest{Root: request.Root, SourceID: request.SourceID, Discover: request.Discover}
		out = append(out, summary)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	if sourceID > 0 && len(out) == 0 {
		return nil, ErrKnowledgeSourceUnknown
	}
	return out, nil
}

type resolvedCalendarGroup struct {
	item calendar.AgendaItem
	rows []calendar.Event
}

func resolveCalendarGroups(ctx context.Context, q knowledgeReader, account, ref string) ([]resolvedCalendarGroup, calendar.Principals, error) {
	principals, err := calendar.LoadPrincipals(ctx, q)
	if err != nil {
		return nil, principals, err
	}
	candidates, err := matchCalendarRef(ctx, q, principals, account, ref)
	if err != nil {
		return nil, principals, err
	}
	seen := map[refCandidate]bool{}
	groups := []resolvedCalendarGroup{}
	for _, candidate := range candidates {
		item, rows, ok, err := calendar.FindWithRows(ctx, q, candidate.principal, candidate.key)
		if err != nil {
			return nil, principals, err
		}
		key := refCandidate{item.Principal, item.Key}
		if ok && !seen[key] {
			seen[key] = true
			groups = append(groups, resolvedCalendarGroup{item, rows})
		}
	}
	return groups, principals, nil
}

func (s *Store) AttendeeSources(ctx context.Context, sourceID int64) (AttendeeSnapshot, error) {
	result := emptyAttendeeSnapshot()
	err := s.attendeeView(ctx, func(q knowledgeReader, available bool) error {
		if !available {
			return nil
		}
		var err error
		result.Coverage.Sources, err = attendeeSourcesInView(ctx, q, nil, sourceID)
		if err != nil {
			return err
		}
		for _, source := range result.Coverage.Sources {
			if source.SnapshotID == nil {
				continue
			}
			result.BindingsTotal += source.BindingsTotal
			if _, _, _, err := auditAttendeeBody(ctx, q, *source.SnapshotID, nil, func(binding AttendeeBinding) error {
				if len(result.Bindings) >= 100 {
					return nil
				}
				binding, err = attendeeBindingState(ctx, q, binding, source, *source.SnapshotID)
				if err != nil {
					return err
				}
				result.Bindings = append(result.Bindings, binding)
				return nil
			}, 0); err != nil {
				return err
			}
		}
		result.BindingsTruncated = result.BindingsTotal > int64(len(result.Bindings))
		return nil
	})
	if err != nil {
		return AttendeeSnapshot{}, err
	}
	return result, nil
}

func memberForSnapshot(ctx context.Context, q knowledgeReader, id int64) (attendeeMember, error) {
	var attempt int64
	if err := q.QueryRowContext(ctx, `select max(attempt_id) from outlook_attendee_members where snapshot_id=?`, id).Scan(&attempt); err != nil {
		return attendeeMember{}, err
	}
	return readAttendeeMember(ctx, q, attempt)
}

func attendeeQualified(summary AttendeeSourceSummary) bool {
	return summary.OriginState == "matched" && summary.CalendarState == "matched" && (summary.LinkState == "linked" || summary.LinkState == "source_local_unlinked")
}

func attendeeAccountAllowed(account *teamsdesktop.Account, scope AttendeeSourceSummary) bool {
	return account == nil || (scope.LinkState == "linked" && scope.CapturedLink.Principal == accountString(account) && scope.CurrentLink.Principal == accountString(account))
}

func attendeeBindingState(ctx context.Context, q knowledgeReader, binding AttendeeBinding, scope AttendeeSourceSummary, currentSnapshot int64) (AttendeeBinding, error) {
	binding.MatchesRead, binding.MatchesOrigin = scope.CalendarState == "matched", scope.OriginState == "matched"
	binding.MatchesLink = scope.LinkState == "linked" || scope.LinkState == "source_local_unlinked"
	var present int
	if err := q.QueryRowContext(ctx, `select count(*) from calendar_source_events where source='outlook' and account_id=? and source_id=? and removed_at is null`, scope.Account, binding.Selection.SourceID).Scan(&present); err != nil {
		return binding, err
	}
	binding.CalendarRowPresent = present > 0
	binding.Retained = !attendeeQualified(scope) || binding.SnapshotID != currentSnapshot || !binding.CalendarRowPresent
	return binding, nil
}

func (s *Store) AttendeeEventEvidence(ctx context.Context, account *teamsdesktop.Account, ref string, filter AttendeeFilter) (AttendeeSnapshot, error) {
	result := emptyAttendeeSnapshot()
	if filter.Limit <= 0 || filter.SourceID < 0 {
		return result, outlookmeetingattendees.ErrInvalid
	}
	err := s.attendeeView(ctx, func(q knowledgeReader, available bool) error {
		groups, _, err := resolveCalendarGroups(ctx, q, accountString(account), ref)
		if err != nil {
			return err
		}
		if len(groups) != 1 {
			return ErrKnowledgeSourceUnknown
		}
		group := groups[0]
		result.EventID = EventID(group.item.Principal, group.item.Key)
		if !available {
			return nil
		}
		return attendeeEventInView(ctx, q, account, group, filter, &result)
	})
	if err != nil {
		return AttendeeSnapshot{}, err
	}
	return result, nil
}

func attendeeEventInView(ctx context.Context, q knowledgeReader, account *teamsdesktop.Account, group resolvedCalendarGroup, filter AttendeeFilter, result *AttendeeSnapshot) error {
	var err error
	result.EventID = EventID(group.item.Principal, group.item.Key)
	accounts := map[string]bool{}
	for _, row := range group.rows {
		if row.Source == calendar.SourceOutlook {
			accounts[row.AccountID] = true
		}
	}
	result.Coverage.Sources, err = attendeeSourcesInView(ctx, q, accounts, 0)
	if err != nil {
		return err
	}
	for sourceIndex, source := range result.Coverage.Sources {
		if source.SourceID == nil || source.SnapshotID == nil {
			continue
		}
		uids := map[string]bool{}
		for _, row := range group.rows {
			if row.Source == calendar.SourceOutlook && row.AccountID == source.Account {
				uids[row.SourceID] = row.RemovedAt == nil
			}
		}
		snapshots := []int64{*source.SnapshotID}
		if filter.History {
			found := map[int64]bool{}
			for uid := range uids {
				rows, err := q.QueryContext(ctx, `select distinct snapshot_id from outlook_attendee_bindings where source_id=? and native_event_id=? order by snapshot_id`, *source.SourceID, uid)
				if err != nil {
					return err
				}
				for rows.Next() {
					var id int64
					if err := rows.Scan(&id); err != nil {
						_ = rows.Close()
						return err
					}
					found[id] = true
				}
				if err := errors.Join(rows.Err(), rows.Close()); err != nil {
					return err
				}
			}
			snapshots = nil
			for id := range found {
				snapshots = append(snapshots, id)
			}
			sort.Slice(snapshots, func(i, j int) bool { return snapshots[i] < snapshots[j] })
		}
		currentBindings, liveBindings := 0, 0
		for _, snapshotID := range snapshots {
			member, err := memberForSnapshot(ctx, q, snapshotID)
			if err != nil {
				return err
			}
			scope, err := attendeeScope(ctx, q, member)
			if err != nil {
				return err
			}
			parents := map[uint32]bool{}
			staged := []AttendeeRow{}
			stagedBindings := []AttendeeBinding{}
			qualified := attendeeQualified(scope)
			show := attendeeAccountAllowed(account, scope) && (filter.History || qualified) && (filter.SourceID == 0 || filter.SourceID == member.SourceID)
			summary, _, _, err := auditAttendeeBody(ctx, q, snapshotID, func(row AttendeeRow) error {
				if show && parents[row.Native.DetailKey] {
					result.Total++
					if len(result.Rows)+len(staged) < min(filter.Limit, 100) {
						row.Retained = !qualified || snapshotID != *source.SnapshotID
						row.ScopeState = scope.ScopeState
						staged = append(staged, row)
					}
				}
				return nil
			}, func(binding AttendeeBinding) error {
				live, present := uids[binding.Selection.SourceID]
				if !present {
					return nil
				}
				if snapshotID == *source.SnapshotID {
					currentBindings++
					if live {
						liveBindings++
					}
				}
				binding.MatchesRead, binding.MatchesOrigin, binding.MatchesLink, binding.CalendarRowPresent = scope.CalendarState == "matched", scope.OriginState == "matched",
					scope.LinkState == "linked" || scope.LinkState == "source_local_unlinked", live
				binding.Retained = !qualified || !live || snapshotID != *source.SnapshotID
				if show && (filter.History || live) {
					parents[binding.Selection.DetailKey] = true
					result.BindingsTotal++
					if len(result.Bindings)+len(stagedBindings) < 100 {
						stagedBindings = append(stagedBindings, binding)
					}
				}
				return nil
			}, 0)
			if err != nil {
				return err
			}
			for i := range staged {
				staged[i].Ambiguous = slices.Contains(summary.AmbiguousKeys, staged[i].Native.Key)
				staged[i].Losses = summary.Losses
			}
			result.Rows = append(result.Rows, staged...)
			result.Bindings = append(result.Bindings, stagedBindings...)
		}
		if source.CalendarState == "matched" {
			switch {
			case currentBindings == 0:
				result.Coverage.Sources[sourceIndex].CalendarState = "row_missing"
			case liveBindings == 0:
				result.Coverage.Sources[sourceIndex].CalendarState = "removed"
			case currentBindings > 1:
				result.Coverage.Sources[sourceIndex].CalendarState = "multiple_selections"
			}
		}
	}
	result.Truncated = result.Total > int64(len(result.Rows))
	result.BindingsTruncated = result.BindingsTotal > int64(len(result.Bindings))
	return nil
}

func (s *Store) AttendeeDetail(ctx context.Context, account *teamsdesktop.Account, id int64) (AttendeeDetailResult, error) {
	var result AttendeeDetailResult
	if id <= 0 {
		return result, ErrKnowledgeSourceUnknown
	}
	err := s.attendeeView(ctx, func(q knowledgeReader, available bool) error {
		if !available {
			return ErrKnowledgeSourceUnknown
		}
		var source, snapshot int64
		var parent uint32
		if err := q.QueryRowContext(ctx, `select source_id,snapshot_id,detail_key from outlook_attendee_occurrences where rowid=?`, id).Scan(&source, &snapshot, &parent); err != nil {
			return err
		}
		sources, err := attendeeSourcesInView(ctx, q, nil, source)
		if err != nil {
			return err
		}
		result.Coverage = AttendeeCoverage{Scope: AttendeeScope, Sources: sources}
		member, err := memberForSnapshot(ctx, q, snapshot)
		if err != nil {
			return err
		}
		scope, err := attendeeScope(ctx, q, member)
		if err != nil {
			return err
		}
		if !attendeeAccountAllowed(account, scope) {
			return ErrKnowledgeSourceUnknown
		}
		result.Bindings = []AttendeeBinding{}
		summary, _, _, err := auditAttendeeBody(ctx, q, snapshot, func(row AttendeeRow) error {
			if row.ID == id {
				parent = row.Native.DetailKey
				row.Retained = !attendeeQualified(scope) || snapshot != *sources[0].SnapshotID
				row.ScopeState = scope.ScopeState
				result.Row = row
			}
			return nil
		}, func(binding AttendeeBinding) error {
			if binding.Selection.DetailKey != parent {
				return nil
			}
			result.BindingsTotal++
			if len(result.Bindings) >= 100 {
				return nil
			}
			binding, err = attendeeBindingState(ctx, q, binding, scope, *sources[0].SnapshotID)
			if err != nil {
				return err
			}
			result.Bindings = append(result.Bindings, binding)
			return nil
		}, 0)
		if err != nil {
			return err
		}
		result.Row.Ambiguous = slices.Contains(summary.AmbiguousKeys, result.Row.Native.Key)
		result.Row.Losses = summary.Losses
		result.BindingsTruncated = result.BindingsTotal > int64(len(result.Bindings))
		return nil
	})
	if err != nil {
		return AttendeeDetailResult{}, err
	}
	return result, nil
}
