package engagecontent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
)

func TestSelectedInputStructureBoundaries(t *testing.T) {
	for _, raw := range []string{"", "[]", "{", "{]", `{"a":1}`, `{"a":"x","a":"y"}`, `{"a":"x"`, `{"a":"x" `, `{"a":""}`, `{"a":"x","b":"y"}`, `{"a":"x"} true`, strings.Repeat("x", 8193), `{"a":"x",1:"y"}`} {
		if _, err := selectedStrings(raw, "a"); err == nil {
			t.Fatal("invalid nested structure admitted")
		}
	}
	id := selectedTestID()
	group := base64.RawURLEncoding.EncodeToString([]byte(`{"_type":"Group","id":"7"}`))
	for _, ctx := range []string{
		`{"internalId":"bad","contextType":"engageCommunity","subEntityId":"bad"}`,
		`{"internalId":"19:bad@EngageCommunity","contextType":"engageCommunity","subEntityId":"bad"}`,
		`{"internalId":"19:` + group + `@EngageCommunity","contextType":"wrong","subEntityId":"bad"}`,
		`{"internalId":"19:` + group + `@EngageCommunity","contextType":"engageCommunity","subEntityId":"{\"deepLinkType\":\"crossapp\",\"path\":\"/other/` + id + `\"}"}`,
		`{"internalId":"19:` + base64.RawURLEncoding.EncodeToString([]byte(`[]`)) + `@EngageCommunity","contextType":"engageCommunity","subEntityId":"{\"deepLinkType\":\"crossapp\",\"path\":\"/threads/` + id + `\"}"}`,
	} {
		target := "https://teams.microsoft.com/l/entity/683f3525-d193-4a67-8d91-22093beab1ca/?context=" + url.QueryEscape(ctx)
		if _, err := ParseThreadURL(target); err == nil {
			t.Fatal("invalid Teams context admitted")
		}
	}
	for _, raw := range []string{"https:opaque", "https://example.test/a?bad;query=1", "https://example.test/a?x=%ZZ", strings.Repeat("x", 262145), "https://example.test/a#bad;fragment"} {
		if SafeThreadLink(raw) {
			t.Fatal("unqualified URL admitted")
		}
	}
}

func TestSelectedDecodeMalformedAndOptionalValues(t *testing.T) {
	target, original := selectedFixture(t)
	for name, mutate := range map[string]func(*scriptResult){
		"base-fatal":        func(r *scriptResult) { r.Fatal = "malformed" },
		"source-credential": func(r *scriptResult) { r.Threads[0].GroupID = "https://example.test/a?sig=private" },
		"bad-utf8":          func(r *scriptResult) { r.Threads[0].Selected = []byte("{\"x\":\"\xff\"}") },
		"bad-surrogate": func(r *scriptResult) {
			r.Threads[0].Selected = []byte(strings.Replace(string(r.Threads[0].Selected), "starter text", `\ud800`, 1))
		},
		"too-many-messages": func(r *scriptResult) {
			var s SelectedThread
			_ = json.Unmarshal(r.Threads[0].Selected, &s)
			s.Messages = append(s.Messages, make([]SelectedMessage, 128)...)
			r.Threads[0].Selected = rawValue(s)
		},
		"too-many-links": func(r *scriptResult) {
			var s SelectedThread
			_ = json.Unmarshal(r.Threads[0].Selected, &s)
			s.Messages[0].Links = make([]SelectedLink, 129)
			r.Threads[0].Selected = rawValue(s)
		},
		"invalid-author": func(r *scriptResult) {
			var s SelectedThread
			_ = json.Unmarshal(r.Threads[0].Selected, &s)
			bad := "\x00"
			s.Messages[0].Author.Name = &bad
			r.Threads[0].Selected = rawValue(s)
		},
		"invalid-clock": func(r *scriptResult) {
			var s SelectedThread
			_ = json.Unmarshal(r.Threads[0].Selected, &s)
			bad := "\x00"
			s.Messages[0].CreatedRaw = &bad
			r.Threads[0].Selected = rawValue(s)
		},
		"invalid-body": func(r *scriptResult) {
			var s SelectedThread
			_ = json.Unmarshal(r.Threads[0].Selected, &s)
			s.Messages[1].Blocks = []string{"\x00"}
			r.Threads[0].Selected = rawValue(s)
		},
		"invalid-attachment-title": func(r *scriptResult) {
			var s SelectedThread
			_ = json.Unmarshal(r.Threads[0].Selected, &s)
			bad := "\x00"
			s.Messages[0].Attachments[0].Title = &bad
			r.Threads[0].Selected = rawValue(s)
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := original
			r.Threads = append([]scriptThread{}, original.Threads...)
			mutate(&r)
			if _, err := decodeSelected(context.Background(), target, r); err == nil {
				t.Fatal("invalid native result admitted")
			}
		})
	}
	v := validator{losses: map[string]int{}}
	bad := "\x00"
	if _, err := selectedClean(&bad, &v); err == nil {
		t.Fatal("invalid optional text admitted")
	}
	signed := "https://example.test/a?sig=private"
	if value, err := selectedClean(&signed, &v); err != nil || *value != "[redacted]" {
		t.Fatal("optional text not scrubbed")
	}
	r := original
	r.Threads = append([]scriptThread{}, original.Threads...)
	var s SelectedThread
	_ = json.Unmarshal(r.Threads[0].Selected, &s)
	s.Messages[1].BodyState = "unmapped"
	s.Messages[1].Blocks = nil
	s.Messages[1].Attachments = nil
	s.Messages[0].Attachments[0].URL = &signed
	r.Threads[0].Selected = rawValue(s)
	if _, err := decodeSelected(context.Background(), target, r); err != nil {
		t.Fatal("partial observations refused", err)
	}
}

