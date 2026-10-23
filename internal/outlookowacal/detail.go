package outlookowacal

import (
	"encoding/json"
	"strings"

	"github.com/ourostack/m365crawl/internal/calendar"
	"github.com/ourostack/m365crawl/internal/teamsdesktop"
	"github.com/ourostack/m365crawl/internal/v8"
)

func (r *eventReader) details(o *v8.Object, e *calendar.Event, known map[calendar.Field]bool) {
	e.BodyHTML, e.BodyText, e.BodyType, known[calendar.FieldBody] = r.body(o)
	e.AttendeesJSON, known[calendar.FieldAttendees] = r.attendees(o)
	if preview, ok := r.text(o, "Preview"); ok {
		e.BodyPreview, known[calendar.FieldBodyPreview] = preview, true
	}
	for _, field := range []struct {
		key    string
		target *string
	}{
		{"OnlineMeetingConferenceId", &e.DialInConferenceID},
		{"OnlineMeetingTollNumber", &e.DialInTollNumber},
	} {
		if s, ok := r.text(o, field.key); ok {
			*field.target, known[calendar.FieldDialIn] = s, true
		}
	}
	if state, ok := r.text(o, "FreeBusyType"); ok {
		switch state {
		case "Free":
			e.ShowAs = "free"
		case "Tentative":
			e.ShowAs = "tentative"
		case "Busy":
			e.ShowAs = "busy"
		default:
			r.notes.Unmapped++
		}
		known[calendar.FieldShowAs] = e.ShowAs != ""
	}
}

func (r *eventReader) body(o *v8.Object) (html, text, kind string, ok bool) {
	value, present := valueOf(o, "Body")
	if !present {
		return "", "", "", false
	}
	body, valid := validObject(value)
	if !valid {
		r.notes.Unmapped++
		return "", "", "", false
	}
	_, haveType := valueOf(body, "BodyType")
	_, haveValue := valueOf(body, "Value")
	if !haveType || !haveValue {
		if haveType || haveValue {
			r.notes.Unmapped++
		}
		return "", "", "", false
	}
	kind, typeOK := r.text(body, "BodyType")
	content, valueOK := r.text(body, "Value")
	if !typeOK || !valueOK {
		return "", "", "", false
	}
	if _, present := valueOf(body, "IsTruncated"); present {
		truncated := r.flag(body, "IsTruncated")
		if !truncated.Known() {
			return "", "", "", false
		}
		if truncated.Is(true) {
			r.notes.Unmapped++
			return "", "", "", false
		}
	}
	switch strings.ToLower(kind) {
	case "html":
		return content, teamsdesktop.HTMLToText(content), "html", true
	case "text":
		return "", content, "text", true
	}
	r.notes.Unmapped++
	return "", "", "", false
}

type calendarAttendee struct {
	Name     string `json:"name"`
	Address  string `json:"address"`
	Type     string `json:"type"`
	Response string `json:"response"`
}

func (r *eventReader) attendees(o *v8.Object) (string, bool) {
	groups := []struct{ key, kind string }{
		{"RequiredAttendees", "required"}, {"OptionalAttendees", "optional"}, {"Resources", "resource"},
	}
	lists := make([][]any, len(groups))
	present := 0
	for i, group := range groups {
		if value, found := valueOf(o, group.key); found {
			list, ok := value.([]any)
			if !ok {
				r.notes.Unmapped++
				return "", false
			}
			lists[i] = list
			present++
		}
	}
	// A missing role list is unknown, not an explicit removal of its previous attendees.
	if present != len(groups) {
		if present > 0 {
			r.notes.PartialAttendees++
		}
		return "", false
	}
	out := []calendarAttendee{}
	for i, list := range lists {
		for _, value := range list {
			row, ok := validObject(value)
			if !ok {
				r.notes.Unmapped++
				return "", false
			}
			value, _ := valueOf(row, "Mailbox")
			mailbox, ok := validObject(value)
			if !ok {
				r.notes.Unmapped++
				return "", false
			}
			before := r.notes.Unmapped
			name, _ := r.text(mailbox, "Name")
			address, _ := r.text(mailbox, "EmailAddress")
			if r.notes.Unmapped != before {
				return "", false
			}
			if strings.TrimSpace(name) == "" && strings.TrimSpace(address) == "" {
				r.notes.Unmapped++
				return "", false
			}
			response, _ := r.response(row)
			out = append(out, calendarAttendee{Name: name, Address: address, Type: groups[i].kind, Response: response})
		}
	}
	data, _ := json.Marshal(out) // These records contain only strings.
	return string(data), true
}

func (r *eventReader) response(o *v8.Object) (string, bool) {
	s, present := r.text(o, "ResponseType")
	if !present {
		return "", false
	}
	switch s {
	case "Unknown":
		return "", false
	case "Accept":
		return "accepted", true
	case "Tentative":
		return "tentative", true
	case "Decline":
		return "declined", true
	case "NoResponseReceived":
		return "none", true
	case "Organizer":
		return "organizer", true
	}
	r.notes.Unmapped++
	return "", false
}
