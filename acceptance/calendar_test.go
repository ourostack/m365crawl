//go:build acceptance

package acceptance

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/calendar"
	"github.com/ourostack/m365crawl/internal/syncer"
	"github.com/ourostack/m365crawl/internal/teamscal"
)

// The Teams calendar checks (plan-calendar-teams.md, "Real-cache acceptance"). They run against the
// calendar, catch-up and recap stores of the Teams root (M365CRAWL_TEAMS_ROOT, or the default),
// synced once into a scratch archive. They log counts, field names, zone labels and durations only:
// never a subject, name, address, URL, id or body. A check fails only where its text says it must
// (every record maps, field parity, no redacted join URL, append-only); the others report.
//
// Order matters in one place: TestRealCalendarAppendOnly syncs the shared archive a second time, so
// it is the last test of this file.

// liveCalendarRecords selects the calendar store's records that are live and decoded.
const liveCalendarRecords = `store='calendar' and database like 'Teams:calendar:%' and removed_at is null and value_json is not null`

// recapHasContent is the content predicate `calendar sources` uses: a recap row with text or items.
const recapHasContent = `(r.headline<>'' or r.short_summary<>'' or r.outline<>'' or r.summary_sections_json<>'' or exists (
  select 1 from calendar_recap_items i where i.account_id=r.account_id and i.call_id=r.call_id and i.superseded_at is null))`

// (1) Every record in the real calendar store maps.
func TestRealCalendarEveryRecordMaps(t *testing.T) {
	run := calendarArchive(t)
	st := openArchive(t, run.db)
	records := count(t, st, `select count(*) from records where `+liveCalendarRecords)
	events := count(t, st, `select count(*) from calendar_source_events where source='teams'`)
	unmapped, refused := run.rep.Omissions[storeOmitUnmapped], run.rep.Omissions[storeOmitRefused]
	t.Logf("calendar records=%d mapped_event_rows=%d calendar_unmapped=%d calendar_refused=%d (sync status %s in %v)",
		records, events, unmapped, refused, run.rep.Status, run.wall.Round(time.Millisecond))
	if records == 0 {
		t.Fatal("the Teams root holds no calendar records")
	}
	if unmapped != 0 {
		t.Errorf("calendar_unmapped is %d, want 0", unmapped)
	}
	if refused != 0 {
		t.Errorf("calendar_refused is %d, want 0", refused)
	}
	if events != records {
		t.Errorf("%d mapped event rows for %d calendar records, want equal", events, records)
	}
}

const (
	storeOmitUnmapped = "calendar_unmapped"
	storeOmitRefused  = "calendar_refused"
)

// jsonArrayLen is the SQL expression for the number of elements of the array at path of the record
// value v, where Teams may store the array itself or a JSON string holding it.
func jsonArrayLen(path string) string {
	return fmt.Sprintf(`(case json_type(v,'%[1]s') when 'array' then json_array_length(v,'%[1]s')
	  when 'text' then (case when json_valid(json_extract(v,'%[1]s')) then json_array_length(json_extract(v,'%[1]s')) else 0 end) else 0 end)`, path)
}

