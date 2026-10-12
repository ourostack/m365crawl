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
}{
	{"note_entities", `select count(*) from note_entities where type=1`, func(c *OverviewCounts) *int { return &c.Notes }},
	{"document_observations", `select count(*) from document_observations`, func(c *OverviewCounts) *int { return &c.Documents }},
	{"people_observations", `select count(*) from people_observations`, func(c *OverviewCounts) *int { return &c.PeopleObservations }},
	{"people", `select count(*) from people`, func(c *OverviewCounts) *int { return &c.TeamsPeople }},
}

// OverviewCounts counts the archive's notes, documents and people. It only reads.
func (s *Store) OverviewCounts(ctx context.Context) (OverviewCounts, error) {
	var out OverviewCounts
	for _, q := range overviewCountQueries {
		var present bool
		if err := s.db.QueryRowContext(ctx, `select count(*)=1 from sqlite_master where type='table' and name=?`, q.table).Scan(&present); err != nil {
			return OverviewCounts{}, err
		}
		if !present {
			continue
		}
		if err := s.db.QueryRowContext(ctx, q.query).Scan(q.into(&out)); err != nil {
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