func TestSelectedAggregateMetadataAndEnvelopeBudgets(t *testing.T) {
	for _, field := range []string{"title", "language"} {
		target, raw := selectedFixture(t)
		var s SelectedThread
		_ = json.Unmarshal(raw.Threads[0].Selected, &s)
		large := strings.Repeat("x", 262144)
		s.Messages[0].Attachments = make([]SelectedAttachment, 31)
		for i := range s.Messages[0].Attachments {
			s.Messages[0].Attachments[i] = SelectedAttachment{ID: "a", Title: &large}
		}
		s.Messages[1].Attachments = []SelectedAttachment{}
		raw.Threads[0].Title = rawValue("")
		if field == "title" {
			raw.Threads[0].Title = rawValue(large)
		} else {
			raw.Threads[0].Language = rawValue(large)
		}
		raw.Threads[0].Selected = rawValue(s)
		if _, err := decodeSelected(context.Background(), target, raw); err == nil {
			t.Fatal("aggregate native metadata exceeded8MiB")
		}
	}
	target, raw := selectedFixture(t)
	r, err := decodeSelected(context.Background(), target, raw)
	if err != nil {
		t.Fatal(err)
	}
	large := strings.Repeat("x", 262144)
	r.Threads[0].Title = &large
	r.Threads[0].Blocks = []string{large}
	s := r.Threads[0].Selected
	s.Messages[0].Blocks = []string{large}
	s.Messages[0].Attachments = make([]SelectedAttachment, 29)
	for i := range s.Messages[0].Attachments {
		s.Messages[0].Attachments[i] = SelectedAttachment{ID: "a", Title: &large}
	}
	if ValidateResult(context.Background(), r) == nil {
		t.Fatal("serialized selected envelope exceeded8MiB")
	}
}

func TestSelectedValidationCancellationAndMalformedOptionals(t *testing.T) {
	target, raw := selectedFixture(t)
	good, err := decodeSelected(context.Background(), target, raw)
	if err != nil {
		t.Fatal(err)
	}
	for at := 1; at <= 20; at++ {
		parent, cancel := context.WithCancel(context.Background())
		ctx := &checkpointContext{Context: parent, cancel: cancel, at: at}
		err := ValidateResult(ctx, good)
		if ctx.n >= at && err == nil {
			t.Fatal("cancellation lost")
		}
		cancel()
	}
	for _, mutate := range []func(*Result){
		func(r *Result) {
			r.State = "metadata_only"
			r.Threads = nil
			r.Losses = []Loss{{"native_content_not_observed", 1}}
		},
		func(r *Result) { bad := "\x00"; r.Threads[0].Selected.Messages[1].Author.Name = &bad },
		func(r *Result) { r.Threads[0].Selected.Messages[1].Blocks = []string{"\x00"} },
		func(r *Result) {
			bad := "\x00"
			r.Threads[0].Selected.Messages[1].Attachments = []SelectedAttachment{{ID: "a", Title: &bad}}
		},
		func(r *Result) {
			relative := "relative/path"
			r.Threads[0].Selected.Messages[1].Attachments = []SelectedAttachment{{ID: "a", URL: &relative}}
		},
		func(r *Result) {
			r.Selection = nil
			r.Qualification = "native_home_observations"
			r.Losses = []Loss{{Code: "clock_unmapped", Count: 1}}
		},
	} {
		var copy Result
		b, _ := json.Marshal(good)
		_ = json.Unmarshal(b, &copy)
		mutate(&copy)
		if ValidateResult(context.Background(), copy) == nil {
			t.Fatal("malformed retained optional admitted")
		}
	}
}