// (2) Field-presence parity between the records' JSON and the mapped columns, with SQLite JSON
// functions over `records` joined to the event rows by record key.
func TestRealCalendarFieldParity(t *testing.T) {
	st := openArchive(t, calendarArchive(t).db)
	const joined = `from (select json_extract(key_json,'$') k, tenant_id||'/'||user_id acct, value_json v from records where ` + liveCalendarRecords + `) r
	  join calendar_source_events e on e.source='teams' and e.account_id=r.acct and e.source_id=r.k`
	events := count(t, st, `select count(*) from calendar_source_events where source='teams'`)
	pairs := count(t, st, `select count(*) `+joined)
	t.Logf("records joined to event rows: %d of %d event rows", pairs, events)
	if pairs != events {
		t.Errorf("%d event rows joined to a record by key, want all %d", pairs, events)
	}
	cid := `coalesce(nullif(json_extract(v,'$.skypeTeamsDataObject.cid'),''), nullif(json_extract(v,'$.skypeTeamsData.cid'),''),
	  (case when json_valid(json_extract(v,'$.skypeTeamsData')) then nullif(json_extract(json_extract(v,'$.skypeTeamsData'),'$.cid'),'') end), '')<>''`
	for _, f := range []struct {
		name, rec, col string
		oneWay         bool // the column may be set without the record saying so (thread id from the join URL)
	}{
		{"join_url", `coalesce(json_extract(v,'$.skypeTeamsMeetingUrl'),'')<>''`, `e.online_meeting_url<>''`, false},
		{"thread_id", cid, `e.teams_thread_id<>''`, true},
		{"attendees", jsonArrayLen("$.attendees") + `>0`, `e.attendees_json<>''`, false},
		{"body", `coalesce(json_extract(v,'$.bodyContent'),'')<>''`, `e.body_html<>''`, false},
		{"structured_locations", jsonArrayLen("$.meetingLocations") + `>0`, `e.locations_json<>''`, false},
		{"attachments", jsonArrayLen("$.attachments") + `>0`, `e.attachments_json<>''`, false},
	} {
		rec := count(t, st, `select count(*) `+joined+` where `+f.rec)
		col := count(t, st, `select count(*) `+joined+` where `+f.col)
		recOnly := count(t, st, `select count(*) `+joined+` where (`+f.rec+`) and not (`+f.col+`)`)
		colOnly := count(t, st, `select count(*) `+joined+` where not (`+f.rec+`) and (`+f.col+`)`)
		t.Logf("parity %-20s records_with=%d columns_with=%d record_only=%d column_only=%d", f.name, rec, col, recOnly, colOnly)
		if recOnly != 0 {
			t.Errorf("%s: %d records carry it and the mapped column is empty", f.name, recOnly)
		}
		if colOnly != 0 && !f.oneWay {
			t.Errorf("%s: %d mapped columns are set and the record has none", f.name, colOnly)
		}
	}
}

// (3) Rich counts, reported against the spike's numbers.
func TestRealCalendarRichCounts(t *testing.T) {
	st := openArchive(t, calendarArchive(t).db)
	c := calendarThresholds
	for _, r := range []struct {
		name  string
		spike int
		q     string
	}{
		{"join_links", c.SpikeJoinLinks, `online_meeting_url<>''`},
		{"with_attendees", c.SpikeWithAttendees, `attendees_json<>''`},
		{"with_body", c.SpikeWithBody, `body_html<>''`},
		{"structured_rooms", c.SpikeStructuredRooms, `locations_json<>''`},
	} {
		got := count(t, st, `select count(*) from calendar_source_events where source='teams' and removed_at is null and `+r.q)
		t.Logf("rich %-17s %d (spike %d, difference %+d)", r.name, got, r.spike, got-r.spike)
	}
}

