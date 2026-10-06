package calendar

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestNormalizeUnknown(t *testing.T) {
	if NormalizeUnknown(nil) != nil || NormalizeUnknown([]Field{}) != nil {
		t.Fatal("none is nil")
	}
	in := []Field{FieldSubject, FieldBody, FieldSubject, FieldAttendees, FieldBody}
	got := NormalizeUnknown(in)
	if !reflect.DeepEqual(got, []Field{FieldAttendees, FieldBody, FieldSubject}) {
		t.Fatalf("%v", got)
	}
	if in[0] != FieldSubject || in[1] != FieldBody {
		t.Fatal("the input must not be reordered")
	}
}

func TestUnknownFieldsFlagsAndText(t *testing.T) {
	e := Event{
		AllDay: TriFalse, IsOrganizer: TriTrue, IsPrivate: TriFalse, Cancelled: TriFalse, IsOnlineMeeting: TriTrue, HasAttachments: TriFalse,
		Unknown: []Field{FieldOrganizer, FieldLocation},
	}
	if got := UnknownFields(e); !reflect.DeepEqual(got, []string{"location", "organizer"}) {
		t.Fatalf("%v", got)
	}
	e.Cancelled, e.AllDay = TriUnknown, TriUnknown
	e.Unknown = []Field{FieldSubject}
	if got := UnknownFields(e); !reflect.DeepEqual(got, []string{"all_day", "cancelled", "subject"}) {
		t.Fatalf("%v", got)
	}
	known := Event{AllDay: TriFalse, IsOrganizer: TriFalse, IsPrivate: TriFalse, Cancelled: TriFalse, IsOnlineMeeting: TriFalse, HasAttachments: TriFalse}
	if UnknownFields(known) != nil {
		t.Fatal("a fully known event has no unknown fields")
	}
}

func TestUnknownFieldsCollapseDetail(t *testing.T) {
	e := Event{
		AllDay: TriFalse, IsOrganizer: TriFalse, IsPrivate: TriFalse, Cancelled: TriFalse, IsOnlineMeeting: TriFalse,
		Unknown: append([]Field{FieldLocation, FieldOrganizer}, allDetail...),
	}
	// has_attachments is a detail field too: known, it keeps the collapse from happening.
	e.HasAttachments = TriFalse
	if got := UnknownFields(e); contains(got, "detail") || !contains(got, "attendees") {
		t.Fatalf("a known has_attachments must not collapse: %v", got)
	}
	e.HasAttachments = TriUnknown
	want := []string{"detail", "location", "organizer"}
	if got := UnknownFields(e); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	// One detail field known: no collapse, the rest are named.
	e.Unknown = without(e.Unknown, FieldReminder)
	if got := UnknownFields(e); contains(got, "detail") || contains(got, "reminder") || !contains(got, "attendees") {
		t.Fatalf("%v", got)
	}
	if len(DetailFields) != 13 {
		t.Fatalf("DetailFields has %d names", len(DetailFields))
	}
}

func TestUnknownColumnRoundTrip(t *testing.T) {
	if unknownColumn(nil) != "" {
		t.Fatal("none is empty")
	}
	text := unknownColumn([]Field{FieldBody, FieldSubject})
	if text != "body,subject" {
		t.Fatalf("%q", text)
	}
	got, err := parseUnknown(text)
	if err != nil || !reflect.DeepEqual(got, []Field{FieldBody, FieldSubject}) {
		t.Fatalf("%v %v", got, err)
	}
	if got, err := parseUnknown(""); got != nil || err != nil {
		t.Fatalf("%v %v", got, err)
	}
	if _, err := parseUnknown("body,start"); err == nil {
		t.Fatal("a name outside the vocabulary is damage")
	}
	// The flags are not in the vocabulary.
	if validField(FieldCancelled) {
		t.Fatal("flags are carried by Tri, not by Unknown")
	}
}

func TestValidateEventRefusesNamesOutsideTheVocabulary(t *testing.T) {
	e := thin(t, t1)
	e.Unknown = []Field{FieldSubject, "start"}
	_, err := Capture(nil, e)
	var bad *InvalidEventError
	if !errors.As(err, &bad) || !strings.Contains(bad.Error(), "start") {
		t.Fatalf("%v", err)
	}
	e.Unknown = []Field{FieldCancelled}
	if err := ValidateEvent(e); err == nil {
		t.Fatal("a flag name in Unknown is refused")
	}
	e.Unknown = []Field{FieldLocation}
	if err := ValidateEvent(e); err != nil {
		t.Fatal(err)
	}
}

func TestEveryUnknownNameHasAUnitAndAValueTest(t *testing.T) {
	for _, n := range unknownVocabulary {
		if _, ok := unitOfName[n]; !ok {
			t.Errorf("%s has no unit", n)
		}
		if valued[n] == nil {
			t.Errorf("%s has no value test", n)
		}
	}
}
