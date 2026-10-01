package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ourostack/teamscrawl/internal/errs"
	"github.com/ourostack/teamscrawl/internal/teamsdesktop"
)

// maxTeamMatches caps how many candidates an ambiguous team name lists.
const maxTeamMatches = 10

// resolveTeam turns a --team value (a team id, or a team name compared case-insensitively and
// exactly) into one team id. A team is a conversation other conversations point at through
// team_id (chats point at themselves, so they never qualify). No match, or a name shared by
// several teams, is a usage error that names how to continue.
func (s *Store) resolveTeam(ctx context.Context, acct *teamsdesktop.Account, q string) (string, error) {
	var w where
	w.add(`exists(select 1 from conversations x where x.tenant_id=t.tenant_id and x.user_id=t.user_id and x.team_id=t.id and x.id<>t.id)`)
	w.add(`(t.id=? or lower(`+teamName+`)=lower(?))`, q, q)
	if acct != nil {
		w.add(`t.tenant_id=? and t.user_id=?`, acct.TenantID, acct.UserID)
	}
	rows, err := s.db.QueryContext(ctx, `select t.id,min(`+teamName+`) from conversations t`+w.sql()+` group by t.id order by min(`+teamName+`), t.id limit ?`, append(w.args, maxTeamMatches+1)...) //nolint:gosec // G202: fragments are package constants; values are placeholders
	if err != nil {
		return "", err
	}
	defer func() { _ = rows.Close() }()
	var ids, labels []string
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return "", err
		}
		ids = append(ids, id)
		labels = append(labels, fmt.Sprintf("%s (%s)", name, id))
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	switch len(ids) {
	case 0:
		return "", NoTeam(q)
	case 1:
		return ids[0], nil
	}
	more := ""
	if len(ids) > maxTeamMatches {
		labels, more = labels[:maxTeamMatches], ", ..."
	}
	c := errs.Usage(fmt.Sprintf("team %q is ambiguous: %s%s", q, strings.Join(labels, ", "), more))
	c.Fix = "Pass the team's id (the value in parentheses) to --team."
	return "", c
}

// NoTeam is the usage error for a --team that matches nothing.
func NoTeam(q string) *errs.Coded {
	c := errs.Usage(fmt.Sprintf("no team matches %q", q))
	c.Fix = "List the teams with `teamscrawl teams`, then pass the team's exact display_name or its team_id to --team. A team Teams has not written to the cache yet is not archived: run `teamscrawl sync` first."
	return c
}

// CheckTeam reports the usage error resolveTeam would raise for q, without running a query.
func (s *Store) CheckTeam(ctx context.Context, acct *teamsdesktop.Account, q string) error {
	_, err := s.resolveTeam(ctx, acct, q)
	return err
}

// convRef names one conversation of one account.
type convRef struct{ tenant, user, id string }

// nameUntitled gives every row whose conversation display name is empty a name built from the
// conversation's other members: up to three names from the people table, then "+N" for the
// rest ("Ana, Ben, Chao +2"). When no member's name is known it is "Unnamed chat (N members, id
// <short id>)", so no two chats share a name and none is blank. A row whose conversation is not
// archived stays blank: there is nothing to name it from. at returns the row's conversation and
// a pointer to its display name.
func nameUntitled[R any](ctx context.Context, s *Store, rows []R, at func(*R) (convRef, *string)) error {
	need := map[convRef][]*string{}
	for i := range rows {
		if ref, name := at(&rows[i]); *name == "" && ref.id != "" {
			need[ref] = append(need[ref], name)
		}
	}
	for ref, names := range need {
		name, err := s.untitledName(ctx, ref)
		if err != nil {
			return err
		}
		for _, p := range names {
			*p = name
		}
	}
	return nil
}

// untitledName builds the name described at nameUntitled for one conversation, or "" when the
// conversation is not archived.
func (s *Store) untitledName(ctx context.Context, ref convRef) (string, error) {
	var kind string
	var members sql.NullString
	err := s.db.QueryRowContext(ctx, `select kind,members_json from conversations where tenant_id=? and user_id=? and id=?`, ref.tenant, ref.user, ref.id).Scan(&kind, &members)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	all := unmarshalNull[string](members)
	me := strings.ToLower("8:orgid:" + ref.user)
	var others []string
	for _, id := range all {
		if strings.ToLower(id) != me {
			others = append(others, id)
		}
	}
	names, err := s.personNames(ctx, ref.tenant, others)
	if err != nil {
		return "", err
	}
	var known []string
	for _, id := range others {
		if n := names[id]; n != "" {
			known = append(known, n)
		}
	}
	if n := teamsdesktop.MemberListName(known, len(others)); n != "" {
		return n, nil
	}
	return unnamedConversation(kind, ref.id, len(all)), nil
}

