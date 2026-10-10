package botcitations

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func mustTooLarge(t *testing.T, got Result, err error) {
	t.Helper()
	var typed *MapError
	if !errors.As(err, &typed) || typed.Code != "teams_bot_input_too_large" || !reflect.DeepEqual(got, Result{}) {
		t.Fatalf("bound published %d citations and %d losses; error=%v", len(got.Observation.Citations), len(got.Losses), err)
	}
}

func TestMapMessagePublicRetentionBoundary(t *testing.T) {
	rows := []any{}
	for i := 0; i < 15; i++ {
		rows = append(rows, map[string]any{"id": int64(1), "title": strings.Repeat("x", 262144)})
	}
	last := map[string]any{"id": int64(1), "title": strings.Repeat("x", 262142)}
	rows = append(rows, last)
	message := map[string]any{"properties": map[string]any{"botCitations": rows}}
	got, err := MapMessage(context.Background(), "m", "s", message)
	if err != nil || len(got.Observation.Citations) != 16 {
		t.Fatalf("public 4MiB inclusive output refused: %#v,%v", got, err)
	}
	last["title"] = strings.Repeat("x", 262143)
	got, err = MapMessage(context.Background(), "m", "s", message)
	mustTooLarge(t, got, err)
}

func TestMapMessagePublicRowAndFieldBounds(t *testing.T) {
	rows := make([]any, 4096)
	message := map[string]any{"properties": map[string]any{"botCitations": rows}}
	got, err := MapMessage(context.Background(), "m", "s", message)
	if err != nil || !reflect.DeepEqual(got.Losses, []Loss{{Code: "teams_bot_citation_unmapped", Count: 4096}}) {
		t.Fatalf("inclusive malformed input-row count refused: %v", err)
	}
	message["properties"].(map[string]any)["botCitations"] = append(rows, nil)
	got, err = MapMessage(context.Background(), "m", "s", message)
	mustTooLarge(t, got, err)
	for _, text := range []string{strings.Repeat("x", 262144), strings.Repeat("😀", 65536)} {
		message := map[string]any{"properties": map[string]any{"botCitations": []any{map[string]any{"id": int64(1), "title": text}}}}
		got, err := MapMessage(context.Background(), "m", "s", message)
		if err != nil || len(got.Observation.Citations) != 1 || got.Observation.Citations[0].Title != text {
			t.Fatalf("inclusive UTF8 field byte bound refused: %v", err)
		}
		message["properties"].(map[string]any)["botCitations"].([]any)[0].(map[string]any)["title"] = text + "x"
		got, err = MapMessage(context.Background(), "m", "s", message)
		mustTooLarge(t, got, err)
	}
}

func TestMapMessagePreflightBoundsBeforeRowRefusal(t *testing.T) {
	for _, key := range []string{"id", "title", "link", "contentType", "content"} {
		for _, text := range []string{strings.Repeat("x", 262145), strings.Repeat("\xff", 262145)} {
			row := map[string]any{"id": "malformed", "title": "Title"}
			row[key] = text
			got, err := MapMessage(context.Background(), "m", "s", map[string]any{"properties": map[string]any{"botCitations": []any{row}}})
			mustTooLarge(t, got, err)
		}
	}
	for _, field := range []string{"id", "name"} {
		got, err := MapMessage(context.Background(), "m", "s", map[string]any{"properties": map[string]any{
			"fromAppMetadata": map[string]any{field: strings.Repeat("x", 262145)},
		}})
		mustTooLarge(t, got, err)
	}
	got, err := MapMessage(context.Background(), "m", "s", map[string]any{"properties": map[string]any{
		"botMetadata": map[string]any{"replyToId": strings.Repeat("x", 262145)},
	}})
	mustTooLarge(t, got, err)
}

func TestMapMessageRetentionChargesOnlyAdmittedOccurrences(t *testing.T) {
	message := map[string]any{"properties": map[string]any{
		"fromAppMetadata": map[string]any{"id": "x", "name": "x"},
		"botMetadata":     map[string]any{"replyToId": "x"},
		"botCitations":    []any{map[string]any{"id": int64(1), "title": "x", "link": "x", "contentType": "x"}},
	}}
	cap := limits{4096, 262144, 8}
	got, err := mapMessage(context.Background(), "m", "s", message, cap)
	if err != nil || len(got.Observation.Citations) != 1 {
		t.Fatalf("inclusive per-occurrence charging refused: %v", err)
	}
	cap.stringBytes = 7
	got, err = mapMessage(context.Background(), "m", "s", message, cap)
	mustTooLarge(t, got, err)
	message["properties"].(map[string]any)["botCitations"].([]any)[0].(map[string]any)["id"] = nil
	cap.stringBytes = 5
	if _, err := mapMessage(context.Background(), "m", "s", message, cap); err != nil {
		t.Fatalf("refused row strings retained/charged: %v", err)
	}
}

func TestMapMessageNullAndMalformedContainers(t *testing.T) {
	for _, props := range []any{nil, map[string]any(nil), []any(nil)} {
		got, err := MapMessage(context.Background(), "m", "s", map[string]any{"properties": props})
		if err != nil || !reflect.DeepEqual(got, Result{Observation: Observation{MessageID: "m", SenderID: "s"}}) {
			t.Fatalf("nested null properties changed empty contract: %v", err)
		}
	}
	for _, message := range []map[string]any{
		{"properties": "bad"},
		{"properties": map[string]any{"botCitations": "bad"}},
	} {
		got, err := MapMessage(context.Background(), "m", "s", message)
		if err != nil || len(got.Losses) != 1 || len(got.Observation.Citations) != 0 {
			t.Fatalf("malformed container concealed or became fatal: %#v,%v", got, err)
		}
	}
	for _, value := range []any{nil, []any(nil), []any{}} {
		got, err := MapMessage(context.Background(), "m", "s", map[string]any{"properties": map[string]any{
			"botCitations": value, "fromAppMetadata": map[string]any(nil), "botMetadata": map[string]any(nil),
		}})
		if err != nil || len(got.Losses) != 0 || got.Observation.Citations != nil {
			t.Fatalf("empty/null native containers confused with unsupported source: %#v,%v", got, err)
		}
	}
}

