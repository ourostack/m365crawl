package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/calendar"
	"github.com/ourostack/m365crawl/internal/errs"
)

const (
	qTeams   = "tenant-1/aaaaaaaa-0000-0000-0000-000000000001"
	qOutlook = "tenant-1/outlook-1"
	qChat    = "19:meeting_Q@thread.v2"
	qSeries  = "SERIES-Q"
)

func qt(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func qtp(s string) *time.Time { t := qt(s); return &t }

// qEvent is a Teams occurrence of the series on a day of November 2026 at 10:00 UTC.
func qEvent(id string, day int, mods ...func(*calendar.Event)) calendar.Event {
	start := time.Date(2026, 11, day, 10, 0, 0, 0, time.UTC)
	e := calendar.Event{
		UnknownDeclared: true, Source: calendar.SourceTeams, AccountID: qTeams, SourceID: id, GlobalID: "uid-" + id, ICalUID: "uid-" + id,
		SeriesKey: qSeries, EventType: calendar.EventOccurrence, Subject: "Q sync " + id, Start: start, End: start.Add(time.Hour),
		LastModified: qtp("2026-11-01T09:00:00Z"), AllDay: calendar.TriFalse, Cancelled: calendar.TriFalse,
		IsOnlineMeeting: calendar.TriTrue, TeamsThreadID: qChat, Response: "accepted",
	}
	for _, m := range mods {
		m(&e)
	}
	return e
}

func (s *Store) qApply(t *testing.T, src calendar.Source, account string, events ...calendar.Event) {
	t.Helper()
	w := calendar.Window{Source: src, AccountID: account, Start: qt("2026-11-01T00:00:00Z"), End: qt("2026-12-01T00:00:00Z"),
		SyncedAt: qt("2026-11-01T10:00:00Z"), CacheFreshAt: qt("2026-11-01T10:00:00Z")}
	if _, err := calendar.ApplySnapshot(context.Background(), s.db, w, events, qt("2026-11-01T10:00:00Z")); err != nil {
		t.Fatal(err)
	}
}

func (s *Store) qExec(t *testing.T, q string, args ...any) {
	t.Helper()
	if _, err := s.db.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

func (s *Store) qMessage(t *testing.T, id, typ, sent, text string) {
	t.Helper()
	s.qExec(t, `insert into messages(tenant_id,user_id,conversation_id,id,sent_at,message_type,content_text,link,updated_at) values('tenant-1','aaaaaaaa-0000-0000-0000-000000000001',?,?,?,?,?,?,?)`,
		qChat, id, sent, typ, text, "https://teams.example.invalid/l/message/"+id, sent)
}

func (s *Store) qLink(t *testing.T) {
	t.Helper()
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := calendar.LinkAccount(context.Background(), tx, calendar.SourceOutlook, qOutlook, qTeams, "config", qt("2026-11-01T10:00:00Z")); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// qArchive holds a series of three occurrences (Nov 3, 10, 17 at 10:00 UTC) and its master, a
// meeting chat with messages of every kind, and recaps on the Nov 3 occurrence.
func qArchive(t *testing.T) *Store {
	t.Helper()
	s := newStore(t)
	qSeed(t, s)
	return s
}

func qSeed(t *testing.T, s *Store) {
	t.Helper()
	master := qEvent("m", 1, func(e *calendar.Event) {
		e.EventType, e.SourceID, e.GlobalID, e.ICalUID = calendar.EventMaster, "master", "uid-master", "uid-master"
		e.RecurrenceJSON = `{"pattern":"weekly"}`
	})
	o1 := qEvent("o1", 3, func(e *calendar.Event) {
		e.AttendeesJSON = `[{"name":"Ada","address":"ada@example.invalid","type":"Required","role":"required","response":"accepted"},{"name":"Bo","response":"declined"},{"name":"Cy","response":"tentative"},{"name":"Di","response":"none"}]`
		e.LocationsJSON = `[{"name":"Room 1","kind":"conferenceRoom","address":"room1@example.invalid"}]`
		e.Location = "Room 1"
		e.BodyText, e.BodyHTML, e.BodyType, e.BodyPreview = "Body text", "<p>Body text</p>", "html", "Body"
		e.AttachmentsJSON = `[{"name":"a.txt","size":3,"content_type":"text/plain"}]`
		e.CategoriesJSON = `["Red"]`
		rem := 10
		e.ReminderMinutes = &rem
		e.DetailAsOf = qtp("2026-11-01T09:00:00Z")
	})
	o2 := qEvent("o2", 10)
	o3 := qEvent("o3", 17)
	s.qApply(t, calendar.SourceTeams, qTeams, master, o1, o2, o3)
	s.qExec(t, `insert into conversations(tenant_id,user_id,id,kind,title,display_name,updated_at) values('tenant-1','aaaaaaaa-0000-0000-0000-000000000001',?,'Meeting','','Q meeting','2026-11-01T00:00:00.000Z')`, qChat)
	// o1: call event inside its window, recording inside its recap window (after the window's own
	// end), a transcript and a recording after o2 started that o2's window takes.
	s.qMessage(t, "m-call", "Event/Call", "2026-11-03T10:00:30.000Z", "Call ended")
	s.qMessage(t, "m-rec", "RichText/Media_CallRecording", "2026-11-03T11:30:00.000Z", "Recording")
	s.qMessage(t, "m-tr", "RichText/Media_CallTranscript", "2026-11-03T12:00:00.000Z", "Transcript")
	s.qMessage(t, "m-rec2", "RichText/Media_CallRecording", "2026-11-10T11:10:00.000Z", "Recording 2")
	s.qMessage(t, "m-late", "RichText/Media_CallRecording", "2026-11-25T11:10:00.000Z", "Unmatched")
	s.qMessage(t, "m-other", "RichText/Html", "2026-11-03T10:30:00.000Z", "Chat text")
	s.qExec(t, `insert into messages(tenant_id,user_id,conversation_id,id,sent_at,message_type,content_text,deleted_at,updated_at) values('tenant-1','aaaaaaaa-0000-0000-0000-000000000001',?,'m-del','2026-11-03T10:40:00.000Z','RichText/Media_CallRecording','gone','2026-11-04T00:00:00.000Z','2026-11-04T00:00:00.000Z')`, qChat)
	s.qExec(t, `insert into calendar_recaps(account_id,call_id,ical_uid,recap_id,link_method,has_catchup,has_recap,headline,short_summary,outline,summary_sections_json,speakers_json,topics_json,recording_url,recording_start_at,recording_end_at,duration_seconds,meeting_start_at,meeting_end_at,expires_at,first_seen_at,updated_at)
	  values(?,'call-1','uid-o1','r1','ical_uid',1,1,'Headline','Short','Outline','[{"title":"s"}]','[{"id":"1"}]','["t"]','https://r.example.invalid/1','2026-11-03T10:02:00.000Z','2026-11-03T11:00:00.000Z',3480,'2026-11-03T10:00:00.000Z','2026-11-03T11:00:00.000Z','2026-12-03T00:00:00.000Z','2026-11-04T00:00:00.000Z','2026-11-04T00:00:00.000Z')`, qTeams)
	s.qExec(t, `insert into calendar_recap_items(account_id,call_id,item_key,kind,origin,title,text,owner_name,speaker_name,at,mentioned_by,highlights_json,ordinal,first_seen_at,updated_at,superseded_at) values
	  (?,'call-1','a1','action_item','recap','Send notes','Send the notes','Ada','Bo',null,'','',1,'2026-11-04T00:00:00.000Z','2026-11-04T00:00:00.000Z',null),
	  (?,'call-1','a2','action_item','catchup','Old item','old','Ada','Bo',null,'','',1,'2026-11-04T00:00:00.000Z','2026-11-04T00:00:00.000Z',null),
	  (?,'call-1','a3','action_item','recap','Gone item','gone','Ada','Bo',null,'','',2,'2026-11-04T00:00:00.000Z','2026-11-04T00:00:00.000Z','2026-11-05T00:00:00.000Z'),
	  (?,'call-1','m1','mention','catchup','','Hello @you','','Bo','2026-11-03T10:20:00.000Z','Bo','["you"]',1,'2026-11-04T00:00:00.000Z','2026-11-04T00:00:00.000Z',null)`, qTeams, qTeams, qTeams, qTeams)
}

func TestEventIDShape(t *testing.T) {
	id := EventID("p", "k")
	if len(id) != 13 || !strings.HasPrefix(id, "ev_") || id != EventID("p", "k") || id == EventID("q", "k") || id == EventID("p", "j") {
		t.Fatalf("id %q", id)
	}
}

func TestCalendarAgendaRows(t *testing.T) {
	s := qArchive(t)
	ctx := context.Background()
	got, err := s.CalendarAgenda(ctx, CalendarFilter{From: qt("2026-11-03T00:00:00Z"), To: qt("2026-11-04T00:00:00Z")})
	if err != nil || got.NoTables || len(got.Rows) != 1 || got.Total != 1 || got.Truncated {
		t.Fatalf("%+v %v", got, err)
	}
	r := got.Rows[0]
	if r.EventID != EventID(qTeams, r.Key) || r.DetailLevel != calendar.DetailFull || !r.HasRecap || r.ActionItems != 1 || r.Recordings != 3 || len(r.Rooms) == 0 {
		t.Fatalf("row: %+v", r)
	}
	if got.Gap {
		t.Fatal("the day is inside the window")
	}
	// The second occurrence gets the recording its own window takes; the third none.
	for day, want := range map[string]int{"2026-11-10": 1, "2026-11-17": 0} {
		a, err := s.CalendarAgenda(ctx, CalendarFilter{From: qt(day + "T00:00:00Z"), To: qt(day + "T23:00:00Z")})
		if err != nil || len(a.Rows) != 1 || a.Rows[0].Recordings != want || a.Rows[0].HasRecap {
			t.Fatalf("%s: %+v %v", day, a, err)
		}
	}
	// The limit cuts the list and says so; masters are hidden unless asked for.
	all, err := s.CalendarAgenda(ctx, CalendarFilter{From: qt("2026-11-01T00:00:00Z"), To: qt("2026-12-01T00:00:00Z"), Limit: 2})
	if err != nil || len(all.Rows) != 2 || !all.Truncated || all.Total != 3 {
		t.Fatalf("%+v %v", all, err)
	}
	withMaster, err := s.CalendarAgenda(ctx, CalendarFilter{From: qt("2026-11-01T00:00:00Z"), To: qt("2026-12-01T00:00:00Z"), IncludeMasters: true})
	if err != nil || withMaster.Total != 4 {
		t.Fatalf("%+v %v", withMaster, err)
	}
	// An account that holds nothing gives nothing.
	none, err := s.CalendarAgenda(ctx, CalendarFilter{Account: &acctB, From: qt("2026-11-03T00:00:00Z"), To: qt("2026-11-04T00:00:00Z")})
	if err != nil || len(none.Rows) != 0 || !none.Gap {
		t.Fatalf("%+v %v", none, err)
	}
	// Unlinked accounts are reported.
	s.qApply(t, calendar.SourceOutlook, qOutlook)
	un, err := s.CalendarAgenda(ctx, CalendarFilter{From: qt("2026-11-03T00:00:00Z"), To: qt("2026-11-04T00:00:00Z")})
	if err != nil || len(un.Unlinked) != 1 || un.Unlinked[0] != qOutlook {
		t.Fatalf("%+v %v", un, err)
	}
}

func TestCalendarAgendaWithoutCalendarTables(t *testing.T) {
	s := newStore(t)
	s.qExec(t, `drop table calendar_recaps`)
	got, err := s.CalendarAgenda(context.Background(), CalendarFilter{From: qt("2026-11-03T00:00:00Z"), To: qt("2026-11-04T00:00:00Z")})
	if err != nil || !got.NoTables || len(got.Rows) != 0 {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := s.CalendarEvent(context.Background(), nil, "ev_x"); !errors.Is(err, ErrNoCalendarTables) {
		t.Fatalf("%v", err)
	}
}

func TestCalendarEventDetail(t *testing.T) {
	s := qArchive(t)
	ctx := context.Background()
	key := calendar.Key(qEvent("o1", 3))
	d, err := s.CalendarEvent(ctx, nil, EventID(qTeams, key))
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Attendees) != 4 || d.Attendees[0].Name != "Ada" || d.Chat == nil || d.Chat.MessageCount != 7 || d.Chat.DisplayName != "Q meeting" {
		t.Fatalf("%+v", d)
	}
	if len(d.Recaps) != 1 || len(d.Recaps[0].ActionItems) != 1 || d.Recaps[0].ActionItems[0].Title != "Send notes" || len(d.Recaps[0].Mentions) != 1 || d.Recaps[0].Mentions[0].At.IsZero() {
		t.Fatalf("recaps: %+v", d.Recaps)
	}
	if d.Series.Key != qSeries || d.Series.Occurrences != 3 || d.Series.MasterID != EventID(qTeams, calendar.Key(qEvent("m", 1, func(e *calendar.Event) { e.GlobalID = "uid-master" }))) || !d.Series.HasMasterRow || len(d.Series.Rule) == 0 {
		t.Fatalf("series: %+v", d.Series)
	}
	var kinds, by []string
	for _, r := range d.Recordings {
		kinds, by = append(kinds, r.Kind), append(by, r.MatchedBy)
	}
	if strings.Join(kinds, ",") != "call_event,recording,transcript" || strings.Join(by, ",") != "window,recap,recap" {
		t.Fatalf("recordings %v %v", kinds, by)
	}
	// m-late matches no occurrence; m-rec2 belongs to the second one.
	if d.SeriesRecordingsTotal != 1 || len(d.SeriesRecordings) != 1 || d.SeriesRecordings[0].MessageID != "m-late" {
		t.Fatalf("series recordings: %+v", d.SeriesRecordings)
	}
}

func TestCalendarEventSeriesRecordingsAreCapped(t *testing.T) {
	s := qArchive(t)
	for i := 0; i < 25; i++ {
		s.qMessage(t, fmt.Sprintf("x%02d", i), "RichText/Media_CallRecording", fmt.Sprintf("2026-12-%02dT10:00:00.000Z", i+1), "x")
	}
	d, err := s.CalendarEvent(context.Background(), nil, EventID(qTeams, calendar.Key(qEvent("o2", 10))))
	if err != nil || d.SeriesRecordingsTotal != 26 || len(d.SeriesRecordings) != 20 || d.SeriesRecordings[0].SentAt.Before(d.SeriesRecordings[1].SentAt) {
		t.Fatalf("%d %d %v", d.SeriesRecordingsTotal, len(d.SeriesRecordings), err)
	}
}

func TestCalendarEventResolvesEveryReference(t *testing.T) {
	s := qArchive(t)
	s.qLink(t)
	ctx := context.Background()
	o2 := qEvent("o2", 10)
	key, id := calendar.Key(o2), EventID(qTeams, calendar.Key(o2))
	// The legacy ids: from the Teams account, and from the Outlook account an event printed before
	// the link was made.
	legacyTeams, legacyOutlook := eventIDOf(qTeams+"|"+key), eventIDOf(qOutlook+"|"+key)
	for name, ref := range map[string]string{"key": key, "id": id, "legacy teams": legacyTeams, "legacy outlook": legacyOutlook, "key prefix": key[:len(key)-1], "id prefix": id[:11]} {
		d, err := s.CalendarEvent(ctx, nil, ref)
		if name == "id prefix" || name == "key prefix" {
			// The shared prefix names several events; the longer ones name one.
			if err == nil && d.EventID != id {
				t.Errorf("%s: %s", name, d.EventID)
			}
			continue
		}
		if err != nil || d.EventID != id {
			t.Errorf("%s: %v %s", name, err, d.EventID)
		}
	}
	// An account filter scopes the lookup to that account's principal.
	if d, err := s.CalendarEvent(ctx, &acctA, id); err != nil || d.EventID != id {
		t.Fatalf("%v", err)
	}
	if _, err := s.CalendarEvent(ctx, &acctB, id); err == nil {
		t.Fatal("another account holds no such event")
	}
}

func TestCalendarEventUsageErrors(t *testing.T) {
	s := qArchive(t)
	ctx := context.Background()
	_, err := s.CalendarEvent(ctx, nil, "ev_nothing")
	var c *errs.Coded
	if !errors.As(err, &c) || c.Code != errs.CodeUsage || !strings.Contains(c.Fix, "m365crawl calendar") {
		t.Fatalf("%v", err)
	}
	_, err = s.CalendarEvent(ctx, nil, "uid-")
	if !errors.As(err, &c) || !strings.Contains(c.Message, "matches 4 calendar events") || !strings.Contains(c.Message, "ev_") || !strings.Contains(c.Fix, "full event_id") {
		t.Fatalf("%v", err)
	}
	// More than ten candidates are summarized.
	var many []calendar.Event
	for i := 0; i < 12; i++ {
		many = append(many, qEvent(fmt.Sprintf("many%02d", i), 20, func(e *calendar.Event) { e.TeamsThreadID = "" }))
	}
	s.qApply(t, calendar.SourceTeams, qTeams, append(many, qEvent("o1", 3), qEvent("o2", 10), qEvent("o3", 17))...)
	_, err = s.CalendarEvent(ctx, nil, "uid-")
	if !errors.As(err, &c) || !strings.Contains(c.Message, "and ") || !strings.Contains(c.Message, "more") {
		t.Fatalf("%v", err)
	}
}

// A timed key that later joined its twin's date key still resolves, by its own key and by the
// id computed from it, to the one joined event.
func TestCalendarEventResolvesAKeyThatJoinedADateKey(t *testing.T) {
	s := newStore(t)
	s.qExec(t, `insert into conversations(tenant_id,user_id,id,kind,title,display_name,updated_at) values('tenant-1','aaaaaaaa-0000-0000-0000-000000000001','c','Chat','','','2026-11-01T00:00:00.000Z')`)
	s.qApply(t, calendar.SourceTeams, qTeams)
	s.qLink(t)
	timed := qEvent("j", 3, func(e *calendar.Event) {
		e.SeriesKey, e.TeamsThreadID = "J", ""
		e.OriginalStart = qtp("2026-11-03T08:00:00Z")
		e.Start, e.End = qt("2026-11-03T08:00:00Z"), qt("2026-11-04T08:00:00Z")
		e.AllDay, e.StartDate, e.EndDate = calendar.TriTrue, "2026-11-03", "2026-11-04"
	})
	// First stored as a 24 hour timed event, so its key is the timed one.
	first := timed
	first.AllDay, first.StartDate, first.EndDate = calendar.TriFalse, "", ""
	first.LastModified = qtp("2026-11-01T08:00:00Z")
	s.qApply(t, calendar.SourceTeams, qTeams, first)
	s.qApply(t, calendar.SourceTeams, qTeams, timed)
	outlook := qEvent("jo", 3, func(e *calendar.Event) {
		e.Source, e.AccountID, e.SourceID, e.SeriesKey, e.TeamsThreadID = calendar.SourceOutlook, qOutlook, "jo", "J", ""
		e.GlobalID, e.ICalUID = timed.GlobalID, timed.ICalUID
		e.OriginalStart = qtp("2026-11-03T00:00:00Z")
		e.AllDay, e.Start, e.End = calendar.TriUnknown, qt("2026-11-03T00:00:00Z"), qt("2026-11-04T00:00:00Z")
	})
	s.qApply(t, calendar.SourceOutlook, qOutlook, outlook)
	ctx := context.Background()
	timedKey, dateKey := calendar.Key(first), calendar.Key(outlook)
	if timedKey == dateKey {
		t.Fatal("the fixture must key the twins apart")
	}
	want := EventID(qTeams, dateKey)
	for _, ref := range []string{timedKey, dateKey, EventID(qTeams, timedKey), eventIDOf(qTeams + "|" + timedKey)} {
		d, err := s.CalendarEvent(ctx, nil, ref)
		if err != nil || d.EventID != want || len(d.Sources) != 2 {
			t.Fatalf("%s: %v %+v", ref, err, d.EventID)
		}
	}
	// The agenda shows the one event, under the date key's id.
	a, err := s.CalendarAgenda(ctx, CalendarFilter{From: qt("2026-11-03T00:00:00Z"), To: qt("2026-11-04T00:00:00Z")})
	if err != nil || len(a.Rows) != 1 || a.Rows[0].EventID != want {
		t.Fatalf("%+v %v", a.Rows, err)
	}
}

func TestParseAttendeesAndRawJSON(t *testing.T) {
	if parseAttendees("") != nil || parseAttendees("not json") != nil {
		t.Fatal("unreadable attendees are none")
	}
	if rawJSON("") != nil || rawJSON("{") != nil || string(rawJSON(`{"a":1}`)) != `{"a":1}` {
		t.Fatal("rawJSON")
	}
}

func TestMatchOccurrenceTies(t *testing.T) {
	at := qt("2026-11-03T12:00:00Z")
	occ := func(key, start string) occurrence {
		return occurrence{key: key, start: qt(start), end: qt(start).Add(time.Hour), windows: [][2]time.Time{{qt("2026-11-03T00:00:00Z"), qt("2026-11-04T00:00:00Z")}}}
	}
	// Among occurrences that qualify, the latest start not after the message wins, whatever the
	// order they are listed in; with none before it, the nearest after.
	for _, order := range [][]occurrence{
		{occ("a", "2026-11-03T10:00:00Z"), occ("b", "2026-11-03T11:00:00Z"), occ("c", "2026-11-03T13:00:00Z")},
		{occ("c", "2026-11-03T13:00:00Z"), occ("b", "2026-11-03T11:00:00Z"), occ("a", "2026-11-03T10:00:00Z")},
	} {
		if key, by := matchOccurrence(order, at); key != "b" || by != "recap" {
			t.Errorf("%s %s", key, by)
		}
	}
	after := []occurrence{occ("y", "2026-11-03T15:00:00Z"), occ("x", "2026-11-03T14:00:00Z"), occ("z", "2026-11-03T16:00:00Z")}
	if key, _ := matchOccurrence(after, at); key != "x" {
		t.Errorf("nearest after: %s", key)
	}
	for _, same := range [][]occurrence{{occ("q", "2026-11-03T11:00:00Z"), occ("p", "2026-11-03T11:00:00Z")}, {occ("p", "2026-11-03T11:00:00Z"), occ("q", "2026-11-03T11:00:00Z")}} {
		if key, _ := matchOccurrence(same, at); key != "p" {
			t.Errorf("a tie goes to the smaller key: %s", key)
		}
	}
	if key, by := matchOccurrence([]occurrence{{key: "w", start: qt("2026-11-03T11:00:00Z"), end: qt("2026-11-03T11:30:00Z")}}, qt("2026-11-03T15:20:00Z")); key != "w" || by != "window" {
		t.Errorf("window with lag: %s %s", key, by)
	}
	if key, _ := matchOccurrence([]occurrence{{key: "w", start: qt("2026-11-03T11:00:00Z"), end: qt("2026-11-03T11:30:00Z")}}, qt("2026-11-03T16:00:00Z")); key != "" {
		t.Errorf("past the lag: %s", key)
	}
}

// Every read the commands make fails cleanly: each query, row step and scan of CalendarAgenda and
// CalendarEvent is failed in turn and must come back as an error, never as a partial answer. A scan
// that the fault driver turns into NULLs is allowed to succeed where the destination takes NULL.
func TestCalendarReadsReportEveryStorageFailure(t *testing.T) {
	ctx := context.Background()
	day := func(s *Store) error {
		_, err := s.CalendarAgenda(ctx, CalendarFilter{From: qt("2026-11-03T00:00:00Z"), To: qt("2026-11-04T00:00:00Z")})
		return err
	}
	ev := func(s *Store) error {
		_, err := s.CalendarEvent(ctx, nil, EventID(qTeams, calendar.Key(qEvent("o1", 3))))
		return err
	}
	nullSafe := map[string][]int{"agenda": {12}, "event": {17}}
	for name, op := range map[string]func(*Store) error{"agenda": day, "event": ev} {
		t.Run(name, func(t *testing.T) {
			sweepReadFaults(t, func(t *testing.T, s *Store) {
				qSeed(t, s)
				s.qLink(t)
			}, func(ctx context.Context, s *Store) error { return op(s) }, nullSafe[name]...)
		})
	}
}

// An event with no series and no iCalUID, in a meeting chat that has no conversation row or a
// conversation titled but not named, still reads cleanly.
func TestCalendarEventOfAThinMeeting(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	lone := qEvent("lone", 5, func(e *calendar.Event) {
		e.SeriesKey, e.ICalUID, e.GlobalID = "", "", ""
		e.TeamsThreadID = "19:meeting_NOCONV@thread.v2"
		e.RecurrenceJSON = `{"pattern":"daily"}`
	})
	titled := qEvent("titled", 6, func(e *calendar.Event) { e.SeriesKey = "T"; e.TeamsThreadID = "19:meeting_TITLED@thread.v2" })
	s.qApply(t, calendar.SourceTeams, qTeams, lone, titled)
	s.qExec(t, `insert into conversations(tenant_id,user_id,id,kind,title,display_name,updated_at) values('tenant-1','aaaaaaaa-0000-0000-0000-000000000001','19:meeting_TITLED@thread.v2','Meeting','Titled only','','2026-11-01T00:00:00.000Z')`)
	d, err := s.CalendarEvent(ctx, nil, EventID(qTeams, calendar.Key(lone)))
	if err != nil || d.Chat != nil || d.Series.Key != "" || string(d.Series.Rule) != `{"pattern":"daily"}` || len(d.Recaps) != 0 {
		t.Fatalf("%+v %v", d, err)
	}
	d, err = s.CalendarEvent(ctx, nil, EventID(qTeams, calendar.Key(titled)))
	if err != nil || d.Chat == nil || d.Chat.DisplayName != "Titled only" {
		t.Fatalf("%+v %v", d.Chat, err)
	}
}

// The recordings of a timed key that joined a date key count toward the joined event, on the
// agenda and in the detail.
func TestJoinedKeysCountTheirRecordings(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	s.qExec(t, `insert into conversations(tenant_id,user_id,id,kind,title,display_name,updated_at) values('tenant-1','aaaaaaaa-0000-0000-0000-000000000001',?,'Meeting','','Q meeting','2026-11-01T00:00:00.000Z')`, qChat)
	s.qApply(t, calendar.SourceTeams, qTeams)
	s.qLink(t)
	timed := qEvent("j", 3, func(e *calendar.Event) {
		e.SeriesKey = "J"
		e.OriginalStart = qtp("2026-11-03T08:00:00Z")
		e.Start, e.End = qt("2026-11-03T08:00:00Z"), qt("2026-11-04T08:00:00Z")
		e.LastModified = qtp("2026-11-01T08:00:00Z")
	})
	allDay := timed
	allDay.AllDay, allDay.StartDate, allDay.EndDate = calendar.TriTrue, "2026-11-03", "2026-11-04"
	s.qApply(t, calendar.SourceTeams, qTeams, timed)
	outlook := qEvent("jo", 3, func(e *calendar.Event) {
		e.Source, e.AccountID, e.SourceID, e.SeriesKey, e.TeamsThreadID = calendar.SourceOutlook, qOutlook, "jo", "J", ""
		e.GlobalID, e.ICalUID = timed.GlobalID, timed.ICalUID
		e.OriginalStart = qtp("2026-11-03T00:00:00Z")
		e.AllDay, e.Start, e.End = calendar.TriUnknown, qt("2026-11-03T00:00:00Z"), qt("2026-11-04T00:00:00Z")
	})
	s.qApply(t, calendar.SourceOutlook, qOutlook, outlook)
	s.qMessage(t, "jr", "RichText/Media_CallRecording", "2026-11-03T09:00:00.000Z", "rec")
	// The joined keys are one event for its recaps too.
	s.sRecap(t, "jr-call", timed.ICalUID, "2026-11-03T08:01:00.000Z", "Ada")
	a, err := s.CalendarAgenda(ctx, CalendarFilter{From: qt("2026-11-03T00:00:00Z"), To: qt("2026-11-04T00:00:00Z")})
	if err != nil || len(a.Rows) != 1 || a.Rows[0].Recordings != 1 || !a.Rows[0].HasRecap {
		t.Fatalf("%+v %v", a.Rows, err)
	}
	d, err := s.CalendarEvent(ctx, nil, a.Rows[0].EventID)
	if err != nil || len(d.Recordings) != 1 || len(d.Recaps) != 1 {
		t.Fatalf("%+v %v", d.Recordings, err)
	}
}

// One key held by both linked sources is one candidate, and one event with two sources.
func TestCalendarEventHeldByTwoSources(t *testing.T) {
	s := qArchive(t)
	s.qLink(t)
	o2 := qEvent("o2", 10)
	twin := o2
	twin.Source, twin.AccountID, twin.SourceID = calendar.SourceOutlook, qOutlook, "o2-outlook"
	twin.TeamsThreadID = ""
	s.qApply(t, calendar.SourceOutlook, qOutlook, twin)
	if calendar.Key(twin) != calendar.Key(o2) {
		t.Fatal("the fixture must key the twins together")
	}
	d, err := s.CalendarEvent(context.Background(), nil, calendar.Key(o2))
	if err != nil || len(d.Sources) != 2 {
		t.Fatalf("%v %+v", err, d.Sources)
	}
}

// A recap with no event is listed with its content: one with no iCalUID, and one whose iCalUID no
// event of its account holds. A recap an event holds is not listed.
func TestCalendarAgendaListsRecapsWithNoEvent(t *testing.T) {
	s := qArchive(t)
	ctx := context.Background()
	insert := func(call, ical, start string) {
		s.qExec(t, `insert into calendar_recaps(account_id,call_id,ical_uid,recap_id,link_method,has_catchup,has_recap,headline,short_summary,outline,summary_sections_json,speakers_json,topics_json,recording_url,meeting_start_at,meeting_end_at,first_seen_at,updated_at)
		  values(?,?,?,'','',0,1,'H '||?,'Short','','','','','',?,?,'2026-11-04T00:00:00.000Z','2026-11-04T00:00:00.000Z')`, qTeams, call, ical, call, start, start)
	}
	insert("impromptu", "", "2026-11-05T09:00:00.000Z")
	insert("orphan", "uid-nobody", "2026-11-05T10:00:00.000Z")
	insert("later", "", "2026-11-20T10:00:00.000Z")
	s.qExec(t, `insert into calendar_recap_items(account_id,call_id,item_key,kind,origin,title,text,owner_name,speaker_name,at,mentioned_by,highlights_json,ordinal,first_seen_at,updated_at,superseded_at) values
	  (?,'impromptu','a1','action_item','recap','Do it','Do it now','Ada','Bo',null,'','',1,'2026-11-04T00:00:00.000Z','2026-11-04T00:00:00.000Z',null)`, qTeams)
	f := CalendarFilter{From: qt("2026-11-01T00:00:00Z"), To: qt("2026-11-10T00:00:00Z")}
	got, err := s.CalendarAgenda(ctx, f)
	if err != nil || got.UnlinkedRecapsTotal != 2 || len(got.UnlinkedRecaps) != 2 {
		t.Fatalf("%+v %v", got.UnlinkedRecaps, err)
	}
	first := got.UnlinkedRecaps[0]
	if first.CallID != "impromptu" || first.Principal != qTeams || len(first.ActionItems) != 1 || first.ActionItems[0].Title != "Do it" || got.UnlinkedRecaps[1].CallID != "orphan" {
		t.Fatalf("%+v", got.UnlinkedRecaps)
	}
	// The recap of the Nov 3 occurrence is held by its event and is not listed.
	f.Limit = 1
	if got, err = s.CalendarAgenda(ctx, f); err != nil || len(got.UnlinkedRecaps) != 1 || got.UnlinkedRecapsTotal != 2 {
		t.Fatalf("%+v %v", got.UnlinkedRecaps, err)
	}
	// An account filter scopes them, and another account's range holds none.
	f.Limit, f.Account = 0, &acctA
	if got, err = s.CalendarAgenda(ctx, f); err != nil || got.UnlinkedRecapsTotal != 2 {
		t.Fatalf("%+v %v", got.UnlinkedRecaps, err)
	}
	f.Account = &acctB
	if got, err = s.CalendarAgenda(ctx, f); err != nil || got.UnlinkedRecapsTotal != 0 {
		t.Fatalf("%+v %v", got.UnlinkedRecaps, err)
	}
}