// personNames returns the display names the people table holds for ids (those with a name).
func (s *Store) personNames(ctx context.Context, tenant string, ids []string) (map[string]string, error) {
	out := map[string]string{}
	if len(ids) == 0 {
		return out, nil
	}
	args := []any{tenant}
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := s.db.QueryContext(ctx, `select id,display_name from people where tenant_id=? and display_name<>'' and id in (?`+strings.Repeat(",?", len(ids)-1)+`)`, args...) //nolint:gosec // G202: only placeholders are repeated
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		out[id] = name
	}
	return out, rows.Err()
}

// unnamedConversation is the last-resort name of a conversation with no title and no known
// member names. The member count and a short piece of the id keep two such chats apart.
func unnamedConversation(kind, id string, members int) string {
	noun := "conversation"
	if strings.EqualFold(kind, "Chat") || strings.EqualFold(kind, "Meeting") {
		noun = "chat"
	}
	short, _, _ := strings.Cut(strings.TrimPrefix(id, "19:"), "@")
	short = short[:min(len(short), 8)]
	switch members {
	case 0:
		return fmt.Sprintf("Unnamed %s (id %s)", noun, short)
	case 1:
		return fmt.Sprintf("Unnamed %s (1 member, id %s)", noun, short)
	}
	return fmt.Sprintf("Unnamed %s (%d members, id %s)", noun, members, short)
}

// TeamRow is one team: a conversation other conversations point at through team_id.
type TeamRow struct {
	TenantID, UserID, ID string
	DisplayName          string
	ChannelCount         int       // conversations that belong to the team, not counting its own
	LastActivityAt       time.Time // newest message time across the team's own conversation and channels
	UnreadCount          int       // unread messages in the team's channels (and its own conversation)
}

// teamsFrom selects the teams: conversations that other conversations name as their team.
const teamsFrom = ` from conversations t where exists(select 1 from conversations x where x.tenant_id=t.tenant_id and x.user_id=t.user_id and x.team_id=t.id and x.id<>t.id)`

// Teams lists the archived teams, newest channel activity first. f.Account narrows to one
// account, f.Limit caps the list and f.Total receives the exact count when it is cut.
func (s *Store) Teams(ctx context.Context, f Filter) ([]TeamRow, bool, error) {
	var w where
	if f.Account != nil {
		w.add(`t.tenant_id=? and t.user_id=?`, f.Account.TenantID, f.Account.UserID)
	}
	cond := strings.TrimPrefix(w.sql(), " where ")
	if cond != "" {
		cond = " and " + cond
	}
	limit := f.limit()
	//nolint:gosec // G202: fragments are package constants; values are placeholders
	rows, err := s.db.QueryContext(ctx, `select t.tenant_id,t.user_id,t.id,`+teamName+`,
 (select count(*) from conversations x where x.tenant_id=t.tenant_id and x.user_id=t.user_id and x.team_id=t.id and x.id<>t.id),
 (select max(x.last_message_at) from conversations x where x.tenant_id=t.tenant_id and x.user_id=t.user_id and (x.team_id=t.id or x.id=t.id)) as last_activity,
 (select count(*) from messages m join conversations c on c.tenant_id=m.tenant_id and c.user_id=m.user_id and c.id=m.conversation_id where c.tenant_id=t.tenant_id and c.user_id=t.user_id and (c.team_id=t.id or c.id=t.id) and `+unreadCond+`)`+
		teamsFrom+cond+` order by last_activity desc, t.id, t.tenant_id, t.user_id limit ?`, append(w.args, limit+1)...)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = rows.Close() }()
	out := []TeamRow{}
	for rows.Next() {
		var r TeamRow
		var last sql.NullString
		if err := rows.Scan(&r.TenantID, &r.UserID, &r.ID, &r.DisplayName, &r.ChannelCount, &last, &r.UnreadCount); err != nil {
			return nil, false, err
		}
		r.LastActivityAt = parseTime(last)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	if len(out) <= limit {
		return out, false, nil
	}
	if f.Total != nil {
		if err := s.db.QueryRowContext(ctx, `select count(*)`+teamsFrom+cond, w.args...).Scan(f.Total); err != nil { //nolint:gosec // G202: fragments are package constants; values are placeholders
			return nil, false, err
		}
	}
	return out[:limit], true, nil
}