// (4) Recap counts and link rate over recap rows with content, by link method, and the time
// tolerance histogram for recaps that match by iCalUID.
func TestRealCalendarRecaps(t *testing.T) {
	st := openArchive(t, calendarArchive(t).db)
	total := count(t, st, `select count(*) from calendar_recaps`)
	content := count(t, st, `select count(*) from calendar_recaps r where `+recapHasContent)
	items := count(t, st, `select count(*) from calendar_recap_items where superseded_at is null`)
	t.Logf("recap rows=%d with_content=%d without_content=%d live_items=%d", total, content, total-content, items)
	byMethod := map[string]int{}
	for _, row := range rowsOf(t, st, `select case when r.ical_uid='' then 'unlinked' when r.link_method='' then 'ical_uid' else r.link_method end, count(*)
	  from calendar_recaps r where `+recapHasContent+` group by 1`) {
		byMethod[asString(row[0])] = asInt(row[1])
	}
	linked := 0
	for m, n := range byMethod {
		if m != "unlinked" {
			linked += n
		}
	}
	t.Logf("recaps with content by link method: %s; link rate %d of %d (%.1f%%)", countsLine(byMethod), linked, content, 100*ratio(linked, content))
	origins := map[string]int{}
	for _, row := range rowsOf(t, st, `select case when has_catchup=1 and has_recap=1 then 'both' when has_catchup=1 then 'store1' else 'store2' end, count(*)
	  from calendar_recaps r where `+recapHasContent+` group by 1`) {
		origins[asString(row[0])] = asInt(row[1])
	}
	t.Logf("recaps with content by origin: %s", countsLine(origins))

	startH, endH := newHistogram(recapDiffEdges, recapDiffOver), newHistogram(recapDiffEdges, recapDiffOver)
	within, n := 0, 0
	for _, row := range rowsOf(t, st, `select r.meeting_start_at, r.meeting_end_at, e.start_at, e.end_at from calendar_recaps r
	  join calendar_source_events e on e.source='teams' and e.account_id=r.account_id and e.ical_uid=r.ical_uid and e.removed_at is null and e.event_type<>'master'
	  where `+recapHasContent+` and r.ical_uid<>'' and r.link_method in ('','ical_uid') and r.meeting_start_at is not null and r.meeting_end_at is not null`) {
		ms, ok1 := parseTime(asString(row[0]))
		me, ok2 := parseTime(asString(row[1]))
		es, ok3 := parseTime(asString(row[2]))
		ee, ok4 := parseTime(asString(row[3]))
		if !ok1 || !ok2 || !ok3 || !ok4 {
			continue
		}
		ds, de := absDuration(ms.Sub(es)), absDuration(me.Sub(ee))
		startH.add(bucketOf(ds, recapDiffEdges, recapDiffOver))
		endH.add(bucketOf(de, recapDiffEdges, recapDiffOver))
		n++
		if ds <= teamscal.RecapTimeTolerance && de <= teamscal.RecapTimeTolerance {
			within++
		}
	}
	t.Logf("recap-to-event time difference, recaps matched by iCalUID (%d): start %s", n, startH)
	t.Logf("recap-to-event time difference, recaps matched by iCalUID (%d): end   %s", n, endH)
	t.Logf("within teamscal.RecapTimeTolerance (%v) on both: %d of %d", teamscal.RecapTimeTolerance, within, n)
}

// (5) Unknown time-zone names (zone labels may be logged).
func TestRealCalendarUnknownZones(t *testing.T) {
	st := openArchive(t, calendarArchive(t).db)
	unknown := map[string]int{}
	for _, row := range rowsOf(t, st, `select time_zone, count(*) from calendar_source_events where source='teams' and time_zone<>'' and time_zone_iana='' group by time_zone`) {
		unknown[asString(row[0])] = asInt(row[1])
	}
	known := count(t, st, `select count(distinct time_zone) from calendar_source_events where source='teams' and time_zone_iana<>''`)
	t.Logf("zones: %d distinct resolved names; unknown names (events): %s", known, countsLine(unknown))
}

// (6) No join URL contains the scrubber's marker.
func TestRealCalendarNoRedactedJoinURL(t *testing.T) {
	st := openArchive(t, calendarArchive(t).db)
	urls := count(t, st, `select count(*) from calendar_source_events where source='teams' and (online_meeting_url<>'' or short_join_url<>'')`)
	bad := count(t, st, `select count(*) from calendar_source_events where source='teams' and (instr(online_meeting_url,'[redacted]')>0 or instr(short_join_url,'[redacted]')>0)`)
	t.Logf("events with a join URL: %d; containing [redacted]: %d", urls, bad)
	if bad != 0 {
		t.Errorf("%d events have a join URL that contains [redacted]", bad)
	}
}

