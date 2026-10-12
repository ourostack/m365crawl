package teamsdesktop

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// stringMetadata is a record whose properties.botMetadata is the JSON string inner, the native
// encoding Teams uses for that container.
func stringMetadata(inner string) []byte {
	quoted, _ := json.Marshal(inner)
	return []byte(`{"properties":{"botMetadata":` + string(quoted) + `}}`)
}

func TestApplicationStringEncodedBotMetadata(t *testing.T) {
	for _, tc := range []struct {
		name, inner, state, failure, loss string
	}{
		{"reply", `{"replyToId":"native-reply","feedbackLoop":{"type":"default"}}`, "mapped", "", ""},
		{"claimless", `{"feedbackLoop":{"type":"default"},"aiMetadata":{}}`, "absent", "", ""},
		{"empty-object", `{}`, "absent", "", ""},
		{"duplicate-keys", `{"replyToId":"a","replyToId":"b"}`, "partial", "", "teams_bot_metadata_unmapped"},
		{"trailing-token", `{"replyToId":"a"} {}`, "partial", "", "teams_bot_metadata_unmapped"},
		{"invalid", `{"replyToId":`, "partial", "", "teams_bot_metadata_unmapped"},
		{"lone-surrogate", `{"replyToId":"\ud800"}`, "partial", "", "teams_bot_metadata_unmapped"},
		{"null", `null`, "partial", "", "teams_bot_metadata_unmapped"},
		{"empty", ``, "partial", "", "teams_bot_metadata_unmapped"},
		{"array", `[]`, "partial", "", "teams_bot_metadata_unmapped"},
		{"string", `"x"`, "partial", "", "teams_bot_metadata_unmapped"},
		{"non-string-reply", `{"replyToId":7}`, "partial", "", "teams_bot_metadata_field_unmapped"},
		{"reply-too-large", `{"replyToId":"` + strings.Repeat("x", (256<<10)+1) + `"}`, "failed", "teams_bot_input_too_large", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ProjectApplication(context.Background(), "message", "sender", stringMetadata(tc.inner), true)
			if err != nil || got.State != tc.state || got.FailureCode != tc.failure {
				t.Fatalf("got %+v %v", got, err)
			}
			if tc.loss != "" && (len(got.Losses) != 1 || got.Losses[0].Code != tc.loss || got.Losses[0].Count != 1) {
				t.Fatalf("losses %+v", got.Losses)
			}
			if tc.state == "mapped" && (got.Observation.ReplyToID == nil || *got.Observation.ReplyToID != "native-reply" || len(got.Losses) != 0) {
				t.Fatalf("reply-to claim: %+v", got)
			}
			if tc.state == "failed" && got.Observation.ReplyToID != nil {
				t.Fatal("a refused record kept a payload")
			}
		})
	}
}

// The string is decoded inside the record's own budget: depth 64 and 131,072 members across both
// parses, and exhaustion inside the string is the record's teams_app_raw_too_large.
func TestApplicationStringMetadataSharesRecordBudget(t *testing.T) {
	nest := func(k int) string { // the inner root is at depth 3, x's arrays start at 4
		return `{"replyToId":"r","x":` + strings.Repeat("[", k) + `0` + strings.Repeat("]", k) + `}`
	}
	members := func(n int) string { // record: properties, botMetadata; inner: replyToId, x, n elements
		return `{"replyToId":"r","x":[` + strings.Repeat("0,", n-1) + `0]}`
	}
	for _, tc := range []struct {
		name, inner, state, failure string
	}{
		{"depth-64", nest(60), "mapped", ""},
		{"depth-65", nest(61), "failed", "teams_app_raw_too_large"},
		{"members-131072", members(131068), "mapped", ""},
		{"members-131073", members(131069), "failed", "teams_app_raw_too_large"},
	} {
		got, err := ProjectApplication(context.Background(), "message", "sender", stringMetadata(tc.inner), true)
		if err != nil || got.State != tc.state || got.FailureCode != tc.failure {
			t.Fatalf("%s: %+v %v", tc.name, got, err)
		}
	}
}

func TestApplicationStringMetadataCancellation(t *testing.T) {
	base, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := &applicationCancelContext{Context: base, cancel: cancel, after: 20} // the record parse checks a handful of times
	got, err := ProjectApplication(ctx, "message", "sender", stringMetadata(`{"replyToId":"r","x":[`+strings.Repeat("0,", 999)+`0]}`), true)
	if !errors.Is(err, context.Canceled) || got.State != "" {
		t.Fatalf("cancellation inside the string: %+v %v", got, err)
	}
}
