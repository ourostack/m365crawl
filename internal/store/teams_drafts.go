package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/ourostack/m365crawl/internal/teamsdesktop"
	"github.com/ourostack/m365crawl/internal/teamsdrafts"
)

type TeamsDraftFilter struct {
	Account          *teamsdesktop.Account
	ID, BodyRevision string
	IncludeRemoved   bool
	Limit            int
}
type TeamsDraftAssociation struct {
	MetadataState             string  `json:"metadata_state"`
	TargetState               string  `json:"target_state"`
	ScanComplete              bool    `json:"scan_complete"`
	ConversationComplete      bool    `json:"conversation_complete"`
	ObservedMatchesLowerBound int     `json:"observed_matches_lower_bound"`
	ConversationID            *string `json:"conversation_id"`
	Kind                      *string `json:"kind"`
	Title                     *string `json:"title"`
	TeamID                    *string `json:"team_id"`
	EvidenceScope             string  `json:"evidence_scope"`
}
type TeamsDraftRow struct {
	Body        teamsdrafts.Body
	Association TeamsDraftAssociation
}
type TeamsDraftClock struct {
	Source string  `json:"source"`
	Scope  string  `json:"scope"`
	Kind   string  `json:"kind"`
	At     *string `json:"at"`
}
type TeamsDraftCoverage struct {
	ArchiveState             string                   `json:"archive_state"`
	ScopeState               string                   `json:"scope_state"`
	RefreshPolicy            string                   `json:"refresh_policy"`
	Ordering                 string                   `json:"ordering"`
	SelectedAccount          *teamsdesktop.Account    `json:"selected_account"`
	ScanComplete             bool                     `json:"scan_complete"`
	BodyScanComplete         bool                     `json:"body_scan_complete"`
	AssociationScanComplete  bool                     `json:"association_scan_complete"`
	VisitedRows              int                      `json:"visited_rows"`
	VisitedMembers           int                      `json:"visited_members"`
	InputBytes               int64                    `json:"input_bytes"`
	ObservedLossesLowerBound []teamsdrafts.Diagnostic `json:"observed_losses_lower_bound"`
	LossesComplete           bool                     `json:"losses_complete"`
	Clocks                   []TeamsDraftClock        `json:"clocks"`
	AdvisoryState            string                   `json:"advisory_state"`
}
type TeamsDraftSnapshot struct {
	Rows      []TeamsDraftRow
	Coverage  TeamsDraftCoverage
	Total     *int
	Truncated bool
}

var (
	ErrTeamsDraftUnknown             = errors.New("unknown cached draft")
	ErrTeamsDraftRemoved             = errors.New("cached draft is archive-removed")
	ErrTeamsDraftAmbiguous           = errors.New("ambiguous cached draft")
	ErrTeamsDraftBodyRevisionChanged = errors.New("cached draft body revision changed")
	ErrTeamsDraftScanIncomplete      = errors.New("cached draft scan incomplete")
)
var teamsDraftReader = func(tx *sql.Tx) knowledgeReader { return tx }

const draftRowCap = 4096
const draftInputCap int64 = 8 << 20
const draftTotalCap int64 = 64 << 20

var draftDigest = regexp.MustCompile(`^[0-9a-f]{64}$`)

type draftBudget struct {
	rows, members int
	bytes         int64
}