// (8) The all-day, cancelled and declined counts, expected 0 until a real one exists.
func TestRealCalendarUnobservedShapes(t *testing.T) {
	st := openArchive(t, calendarArchive(t).db)
	c := calendarThresholds
	for _, s := range []struct {
		name string
		want int
		q    string
	}{
		{"all_day", c.ExpectAllDay, `all_day=1`},
		{"cancelled", c.ExpectCancelled, `cancelled=1`},
		{"declined", c.ExpectDeclined, `response='declined'`},
	} {
		got := count(t, st, `select count(*) from calendar_source_events where source='teams' and `+s.q)
		note := ""
		if got != s.want {
			note = " (NEW SHAPE: record its field names and re-check the mapper's assumption)"
		}
		t.Logf("%s events: %d (expected %d)%s", s.name, got, s.want, note)
	}
}

// occurrenceAgreement counts, for the occurrence and exception rows of one source, how many have
// OccurrenceID(series_key, date of the original start) equal to the row's iCalUID, trying the
// event's own IANA zone first and then UTC.
type occurrenceCounts struct{ total, ownZone, utc, none int }

func occurrenceAgreement(rows [][]any) occurrenceCounts {
	var c occurrenceCounts
	for _, r := range rows {
		series, orig, zone, uid := asString(r[0]), asString(r[1]), asString(r[2]), strings.ToUpper(asString(r[3]))
		ts, ok := parseTime(orig)
		if !ok {
			continue
		}
		c.total++
		if loc, err := time.LoadLocation(zone); zone != "" && err == nil && calendar.OccurrenceID(series, ts.In(loc)) == uid {
			c.ownZone++
		} else if calendar.OccurrenceID(series, ts.UTC()) == uid {
			c.utc++
		} else {
			c.none++
		}
	}
	return c
}

func (c occurrenceCounts) log(t *testing.T, what string) {
	t.Helper()
	agree := c.ownZone + c.utc
	t.Logf("%s OccurrenceID agreement: %d of %d (event's own zone %d, UTC only %d, no agreement %d)", what, agree, c.total, c.ownZone, c.utc, c.none)
	if c.none > 0 {
		t.Logf("FINDING: %d %s occurrences do not agree; correct the encoding or the zone rule (assumption A10)", c.none, what)
	}
}

// (9) The OccurrenceID agreement count and the recording-lag histogram.
func TestRealCalendarOccurrenceIDAndRecordingLag(t *testing.T) {
	st := openArchive(t, calendarArchive(t).db)
	rows := rowsOf(t, st, `select series_key, coalesce(original_start, start_at), time_zone_iana, ical_uid from calendar_source_events
	  where source='teams' and removed_at is null and event_type in ('`+calendar.EventOccurrence+`','`+calendar.EventException+`') and series_key<>'' and ical_uid<>''`)
	occurrenceAgreement(rows).log(t, "Teams")

	lag := newHistogram(recordingLagEdges, recordingLagOver)
	unmatched := 0
	type occ struct{ start, end time.Time }
	byThread := map[string][]occ{}
	for _, r := range rowsOf(t, st, `select teams_thread_id, start_at, end_at from calendar_source_events
	  where source='teams' and removed_at is null and event_type<>'master' and teams_thread_id<>'' and all_day is not 1`) {
		s, ok1 := parseTime(asString(r[1]))
		e, ok2 := parseTime(asString(r[2]))
		if ok1 && ok2 {
			byThread[asString(r[0])] = append(byThread[asString(r[0])], occ{s, e})
		}
	}
	messages := 0
	for _, r := range rowsOf(t, st, `select conversation_id, sent_at from messages where message_type in ('RichText/Media_CallRecording','RichText/Media_CallTranscript')`) {
		occs := byThread[asString(r[0])]
		sent, ok := parseTime(asString(r[1]))
		if !ok || len(occs) == 0 {
			continue
		}
		messages++
		// The occurrence the message belongs to is the latest one that had started when it was sent.
		var best *occ
		for i := range occs {
			if !occs[i].start.After(sent) && (best == nil || occs[i].start.After(best.start)) {
				best = &occs[i]
			}
		}
		if best == nil {
			unmatched++
			continue
		}
		lag.add(bucketOf(sent.Sub(best.end), recordingLagEdges, recordingLagOver))
	}
	t.Logf("recording and transcript messages of cached series: %d; lag after the occurrence ended: %s; sent before any occurrence started: %d", messages, lag, unmatched)
}

