package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/ourostack/m365crawl/internal/botcitations"
	"github.com/ourostack/m365crawl/internal/teamsdesktop"
)

// Application evidence states. mapped, partial, failed and absent are stored per representation;
// not_derived is only a read state: the archive holds no representation of the message's current
// raw revision under the active mapper version yet.
const (
	AppStateMapped     = "mapped"
	AppStatePartial    = "partial"
	AppStateFailed     = "failed"
	AppStateAbsent     = "absent"
	AppStateNotDerived = "not_derived"
	// AppProjectionCorrupt is the fixed failure of stored evidence that does not validate: its
	// typed JSON, source identity, raw digest, mapper version or pointer.
	AppProjectionCorrupt = "teams_app_projection_corrupt"
	// RetainedCurrentFailed is why a retained representation is served: the current raw revision
	// failed to project, and the last good representation of the same message is shown instead.
	RetainedCurrentFailed = "current_failed"
)

// ApplicationCitation is one admitted native citation. ID is the native integer id as a canonical
// signed-decimal string, so no JSON reader rounds it through a float.
type ApplicationCitation struct {
	Position             int     `json:"position"`
	ID                   string  `json:"id"`
	Title                string  `json:"title"`
	Link                 *string `json:"link"`
	ContentType          *string `json:"content_type"`
	NestedContentPresent bool    `json:"nested_content_present"`
}

// ApplicationPayload is the typed projection one representation stores in projection_json. Every
// claim is the message's own native claim, not a verified bot identity or a resolved document.
type ApplicationPayload struct {
	AppID       *string               `json:"app_id"`
	AppName     *string               `json:"app_name"`
	ReplyToID   *string               `json:"reply_to_id"`
	Citations   []ApplicationCitation `json:"citations"`
	Losses      []botcitations.Loss   `json:"losses"`
	SourceSlots *int                  `json:"source_slots"`
}

func (p ApplicationPayload) hasClaims() bool {
	return p.AppID != nil || p.AppName != nil || p.ReplyToID != nil || len(p.Citations) > 0
}

// ApplicationRevision is the source revision a representation projects: the complete message key,
// the source sender, raw presence, the SHA256 of the exact retained raw bytes (empty when raw is
// absent, or not computed yet) and the mapper version.
type ApplicationRevision struct {
	TenantID, UserID, ConversationID, MessageID, SenderID, RawDigest string
	RawPresent                                                       bool
	MapperVersion                                                    int
}

// ApplicationRepresentation is one immutable stored projection. DerivedAt is when it was first
// derived: not a source refresh, an edit or a contact time.
type ApplicationRepresentation struct {
	ID                 int64
	Revision           ApplicationRevision
	State, FailureCode string
	DerivedAt          time.Time
	Payload            ApplicationPayload
}

// MessageApplicationEnvelope is the application evidence of one selected message. State is the
// current revision's state under the active mapper version. Served is what the read serves: the
// current representation, or, when the current one failed, the validated last good one (Retained),
// which may come from an older mapper version. Served is nil for not_derived and corrupt evidence.
type MessageApplicationEnvelope struct {
	State, FailureCode  string
	ActiveMapperVersion int
	Current             ApplicationRevision
	Served              *ApplicationRepresentation
	Retained            bool
	RetainedReason      string
	BackfillPending     bool
}

// messageIdentity is the complete message key and source sender a projection is made for.
type messageIdentity struct {
	TenantID, UserID, ConversationID, MessageID, SenderID string
}

// projectApplication is teamsdesktop.ProjectApplication; tests replace it.
var projectApplication = teamsdesktop.ProjectApplication

