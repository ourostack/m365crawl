//go:build acceptance

package acceptance

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"
)

type owaExpectedText struct {
	Value string
	Known bool
}

type owaExpectedAttendee struct {
	Name     string `json:"name"`
	Address  string `json:"address"`
	Type     string `json:"type"`
	Response string `json:"response"`
}

type owaOptionalExpected struct {
	Fields                     map[string]owaExpectedText
	DialConference, DialNumber owaExpectedText
	Attendees                  []owaExpectedAttendee
	AttendeesKnown             bool
	Invalid, PartialAttendees  int
	Redacted                   int
	err                        error
}

func (out *owaOptionalExpected) text(ctx context.Context, value any) owaExpectedText {
	if ctx.Err() != nil {
		out.err = ctx.Err()
		return owaExpectedText{}
	}
	if !owaSurveyPresent(value) {
		return owaExpectedText{}
	}
	text, ok := value.(string)
	if !ok || !utf8.ValidString(text) {
		out.Invalid++
		return owaExpectedText{}
	}
	if len(text) > owaMaxPayloadBytes {
		out.err = errors.New("calendar_oracle_text_budget_refused")
		return owaExpectedText{}
	}
	clean, count, err := owaOraclePrivacyText(ctx, text)
	if err != nil {
		out.err = err
		return owaExpectedText{}
	}
	out.Redacted += count
	return owaExpectedText{Value: clean, Known: true}
}

func (out *owaOptionalExpected) enum(ctx context.Context, value any, vocabulary map[string]string) owaExpectedText {
	text := out.text(ctx, value)
	if !text.Known {
		return text
	}
	normalized, supported := vocabulary[text.Value]
	if !supported {
		out.Invalid++
		return owaExpectedText{}
	}
	return owaExpectedText{Value: normalized, Known: true}
}

func owaOracleResponseVocabulary() map[string]string {
	return map[string]string{"Accept": "accepted", "Tentative": "tentative", "Decline": "declined", "NoResponseReceived": "none", "Organizer": "organizer"}
}

func owaProjectOptional(ctx context.Context, value any) (owaOptionalExpected, error) {
	out := owaOptionalExpected{Fields: map[string]owaExpectedText{}}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	fields, ok := owaSurveyObject(value)
	if !ok {
		return out, errors.New("calendar_oracle_object_refused")
	}
	for target, source := range map[string]string{
		"subject": "Subject", "location": "Location", "body_preview": "Preview", "join_url": "OnlineMeetingJoinUrl",
	} {
		out.Fields[target] = out.text(ctx, fields[source])
	}
	out.Fields["organizer"], out.Fields["organizer_address"] = owaExpectedText{}, owaExpectedText{}
	if raw := fields["Organizer"]; owaSurveyPresent(raw) {
		organizer, valid := owaSurveyObject(raw)
		if !valid {
			out.Invalid++
		} else if raw := organizer["Mailbox"]; owaSurveyPresent(raw) {
			mailbox, valid := owaSurveyObject(raw)
			if !valid {
				out.Invalid++
			} else {
				out.Fields["organizer"] = out.text(ctx, mailbox["Name"])
				out.Fields["organizer_address"] = out.text(ctx, mailbox["EmailAddress"])
			}
		}
	}
	out.Fields["response"] = out.enum(ctx, fields["ResponseType"], owaOracleResponseVocabulary())
	out.Fields["show_as"] = out.enum(ctx, fields["FreeBusyType"], map[string]string{"Free": "free", "Tentative": "tentative", "Busy": "busy"})
	out.Fields["event_type"] = out.enum(ctx, fields["CalendarItemType"], map[string]string{
		"Single": "single", "Occurrence": "occurrence", "Exception": "exception", "RecurringMaster": "master",
	})
	conference := out.text(ctx, fields["OnlineMeetingConferenceId"])
	number := out.text(ctx, fields["OnlineMeetingTollNumber"])
	out.DialConference, out.DialNumber = conference, number
	out.Fields["dial_in"] = owaExpectedText{Known: conference.Known || number.Known}
	out.attendees(ctx, fields)
	if err := ctx.Err(); err != nil {
		return out, errors.Join(out.err, err)
	}
	return out, out.err
}

func (out *owaOptionalExpected) attendees(ctx context.Context, fields map[string]any) {
	groups := []struct{ source, role string }{
		{"RequiredAttendees", "required"}, {"OptionalAttendees", "optional"}, {"Resources", "resource"},
	}
	lists := make([][]any, len(groups))
	present := 0
	for i, group := range groups {
		if ctx.Err() != nil {
			out.err = ctx.Err()
			return
		}
		raw := fields[group.source]
		if !owaSurveyPresent(raw) {
			continue
		}
		list, valid := raw.([]any)
		if !valid {
			out.Invalid++
			return
		}
		if len(list) > owaMaxQueryRows {
			out.err = errors.New("calendar_oracle_attendee_budget_refused")
			return
		}
		lists[i], present = list, present+1
	}
	if present != len(groups) {
		if present != 0 {
			out.PartialAttendees++
		}
		return
	}
	rows := []owaExpectedAttendee{}
	responses := owaOracleResponseVocabulary()
	for i, list := range lists {
		for _, raw := range list {
			if ctx.Err() != nil {
				out.err = ctx.Err()
				return
			}
			row, valid := owaSurveyObject(raw)
			if !valid {
				out.Invalid++
				return
			}
			mailbox, valid := owaSurveyObject(row["Mailbox"])
			if !valid {
				out.Invalid++
				return
			}
			before := out.Invalid
			name, address := out.text(ctx, mailbox["Name"]), out.text(ctx, mailbox["EmailAddress"])
			if out.err != nil || out.Invalid != before {
				return
			}
			if strings.TrimSpace(name.Value) == "" && strings.TrimSpace(address.Value) == "" {
				out.Invalid++
				return
			}
			response := out.enum(ctx, row["ResponseType"], responses)
			rows = append(rows, owaExpectedAttendee{Name: name.Value, Address: address.Value, Type: groups[i].role, Response: response.Value})
		}
	}
	out.Attendees, out.AttendeesKnown = rows, true
}
