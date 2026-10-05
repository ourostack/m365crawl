package teamscal

import (
	"time"

	"github.com/ourostack/teamscrawl/internal/calendar"
	"github.com/ourostack/teamscrawl/internal/teamsdesktop"
)

// MapCatchUpRecord maps one record of the meeting catch-up store (key and iCalUid are the event's
// iCalUID) to one recap per call in its data array, with each call's action items and mentions.
// An empty data array creates nothing: no recap, no stand-in call id. An item without a callId is
// skipped and counted in notes.Skipped. When key is empty the record's own iCalUid is used.
//
// The per-call duration is in milliseconds (measured on a real cache); the recording ends that long
// after it starts.
func MapCatchUpRecord(acct teamsdesktop.Account, key string, valueJSON []byte) ([]calendar.Recap, []calendar.RecapItem, MapNotes, error) {
	var notes MapNotes
	m, err := decode(valueJSON)
	if err != nil {
		return nil, nil, notes, err
	}
	uid := firstNonEmpty(key, str(m["iCalUid"]))
	if uid == "" {
		return nil, nil, notes, &UnmappedError{Reason: "no iCalUid"}
	}
	account := AccountID(acct)
	expires, meetingEnd := moment(m["expiration"]), moment(m["meetingEndTime"])
	var recaps []calendar.Recap
	var items []calendar.RecapItem
	for _, it := range array(m["data"]) {
		c := object(it)
		callID := str(c["callId"])
		if callID == "" {
			notes.Skipped++
			continue
		}
		r := calendar.Recap{
			AccountID: account, CallID: callID, ICalUID: uid, HasCatchUp: true,
			Headline: str(c["headline"]), Outline: str(c["outline"]),
			SpeakersJSON: mapSpeakers(c["speakers"]),
			RecordingURL: str(c["url"]), IsMissed: boolean(c["isMissed"]),
			ExpiresAt: timePtr(expires), MeetingEndAt: timePtr(meetingEnd),
		}
		var millis float64
		if ms, ok := number(c["duration"]); ok && ms > 0 {
			millis = ms
		}
		r.DurationSeconds = int(millis / 1000)
		r.RecordingStartAt = timePtr(moment(c["recordingStartTime"]))
		if r.RecordingStartAt != nil && millis > 0 {
			end := r.RecordingStartAt.Add(time.Duration(millis) * time.Millisecond)
			r.RecordingEndAt = &end
		}
		recaps = append(recaps, r)

		for i, t := range array(c["tasks"]) {
			o := object(t)
			items = append(items, newItem(account, callID, calendar.ItemActionItem, calendar.OriginCatchUp, i, calendar.RecapItem{
				Title: str(o["headline"]), Text: str(o["text"]), OwnerName: str(o["ownerDisplayName"]),
				SpeakerName: str(o["speaker"]), At: timePtr(moment(o["time"])),
			}))
		}
		for i, mn := range array(c["mentions"]) {
			o := object(mn)
			if o == nil {
				continue
			}
			ri := calendar.RecapItem{Text: str(o["text"]), SpeakerName: str(o["speaker"]), At: timePtr(moment(o["absoluteTime"]))}
			if h, ok := o["highlights"]; ok && h != nil {
				ri.HighlightsJSON = marshal(jsonish(h))
			}
			items = append(items, newItem(account, callID, calendar.ItemMention, calendar.OriginCatchUp, i, ri))
		}
	}
	return recaps, items, notes, nil
}

// MapRecapRecord maps one record of the meeting recap store (key is its recapId) to one recap
// with its action items and mentions. Its callId joins it to a catch-up recap of the same call;
// iCalUID stays empty, and the link step fills it from the event. A record without a callId is
// unmapped.
func MapRecapRecord(acct teamsdesktop.Account, key string, valueJSON []byte) (calendar.Recap, []calendar.RecapItem, error) {
	if key == "" {
		return calendar.Recap{}, nil, &UnmappedError{Reason: "no recapId"}
	}
	m, err := decode(valueJSON)
	if err != nil {
		return calendar.Recap{}, nil, err
	}
	callID := str(m["callId"])
	if callID == "" {
		return calendar.Recap{}, nil, &UnmappedError{Reason: "no callId"}
	}
	account := AccountID(acct)
	r := calendar.Recap{
		AccountID: account, CallID: callID, RecapID: key, HasRecap: true,
		ShortSummary:         str(m["shortSummary"]),
		SummarySectionsJSON:  rawList(m["meetingSummary"]),
		SpeakersJSON:         rawList(m["speakersSegmentations"]),
		TopicsJSON:           rawList(m["topicSegmentations"]),
		MeetingStartAt:       timePtr(moment(m["meetingStartTime"])),
		MeetingEndAt:         timePtr(moment(m["meetingEndTime"])),
		RecordingStartAt:     timePtr(moment(m["recordingStartTime"])),
		RecordingEndAt:       timePtr(moment(m["recordingEndTime"])),
		OrganizerID:          str(m["organizerId"]),
		AttendanceStatus:     str(m["userAttendanceStatus"]),
		HasConfRoomConnected: boolean(m["hasConfRoomConnected"]),
	}
	if n, ok := integer(m["attendeesCount"]); ok {
		r.AttendeesCount = int(n)
	}
	if r.RecordingStartAt != nil && r.RecordingEndAt != nil {
		r.DurationSeconds = int(r.RecordingEndAt.Sub(*r.RecordingStartAt) / time.Second)
	}

	var items []calendar.RecapItem
	for i, a := range array(m["actionItems"]) {
		o := object(a)
		if o == nil {
			continue
		}
		items = append(items, newItem(account, callID, calendar.ItemActionItem, calendar.OriginRecap, i, calendar.RecapItem{
			Title: str(o["actionItemTitle"]), Text: str(o["displayCleanContent"]),
			OwnerName: str(o["ownerName"]), SpeakerName: str(o["speakerName"]),
		}))
	}
	for i, a := range array(m["atMentionEvents"]) {
		o := object(a)
		if o == nil {
			continue
		}
		items = append(items, newItem(account, callID, calendar.ItemMention, calendar.OriginRecap, i, calendar.RecapItem{
			Text: str(o["utteranceContainingTheMention"]), MentionedBy: str(o["mentionedByUserDisplayName"]),
			At: timePtr(moment(o["dateTimeUtc"])),
		}))
	}
	return r, items, nil
}

// newItem fills the identity of an item: its call, kind, origin, position in its source list and
// the stable item key.
func newItem(account, callID, kind, origin string, ordinal int, it calendar.RecapItem) calendar.RecapItem {
	it.AccountID, it.CallID, it.Kind, it.Origin, it.Ordinal = account, callID, kind, origin, ordinal
	it.ItemKey = calendar.ItemKey(kind, origin, ordinal, it.Title, it.Text)
	return it
}

type speaker struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// mapSpeakers stores the catch-up speakers as {id, name}; entries with neither are dropped.
func mapSpeakers(v any) string {
	var out []speaker
	for _, it := range array(v) {
		o := object(it)
		s := speaker{ID: str(o["id"]), Name: str(o["displayName"])}
		if s.ID != "" || s.Name != "" {
			out = append(out, s)
		}
	}
	return listJSON(out)
}

// rawList keeps a list from the recap store as seen, "" when it is absent or empty. The shapes of
// the segmentation lists are not documented, so they are not reinterpreted.
func rawList(v any) string {
	return listJSON(array(v))
}
