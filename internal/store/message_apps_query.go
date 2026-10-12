package store

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"
)

// afterMessageSelection runs between a read's canonical message selection and its evidence load;
// tests use it to publish from a second handle at exactly that point.
var afterMessageSelection = func() {}

const (
	appSchemaQuery = `select
 (select count(*) from sqlite_master where type='table' and name in ('message_app_observations','message_app_current','meta')),
 (select count(*) from pragma_table_info('messages') where name='raw_digest')`
	appFactsQuery = `select m.raw_json is not null, coalesce(m.raw_digest,''), m.raw_digest is not null, coalesce(p.current_id,0), coalesce(p.last_good_id,0)
 from messages m left join message_app_current p on p.tenant_id=m.tenant_id and p.user_id=m.user_id and p.conversation_id=m.conversation_id and p.message_id=m.id
 where m.tenant_id=? and m.user_id=? and m.conversation_id=? and m.id=?`
	// legacyAppFactsQuery reads an archive from before the evidence tables: raw presence only.
	legacyAppFactsQuery    = `select m.raw_json is not null, '', 0, 0, 0 from messages m where m.tenant_id=? and m.user_id=? and m.conversation_id=? and m.id=?`
	appRepresentationQuery = `select tenant_id,user_id,conversation_id,message_id,sender_id,raw_present,raw_digest,mapper_version,state,failure_code,projection_json,derived_at
 from message_app_observations where rowid=?`
)

// appFacts is what the archive says about one selected message's application evidence.
type appFacts struct {
	rawPresent, digestPresent bool
	digest                    string
	currentID, lastGoodID     int64
}

// loadMessageApplications sets Application on rows already selected through q, the read
// transaction that selected them, so a message body is never paired with the evidence of a later
// revision. It reads at most the current and one last good representation per message and never
// writes: an archive without the evidence tables or meta (alpha.1), a message whose digest or pointer is missing and
// evidence of an older mapper version are not_derived. Stored evidence that does not validate is a
// per-message teams_app_projection_corrupt failure; only database errors and cancellation fail the
// read.
func loadMessageApplications(ctx context.Context, q knowledgeReader, rows []MessageRow) ([]MessageRow, error) {
	if len(rows) == 0 {
		return rows, nil
	}
	var tables, column int
	if err := q.QueryRowContext(ctx, appSchemaQuery).Scan(&tables, &column); err != nil {
		return nil, err
	}
	derived, pending, facts := tables == 3 && column == 1, true, legacyAppFactsQuery
	if derived {
		var marker string
		if err := q.QueryRowContext(ctx, `select coalesce((select value from meta where key=?),'')`, messageAppKey).Scan(&marker); err != nil {
			return nil, err
		}
		pending, facts = marker != strconv.Itoa(activeApplicationVersion), appFactsQuery
	}
	for i := range rows {
		r := &rows[i]
		var f appFacts
		if err := q.QueryRowContext(ctx, facts, r.TenantID, r.UserID, r.ConversationID, r.ID).
			Scan(&f.rawPresent, &f.digest, &f.digestPresent, &f.currentID, &f.lastGoodID); err != nil {
			return nil, err
		}
		env, err := resolveApplication(ctx, q, *r, f, derived, pending)
		if err != nil {
			return nil, err
		}
		r.Application = env
	}
	return rows, nil
}

func resolveApplication(ctx context.Context, q knowledgeReader, r MessageRow, f appFacts, derived, pending bool) (MessageApplicationEnvelope, error) {
	env := MessageApplicationEnvelope{State: AppStateNotDerived, ActiveMapperVersion: activeApplicationVersion, BackfillPending: pending,
		Current: ApplicationRevision{TenantID: r.TenantID, UserID: r.UserID, ConversationID: r.ConversationID, MessageID: r.ID, SenderID: r.SenderID,
			RawDigest: f.digest, RawPresent: f.rawPresent, MapperVersion: activeApplicationVersion}}
	if !derived || f.currentID == 0 || f.rawPresent != f.digestPresent {
		return env, nil
	}
	cur, ok, err := loadRepresentation(ctx, q, f.currentID)
	switch {
	case err != nil:
		return env, err
	case !ok || !sameMessage(cur.Revision, env.Current):
		return corruptApplication(env), nil
	case cur.Revision.MapperVersion != activeApplicationVersion:
		return env, nil
	case cur.Revision != env.Current:
		return corruptApplication(env), nil
	}
	env.State, env.FailureCode, env.Served = cur.State, cur.FailureCode, &cur
	if cur.State != AppStateFailed || f.lastGoodID == 0 {
		return env, nil
	}
	good, ok, err := loadRepresentation(ctx, q, f.lastGoodID)
	if err != nil {
		return env, err
	}
	if !ok || !sameMessage(good.Revision, env.Current) || !lastGoodEligible(good.State, good.Payload) {
		return corruptApplication(env), nil
	}
	env.Served, env.Retained, env.RetainedReason = &good, true, RetainedCurrentFailed
	return env, nil
}

func corruptApplication(env MessageApplicationEnvelope) MessageApplicationEnvelope {
	env.State, env.FailureCode, env.Served, env.Retained, env.RetainedReason = AppStateFailed, AppProjectionCorrupt, nil, false, ""
	return env
}

// sameMessage says whether a representation belongs to the message: the same complete key.
func sameMessage(a, b ApplicationRevision) bool {
	return a.TenantID == b.TenantID && a.UserID == b.UserID && a.ConversationID == b.ConversationID && a.MessageID == b.MessageID
}

// loadRepresentation reads one representation. ok is false when the row is missing or does not
// validate: its typed JSON, its state and failure code, or its first derivation time.
func loadRepresentation(ctx context.Context, q knowledgeReader, id int64) (ApplicationRepresentation, bool, error) {
	rep := ApplicationRepresentation{ID: id}
	v := &rep.Revision
	var payload, derived string
	err := q.QueryRowContext(ctx, appRepresentationQuery, id).Scan(&v.TenantID, &v.UserID, &v.ConversationID, &v.MessageID, &v.SenderID,
		&v.RawPresent, &v.RawDigest, &v.MapperVersion, &rep.State, &rep.FailureCode, &payload, &derived)
	if errors.Is(err, sql.ErrNoRows) {
		return rep, false, nil
	}
	if err != nil {
		return rep, false, err
	}
	var okPayload bool
	rep.Payload, okPayload = decodePayload(payload)
	at, errAt := time.Parse(timeLayout, derived)
	rep.DerivedAt = at
	return rep, okPayload && errAt == nil && validState(rep.State, rep.FailureCode, rep.Payload), nil
}
