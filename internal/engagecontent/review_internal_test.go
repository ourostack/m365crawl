package engagecontent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestReviewIndependentOptionalScalarEvidence(t *testing.T) {
	raw := nativeResult()
	raw.Threads[0].Title = json.RawMessage(`"\ud800"`)
	if got, err := decode(context.Background(), raw); err == nil {
		t.Fatalf("malformed raw optional Unicode normalized: %+v", got)
	}
	for _, scalar := range []json.RawMessage{[]byte(" null "), []byte("\nnull\t")} {
		raw = nativeResult()
		raw.Threads[0].IsDeleted = scalar
		got, err := decode(context.Background(), raw)
		if err != nil || got.Threads[0].IsDeleted != nil {
			t.Fatalf("JSON null turned into known false: %+v, %v", got, err)
		}
	}
	for _, scalar := range []json.RawMessage{
		[]byte(`"\udc00"`), []byte(`"\ud800\u0041"`), []byte(`"` + "\xff" + `"`),
	} {
		raw = nativeResult()
		raw.Threads[0].Title = scalar
		if got, err := decode(context.Background(), raw); err == nil {
			t.Fatalf("malformed optional scalar published: %+v", got)
		}
	}
	for _, scalar := range []json.RawMessage{
		[]byte(`"\ud83d\ude00"`), []byte(`"\\ud800"`), []byte(`"\u0061\n"`), []byte(`"literal"`),
	} {
		raw = nativeResult()
		raw.Threads[0].Title = scalar
		if got, err := decode(context.Background(), raw); err != nil || got.Threads[0].Title == nil {
			t.Fatalf("valid exact string scalar refused: %+v, %v", got, err)
		}
	}
}

func TestReviewRegistrationGenerationAndVersionEnvelopes(t *testing.T) {
	quickCollection(t)
	for _, generation := range []string{"", strings.Repeat("x", 129)} {
		raw := nativeResult()
		raw.Generation = generation
		if got, err := ObserveHome(context.Background(), &fakeDriver{raw: raw}); err == nil {
			t.Fatalf("missing/oversized document generation published: %+v", got)
		}
	}
	if got, err := ObserveHome(context.Background(), &fakeDriver{raw: nativeResult(), clickGeneration: "replacement"}); err == nil {
		t.Fatalf("document replaced before click published: %+v", got)
	}
	for _, value := range []json.RawMessage{
		[]byte(`{"nativeNumber":`), rawValue(map[string]any{"nativeNumber": 2}),
		rawValue(map[string]any{"nativeNumber": strings.Repeat("1", 257)}),
	} {
		raw := nativeResult()
		raw.Threads[0].Version = value
		if got, err := decode(context.Background(), raw); err == nil {
			t.Fatalf("malformed native number envelope published: %+v", got)
		}
	}
}
