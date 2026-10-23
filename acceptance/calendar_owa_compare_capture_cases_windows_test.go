//go:build acceptance && windows

package acceptance

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/calendar"
	"github.com/ourostack/m365crawl/internal/outlookowacal"
	"github.com/ourostack/m365crawl/internal/store"
)

func TestOWACalendarPersistedCaptureKnowledgeLiterals(t *testing.T) {
	for _, shape := range []string{"empty_html", "empty_text", "empty_preview", "offline_no_links", "empty_join", "offline_with_join"} {
		t.Run(shape, func(t *testing.T) {
			value := owaTemporalLiteral()
			switch shape {
			case "empty_html":
				owaTemporalSet(value, "Body", owaObject("BodyType", "HTML", "Value", ""))
			case "empty_text":
				owaTemporalSet(value, "Body", owaObject("BodyType", "Text", "Value", ""))
			case "empty_preview":
				owaTemporalSet(value, "Preview", "")
			case "offline_no_links":
				owaTemporalSet(value, "IsOnlineMeeting", false)
			case "empty_join":
				owaTemporalSet(value, "OnlineMeetingJoinUrl", "")
			case "offline_with_join":
				owaTemporalSet(value, "IsOnlineMeeting", false)
				owaTemporalSet(value, "OnlineMeetingJoinUrl", "https://example.invalid/synthetic-join")
			}
			oracle, err := owaNewWindowsZoneOracle(t.Context())
			if err != nil {
				t.Fatal("synthetic_zone_oracle_refused")
			}
			event, err := owaProjectEvent(t.Context(), oracle, value)
			if err != nil {
				t.Fatal("synthetic_expected_event_refused")
			}
			row, err := owaCalendarExpectedSQL("outlook/Synthetic", event)
			if err != nil {
				t.Fatal("synthetic_expected_sql_refused")
			}
			unknown := "," + row["unknown_fields"].(string) + ","
			if (shape == "empty_html" || shape == "empty_text") &&
				(row["body_type"] != "" || !strings.Contains(unknown, ",body,")) {
				t.Fatal("empty_cached_body_invented_persisted_content_or_knowledge")
			}
			if shape == "empty_preview" && !strings.Contains(unknown, ",body_preview,") {
				t.Fatal("empty_preview_invented_a_persisted_statement")
			}
			if shape == "offline_no_links" || shape == "empty_join" {
				for _, field := range []string{"join_url", "short_join_url", "dial_in", "meeting_chat_id"} {
					if strings.Contains(unknown, ","+field+",") != (shape == "empty_join") {
						t.Fatal("empty_or_removed_meeting_link_knowledge_mismatch")
					}
				}
			}
			actual, _, err := outlookowacal.MapEvent("outlook/Synthetic", value)
			if err != nil {
				t.Fatal("synthetic_actual_mapping_refused")
			}
			db := filepath.Join(owaScratch(t), "calendar.db")
			st, err := store.Open(t.Context(), db)
			if err != nil {
				t.Fatal("synthetic_calendar_archive_open_refused")
			}
			t.Cleanup(func() { owaCloseArchive(t, st) })
			expected := owaCalendarExpectedRead{Rows: []owaEventExpected{event}, Counts: map[string]int64{}, Released: true}
			for _, field := range owaCalendarSurveyFields {
				expected.Counts[field] = 0
			}
			for _, field := range []string{"records", "decoded", "objects", "uid_valid"} {
				expected.Counts[field] = 1
			}
			now := time.Date(2031, 3, 1, 0, 0, 0, 0, time.UTC)
			for _, version := range []int{4, 5} {
				_, err = st.CommitOutlook(t.Context(), store.OutlookBatch{
					Account: "outlook/Synthetic", Origin: "synthetic-owned", Events: []calendar.Event{actual},
					At: now, FreshAt: now, Zone: time.UTC, Stamp: store.OutlookStamp(version, time.UTC), PreserveIdentity: true,
				}, func(store.CalendarResult) store.Run {
					return store.Run{Source: "outlook|Synthetic", StartedAt: now, FinishedAt: now, Status: "ok"}
				})
				if err != nil {
					t.Fatal("synthetic_actual_persistence_refused")
				}
				read := owaArchive(t, db)
				findings, compareErr := owaCompareCalendarContext(t.Context(), read, "outlook/Synthetic", expected)
				owaCloseArchive(t, read)
				if compareErr != nil || len(findings) != 0 {
					t.Fatalf("persisted_capture_literal_differential_mismatch fields=%v", findings)
				}
			}
		})
	}
}