func TestMapMessageInputAndOutputStayIndependent(t *testing.T) {
	message, before := nativeMessage(), nativeMessage()
	got, err := MapMessage(context.Background(), "m", "s", message)
	if err != nil || !reflect.DeepEqual(message, before) {
		t.Fatalf("mapper mutated source: %v", err)
	}
	props := message["properties"].(map[string]any)
	props["fromAppMetadata"].(map[string]any)["name"] = "Changed"
	props["botCitations"].([]any)[0].(map[string]any)["title"] = "Changed"
	if got.Observation.AppName == nil || *got.Observation.AppName != "Observed" || got.Observation.Citations[0].Title != "A" {
		t.Fatal("returned fields alias mutable source containers")
	}
	cyclic := map[string]any{}
	cyclic["unselected"] = cyclic
	props["ignored"] = cyclic
	props["botCitations"].([]any)[0].(map[string]any)["content"] = map[string]any{
		"ignored": cyclic, "large": strings.Repeat("x", 262145),
	}
	got, err = MapMessage(context.Background(), "m", "s", message)
	if err != nil || len(got.Observation.Citations) != 2 || !got.Observation.Citations[0].NestedContentPresent {
		t.Fatalf("unselected/cyclic content traversed or suppressed native peers: %v", err)
	}
}

func TestMapMessageIdentityAndMetadataRetentionBounds(t *testing.T) {
	for _, test := range []struct {
		id  string
		cap limits
	}{
		{strings.Repeat("x", 262145), limits{4096, 262144, 4 << 20}},
		{"m", limits{4096, 262144, 0}},
		{"m", limits{4096, 262144, 2}},
		{"m", limits{4096, 262144, 10}},
		{"m", limits{4096, 262144, 14}},
	} {
		got, err := mapMessage(context.Background(), test.id, "s", nativeMessage(), test.cap)
		mustTooLarge(t, got, err)
	}
	got, err := MapMessage(context.Background(), strings.Repeat("\xff", 262145), "s", nativeMessage())
	if err == nil || err.Error() != "teams_bot_input_unsupported" || !reflect.DeepEqual(got, Result{}) {
		t.Fatalf("invalid oversized identity changed refusal priority: %v", err)
	}
}

func TestMapMessageCancellationAtEveryWorkCheckpoint(t *testing.T) {
	for at := 1; at < 200; at++ {
		ctx := &checkedContext{Context: context.Background(), cancelAt: at, failure: context.Canceled}
		got, err := MapMessage(ctx, "m", "s", nativeMessage())
		if err == nil {
			if len(got.Observation.Citations) != 2 {
				t.Fatal("unarmed read lost citations")
			}
			return
		}
		if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(got, Result{}) {
			t.Fatalf("cancel boundary %d published partial observation: %v", at, err)
		}
	}
	t.Fatal("no unarmed boundary within finite work")
}

type checkedContext struct {
	context.Context
	calls, cancelAt int
	failure         error
}

func (c *checkedContext) Err() error {
	c.calls++
	if c.calls >= c.cancelAt {
		return c.failure
	}
	return nil
}

func TestMapMessageTerminalFailure(t *testing.T) {
	for _, failure := range []error{context.Canceled, context.DeadlineExceeded} {
		for _, input := range []struct {
			id      string
			message map[string]any
			cap     limits
		}{
			{"", nativeMessage(), limits{4096, 262144, 4 << 20}},
			{"m", nil, limits{4096, 262144, 4 << 20}},
			{"m", nativeMessage(), limits{0, 262144, 4 << 20}},
			{"m", nativeMessage(), limits{4096, 0, 4 << 20}},
			{"m", nativeMessage(), limits{4096, 262144, 1}},
		} {
			probe := &checkedContext{Context: context.Background(), cancelAt: 1000, failure: failure}
			if _, err := mapMessage(probe, input.id, "s", input.message, input.cap); err == nil {
				t.Fatal("fatal-boundary control unexpectedly succeeded")
			}
			ctx := &checkedContext{Context: context.Background(), cancelAt: probe.calls, failure: failure}
			got, err := mapMessage(ctx, input.id, "s", input.message, input.cap)
			if !errors.Is(err, failure) || !reflect.DeepEqual(got, Result{}) {
				t.Fatalf("terminal failure lost context precedence: %#v,%v", got, err)
			}
		}
	}
}

func TestMapMessageNilContextIsExplicitRefusal(t *testing.T) {
	defer func() {
		if recover() != nil {
			t.Error("nil context panicked instead of a fixed unsupported error")
		}
	}()
	got, err := MapMessage(nil, "m", "s", nativeMessage()) //nolint:staticcheck // Explicitly exercise the documented nil-context refusal.
	var typed *MapError
	if !errors.As(err, &typed) || typed.Code != "teams_bot_input_unsupported" || !reflect.DeepEqual(got, Result{}) {
		t.Fatalf("nil context not refused: %#v,%v", got, err)
	}
}
