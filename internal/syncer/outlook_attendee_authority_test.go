package syncer

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"
)

func attendeeArchiveState(t *testing.T, path string) string {
	t.Helper()
	db := openRaw(t, path)
	var out strings.Builder
	for _, query := range []string{
		`select * from calendar_source_events`, `select * from calendar_sources`,
		`select * from calendar_covered_days`, `select * from calendar_account_links`,
		`select * from calendar_matches`, `select * from calendar_recaps`,
		`select * from calendar_recap_items`, `select * from calendar_losses`,
		`select * from outlook_calendar_reads`, `select * from outlook_calendar_read_state`,
		`select * from outlook_link_events`, `select * from outlook_link_state`,
		`select * from outlook_attendee_sources`, `select * from outlook_attendee_requests`,
		`select * from outlook_attendee_snapshots`, `select * from outlook_attendee_occurrences`,
		`select * from outlook_attendee_bindings`, `select * from outlook_attendee_members`,
		`select * from mail_folders`, `select * from mail_messages`,
		`select * from mail_recipients`, `select * from mail_attachments`,
		`select * from mail_coverage`, `select * from mail_absent`,
		`select * from mail_source_ids`, `select * from knowledge_locators`,
		`select * from knowledge_attempts`, `select * from knowledge_sources`,
		`select * from meta`, `select * from sync_runs`,
	} {
		rows, err := db.Query(query)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		out.WriteString(query)
		var encoded []string
		for rows.Next() {
			values := make([]any, len(columns))
			targets := make([]any, len(columns))
			for i := range values {
				targets[i] = &values[i]
			}
			if err := rows.Scan(targets...); err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(values)
			if err != nil {
				t.Fatal(err)
			}
			encoded = append(encoded, string(raw))
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
		sort.Strings(encoded)
		out.WriteString(strings.Join(encoded, "\n"))
	}
	return out.String()
}
