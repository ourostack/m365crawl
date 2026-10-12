package store

import "context"

// OverviewCounts are the archived rows of the sources the overview reports beside chats, mail and
// the calendar. A count is zero when an older archive lacks the source's table.
type OverviewCounts struct {
	Notes, Documents, PeopleObservations, TeamsPeople, MailPeople int
}

// overviewCountQueries count each source's rows: notes are pages (entity type 1), as notes list
// returns them; documents and people observations count every retained row.
var overviewCountQueries = []struct {
	table, query string
	into         func(*OverviewCounts) *int
	byTenant     bool // the query takes the tenant: only Teams people carry one
}{
	{"note_entities", `select count(*) from note_entities where type=1`, func(c *OverviewCounts) *int { return &c.Notes }, false},
	{"document_observations", `select count(*) from document_observations`, func(c *OverviewCounts) *int { return &c.Documents }, false},
	{"people_observations", `select count(*) from people_observations`, func(c *OverviewCounts) *int { return &c.PeopleObservations }, false},
	{"people", `select count(*) from people where ? in ('', tenant_id)`, func(c *OverviewCounts) *int { return &c.TeamsPeople }, true},
}

// OverviewCounts counts the archive's notes, documents and people. A non-empty tenant narrows the
// Teams people; nothing else in it records a tenant. It only reads.
func (s *Store) OverviewCounts(ctx context.Context, tenant string) (OverviewCounts, error) {
	var out OverviewCounts
	for _, q := range overviewCountQueries {
		var present bool
		if err := s.db.QueryRowContext(ctx, `select count(*)=1 from sqlite_master where type='table' and name=?`, q.table).Scan(&present); err != nil {
			return OverviewCounts{}, err
		}
		if !present {
			continue
		}
		var args []any
		if q.byTenant {
			args = append(args, tenant)
		}
		if err := s.db.QueryRowContext(ctx, q.query, args...).Scan(q.into(&out)); err != nil {
			return OverviewCounts{}, err
		}
	}
	mail, err := s.mailPeopleCount(ctx, "")
	if err != nil {
		return OverviewCounts{}, err
	}
	out.MailPeople = mail
	return out, nil
}
