package store

import (
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/calendar"
)

func TestOutlookMapperBumpPreservesUnknownKnowledge(t *testing.T) {
	s := newStore(t)
	event := outlookEvent(calendar.TriUnknown, base)
	applyOutlookBatch(t, s, OutlookStamp(1, time.UTC), event)
	const expected = "attachments,attendees,body,body_preview,categories,dial_in,join_url,meeting_chat_id,recurrence,reminder,response,rooms,short_join_url,show_as,time_zone,time_zone_iana,utc_offset"
	var firstSeen string
	if err := s.db.QueryRow(`select first_seen_at from calendar_source_events where source_id='AA11'`).Scan(&firstSeen); err != nil {
		t.Fatal(err)
	}
	for step, stamp := range []string{OutlookStamp(1, time.UTC), OutlookStamp(2, time.UTC)} {
		applyOutlookBatch(t, s, stamp, event)
		var unknown, first string
		if err := s.db.QueryRow(`select unknown_fields,first_seen_at from calendar_source_events where source_id='AA11'`).Scan(&unknown, &first); err != nil {
			t.Fatal(err)
		}
		if unknown != expected || first != firstSeen {
			t.Fatalf("calendar requalification knowledge step=%d unknown_fields=%q first_seen_changed=%t", step, unknown, first != firstSeen)
		}
	}
}