// text fetches use descriptors and precharge BEFORE any variable archive string
// enters Go. All table/column names below are private, fixed call-site literals.
func (b *draftBudget) text(ctx context.Context, r knowledgeReader, table string, columns []string, id int64) ([]sql.NullString, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if b.rows >= draftRowCap {
		return nil, false, nil
	}
	b.rows++
	desc := make([]string, 0, len(columns)*2)
	dest := make([]any, 0, len(columns)*2)
	types := make([]string, len(columns))
	lengths := make([]int64, len(columns))
	for i, c := range columns {
		desc = append(desc, "typeof("+c+")", "coalesce(length(CAST("+c+" AS BLOB)),0)")
		dest = append(dest, &types[i], &lengths[i])
	}
	q := " from " + table + " where rowid=?"
	if err := r.QueryRowContext(ctx, "select "+strings.Join(desc, ",")+q, id).Scan(dest...); err != nil {
		return nil, false, err
	}
	var sum int64
	for i, kind := range types {
		if (kind != "text" && kind != "null") || lengths[i] < 0 || lengths[i] > draftInputCap {
			return nil, false, nil
		}
		sum += lengths[i]
	}
	if sum > draftTotalCap-b.bytes {
		return nil, false, nil
	}
	b.bytes += sum
	values := make([]sql.NullString, len(columns))
	dest = dest[:0]
	args := []any{id}
	for i, c := range columns {
		q += " and typeof(" + c + ")=? and coalesce(length(CAST(" + c + " AS BLOB)),0)=?"
		args = append(args, types[i], lengths[i])
		dest = append(dest, &values[i])
	}
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if err := r.QueryRowContext(ctx, "select "+strings.Join(columns, ",")+q, args...).Scan(dest...); err != nil {
		return nil, false, err
	}
	for _, v := range values {
		if v.Valid && !utf8.ValidString(v.String) {
			return nil, false, nil
		}
	}
	return values, true, ctx.Err()
}

func draftRowIDs(ctx context.Context, r knowledgeReader, table, where string, args []any, remaining int) (ids []int64, complete bool, err error) {
	if remaining < 0 {
		remaining = 0
	}
	args = append(args, remaining+1)
	rows, err := r.QueryContext(ctx, "select rowid from "+table+" where "+where+" order by rowid limit ?", args...)
	if err != nil {
		return nil, false, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			return nil, false, err
		}
		ids = append(ids, id)
	}
	if err = rows.Err(); err != nil {
		return nil, false, err
	}
	complete = len(ids) <= remaining
	if !complete {
		ids = ids[:remaining]
	}
	return ids, complete, nil
}

var draftRecordColumns = []string{"source", "tenant_id", "user_id", "database", "store", "key_json", "value_json", "first_seen_at", "updated_at", "removed_at"}

func draftEnvelope(v []sql.NullString) (teamsdrafts.Envelope, bool) {
	for _, i := range []int{0, 1, 2, 3, 4, 5, 7, 8} {
		if !v[i].Valid {
			return teamsdrafts.Envelope{}, false
		}
	}
	e := teamsdrafts.Envelope{Source: v[0].String, TenantID: v[1].String, UserID: v[2].String, Database: v[3].String, Store: v[4].String, KeyJSON: v[5].String, FirstSeenAt: v[7].String, UpdatedAt: v[8].String}
	if v[9].Valid {
		e.RemovedAt = &v[9].String
	}
	return e, true
}

func (s *Store) TeamsDrafts(ctx context.Context, f TeamsDraftFilter) (TeamsDraftSnapshot, error) {
	if f.Limit < 0 || (f.ID != "" && (!strings.HasPrefix(f.ID, "teams-draft:") || !draftDigest.MatchString(strings.TrimPrefix(f.ID, "teams-draft:")))) || (f.BodyRevision != "" && (!draftDigest.MatchString(f.BodyRevision) || f.ID == "")) {
		return TeamsDraftSnapshot{}, errors.New("invalid cached draft filter")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return TeamsDraftSnapshot{}, err
	}
	defer func() { _ = tx.Rollback() }()
	out, err := readTeamsDrafts(ctx, teamsDraftReader(tx), f)
	if err != nil {
		return TeamsDraftSnapshot{}, err
	}
	if err = tx.Commit(); err != nil {
		return TeamsDraftSnapshot{}, err
	}
	return out, nil
}

