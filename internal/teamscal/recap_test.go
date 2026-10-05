package teamscal

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/teamscrawl/internal/calendar"
)

const catchUpTwoCalls = `{
 "iCalUid":"UID-RICH","meetingEndTime":{"$date":"2026-03-10T18:00:00.000Z"},"expiration":{"$date":"2026-04-09T18:00:00.000Z"},
 "data":[
  {"callId":"call-1","isMissed":false,"url":"https://recordings.example.test/1","duration":3600000,
   "recordingStartTime":{"$date":"2026-03-10T17:00:30.000Z"},
   "speakers":[{"id":"sp-1","displayName":"Alex Fixture"},{"id":"sp-2","displayName":"Blake Fixture"},"junk",{}],
   "headline":"Planning","outline":"1. Scope\n2. Dates",
   "tasks":[{"headline":"Send notes","text":"Alex sends the notes","ownerDisplayName":"Alex Fixture","speaker":"Blake Fixture","time":{"$date":"2026-03-10T17:20:00.000Z"}},
            {"headline":"Book room","text":"","ownerDisplayName":"Blake Fixture","speaker":"Alex Fixture","time":"00:12:03"}],
   "mentions":[{"speaker":"Blake Fixture","time":"00:05:10","absoluteTime":{"$date":"2026-03-10T17:05:10.000Z"},"text":"Alex, can you check?","highlights":[{"start":0,"end":4}]},
               {"speaker":"Alex Fixture","text":"No highlights here"}, "junk"]},
  {"callId":"call-2","isMissed":true,"url":"","headline":"Follow up","outline":"","speakers":[],"tasks":[],"mentions":[]},
  {"headline":"no call id"}
 ]}`

func TestMapCatchUpTwoCalls(t *testing.T) {
	recaps, items, notes, err := MapCatchUpRecord(testAcct, "UID-RICH", []byte(catchUpTwoCalls))
	if err != nil {
		t.Fatal(err)
	}
	if len(recaps) != 2 || notes.Skipped != 1 {
		t.Fatalf("recaps %d, skipped %d", len(recaps), notes.Skipped)
	}
	r := recaps[0]
	start := utcTime("2026-03-10T17:00:30Z")
	end := start.Add(3600 * time.Second)
	exp := utcTime("2026-04-09T18:00:00Z")
	meetEnd := utcTime("2026-03-10T18:00:00Z")
	if r.AccountID != "tenant-1/user-1" || r.CallID != "call-1" || r.ICalUID != "UID-RICH" || !r.HasCatchUp || r.HasRecap || r.RecapID != "" ||
		r.Headline != "Planning" || r.Outline != "1. Scope\n2. Dates" || r.RecordingURL != "https://recordings.example.test/1" ||
		r.DurationSeconds != 3600 || r.RecordingStartAt == nil || !r.RecordingStartAt.Equal(start) || r.RecordingEndAt == nil || !r.RecordingEndAt.Equal(end) ||
		r.ExpiresAt == nil || !r.ExpiresAt.Equal(exp) || r.MeetingEndAt == nil || !r.MeetingEndAt.Equal(meetEnd) || r.MeetingStartAt != nil || r.IsMissed {
		t.Errorf("recap 1 = %+v", r)
	}
	if r.SpeakersJSON != `[{"id":"sp-1","name":"Alex Fixture"},{"id":"sp-2","name":"Blake Fixture"}]` {
		t.Errorf("speakers = %q", r.SpeakersJSON)
	}
	r2 := recaps[1]
	if r2.CallID != "call-2" || !r2.IsMissed || r2.SpeakersJSON != "" || r2.RecordingStartAt != nil || r2.RecordingEndAt != nil || r2.DurationSeconds != 0 || r2.RecordingURL != "" {
		t.Errorf("recap 2 = %+v", r2)
	}

	var actions, mentions []calendar.RecapItem
	for _, it := range items {
		if it.AccountID != "tenant-1/user-1" || it.Origin != calendar.OriginCatchUp || it.CallID != "call-1" {
			t.Errorf("item = %+v", it)
		}
		if it.ItemKey != calendar.ItemKey(it.Kind, it.Origin, it.Ordinal, it.Title, it.Text) {
			t.Errorf("item key %q not calendar.ItemKey", it.ItemKey)
		}
		switch it.Kind {
		case calendar.ItemActionItem:
			actions = append(actions, it)
		case calendar.ItemMention:
			mentions = append(mentions, it)
		default:
			t.Errorf("kind %q", it.Kind)
		}
	}
	if len(actions) != 2 || len(mentions) != 2 {
		t.Fatalf("actions %d mentions %d", len(actions), len(mentions))
	}
	a := actions[0]
	if a.Title != "Send notes" || a.Text != "Alex sends the notes" || a.OwnerName != "Alex Fixture" || a.SpeakerName != "Blake Fixture" ||
		a.At == nil || !a.At.Equal(utcTime("2026-03-10T17:20:00Z")) || a.Ordinal != 0 {
		t.Errorf("action 1 = %+v", a)
	}
	if actions[1].At != nil || actions[1].Ordinal != 1 || actions[1].OwnerName != "Blake Fixture" {
		t.Errorf("action 2 (relative time is not an instant) = %+v", actions[1])
	}
	m := mentions[0]
	if m.Text != "Alex, can you check?" || m.SpeakerName != "Blake Fixture" || m.At == nil || !m.At.Equal(utcTime("2026-03-10T17:05:10Z")) ||
		m.HighlightsJSON != `[{"end":4,"start":0}]` {
		t.Errorf("mention 1 = %+v", m)
	}
	if mentions[1].HighlightsJSON != "" || mentions[1].At != nil || mentions[1].Ordinal != 1 {
		t.Errorf("mention 2 = %+v", mentions[1])
	}
}

