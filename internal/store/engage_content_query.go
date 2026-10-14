package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"time"

	"github.com/ourostack/m365crawl/internal/engagecontent"
)

func hasEngageSchema(ctx context.Context, r knowledgeReader) (bool, error) {
	var n int
	if err := r.QueryRowContext(ctx, `select count(*) from sqlite_master where type='table' and name in ('engage_locator_metadata','engage_source_metadata','engage_sessions','engage_occurrences','engage_capture_members','engage_source_state','engage_fts')`).Scan(&n); err != nil {
		return false, err
	}
	if n == 7 {
		return true, nil
	}
	var version int
	if err := r.QueryRowContext(ctx, `select coalesce(max(version),0) from schema_migrations`).Scan(&version); err != nil {
		return false, err
	}
	if n != 0 || version >= 18 {
		return false, errEngageIntegrity
	}
	return false, nil
}

func engageSummary(s EngageSession) EngageSessionSummary {
	n := 0
	for _, t := range s.Result.Threads {
		n += len(t.Blocks)
	}
	return EngageSessionSummary{s.ID, s.SourceID, s.CaptureID, s.RepresentationDigest, s.Qualification, s.Result.State, len(s.Result.Threads), n, s.Result.Losses, s.AcquiredAt, s.CapturedAt}
}

