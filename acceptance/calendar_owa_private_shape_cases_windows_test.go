//go:build acceptance && windows

package acceptance

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestOWAPrivateShapesNeverRetainValuesOrUnrecognizedKeys(t *testing.T) {
	value := owaObject(
		"ResponseType", "Unknown",
		"FreeBusyType", "synthetic-private-value",
		"Subject", "synthetic-private-subject",
		"synthetic-private-key", "synthetic-private-value",
		"Body", owaObject("BodyType", false, "Value", "synthetic-private-body"),
	)
	got := owaPrivateShapes(value)
	want := []owaPrivateShape{
		{"Body.BodyType", "boolean", "text_type", 1},
		{"FreeBusyType", "string", "unsupported_enum", 1},
		{"ResponseType", "string", "explicit_unknown_enum", 1},
	}
	data, err := json.Marshal(got)
	if err != nil || !reflect.DeepEqual(got, want) || strings.Contains(string(data), "synthetic-private") {
		t.Fatal("field_type_diagnostic_missing_or_disclosed_content")
	}
}

func TestOWAPrivateShapesCountNestedResponseTypesWithoutContent(t *testing.T) {
	row := func(response any) any {
		return owaObject("Mailbox", owaObject("Name", "synthetic-private-name", "EmailAddress", "synthetic-private-address"), "ResponseType", response)
	}
	value := owaObject(
		"RequiredAttendees", []any{row("Unknown"), row("Unknown"), row("Accept")},
		"OptionalAttendees", []any{row(false)},
		"Resources", []any{},
	)
	want := []owaPrivateShape{
		{"OptionalAttendees[].ResponseType", "boolean", "text_type", 1},
		{"RequiredAttendees[].ResponseType", "string", "explicit_unknown_enum", 2},
	}
	got := owaPrivateShapes(value)
	data, err := json.Marshal(got)
	if err != nil || !reflect.DeepEqual(got, want) || strings.Contains(string(data), "synthetic-private") {
		t.Fatal("nested_field_type_count_or_privacy_failed")
	}
}