func TestMapCatchUpMentionObjects(t *testing.T) {
	_, items, _, err := MapCatchUpRecord(testAcct, "U", []byte(`{"data":[{"callId":"c","mentions":[{"speaker":"S","time":"1","absoluteTime":{"$date":"2026-03-10T17:05:10.000Z"},"text":"T","highlights":[{"text":"hi","offset":2}]}]}]}`))
	if err != nil || len(items) != 1 || items[0].HighlightsJSON != `[{"offset":2,"text":"hi"}]` {
		t.Fatalf("items %+v err %v", items, err)
	}
}

func TestMapCatchUpEmptyDataCreatesNothing(t *testing.T) {
	for _, js := range []string{`{"iCalUid":"U","data":[],"meetingEndTime":{"$date":"2026-03-10T18:00:00.000Z"}}`, `{"iCalUid":"U"}`, `{"iCalUid":"U","data":null}`} {
		recaps, items, notes, err := MapCatchUpRecord(testAcct, "U", []byte(js))
		if err != nil || len(recaps) != 0 || len(items) != 0 || notes.Skipped != 0 {
			t.Errorf("%s -> recaps %v items %v notes %+v err %v", js, recaps, items, notes, err)
		}
	}
}

func TestMapCatchUpItemWithoutCallIDSkipped(t *testing.T) {
	recaps, items, notes, err := MapCatchUpRecord(testAcct, "U", []byte(`{"data":[{"headline":"x","tasks":[{"headline":"t"}]},"junk",{"callId":"ok"}]}`))
	if err != nil || len(recaps) != 1 || recaps[0].CallID != "ok" || len(items) != 0 || notes.Skipped != 2 {
		t.Fatalf("recaps %+v items %+v notes %+v err %v", recaps, items, notes, err)
	}
}

func TestMapCatchUpKeyFallbackAndUnmapped(t *testing.T) {
	recaps, _, _, err := MapCatchUpRecord(testAcct, "", []byte(`{"iCalUid":"FROM-FIELD","data":[{"callId":"c"}]}`))
	if err != nil || recaps[0].ICalUID != "FROM-FIELD" {
		t.Fatalf("recaps %+v err %v", recaps, err)
	}
	recaps, _, _, err = MapCatchUpRecord(testAcct, "KEY-WINS", []byte(`{"iCalUid":"other","data":[{"callId":"c"}]}`))
	if err != nil || recaps[0].ICalUID != "KEY-WINS" {
		t.Fatalf("recaps %+v err %v", recaps, err)
	}
	for _, c := range []struct{ key, js string }{{"", `{"data":[]}`}, {"k", `[1]`}} {
		_, _, _, err = MapCatchUpRecord(testAcct, c.key, []byte(c.js))
		var um *UnmappedError
		if !errors.As(err, &um) {
			t.Errorf("%q %s: err = %v", c.key, c.js, err)
		}
	}
}