// readEngage streams canonical sessions, auditing every membership/index binding
// before filtering. Only bounded selected rows and body-free source summaries
// survive a session iteration. The reader is always the caller's transaction.
func readEngage(ctx context.Context, r knowledgeReader, f EngageFilter, detailID int64, wantRows bool, creatingLocatorID int64) (EngageSnapshot, error) {
	out := EngageSnapshot{Rows: []EngageObservationRow{}, Registrations: []EngageRegistration{}, Sources: []EngageSource{}}
	ok, err := hasEngageSchema(ctx, r)
	if err != nil || !ok {
		return out, err
	}
	// Missing or orphan namespace rows cannot disappear through inner joins.
	var bad int
	err = r.QueryRowContext(ctx, `select
 (select count(*) from knowledge_locators l left join engage_locator_metadata m on m.locator_id=l.id where l.kind='engage_home' and m.locator_id is null)+
 (select count(*) from knowledge_locators l where l.kind='engage_home' and l.latest_attempt is null and l.id<>?)+
 (select count(*) from knowledge_locators l where l.kind='engage_home' and
 (l.latest_attempt is not (select max(a.id) from knowledge_attempts a where a.locator_id=l.id) or
 l.last_admitted is not (select max(a.id) from knowledge_attempts a where a.locator_id=l.id and a.source_id is not null)))+
 (select count(*) from engage_locator_metadata m left join knowledge_locators l on l.id=m.locator_id where l.id is null or l.kind<>'engage_home' or l.platform<>'browser' or l.locator<>'https://engage.cloud.microsoft/' or m.target_url<>l.locator)+
 (select count(*) from knowledge_sources s join knowledge_locators l on l.id=s.locator_id left join engage_source_metadata m on m.source_id=s.id left join engage_source_state st on st.source_id=s.id where l.kind='engage_home' and (m.source_id is null or st.source_id is null))+
 (select count(*) from engage_source_metadata m left join knowledge_sources s on s.id=m.source_id left join knowledge_locators l on l.id=s.locator_id where s.id is null or l.id is null or l.kind<>'engage_home')+
 (select count(*) from knowledge_sources s join knowledge_locators l on l.id=s.locator_id where l.kind='engage_home' and
 not exists(select 1 from knowledge_attempts a where a.id=s.admitted_attempt and a.source_id=s.id and a.locator_id=s.locator_id))+
 (select count(*) from engage_source_state st left join engage_source_metadata m on m.source_id=st.source_id where m.source_id is null)+
 (select count(*) from engage_sessions e left join engage_source_metadata m on m.source_id=e.source_id where m.source_id is null)+
 (select count(*) from engage_occurrences o left join engage_sessions e on e.rowid=o.session_id where e.rowid is null)+
 (select count(*) from engage_fts f left join engage_occurrences o on o.rowid=f.rowid where o.rowid is null)+
 (select count(*) from engage_capture_members m left join knowledge_attempts a on a.id=m.capture_id left join engage_sessions e on e.rowid=m.session_id where a.id is null or e.rowid is null or a.source_id is not e.source_id)`, creatingLocatorID).Scan(&bad)
	if err != nil {
		return out, err
	}
	if bad != 0 {
		return out, errEngageIntegrity
	}
	regs, err := r.QueryContext(ctx, `select l.id,l.latest_attempt,l.last_admitted,
 a.started_at,a.finished_at,coalesce(a.outcome,'pending'),a.reason,g.source_id
 from knowledge_locators l join knowledge_attempts a on a.id=l.latest_attempt
 left join knowledge_attempts g on g.id=l.last_admitted where l.kind='engage_home' order by l.id`)
	if err != nil {
		return out, err
	}
	for regs.Next() {
		var reg EngageRegistration
		var admitted, goodSource sql.NullInt64
		var started, finished sql.NullString
		if err := regs.Scan(&reg.ID, &reg.LatestAttemptID, &admitted, &started, &finished, &reg.Outcome, &reg.Reason, &goodSource); err != nil {
			_ = regs.Close()
			return out, err
		}
		times, err := knowledgeTimes(started, finished)
		if err != nil {
			_ = regs.Close()
			return out, err
		}
		reg.AttemptedAt, reg.FinishedAt = times[0], times[1]
		reg.Target, reg.AccountState = EngageHomeURL, "unknown"
		if admitted.Valid && admitted.Int64 == reg.LatestAttemptID {
			reg.AccountState = "verified_at_last_success"
		}
		if goodSource.Valid {
			v := goodSource.Int64
			reg.LastSuccessfulInstanceID = &v
		}
		out.Registrations = append(out.Registrations, reg)
	}
	if err := regs.Err(); err != nil {
		_ = regs.Close()
		return out, err
	}
	// Audit non-admitted attempts independently. Successful attempts are checked
	// against their exact canonical session below, including intermediate reuse.
	attempts, err := r.QueryContext(ctx, `select a.id,a.source_id,a.started_at,a.finished_at,coalesce(a.outcome,''),coalesce(a.coverage,''),a.reason,coalesce(a.losses_json,'{}'),a.acquisition_signature,a.content_digest,
 exists(select 1 from engage_capture_members m where m.capture_id=a.id)
 from knowledge_attempts a join knowledge_locators l on l.id=a.locator_id where l.kind='engage_home' order by a.id`)
	if err != nil {
		return out, err
	}
	for attempts.Next() {
		var id int64
		var source sql.NullInt64
		var start, finish, signature, digest sql.NullString
		var status, coverage, reason, losses string
		var member bool
		if err := attempts.Scan(&id, &source, &start, &finish, &status, &coverage, &reason, &losses, &signature, &digest, &member); err != nil {
			_ = attempts.Close()
			return out, err
		}
		times, err := knowledgeTimes(start, finish)
		if err != nil || times[0] == nil || signature.Valid {
			_ = attempts.Close()
			if err != nil {
				return out, err
			}
			return out, errEngageIntegrity
		}
		if source.Valid {
			if !member || !finish.Valid {
				_ = attempts.Close()
				return out, errEngageIntegrity
			}
		} else {
			if member || digest.Valid || losses != "{}" {
				_ = attempts.Close()
				return out, errEngageIntegrity
			}
			switch {
			case !finish.Valid:
				if status != "pending" || coverage != "unknown" || reason != "" {
					_ = attempts.Close()
					return out, errEngageIntegrity
				}
			case reason == "stale_attempt":
				if coverage != "not_admitted" || (status != "partial" && !validEngageFailure(status, "engage_capture_failed")) {
					_ = attempts.Close()
					return out, errEngageIntegrity
				}
			default:
				if !validEngageFailure(status, reason) || coverage != "unknown" {
					_ = attempts.Close()
					return out, errEngageIntegrity
				}
			}
		}
	}
	if err := attempts.Err(); err != nil {
		_ = attempts.Close()
		return out, err
	}
	srcRows, err := r.QueryContext(ctx, `select s.id,s.locator_id,s.admitted_attempt,s.complete_attempt,s.source_observed_at,s.boundary,s.qualification,m.account_json,st.current_session_id,st.last_good_session_id,a.finished_at,
 (select max(id) from knowledge_attempts where source_id=s.id),
 (select session_id from engage_capture_members where capture_id=s.admitted_attempt),
 (select m2.session_id from knowledge_attempts a2 join engage_capture_members m2 on m2.capture_id=a2.id where a2.source_id=s.id and exists(select 1 from engage_occurrences o where o.session_id=m2.session_id) order by a2.id desc limit 1)
 from knowledge_sources s join engage_source_metadata m on m.source_id=s.id join engage_source_state st on st.source_id=s.id
 join knowledge_attempts a on a.id=s.admitted_attempt order by s.id`)
	if err != nil {
		return out, err
	}
	for srcRows.Next() {
		var src EngageSource
		var complete, good, expectedGood sql.NullInt64
		var observed sql.NullString
		var boundary, qual, account string
		var admitted sql.NullString
		var maxAttempt, current int64
		if err := srcRows.Scan(&src.ID, &src.RegistrationID, &src.AdmittedAttemptID, &complete, &observed, &boundary, &qual, &account, &src.CurrentSessionID, &good, &admitted, &maxAttempt, &current, &expectedGood); err != nil {
			_ = srcRows.Close()
			return out, err
		}
		if json.Unmarshal([]byte(account), &src.Account) != nil || account != engageJSON(src.Account) || boundary != engageBoundary(src.Account) || qual != EngageQualification || complete.Valid || observed.Valid || src.AdmittedAttemptID != maxAttempt || src.CurrentSessionID != current || good != expectedGood {
			_ = srcRows.Close()
			return out, errEngageIntegrity
		}
		// Every attempt clock was validated in the preceding pass.
		times, _ := knowledgeTimes(admitted)
		src.AdmittedAt = times[0]
		src.Qualification = qual
		if good.Valid {
			v := good.Int64
			src.LastGoodSessionID = &v
		}
		for _, reg := range out.Registrations {
			if reg.ID == src.RegistrationID && reg.LastSuccessfulInstanceID != nil {
				src.IsLastSuccessfulInstance = *reg.LastSuccessfulInstanceID == src.ID
			}
		}
		out.Sources = append(out.Sources, src)
	}
	if err := srcRows.Err(); err != nil {
		_ = srcRows.Close()
		return out, err
	}
	sessions, err := r.QueryContext(ctx, `select rowid,source_id,capture_id,representation_digest,qualification,
 case when octet_length(result_json)<=67108864 then result_json else '' end,acquired_at,captured_at from engage_sessions order by rowid`)
	if err != nil {
		return out, err
	}
	for sessions.Next() {
		if err := ctx.Err(); err != nil {
			_ = sessions.Close()
			return out, err
		}
		var session EngageSession
		var encoded, acquired, captured string
		if err := sessions.Scan(&session.ID, &session.SourceID, &session.CaptureID, &session.RepresentationDigest, &session.Qualification, &encoded, &acquired, &captured); err != nil {
			_ = sessions.Close()
			return out, err
		}
		if json.Unmarshal([]byte(encoded), &session.Result) != nil || encoded != engageJSON(session.Result) || session.RepresentationDigest != engageDigest(session.SourceID, encoded) || session.Qualification != EngageQualification {
			_ = sessions.Close()
			return out, errEngageIntegrity
		}
		if err := engagecontent.ValidateResult(ctx, session.Result); err != nil {
			_ = sessions.Close()
			return out, err
		}
		times, err := knowledgeTimes(sql.NullString{String: acquired, Valid: true}, sql.NullString{String: captured, Valid: true})
		if err != nil {
			_ = sessions.Close()
			return out, err
		}
		session.AcquiredAt, session.CapturedAt = *times[0], *times[1]
		if session.AcquiredAt.IsZero() || session.CapturedAt.IsZero() {
			_ = sessions.Close()
			return out, errEngageIntegrity
		}
		idx := -1
		for i := range out.Sources {
			if out.Sources[i].ID == session.SourceID {
				idx = i
				break
			}
		}
		if idx < 0 || out.Sources[idx].Account != session.Result.Account {
			_ = sessions.Close()
			return out, errEngageIntegrity
		}
		src := &out.Sources[idx]
		if src.CurrentSessionID == session.ID {
			src.CurrentSession = engageSummary(session)
			src.Coverage = engageCoverage(session.Result)
		}
		if err := auditEngageMembers(ctx, r, session, src.RegistrationID); err != nil {
			_ = sessions.Close()
			return out, err
		}
		rows, err := readEngageOccurrences(ctx, r, session, *src, f, detailID, wantRows)
		if err != nil {
			_ = sessions.Close()
			return out, err
		}
		out.Rows = append(out.Rows, rows...)
		sort.SliceStable(out.Rows, func(i, j int) bool { return engageRowBefore(out.Rows[i], out.Rows[j]) })
		if f.Limit > 0 && len(out.Rows) > f.Limit {
			out.Truncated = true
			out.Rows = out.Rows[:f.Limit]
		}
	}
	if err := sessions.Err(); err != nil {
		_ = sessions.Close()
		return out, err
	}
	if f.SourceID > 0 {
		sources := []EngageSource{}
		regs := []EngageRegistration{}
		for _, src := range out.Sources {
			if src.ID == f.SourceID {
				sources = append(sources, src)
				for _, reg := range out.Registrations {
					if reg.ID == src.RegistrationID {
						regs = append(regs, reg)
					}
				}
			}
		}
		out.Sources, out.Registrations = sources, regs
	}
	return out, nil
}

