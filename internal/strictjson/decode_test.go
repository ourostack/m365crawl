package strictjson

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestBudgetPreservesExactNumbersAndRejectsAmbiguousJSON(t *testing.T) {
	b := Budget{Context: context.Background(), MaxDepth: 64, MaxMembers: 131072}
	value, err := b.Parse([]byte(`{"id":9223372036854775807,"text":"\ud83d\ude00"}`))
	if err != nil {
		t.Fatal(err)
	}
	object := value.(map[string]any)
	if object["id"] != json.Number("9223372036854775807") || object["text"] != "😀" {
		t.Fatalf("canonical numeric/string words changed: %#v", object)
	}
	for _, raw := range []string{
		`{"id":1,"i\u0064":2}`, `{} {}`, `{}x`, `{"a":"\ud800"}`, `{"a":"\udc00"}`,
		`{"a":"\ud800\u0041"}`, `{"a":"\uZZZZ"}`, `{"a":"` + string([]byte{0xff}) + `"}`, `{"a":`,
		`{1:2}`, `[1`, `{"a":1`, `{"a":"\`, `{"a":"\u1`, `{"a":"\n"}x`, `[}`, `{]`,
	} {
		b := Budget{Context: context.Background(), MaxDepth: 64, MaxMembers: 131072}
		if got, err := b.Parse([]byte(raw)); !errors.Is(err, ErrInvalid) || got != nil {
			t.Errorf("ambiguous raw admitted: %q %#v %v", raw, got, err)
		}
	}
}

func TestBudgetMemberLimitAccumulatesAcrossValues(t *testing.T) {
	b := Budget{Context: context.Background(), MaxDepth: 2, MaxMembers: 2}
	for _, raw := range []string{`{"a":1}`, `[2]`} {
		if _, err := b.Parse([]byte(raw)); err != nil {
			t.Fatalf("inclusive cumulative budget refused: %v", err)
		}
	}
	if value, err := b.Parse([]byte(`{"b":3}`)); !errors.Is(err, ErrTooLarge) || value != nil {
		t.Fatalf("third member bypassed cumulative budget: %#v %v", value, err)
	}
	b = Budget{Context: context.Background(), MaxDepth: 2, MaxMembers: 10}
	if value, err := b.Parse([]byte(`{"a":[1]}`)); !errors.Is(err, ErrTooLarge) || value != nil {
		t.Fatalf("third depth bypassed budget: %#v %v", value, err)
	}
}

func TestBudgetCancellationPublishesNoValue(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	b := Budget{Context: ctx, MaxDepth: 64, MaxMembers: 131072}
	if value, err := b.Parse([]byte(`{"a":1}`)); !errors.Is(err, context.Canceled) || value != nil {
		t.Fatalf("cancelled parser published value: %#v %v", value, err)
	}
}

func TestBudgetPublicStructuralBoundaries(t *testing.T) {
	for _, count := range []int{131072, 131073} {
		raw := "[" + strings.Repeat("0,", count-1) + "0]"
		b := Budget{Context: context.Background(), MaxDepth: 64, MaxMembers: 131072}
		_, err := b.Parse([]byte(raw))
		if (count == 131072 && err != nil) || (count == 131073 && !errors.Is(err, ErrTooLarge)) {
			t.Fatalf("member count %d: %v", count, err)
		}
	}

}

func TestBudgetNilConfigurationAndInclusiveDepth(t *testing.T) {
	for _, budget := range []*Budget{nil, {MaxDepth: 64, MaxMembers: 131072}} {
		if value, err := budget.Parse([]byte(`{}`)); value != nil || !errors.Is(err, ErrInvalid) {
			t.Fatal("missing context became successful JSON admission")
		}
	}
	for _, depth := range []int{64, 65} {
		raw := strings.Repeat("[", depth-1) + "0" + strings.Repeat("]", depth-1)
		b := Budget{Context: context.Background(), MaxDepth: 64, MaxMembers: 131072}
		value, err := b.Parse([]byte(raw))
		if (depth == 64 && err != nil) || (depth == 65 && (!errors.Is(err, ErrTooLarge) || value != nil)) {
			t.Fatalf("depth boundary %d admitted wrong state: %v", depth, err)
		}
	}
}

// ParseAt continues a budget at a container's real depth and shares its member count, so a nested
// string-encoded value cannot widen the limits of the record that holds it.
func TestParseAtSharesDepthAndMembers(t *testing.T) {
	ctx := context.Background()
	b := Budget{Context: ctx, MaxDepth: 4, MaxMembers: 100}
	if v, err := b.ParseAt([]byte(`{"a":1}`), 3); err != nil || v.(map[string]any)["a"] == nil {
		t.Fatalf("depth 4 at start 3: %v %v", v, err)
	}
	if _, err := b.ParseAt([]byte(`{"a":{"b":1}}`), 3); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("depth 5 at start 3: %v", err)
	}
	m := Budget{Context: ctx, MaxDepth: 64, MaxMembers: 3}
	if _, err := m.Parse([]byte(`{"x":"s","y":1}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ParseAt([]byte(`{"a":1}`), 3); err != nil {
		t.Fatalf("third member: %v", err)
	}
	if _, err := m.ParseAt([]byte(`{"b":1}`), 3); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("fourth member shared across parses: %v", err)
	}
	for _, raw := range []string{`{"a":1,"a":2}`, `{} 1`, `"\ud800"`, ``} {
		inner := Budget{Context: ctx, MaxDepth: 64, MaxMembers: 100}
		if _, err := inner.ParseAt([]byte(raw), 3); !errors.Is(err, ErrInvalid) {
			t.Errorf("%q: %v", raw, err)
		}
	}
	if _, err := (*Budget)(nil).ParseAt([]byte(`{}`), 3); !errors.Is(err, ErrInvalid) {
		t.Fatal("nil budget")
	}
}