func readTeamsDrafts(ctx context.Context, r knowledgeReader, f TeamsDraftFilter) (TeamsDraftSnapshot, error) {
	budget := &draftBudget{}
	out := TeamsDraftSnapshot{Coverage: TeamsDraftCoverage{ArchiveState: "available", ScopeState: "no_eligible_own_projection", RefreshPolicy: "explicit_ordinary_sync_only", SelectedAccount: f.Account, Ordering: "tuple_order", AssociationScanComplete: true, AdvisoryState: "unavailable"}}
	where := "store='drafts' and database like 'Teams:streams-replychain-manager:%'"
	args := []any{}
	if f.Account != nil {
		where += " and tenant_id=? and user_id=?"
		args = append(args, f.Account.TenantID, f.Account.UserID)
	}
	ids, bodyComplete, err := draftRowIDs(ctx, r, "records", where, args, draftRowCap)
	if err != nil {
		return out, err
	}
	bodies := []teamsdrafts.Body{}
	scopes := map[[3]string]bool{}
	for _, id := range ids {
		v, ok, err := budget.text(ctx, r, "records", draftRecordColumns, id)
		if err != nil {
			return out, err
		}
		if !ok {
			bodyComplete = false
			continue
		}
		e, valid := draftEnvelope(v)
		if !valid {
			bodyComplete = false
			continue
		}
		manager, account, eligible := teamsdesktop.ParseDatabaseName(e.Database)
		if !teamsdrafts.EligibleSource(e.Source) || !eligible || manager != "streams-replychain-manager" || account.TenantID != e.TenantID || account.UserID != e.UserID {
			continue
		}
		scopes[[3]string{e.Source, e.TenantID, e.UserID}] = true
		if !v[6].Valid {
			bodyComplete = false
			out.Coverage.ScopeState = "body_unavailable"
			continue
		}
		decoded, err := teamsdrafts.DecodeWrapper(ctx, e, []byte(v[6].String), draftRowCap-budget.members)
		if err != nil {
			return out, err
		}
		budget.members += decoded.VisitedMembers
		if !decoded.Complete {
			bodyComplete = false
			continue
		}
		bodies = append(bodies, decoded.Bodies...)
		out.Coverage.ObservedLossesLowerBound = append(out.Coverage.ObservedLossesLowerBound, decoded.Diagnostics...)
	}
	out.Coverage.BodyScanComplete = bodyComplete
	if len(ids) == 0 {
		out.Coverage.ScopeState = "no_eligible_archived_family"
	}
	if len(bodies) > 0 {
		out.Coverage.ScopeState = "eligible_own_projection"
	}
	// One complete per-source/account metadata universe is shared by its bodies.
	type metadataRow struct {
		value   teamsdrafts.Metadata
		removed bool
	}
	type universe struct {
		rows     []metadataRow
		complete bool
	}
	universes := map[[3]string]universe{}
	for _, body := range bodies {
		e := body.Envelope
		key := [3]string{e.Source, e.TenantID, e.UserID}
		u, exists := universes[key]
		if !exists {
			u.complete = true
			ids, complete, err := draftRowIDs(ctx, r, "records", "source=? and tenant_id=? and user_id=? and store='drafts-metadata-items' and database like 'Teams:messaging-slice-manager:%'", []any{e.Source, e.TenantID, e.UserID}, draftRowCap-budget.rows)
			if err != nil {
				return out, err
			}
			u.complete = complete
			for _, id := range ids {
				v, ok, err := budget.text(ctx, r, "records", draftRecordColumns, id)
				if err != nil {
					return out, err
				}
				if !ok {
					u.complete = false
					continue
				}
				en, valid := draftEnvelope(v)
				if !valid {
					u.complete = false
					continue
				}
				manager, acct, valid := teamsdesktop.ParseDatabaseName(en.Database)
				if !valid || manager != "messaging-slice-manager" || acct.TenantID != e.TenantID || acct.UserID != e.UserID {
					u.complete = false
					continue
				}
				meta, err := teamsdrafts.DecodeMetadata([]byte(v[6].String))
				if err != nil || !v[6].Valid {
					u.complete = false
					continue
				}
				u.rows = append(u.rows, metadataRow{meta, en.RemovedAt != nil})
			}
			universes[key] = u
		}
		a := TeamsDraftAssociation{MetadataState: "missing", TargetState: "association_unknown", ScanComplete: u.complete, EvidenceScope: "account_scoped_archive_not_source_profile"}
		historical, contradictory := false, false
		var matched teamsdrafts.Metadata
		for _, m := range u.rows {
			match, conflict := teamsdrafts.MetadataMatches(body, m.value)
			if match {
				a.ObservedMatchesLowerBound++
				matched = m.value
				historical = historical || m.removed
			}
			contradictory = contradictory || conflict
		}
		switch {
		case !u.complete:
			a.MetadataState = "incomplete"
		case contradictory:
			a.MetadataState = "contradictory"
		case a.ObservedMatchesLowerBound > 1:
			a.MetadataState = "ambiguous"
		case a.ObservedMatchesLowerBound == 1:
			a.MetadataState = "matched"
			if historical || e.RemovedAt != nil {
				a.MetadataState = "historical_only"
			}
			if body.Claim.SourceThreadID.Value != nil {
				target := *body.Claim.SourceThreadID.Value
				ids, complete, err := draftRowIDs(ctx, r, "conversations", "tenant_id=? and user_id=? and id=?", []any{e.TenantID, e.UserID, target}, draftRowCap-budget.rows)
				if err != nil {
					return out, err
				}
				a.ConversationComplete = complete
				if len(ids) == 1 {
					v, ok, err := budget.text(ctx, r, "conversations", []string{"id", "kind", "title", "team_id"}, ids[0])
					if err != nil {
						return out, err
					}
					a.ConversationComplete = a.ConversationComplete && ok
					if ok {
						a.ConversationID = &v[0].String
						a.Kind = &v[1].String
						a.Title = &v[2].String
						a.TeamID = &v[3].String
						if matched.ConversationType.Value != nil && *matched.ConversationType.Value != v[1].String {
							a.MetadataState = "contradictory"
						} else if complete && v[0].Valid && v[1].Valid {
							a.TargetState = "resolved"
							if a.MetadataState == "historical_only" {
								a.TargetState = "historical"
							}
						}
					}
				}
			}
		}
		if !a.ScanComplete || !a.ConversationComplete && a.MetadataState == "matched" {
			out.Coverage.AssociationScanComplete = false
		}
		out.Rows = append(out.Rows, TeamsDraftRow{body, a})
	}
	auxComplete, err := draftAuxiliary(ctx, r, budget, scopes, &out.Coverage)
	if err != nil {
		return out, err
	}
	out.Coverage.ScanComplete = bodyComplete && out.Coverage.AssociationScanComplete && auxComplete
	out.Coverage.LossesComplete = bodyComplete && auxComplete
	if !bodyComplete {
		out.Coverage.Ordering = "bounded_prefix_tuple_order"
	}
	out.Coverage.VisitedRows = budget.rows
	out.Coverage.VisitedMembers = budget.members
	out.Coverage.InputBytes = budget.bytes
	if f.ID != "" {
		selected := []TeamsDraftRow{}
		for _, row := range out.Rows {
			if row.Body.ID == f.ID {
				selected = append(selected, row)
			}
		}
		if len(selected) == 0 {
			if !bodyComplete {
				return TeamsDraftSnapshot{}, ErrTeamsDraftScanIncomplete
			}
			return TeamsDraftSnapshot{}, ErrTeamsDraftUnknown
		}
		if selected[0].Body.Envelope.RemovedAt != nil && !f.IncludeRemoved {
			return TeamsDraftSnapshot{}, ErrTeamsDraftRemoved
		}
		if len(selected) > 1 {
			return TeamsDraftSnapshot{}, ErrTeamsDraftAmbiguous
		}
		if f.BodyRevision != "" && selected[0].Body.BodyRevision != f.BodyRevision {
			return TeamsDraftSnapshot{}, ErrTeamsDraftBodyRevisionChanged
		}
		out.Rows = selected
		return out, nil
	}
	retained := out.Rows[:0]
	for _, row := range out.Rows {
		if row.Body.Envelope.RemovedAt == nil || f.IncludeRemoved {
			retained = append(retained, row)
		}
	}
	out.Rows = retained
	sort.Slice(out.Rows, func(i, j int) bool {
		a, b := out.Rows[i].Body, out.Rows[j].Body
		for _, pair := range [][2]string{{a.Envelope.Source, b.Envelope.Source}, {a.Envelope.Database, b.Envelope.Database}, {a.Envelope.KeyJSON, b.Envelope.KeyJSON}, {a.MemberKey, b.MemberKey}} {
			if pair[0] != pair[1] {
				return pair[0] < pair[1]
			}
		}
		return false
	})
	if bodyComplete {
		total := len(out.Rows)
		out.Total = &total
	}
	limit := f.Limit
	if limit == 0 {
		limit = 50
	}
	if len(out.Rows) > limit {
		out.Truncated = true
		out.Rows = out.Rows[:limit]
	}
	return out, nil
}