func auditEngageMembers(ctx context.Context, r knowledgeReader, s EngageSession, locatorID int64) error {
	rows, err := r.QueryContext(ctx, `select a.id,a.source_id,a.locator_id,a.finished_at,a.outcome,a.coverage,a.losses_json,a.reason,a.content_digest
 from engage_capture_members m join knowledge_attempts a on a.id=m.capture_id where m.session_id=? order by a.id`, s.ID)
	if err != nil {
		return err
	}
	original := false
	for rows.Next() {
		var id, source, locator int64
		var finished, status, coverage, losses, reason, digest string
		if err := rows.Scan(&id, &source, &locator, &finished, &status, &coverage, &losses, &reason, &digest); err != nil {
			_ = rows.Close()
			return err
		}
		if id < s.CaptureID || source != s.SourceID || locator != locatorID || status != "partial" || coverage != engageCoverage(s.Result) || losses != engageJSON(engageLosses(s.Result)) || reason != "" || digest != s.RepresentationDigest {
			_ = rows.Close()
			return errEngageIntegrity
		}
		if _, err := time.Parse(time.RFC3339Nano, finished); err != nil {
			_ = rows.Close()
			return errEngageIntegrity
		}
		if id == s.CaptureID {
			original = true
			if finished != knowledgeTime(s.CapturedAt) {
				_ = rows.Close()
				return errEngageIntegrity
			}
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if !original {
		return errEngageIntegrity
	}
	return nil
}

func readEngageOccurrences(ctx context.Context, r knowledgeReader, s EngageSession, src EngageSource, f EngageFilter, detailID int64, wantRows bool) ([]EngageObservationRow, error) {
	rows, err := r.QueryContext(ctx, `select o.rowid,o.source_id,o.ordinal,o.native_thread_id,o.native_starter_id,o.starter_modified_at,f.title,f.text,f.metadata,
 (select count(*) from engage_fts f2 where f2.rowid=o.rowid)
 from engage_occurrences o left join engage_fts f on f.rowid=o.rowid where o.session_id=? order by o.ordinal`, s.ID)
	if err != nil {
		return nil, err
	}
	out := []EngageObservationRow{}
	n := 0
	for rows.Next() {
		var id, source int64
		var ordinal, count int
		var threadID, starterID string
		var modified, title, text, metadata sql.NullString
		if err := rows.Scan(&id, &source, &ordinal, &threadID, &starterID, &modified, &title, &text, &metadata, &count); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if n >= len(s.Result.Threads) {
			_ = rows.Close()
			return nil, errEngageIntegrity
		}
		thread := s.Result.Threads[n]
		wantTitle, wantText, wantMeta := engageIndex(s, thread)
		clock := engageNativeClock(thread.StarterUpdatedAt)
		if id <= 0 || source != s.SourceID || ordinal != n || threadID != thread.ID || starterID != thread.StarterID || modified.Valid != (clock != nil) || (clock != nil && modified.String != clock) || count != 1 || !title.Valid || !text.Valid || !metadata.Valid || title.String != wantTitle || text.String != wantText || metadata.String != wantMeta {
			_ = rows.Close()
			return nil, errEngageIntegrity
		}
		n++
		row := EngageObservationRow{id, source, s.ID, s.CaptureID, s.RepresentationDigest, s.Qualification, thread, s.Result.Losses, s.Result.Account, s.AcquiredAt, s.CapturedAt, s.ID != src.CurrentSessionID || !src.IsLastSuccessfulInstance}
		if !wantRows || (f.SourceID > 0 && source != f.SourceID) || (detailID > 0 && id != detailID) {
			continue
		}
		at := thread.StarterUpdatedAt
		if f.ModifiedSince != nil && (at == nil || at.Before(*f.ModifiedSince)) {
			continue
		}
		if f.ModifiedUntil != nil && (at == nil || at.After(*f.ModifiedUntil)) {
			continue
		}
		if f.Query != "" {
			var hit int
			if err := r.QueryRowContext(ctx, `select count(*) from engage_fts where engage_fts match ? and rowid=?`, f.Query, id).Scan(&hit); err != nil {
				_ = rows.Close()
				return nil, err
			}
			if hit == 0 {
				continue
			}
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if n != len(s.Result.Threads) {
		return nil, errEngageIntegrity
	}
	return out, nil
}

func engageRowBefore(a, b EngageObservationRow) bool {
	x, y := a.Thread.StarterUpdatedAt, b.Thread.StarterUpdatedAt
	if (x != nil) != (y != nil) {
		return x != nil
	}
	if x != nil && !x.Equal(*y) {
		return x.After(*y)
	}
	label := func(r EngageObservationRow) string {
		if r.Thread.Title != nil {
			return *r.Thread.Title
		}
		return r.Thread.ID
	}
	if label(a) != label(b) {
		return label(a) < label(b)
	}
	if a.SourceID != b.SourceID {
		return a.SourceID < b.SourceID
	}
	if a.SessionID != b.SessionID {
		return a.SessionID < b.SessionID
	}
	if a.Thread.Ordinal != b.Thread.Ordinal {
		return a.Thread.Ordinal < b.Thread.Ordinal
	}
	return a.ID < b.ID
}

func (s *Store) engageSnapshot(ctx context.Context, f EngageFilter, id int64, wantRows bool) (EngageSnapshot, error) {
	if f.SourceID < 0 || f.Limit < 0 || f.Limit == math.MaxInt {
		return EngageSnapshot{}, errors.New("engage: invalid filter")
	}
	if f.Limit == 0 && wantRows {
		f.Limit = 128
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return EngageSnapshot{}, err
	}
	defer func() { _ = tx.Rollback() }()
	out, err := readEngage(ctx, tx, f, id, wantRows, 0)
	if err != nil {
		return EngageSnapshot{}, err
	}
	if err := tx.Commit(); err != nil {
		return EngageSnapshot{}, err
	}
	return out, nil
}

// EngageSnapshot defaults an unspecified output limit to128; Truncated reports
// additional matches. This is an archive-output bound, not a producer limit.
func (s *Store) EngageSnapshot(ctx context.Context, f EngageFilter) (EngageSnapshot, error) {
	return s.engageSnapshot(ctx, f, 0, true)
}
func (s *Store) EngageSources(ctx context.Context) (EngageSnapshot, error) {
	return s.engageSnapshot(ctx, EngageFilter{}, 0, false)
}
func (s *Store) EngageDetail(ctx context.Context, id int64) (EngageDetailResult, error) {
	snap, err := s.engageSnapshot(ctx, EngageFilter{}, id, true)
	if err != nil {
		return EngageDetailResult{}, err
	}
	if id <= 0 || len(snap.Rows) != 1 {
		return EngageDetailResult{}, ErrKnowledgeSourceUnknown
	}
	out := EngageDetailResult{Observation: snap.Rows[0]}
	for _, src := range snap.Sources {
		if src.ID == out.Observation.SourceID {
			out.Source = src
		}
	}
	for _, reg := range snap.Registrations {
		if reg.ID == out.Source.RegistrationID {
			out.Registration = reg
		}
	}
	return out, nil
}