func rawDigest(raw []byte, present bool) string {
	if !present {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func encodePayload(p ApplicationPayload) string {
	b, _ := json.Marshal(p) // plain strings, ints and pointers: it cannot fail
	return string(b)
}

func payloadOf(p teamsdesktop.ApplicationProjection) ApplicationPayload {
	out := ApplicationPayload{AppID: p.Observation.AppID, AppName: p.Observation.AppName, ReplyToID: p.Observation.ReplyToID,
		Citations: []ApplicationCitation{}, Losses: append([]botcitations.Loss{}, p.Losses...), SourceSlots: p.SourceSlots}
	for _, c := range p.Observation.Citations {
		out.Citations = append(out.Citations, ApplicationCitation{Position: c.Position, ID: strconv.FormatInt(c.ID, 10), Title: c.Title,
			Link: c.Link, ContentType: c.ContentType, NestedContentPresent: c.NestedContentPresent})
	}
	return out
}

// publishMessageApplication makes the representation of one accepted raw revision current, in the
// caller's transaction: it reuses the immutable representation of the same revision when one
// exists (keeping its first derivation time) and projects the raw JSON otherwise. A mapped
// representation, or a partial one with an admitted claim, becomes the last good one; an absent one
// clears it; a failed one, or a partial one without claims, leaves it. It returns the current
// state. Only cancellation and database errors fail it; a raw revision that cannot be read is a
// failed representation.
func publishMessageApplication(ctx context.Context, tx *sql.Tx, id messageIdentity, raw []byte, rawPresent bool, at time.Time) (string, error) {
	digest := rawDigest(raw, rawPresent)
	key := []any{id.TenantID, id.UserID, id.ConversationID, id.MessageID, id.SenderID, bool01(rawPresent), digest, activeApplicationVersion}
	var rowid int64
	var state, payloadJSON string
	err := tx.QueryRowContext(ctx, `select rowid, state, projection_json from message_app_observations
 where tenant_id=? and user_id=? and conversation_id=? and message_id=? and sender_id=? and raw_present=? and raw_digest=? and mapper_version=?`, key...).
		Scan(&rowid, &state, &payloadJSON)
	var payload ApplicationPayload
	valid := true // a representation this call projects is valid by construction
	switch {
	case errors.Is(err, sql.ErrNoRows):
		projected, err := projectApplication(ctx, id.MessageID, id.SenderID, raw, rawPresent)
		if err != nil {
			return "", err
		}
		state, payload = projected.State, payloadOf(projected)
		res, err := tx.ExecContext(ctx, `insert into message_app_observations(tenant_id,user_id,conversation_id,message_id,sender_id,raw_present,raw_digest,mapper_version,state,failure_code,projection_json,derived_at)
 values(?,?,?,?,?,?,?,?,?,?,?,?)`, append(key, state, projected.FailureCode, encodePayload(payload), fmtTime(at))...)
		if err != nil {
			return "", err
		}
		if rowid, err = res.LastInsertId(); err != nil {
			return "", err
		}
	case err != nil:
		return "", err
	default:
		payload, valid = decodePayload(payloadJSON) // a damaged reused row is never last good; reads report it
	}
	keep, lastGood := 1, any(nil)
	switch {
	case valid && lastGoodEligible(state, payload):
		keep, lastGood = 0, rowid
	case state == AppStateAbsent:
		keep = 0
	}
	_, err = tx.ExecContext(ctx, `insert into message_app_current(tenant_id,user_id,conversation_id,message_id,current_id,last_good_id) values(?,?,?,?,?,?)
 on conflict(tenant_id,user_id,conversation_id,message_id) do update set current_id=excluded.current_id,
 last_good_id=case when ?=1 then message_app_current.last_good_id else excluded.last_good_id end`,
		id.TenantID, id.UserID, id.ConversationID, id.MessageID, rowid, lastGood, keep)
	return state, err
}

// lastGoodEligible says whether a representation can be served when a later revision fails: a
// mapped one, or a partial one that admitted at least one claim.
func lastGoodEligible(state string, p ApplicationPayload) bool {
	return state == AppStateMapped || (state == AppStatePartial && p.hasClaims())
}

// decodePayload reads stored projection JSON, accepting only the exact canonical encoding of a
// payload that validates.
func decodePayload(s string) (ApplicationPayload, bool) {
	var p ApplicationPayload
	dec := json.NewDecoder(strings.NewReader(s))
	dec.DisallowUnknownFields()
	if dec.Decode(&p) != nil || p.Citations == nil || p.Losses == nil || encodePayload(p) != s {
		return ApplicationPayload{}, false
	}
	return p, validPayload(p)
}

// validPayload checks what JSON decoding cannot: canonical decimal citation ids, increasing native
// positions inside the recognized source slots, and sorted positive loss counts.
func validPayload(p ApplicationPayload) bool {
	last := -1
	for _, c := range p.Citations {
		n, err := strconv.ParseInt(c.ID, 10, 64)
		if err != nil || n < 0 || strconv.FormatInt(n, 10) != c.ID || c.Position <= last {
			return false
		}
		last = c.Position
	}
	if (p.SourceSlots == nil && last >= 0) || (p.SourceSlots != nil && *p.SourceSlots <= last) {
		return false
	}
	for i, l := range p.Losses {
		if l.Code == "" || l.Count <= 0 || (i > 0 && p.Losses[i-1].Code >= l.Code) {
			return false
		}
	}
	return true
}

// validState checks that a representation's state, fixed failure code and payload agree.
func validState(state, failure string, p ApplicationPayload) bool {
	empty := !p.hasClaims() && len(p.Losses) == 0
	switch state {
	case AppStateMapped:
		return failure == "" && len(p.Losses) == 0
	case AppStatePartial:
		return failure == "" && len(p.Losses) > 0
	case AppStateAbsent:
		return failure == "" && empty
	default: // failed: the table's check constraint admits no other state
		return fixedCode(failure) && empty && p.SourceSlots == nil
	}
}

// fixedCode says whether s looks like one of the fixed snake_case failure codes.
func fixedCode(s string) bool {
	return s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyz_") == ""
}
