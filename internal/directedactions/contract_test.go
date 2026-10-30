package directedactions

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

const contractDir = "../../contracts/directed-actions/v0"

type contractCase struct {
	Name        string  `json:"name"`
	Description string  `json:"description"`
	State       State   `json:"state"`
	Request     Request `json:"request"`
	Expect      Result  `json:"expect"`
}

func decodeStrict(t *testing.T, path string, v any) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}

func loadCases(t *testing.T) []contractCase {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(contractDir, "cases", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no contract cases found")
	}
	cases := make([]contractCase, 0, len(paths))
	for _, path := range paths {
		var c contractCase
		decodeStrict(t, path, &c)
		if want := strings.TrimSuffix(filepath.Base(path), ".json"); c.Name != want {
			t.Fatalf("%s: name %q does not match its file name", path, c.Name)
		}
		if c.Description == "" {
			t.Fatalf("%s: description is empty", path)
		}
		cases = append(cases, c)
	}
	return cases
}

func TestContractCases(t *testing.T) {
	for _, c := range loadCases(t) {
		t.Run(c.Name, func(t *testing.T) {
			got := Decide(c.State, c.Request)
			if !reflect.DeepEqual(got, c.Expect) {
				t.Fatalf("%s\n got  %+v\n want %+v", c.Description, got, c.Expect)
			}
		})
	}
}

// Every refusal code, effect, exit class and decision the package can return appears in at least
// one shared case, so another implementation that passes the cases agrees on all of them.
func TestCasesCoverEveryAnswer(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range loadCases(t) {
		seen["decision:"+string(c.Expect.Decision)] = true
		seen["code:"+string(c.Expect.Code)] = true
		seen["effect:"+string(c.Expect.Effect)] = true
		seen["exit:"+string(c.Expect.ExitClass)] = true
		seen["outcome:"+string(c.Expect.EarlierOutcome)] = true
	}
	var want []string
	for _, d := range []Decision{Accept, Refuse, Replay} {
		want = append(want, "decision:"+string(d))
	}
	for _, code := range Codes() {
		want = append(want, "code:"+string(code))
	}
	for _, e := range []Effect{OnlyYou, VisibleToOthers} {
		want = append(want, "effect:"+string(e))
	}
	for _, x := range []ExitClass{ExitDone, ExitRequestProblem, ExitPersonNeeded, ExitUnknown} {
		want = append(want, "exit:"+string(x))
	}
	for _, o := range []Outcome{Queued, Sent, Unknown} {
		want = append(want, "outcome:"+string(o))
	}
	for _, w := range want {
		if !seen[w] {
			t.Errorf("no case expects %s", w)
		}
	}
}

func TestSchemaMatchesPackage(t *testing.T) {
	var schema struct {
		Required   []string `json:"required"`
		Properties map[string]struct {
			Const      string                     `json:"const"`
			Enum       []string                   `json:"enum"`
			Properties map[string]json.RawMessage `json:"properties"`
		} `json:"properties"`
	}
	raw, err := os.ReadFile(filepath.Join(contractDir, "envelope.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	if got := schema.Properties["contract"].Const; got != Contract {
		t.Errorf("schema contract %q, package %q", got, Contract)
	}
	enum := slices.Clone(schema.Properties["action"].Enum)
	slices.Sort(enum)
	if actions := Actions(); !slices.Equal(enum, actions) {
		t.Errorf("schema actions %v, package actions %v", enum, actions)
	}
	if want := []string{"contract", "action", "agent", "reason"}; !slices.Equal(schema.Required, want) {
		t.Errorf("schema required %v, want %v", schema.Required, want)
	}
	checkFields(t, "request", schema.Properties, reflect.TypeFor[Request]())
	checkFields(t, "target", schema.Properties["target"].Properties, reflect.TypeFor[Target]())
	checkFields(t, "content", schema.Properties["content"].Properties, reflect.TypeFor[Content]())
}

func checkFields[V any](t *testing.T, what string, props map[string]V, typ reflect.Type) {
	t.Helper()
	var fromSchema, fromType []string
	for k := range props {
		fromSchema = append(fromSchema, k)
	}
	for f := range typ.Fields() {
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		fromType = append(fromType, name)
	}
	slices.Sort(fromSchema)
	slices.Sort(fromType)
	if !slices.Equal(fromSchema, fromType) {
		t.Errorf("%s fields: schema %v, package %v", what, fromSchema, fromType)
	}
}

func TestActionEffects(t *testing.T) {
	for _, a := range Actions() {
		effect, ok := EffectOf(a)
		switch a {
		case "turn_writes_on", "turn_writes_off":
			if ok {
				t.Errorf("%s: the writes switch has no effect class, got %q", a, effect)
			}
		default:
			if !ok || (effect != OnlyYou && effect != VisibleToOthers) {
				t.Errorf("%s: effect %q, ok %v", a, effect, ok)
			}
		}
	}
	if _, ok := EffectOf("delete_teams_message"); ok {
		t.Error("an unknown action has an effect class")
	}
}

func TestActionFieldsAreKnown(t *testing.T) {
	for name, a := range actions {
		for _, f := range slices.Concat(a.required, a.optional) {
			if !slices.Contains(fieldOrder, f) {
				t.Errorf("%s lists unknown field %q", name, f)
			}
		}
	}
}