func TestMapCatchUpDurationForms(t *testing.T) {
	recaps, _, _, _ := MapCatchUpRecord(testAcct, "U", []byte(`{"data":[{"callId":"a","duration":90700,"recordingStartTime":"2026-03-10T17:00:00Z"},{"callId":"b","duration":0,"recordingStartTime":"2026-03-10T17:00:00Z"},{"callId":"c","duration":-5}]}`))
	if recaps[0].DurationSeconds != 90 || recaps[0].RecordingEndAt == nil || !recaps[0].RecordingEndAt.Equal(utcTime("2026-03-10T17:01:30.7Z")) {
		t.Errorf("float duration: %+v", recaps[0])
	}
	if recaps[1].RecordingEndAt != nil || recaps[2].RecordingEndAt != nil {
		t.Errorf("no end without both start and a duration: %+v %+v", recaps[1], recaps[2])
	}
}

const recapRecord = `{
 "callId":"call-1","shortSummary":"A short summary.","adaptiveRecap":{"x":1},
 "meetingSummary":[{"title":"Scope","text":"We agreed on scope."}],
 "actionItems":[{"actionItemTitle":"Send notes","displayCleanContent":"Alex sends the notes","ownerName":"Alex Fixture","speakerName":"Blake Fixture"},
                {"actionItemTitle":"Book room","displayCleanContent":"","ownerName":"","speakerName":""}, "junk"],
 "atMentionEvents":[{"utteranceContainingTheMention":"Alex, can you check?","mentionedByUserDisplayName":"Blake Fixture","dateTimeUtc":{"$date":"2026-03-10T17:05:10.000Z"}},{"utteranceContainingTheMention":"later"}, 3],
 "speakersSegmentations":[{"speaker":"sp-1","start":0}],"topicSegmentations":[{"topic":"Scope","start":0}],
 "meetingStartTime":{"$date":"2026-03-10T17:00:00.000Z"},"meetingEndTime":{"$date":"2026-03-10T18:00:00.000Z"},
 "recordingStartTime":{"$date":"2026-03-10T17:00:30.000Z"},"recordingEndTime":{"$date":"2026-03-10T17:59:30.000Z"},
 "organizerId":"org-1","attendeesCount":6,"userAttendanceStatus":"Attended","hasConfRoomConnected":true
}`

func TestMapRecapRecord(t *testing.T) {
	r, items, err := MapRecapRecord(testAcct, "recap-key-1", []byte(recapRecord))
	if err != nil {
		t.Fatal(err)
	}
	if r.AccountID != "tenant-1/user-1" || r.CallID != "call-1" || r.RecapID != "recap-key-1" || r.ICalUID != "" || !r.HasRecap || r.HasCatchUp ||
		r.ShortSummary != "A short summary." || r.Headline != "" || r.Outline != "" ||
		r.SummarySectionsJSON != `[{"text":"We agreed on scope.","title":"Scope"}]` ||
		r.SpeakersJSON != `[{"speaker":"sp-1","start":0}]` || r.TopicsJSON != `[{"start":0,"topic":"Scope"}]` ||
		r.MeetingStartAt == nil || !r.MeetingStartAt.Equal(utcTime("2026-03-10T17:00:00Z")) || r.MeetingEndAt == nil || !r.MeetingEndAt.Equal(utcTime("2026-03-10T18:00:00Z")) ||
		r.RecordingStartAt == nil || !r.RecordingStartAt.Equal(utcTime("2026-03-10T17:00:30Z")) || r.RecordingEndAt == nil || !r.RecordingEndAt.Equal(utcTime("2026-03-10T17:59:30Z")) ||
		r.DurationSeconds != 3540 || r.OrganizerID != "org-1" || r.AttendeesCount != 6 || r.AttendanceStatus != "Attended" || !r.HasConfRoomConnected ||
		r.RecordingURL != "" || r.ExpiresAt != nil {
		t.Errorf("recap = %+v", r)
	}
	if len(items) != 4 {
		t.Fatalf("items = %+v", items)
	}
	a := items[0]
	if a.Kind != calendar.ItemActionItem || a.Origin != calendar.OriginRecap || a.Title != "Send notes" || a.Text != "Alex sends the notes" || a.OwnerName != "Alex Fixture" || a.SpeakerName != "Blake Fixture" || a.CallID != "call-1" || a.AccountID != "tenant-1/user-1" {
		t.Errorf("action = %+v", a)
	}
	if items[1].Ordinal != 1 || items[1].Title != "Book room" {
		t.Errorf("action 2 = %+v", items[1])
	}
	m := items[2]
	if m.Kind != calendar.ItemMention || m.Origin != calendar.OriginRecap || m.Text != "Alex, can you check?" || m.MentionedBy != "Blake Fixture" || m.At == nil || !m.At.Equal(utcTime("2026-03-10T17:05:10Z")) || m.Ordinal != 0 {
		t.Errorf("mention = %+v", m)
	}
	if items[3].Ordinal != 1 || items[3].At != nil || items[3].MentionedBy != "" {
		t.Errorf("mention 2 = %+v", items[3])
	}
	for _, it := range items {
		if it.ItemKey != calendar.ItemKey(it.Kind, it.Origin, it.Ordinal, it.Title, it.Text) {
			t.Errorf("item key %q", it.ItemKey)
		}
	}
}