func draftAuxiliary(ctx context.Context, r knowledgeReader, budget *draftBudget, scopes map[[3]string]bool, c *TeamsDraftCoverage) (bool, error) {
	complete := true
	keys := make([][3]string, 0, len(scopes))
	for key := range scopes {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		for n := range keys[i] {
			if keys[i][n] != keys[j][n] {
				return keys[i][n] < keys[j][n]
			}
		}
		return false
	})
	sources := map[string]bool{}
	for _, key := range keys {
		sources[key[0]] = true
		ids, done, err := draftRowIDs(ctx, r, "records", "source=? and tenant_id=? and user_id=? and store='drafts-internal-items' and database like 'Teams:messaging-slice-manager:%'", []any{key[0], key[1], key[2]}, draftRowCap-budget.rows)
		if err != nil {
			return false, err
		}
		complete = complete && done
		for _, id := range ids {
			v, ok, err := budget.text(ctx, r, "records", draftRecordColumns, id)
			if err != nil {
				return false, err
			}
			if !ok || !v[6].Valid || !json.Valid([]byte(v[6].String)) {
				complete = false
				c.AdvisoryState = "incomplete"
				continue
			}
			if c.AdvisoryState != "incomplete" {
				c.AdvisoryState = "available_opaque_not_body_total"
			}
		}
		if !done {
			c.AdvisoryState = "incomplete"
		}
	}
	// Run-level account receipts and source attempts are distinct universes.
	ids, done, err := draftRowIDs(ctx, r, "sync_runs", "source='' or source in (select source from records where store='drafts' and database like 'Teams:streams-replychain-manager:%')", nil, draftRowCap-budget.rows)
	if err != nil {
		return false, err
	}
	complete = complete && done
	for _, id := range ids {
		v, ok, err := budget.text(ctx, r, "sync_runs", []string{"source", "status", "started_at", "finished_at", "accounts_json", "omissions_json"}, id)
		if err != nil {
			return false, err
		}
		if !ok {
			complete = false
			continue
		}
		source, status := v[0].String, v[1].String
		if source != "" && !sources[source] {
			continue
		}
		if source == "" {
			if !v[4].Valid {
				continue
			}
			var accounts []string
			if json.Unmarshal([]byte(v[4].String), &accounts) != nil {
				complete = false
				continue
			}
			for _, key := range keys {
				covered := false
				for _, account := range accounts {
					if account == "*" || account == key[1]+"/"+key[2] {
						covered = true
					}
				}
				if covered && (status == "ok" || status == "ok_with_omissions" || status == "unchanged") {
					at := v[3]
					if !at.Valid {
						at = v[2]
					}
					if at.Valid {
						c.Clocks = append(c.Clocks, TeamsDraftClock{Scope: key[1] + "/" + key[2], Kind: "account_success_not_draft_store", At: &at.String})
					}
				}
			}
			continue
		}
		if v[2].Valid {
			c.Clocks = append(c.Clocks, TeamsDraftClock{Source: source, Scope: "source_wide_not_draft_store", Kind: "attempt_started", At: &v[2].String})
		}
		if v[3].Valid {
			c.Clocks = append(c.Clocks, TeamsDraftClock{Source: source, Scope: "source_wide_not_draft_store", Kind: "attempt_finished_" + status, At: &v[3].String})
		}
		if v[5].Valid {
			var losses map[string]int
			if json.Unmarshal([]byte(v[5].String), &losses) != nil {
				complete = false
				continue
			}
			names := make([]string, 0, len(losses))
			for name := range losses {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				if losses[name] < 0 {
					complete = false
					continue
				}
				c.ObservedLossesLowerBound = append(c.ObservedLossesLowerBound, teamsdrafts.Diagnostic{Code: name, Category: "source_wide_not_draft_store", Count: losses[name]})
			}
		}
	}
	return complete, nil
}
