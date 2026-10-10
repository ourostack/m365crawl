package botcitations

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"testing"
)

func nativeMessage() map[string]any {
	return map[string]any{"properties": map[string]any{
		"fromAppMetadata": map[string]any{"id": "app", "name": "Observed"},
		"botMetadata":     map[string]any{"replyToId": "r"},
		"botCitations": []any{
			map[string]any{"id": int64(1), "title": "A"},
			map[string]any{"id": int64(1), "title": "B", "link": ""},
		},
	}}
}

func TestMapMessageCitationOrder(t *testing.T) {
	got, err := MapMessage(context.Background(), "m", "human", nativeMessage())
	app, name, reply, empty := "app", "Observed", "r", ""
	want := Result{Observation: Observation{MessageID: "m", SenderID: "human",
		AppID: &app, AppName: &name, ReplyToID: &reply,
		Citations: []Citation{{Position: 0, ID: 1, Title: "A"}, {Position: 1, ID: 1, Title: "B", Link: &empty}}}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("source-preserving positioned metadata missing: %#v,%v", got, err)
	}
}

func TestMapMessageLossIsolation(t *testing.T) {
	message := nativeMessage()
	props := message["properties"].(map[string]any)
	props["fromAppMetadata"], props["botMetadata"] = "malformed", 1
	props["botCitations"] = []any{nil}
	got, err := MapMessage(context.Background(), "m", "s", message)
	want := Result{Observation: Observation{MessageID: "m", SenderID: "s"}, Losses: []Loss{
		{Code: "teams_bot_citation_unmapped", Count: 1}, {Code: "teams_bot_metadata_unmapped", Count: 2},
	}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("container failures counted as child failures: %#v,%v", got, err)
	}
	message = nativeMessage()
	props = message["properties"].(map[string]any)
	props["fromAppMetadata"].(map[string]any)["id"] = 42
	props["botCitations"] = []any{
		map[string]any{"id": int64(1), "title": "First"}, "bad",
		map[string]any{"id": int64(1), "title": "Third", "content": map[string]any{"unconsumed": "text"}},
	}
	got, err = MapMessage(context.Background(), "m", "s", message)
	if err != nil || got.Observation.AppID != nil || got.Observation.AppName == nil || got.Observation.ReplyToID == nil ||
		len(got.Observation.Citations) != 2 || got.Observation.Citations[1].Position != 2 || !got.Observation.Citations[1].NestedContentPresent ||
		!reflect.DeepEqual(got.Losses, []Loss{
			{Code: "teams_bot_citation_content_unmapped", Count: 1}, {Code: "teams_bot_citation_unmapped", Count: 1},
			{Code: "teams_bot_metadata_field_unmapped", Count: 1},
		}) {
		t.Fatalf("metadata/row failure erased healthy peers or compacted positions: %#v,%v", got, err)
	}
}

func TestMapMessageNativeIntegers(t *testing.T) {
	for _, test := range []struct {
		value any
		want  int64
		valid bool
	}{
		{int64(0), 0, true}, {int64(math.MaxInt64), math.MaxInt64, true},
		{json.Number("9223372036854775807"), math.MaxInt64, true},
		{float64(9007199254740991), 9007199254740991, true}, {math.Copysign(0, -1), 0, true},
		{json.Number("0"), 0, true},
		{float64(9007199254740992), 0, false}, {json.Number("9223372036854775808"), 0, false},
		{json.Number("-0"), 0, false}, {json.Number("1.0"), 0, false}, {json.Number("1e0"), 0, false},
		{json.Number("+1"), 0, false}, {json.Number("01"), 0, false}, {json.Number(" 1"), 0, false},
		{json.Number(""), 0, false}, {int64(-1), 0, false}, {float64(-1), 0, false}, {1.5, 0, false},
		{math.NaN(), 0, false}, {math.Inf(1), 0, false}, {math.Inf(-1), 0, false},
		{float32(1), 0, false}, {int(1), 0, false}, {uint64(1), 0, false}, {"1", 0, false},
	} {
		got, err := MapMessage(context.Background(), "m", "s", map[string]any{"properties": map[string]any{
			"botCitations": []any{map[string]any{"id": test.value, "title": "Title"}},
		}})
		if err != nil {
			t.Fatalf("numeric row became fatal: %v", err)
		}
		if test.valid {
			if len(got.Observation.Citations) != 1 || got.Observation.Citations[0].ID != test.want || len(got.Losses) != 0 {
				t.Fatalf("native integer changed: %#v -> %#v", test.value, got)
			}
		} else if len(got.Observation.Citations) != 0 || !reflect.DeepEqual(got.Losses, []Loss{{Code: "teams_bot_citation_unmapped", Count: 1}}) {
			t.Fatalf("non-native numeric identity coerced: %#v -> %#v", test.value, got)
		}
	}
}

func TestMapMessageInputRefusal(t *testing.T) {
	for _, test := range []struct {
		message    map[string]any
		id, sender string
	}{
		{nil, "m", "s"}, {map[string]any{}, "", "s"}, {map[string]any{}, "m", "\xff"},
	} {
		got, err := MapMessage(context.Background(), test.id, test.sender, test.message)
		var typed *MapError
		if !errors.As(err, &typed) || typed.Code != "teams_bot_input_unsupported" || !reflect.DeepEqual(got, Result{}) {
			t.Fatalf("invalid supplied identity/message admitted: %#v,%v", got, err)
		}
	}
}