// (10) A field-name census of calendar-internal-data/syncState: names and counts only.
func TestRealCalendarSyncStateCensus(t *testing.T) {
	st := openArchive(t, calendarArchive(t).db)
	rows := rowsOf(t, st, `select value_json from records where store='calendar-internal-data' and key_json like '%syncState%' and removed_at is null and value_json is not null`)
	names := map[string]int{}
	for _, r := range rows {
		var m map[string]any
		if json.Unmarshal([]byte(asString(r[0])), &m) != nil {
			names["<not an object>"]++
			continue
		}
		for k, v := range m {
			names[safeLabel(k)+":"+jsonKind(v)]++
		}
	}
	t.Logf("syncState records: %d; top-level field names and kinds: %s", len(rows), countsLine(names))
}

func jsonKind(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "bool"
	case float64:
		return "number"
	case string:
		return "string"
	case []any:
		return "array"
	}
	return "object"
}

// (11) Recaps whose expires_at has passed, and whether their rows are still present.
func TestRealCalendarExpiredRecaps(t *testing.T) {
	st := openArchive(t, calendarArchive(t).db)
	now := time.Now().UTC()
	total, expired, expiredWithContent, noExpiry := 0, 0, 0, 0
	for _, r := range rowsOf(t, st, `select r.expires_at, `+recapHasContent+` from calendar_recaps r`) {
		total++
		exp, ok := parseTime(asString(r[0]))
		switch {
		case !ok:
			noExpiry++
		case exp.Before(now):
			expired++
			if asInt(r[1]) == 1 {
				expiredWithContent++
			}
		}
	}
	t.Logf("recap rows %d: expires_at passed %d (still present with content %d), no expires_at %d; rows are kept after expiry by design", total, expired, expiredWithContent, noExpiry)
	if expired > 0 && expiredWithContent == 0 {
		t.Logf("FINDING: every expired recap row has lost its content")
	}
}

// (7) Append-only on a real archive: a second sync leaves calendar_source_events non-decreasing and
// detail_as_of never moves backward. It syncs the shared archive again, so it runs last.
func TestRealCalendarAppendOnly(t *testing.T) {
	run := calendarArchive(t)
	type row struct {
		firstSeen string
		detail    time.Time
	}
	read := func() map[string]row {
		st := openArchive(t, run.db)
		out := map[string]row{}
		for _, r := range rowsOf(t, st, `select account_id||'|'||event_key, first_seen_at, coalesce(detail_as_of,'') from calendar_source_events where source='teams'`) {
			d, _ := parseTime(asString(r[2]))
			out[asString(r[0])] = row{asString(r[1]), d}
		}
		return out
	}
	before := read()
	start := time.Now()
	rep, _, err := syncer.Run(context.Background(), syncer.Options{Root: calendarTeamsRoot(), DBPath: run.db})
	if err != nil {
		t.Fatalf("second sync: %v", err)
	}
	after := read()
	t.Logf("second sync: status %s in %v; calendar_source_events %d -> %d", rep.Status, time.Since(start).Round(time.Millisecond), len(before), len(after))
	if len(after) < len(before) {
		t.Errorf("calendar_source_events fell from %d to %d rows", len(before), len(after))
	}
	missing, moved, firstSeenChanged := 0, 0, 0
	for k, b := range before {
		a, ok := after[k]
		switch {
		case !ok:
			missing++
		case a.detail.Before(b.detail):
			moved++
		}
		if ok && a.firstSeen != b.firstSeen {
			firstSeenChanged++
		}
	}
	t.Logf("rows missing %d, detail_as_of moved backward %d, first_seen_at changed %d", missing, moved, firstSeenChanged)
	if missing != 0 || moved != 0 || firstSeenChanged != 0 {
		t.Errorf("not append-only: %d rows vanished, %d detail_as_of moved backward, %d first_seen_at changed", missing, moved, firstSeenChanged)
	}
}