func TestMapRecapNumericForms(t *testing.T) {
	r, items, err := MapRecapRecord(testAcct, "k", []byte(`{"callId":"c","attendeesCount":"7","userAttendanceStatus":2,"recordingStartTime":{"$date":"2026-03-10T17:00:30.000Z"}}`))
	if err != nil || r.AttendeesCount != 0 || r.AttendanceStatus != "2" || r.DurationSeconds != 0 || len(items) != 0 {
		t.Fatalf("recap %+v items %v err %v", r, items, err)
	}
}

func TestMapRecapUnmapped(t *testing.T) {
	for name, c := range map[string]struct{ key, js string }{
		"no call id": {"k", `{"shortSummary":"x"}`},
		"no key":     {"", `{"callId":"c"}`},
		"not object": {"k", `"x"`},
	} {
		_, _, err := MapRecapRecord(testAcct, c.key, []byte(c.js))
		var um *UnmappedError
		if !errors.As(err, &um) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestMapRecapJoinsByCallID(t *testing.T) {
	recaps, _, _, err := MapCatchUpRecord(testAcct, "UID-RICH", []byte(catchUpTwoCalls))
	if err != nil {
		t.Fatal(err)
	}
	two, _, err := MapRecapRecord(testAcct, "recap-key-1", []byte(recapRecord))
	if err != nil {
		t.Fatal(err)
	}
	one := recaps[0]
	if one.AccountID != two.AccountID || one.CallID != two.CallID {
		t.Fatalf("keys differ: %s/%s vs %s/%s", one.AccountID, one.CallID, two.AccountID, two.CallID)
	}
	// Both mappers key the same row, so the core folds them into one. (CaptureRecap replaces whole
	// field groups, so the later store's summary and recording groups win over the earlier one's;
	// that is the core's rule and is not asserted here.)
	merged := calendar.CaptureRecap(&one, two)
	if !merged.HasCatchUp || !merged.HasRecap || merged.ICalUID != "UID-RICH" || merged.RecapID != "recap-key-1" || merged.ShortSummary != "A short summary." {
		t.Fatalf("merged = %+v", merged)
	}
	if !strings.Contains(merged.SummarySectionsJSON, "Scope") {
		t.Errorf("summary sections lost: %q", merged.SummarySectionsJSON)
	}
}

func TestMapCatchUpDurationIsMilliseconds(t *testing.T) {
	// Real values run from 302000 to 3766000: 5 to 63 minutes.
	recaps, _, _, _ := MapCatchUpRecord(testAcct, "U", []byte(`{"data":[{"callId":"a","duration":302000,"recordingStartTime":"2026-03-10T17:00:00Z"},{"callId":"b","duration":3766000,"recordingStartTime":"2026-03-10T17:00:00Z"}]}`))
	if recaps[0].DurationSeconds != 302 || !recaps[0].RecordingEndAt.Equal(utcTime("2026-03-10T17:05:02Z")) ||
		recaps[1].DurationSeconds != 3766 || !recaps[1].RecordingEndAt.Equal(utcTime("2026-03-10T18:02:46Z")) {
		t.Fatalf("%+v %+v", recaps[0], recaps[1])
	}
}
